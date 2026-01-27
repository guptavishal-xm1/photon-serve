package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"rom-server/internal/config"
	"rom-server/internal/models"
	"rom-server/internal/services"
)

// Handlers contains all HTTP handlers with their dependencies
type Handlers struct {
	cfg         *config.Config
	fileService *services.FileService
	logger      *log.Logger
}

// NewHandlers creates a new Handlers instance
func NewHandlers(cfg *config.Config, fs *services.FileService, logger *log.Logger) *Handlers {
	return &Handlers{
		cfg:         cfg,
		fileService: fs,
		logger:      logger,
	}
}

// Health handles health check requests
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	resp := models.HealthResponse{
		Status:    "ok",
		Timestamp: time.Now(),
		Version:   "2.0.0",
	}
	h.sendJSON(w, http.StatusOK, resp)
}

// GetConfig returns public configuration for frontend
func (h *Handlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	// Cache config in browser for 5 minutes (it rarely changes)
	w.Header().Set("Cache-Control", "public, max-age=300")
	
	stats := h.fileService.GetCategoryStats()
	
	resp := models.ConfigResponse{
		AppName:     h.cfg.Text.AppName,
		AppTitle:    h.cfg.Text.AppTitle,
		AppSubtitle: h.cfg.Text.AppSubtitle,
		DeviceName:  h.cfg.Text.DeviceName,
		Categories:  stats,
		Text: models.TextMessages{
			UploadSuccess: h.cfg.Text.UploadSuccess,
			UploadFailed:  h.cfg.Text.UploadFailed,
			NoFilesFound:  h.cfg.Text.NoFilesFound,
			CopySuccess:   h.cfg.Text.CopySuccess,
			CopyFailed:    h.cfg.Text.CopyFailed,
		},
	}
	h.sendJSON(w, http.StatusOK, resp)
}

// ListFiles handles file listing requests
func (h *Handlers) ListFiles(w http.ResponseWriter, r *http.Request) {
	files, err := h.fileService.ListFiles()
	if err != nil {
		h.logger.Printf("Error listing files: %v", err)
		h.sendError(w, http.StatusInternalServerError, h.cfg.Text.ServerError)
		return
	}

	resp := models.ListResponse{
		Files:      files,
		TotalCount: len(files),
	}
	h.sendJSON(w, http.StatusOK, resp)
}

// Upload handles file upload requests
func (h *Handlers) Upload(w http.ResponseWriter, r *http.Request) {
	// Only POST allowed
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}

	// Acquire upload slot (blocks if at limit)
	h.fileService.AcquireUploadSlot()
	defer h.fileService.ReleaseUploadSlot()

	// Limit body size
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.GetMaxUploadSize())

	// Validate category from Query Param (Fail Fast)
	// We prefer query param for category to avoid parsing the whole body
	// just to find out the category is invalid.
	category := r.URL.Query().Get("category")
	
	// Fallback to FormValue if not in query (forces body read, but supports legacy clients)
	if category == "" {
		category = r.FormValue("category")
	}

	if !h.cfg.IsValidCategory(category) {
		h.sendError(w, http.StatusBadRequest, "Invalid category (use ?category= param)")
		return
	}

	// Parse multipart form with 32MB memory buffer
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		h.logger.Printf("Upload parse error: %v", err)
		h.sendError(w, http.StatusRequestEntityTooLarge, h.cfg.Text.FileTooLarge)
		return
	}

	// Get file
	file, handler, err := r.FormFile("zipfile")
	if err != nil {
		h.sendError(w, http.StatusBadRequest, h.cfg.Text.InvalidFile)
		return
	}
	defer file.Close()

	// Sanitize filename
	safeFilename := services.SanitizeFilename(handler.Filename)
	ext := filepath.Ext(safeFilename)
	if !h.cfg.IsAllowedExtension(ext) {
		h.sendError(w, http.StatusBadRequest, "File type not allowed. Allowed: "+h.cfg.AllowedExts[0])
		return
	}

	// Validate ZIP magic bytes
	header := make([]byte, 4)
	if _, err := file.Read(header); err != nil {
		h.sendError(w, http.StatusBadRequest, h.cfg.Text.InvalidFile)
		return
	}
	file.Seek(0, io.SeekStart)

	if !services.ValidateZipMagicBytes(header) {
		h.logger.Printf("Security Alert: Invalid ZIP signature for %s", safeFilename)
		h.sendError(w, http.StatusBadRequest, "Invalid file format (Not a real ZIP)")
		return
	}

	// Save file
	if err := h.fileService.SaveFile(category, safeFilename, file); err != nil {
		h.logger.Printf("Save error: %v", err)
		h.sendError(w, http.StatusInternalServerError, h.cfg.Text.UploadFailed)
		return
	}

	h.logger.Printf("Success: Uploaded %s to [%s]", safeFilename, category)
	
	resp := models.UploadResponse{
		Success:  true,
		Message:  h.cfg.Text.UploadSuccess,
		Filename: safeFilename,
		Category: category,
	}
	h.sendJSON(w, http.StatusOK, resp)
}

// Delete handles file deletion requests
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}

	category := r.URL.Query().Get("category")
	filename := r.URL.Query().Get("filename")

	if category == "" || filename == "" {
		h.sendError(w, http.StatusBadRequest, "Category and filename required")
		return
	}

	if !h.cfg.IsValidCategory(category) {
		h.sendError(w, http.StatusBadRequest, "Invalid category")
		return
	}

	if err := h.fileService.DeleteFile(category, filename); err != nil {
		h.logger.Printf("Delete error: %v", err)
		h.sendError(w, http.StatusNotFound, "File not found")
		return
	}

	h.logger.Printf("Deleted: %s from [%s]", filename, category)
	h.sendJSON(w, http.StatusOK, map[string]string{"message": "File deleted"})
}

// ServeDownload serves files with concurrency control
func (h *Handlers) ServeDownload(baseDir string) http.Handler {
	fileServer := http.FileServer(http.Dir(baseDir))
	
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Acquire download slot
		h.fileService.AcquireDownloadSlot()
		defer h.fileService.ReleaseDownloadSlot()

		// Track download stats (Best effort, ignore errors)
		// URL is /downloads/category/filename
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/downloads/"), "/")
		if len(parts) >= 2 {
			category := parts[0]
			filename := parts[1]
			// Handle potential URL encoding
			if decoded, err := url.QueryUnescape(filename); err == nil {
				filename = decoded
			}
			h.fileService.IncrementDownloadCount(category, filename)
		}

		// Add download-specific headers
		w.Header().Set("Cache-Control", "public, max-age=3600")
		
		// Serve the file
		http.StripPrefix("/downloads/", fileServer).ServeHTTP(w, r)
	})
}

// sendJSON sends a JSON response
func (h *Handlers) sendJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// sendError sends an error response
func (h *Handlers) sendError(w http.ResponseWriter, status int, message string) {
	resp := models.ErrorResponse{
		Error: message,
		Code:  status,
	}
	h.sendJSON(w, status, resp)
}
