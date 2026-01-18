package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// --- CONFIGURATION ---
const (
	// 5GB Max Upload Size (Adjust if your ROMs get bigger)
	MaxUploadSize = 5 * 1024 * 1024 * 1024 
	
	// Directory Permissions (User read/write, Group read, World read)
	DirPerms = 0755
)

// Config holds dynamic settings from Environment Variables
type Config struct {
	Port      string
	APIKey    string
	UploadDir string
}

func loadConfig() Config {
	return Config{
		Port:      getEnv("PORT", "8080"),
		APIKey:    getEnv("API_KEY", "changeme"), // You MUST set this in systemd
		UploadDir: getEnv("UPLOAD_DIR", "uploads"),
	}
}

// FileInfo is the JSON response struct
type FileInfo struct {
	Category  string `json:"category"`
	Filename  string `json:"filename"`
	Size      string `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

func main() {
	// 1. Setup Configuration & Logger
	cfg := loadConfig()
	logger := log.New(os.Stdout, "[ROM-SERVER] ", log.LstdFlags)

	if cfg.APIKey == "changeme" {
		logger.Println("WARNING: Using default API Key! Set API_KEY environment variable.")
	}

	// 2. Prepare Storage Directories
	// We create 'temp' for partial uploads to ensure atomicity
	dirs := []string{"vanilla", "gapps", "temp"}
	for _, d := range dirs {
		path := filepath.Join(cfg.UploadDir, d)
		if err := os.MkdirAll(path, DirPerms); err != nil {
			logger.Fatalf("Failed to create directory %s: %v", path, err)
		}
	}

	// 3. Router Setup
	mux := http.NewServeMux()

	// Dashboard UI
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "download.html")
	})

	// Admin Upload Dashboard (moved from / to /admin)
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	// API Endpoints
	mux.HandleFunc("/upload", authMiddleware(cfg.APIKey, logger, handleUpload(cfg.UploadDir, logger)))
	mux.HandleFunc("/list", handleList(cfg.UploadDir, logger))

	// File Download Server (Uses OS sendfile for zero-copy performance)
	// We strip prefix so /downloads/vanilla/rom.zip maps to ./uploads/vanilla/rom.zip
	fs := http.StripPrefix("/downloads/", http.FileServer(http.Dir(cfg.UploadDir)))
	mux.Handle("/downloads/", fs)

	// 4. Server Configuration (Timeouts are critical for production)
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      securityHeaders(mux),
		ReadTimeout:  1 * time.Hour,  // Allow 1 hour for slow 3GB uploads
		WriteTimeout: 1 * time.Hour,  // Allow 1 hour for slow downloads
		IdleTimeout:  120 * time.Second,
	}

	// 5. Start Server in Background
	go func() {
		logger.Printf("Server started on :%s", cfg.Port)
		logger.Printf("Storage Path: %s", cfg.UploadDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Server error: %v", err)
		}
	}()

	// 6. Graceful Shutdown Logic
	// Waits for interrupt signal (Ctrl+C or Systemd stop)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Println("Shutting down server...")

	// Give active uploads 30 seconds to finish before force killing
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatalf("Server forced to shutdown: %v", err)
	}

	logger.Println("Server exited cleanly")
}

// --- HANDLERS ---

func handleUpload(baseDir string, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A. Method Check
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		// B. Body Size Limit (Hard Stop for too large files)
		r.Body = http.MaxBytesReader(w, r.Body, MaxUploadSize)
		// Use 32MB memory buffer, larger files spill to disk (prevents memory exhaustion)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			logger.Printf("Upload blocked: File too large or parse error: %v", err)
			http.Error(w, "File too large (Max 5GB)", http.StatusRequestEntityTooLarge)
			return
		}

		// C. Parse File
		file, handler, err := r.FormFile("zipfile")
		if err != nil {
			http.Error(w, "Invalid file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		category := r.FormValue("category")
		if category != "vanilla" && category != "gapps" {
			http.Error(w, "Invalid category. Must be 'vanilla' or 'gapps'", http.StatusBadRequest)
			return
		}

		// D. Sanitize Filename (Security)
		// We take only the Base name to prevent directory traversal (../../)
		safeFilename := filepath.Base(handler.Filename)
		if filepath.Ext(safeFilename) != ".zip" {
			http.Error(w, "Only .zip files allowed", http.StatusBadRequest)
			return
		}

		// E. Magic Byte Check (Verify it's actually a ZIP)
		// Read first 4 bytes
		buff := make([]byte, 4)
		if _, err := file.Read(buff); err != nil {
			http.Error(w, "Read error", http.StatusInternalServerError)
			return
		}
		// Reset file pointer to beginning
		file.Seek(0, 0)
		
		// Zip signature is 0x50 0x4B 0x03 0x04
		if buff[0] != 0x50 || buff[1] != 0x4B || buff[2] != 0x03 || buff[3] != 0x04 {
			logger.Printf("Security Alert: Uploaded file %s is not a valid zip", safeFilename)
			http.Error(w, "Invalid file format (Not a real ZIP)", http.StatusBadRequest)
			return
		}

		// F. Atomic Write Strategy
		// 1. Create a temp file
		tempDir := filepath.Join(baseDir, "temp")
		tempFile, err := os.CreateTemp(tempDir, "upload-*.tmp")
		if err != nil {
			logger.Printf("Temp file error: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		tempName := tempFile.Name()
		// Ensure file handle is closed to prevent descriptor leak
		defer tempFile.Close()
		// Ensure temp file is deleted if function exits early or crashes
		defer os.Remove(tempName)

		// 2. Stream data to temp file
		if _, err := io.Copy(tempFile, file); err != nil {
			logger.Printf("Write error: %v", err)
			http.Error(w, "Upload interrupted", http.StatusInternalServerError)
			return
		}
		// Close temp file before moving (defer will close again safely)
		if err := tempFile.Close(); err != nil {
			logger.Printf("Failed to close temp file: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// 3. "The Swap" (Critical Zone)
		finalDir := filepath.Join(baseDir, category)
		finalPath := filepath.Join(finalDir, safeFilename)

		// Clean the target directory (Enforce 1 file rule)
		// First ensure directory exists, then clean old files
		if err := os.MkdirAll(finalDir, DirPerms); err != nil {
			logger.Printf("Failed to create target directory: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		// Remove all existing files in the directory
		if entries, err := os.ReadDir(finalDir); err == nil {
			for _, entry := range entries {
				os.Remove(filepath.Join(finalDir, entry.Name()))
			}
		}

		// Move temp file to final destination
		if err := os.Rename(tempName, finalPath); err != nil {
			// If Rename fails (rare on same disk), try manual copy
			if copyErr := manualMove(tempName, finalPath); copyErr != nil {
				logger.Printf("Final move failed: %v", copyErr)
				http.Error(w, "Failed to save file", http.StatusInternalServerError)
				return
			}
		}

		// G. Success Response
		logger.Printf("Success: Uploaded %s to [%s]", safeFilename, category)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Upload successful"))
	}
}

func handleList(baseDir string, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var files []FileInfo
		categories := []string{"vanilla", "gapps"}

		for _, cat := range categories {
			dir := filepath.Join(baseDir, cat)
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".zip") {
					info, _ := e.Info()
					sizeMB := info.Size() / 1024 / 1024
					files = append(files, FileInfo{
						Category:  cat,
						Filename:  e.Name(),
						Size:      fmt.Sprintf("%d MB", sizeMB),
						UpdatedAt: info.ModTime().Format("2006-01-02 15:04"),
					})
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(files)
	}
}

// --- MIDDLEWARE ---

// Security Headers Middleware
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		next.ServeHTTP(w, r)
	})
}

// Authentication Middleware with Constant Time Comparison
func authMiddleware(validKey string, logger *log.Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get Key from Header (preferred) or Query/Form
		userKey := r.Header.Get("X-API-Key")
		if userKey == "" {
			userKey = r.FormValue("key")
		}

		// ConstantTimeCompare prevents timing attacks
		if subtle.ConstantTimeCompare([]byte(userKey), []byte(validKey)) != 1 {
			logger.Printf("Unauthorized access attempt from %s", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// --- UTILITIES ---

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// manualMove handles moving files across different partitions if os.Rename fails
func manualMove(source, dest string) error {
	inputFile, err := os.Open(source)
	if err != nil { return err }
	defer inputFile.Close()

	outputFile, err := os.Create(dest)
	if err != nil { return err }

	if _, err := io.Copy(outputFile, inputFile); err != nil {
		outputFile.Close()
		return err
	}
	
	// Explicitly close and sync output file before removing source
	if err := outputFile.Sync(); err != nil {
		outputFile.Close()
		return err
	}
	if err := outputFile.Close(); err != nil {
		return err
	}
	
	return os.Remove(source)
}