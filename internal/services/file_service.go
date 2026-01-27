package services

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"rom-server/internal/config"
	"rom-server/internal/models"
)

// FileService handles all file operations with concurrency control
type FileService struct {
	cfg            *config.Config
	uploadSem      chan struct{} // Semaphore for upload concurrency
	downloadSem    chan struct{} // Semaphore for download concurrency
	mu             sync.RWMutex  // Mutex for file operations
	downloadCounts map[string]int64
	statsPath      string
	
	// Cache for file listing (reduces disk IO)
	cachedFiles []models.FileInfo
	cacheValid  bool
}

// NewFileService creates a new FileService with concurrency limits
func NewFileService(cfg *config.Config) *FileService {
	fs := &FileService{
		cfg:            cfg,
		uploadSem:      make(chan struct{}, cfg.Concurrency.MaxConcurrentUploads),
		downloadSem:    make(chan struct{}, cfg.Concurrency.MaxConcurrentDownloads),
		downloadCounts: make(map[string]int64),
		statsPath:      filepath.Join(cfg.Storage.UploadDir, "stats.json"),
	}
	// Try to load existing stats (ignore error on first run)
	_ = fs.loadStats()
	return fs
}

// loadStats loads download counts from JSON file
func (s *FileService) loadStats() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.statsPath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.downloadCounts)
}

// saveStats saves download counts to JSON file
func (s *FileService) saveStats() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.downloadCounts, "", "  ")
	s.mu.RUnlock()
	
	if err != nil {
		return err
	}
	return os.WriteFile(s.statsPath, data, 0644)
}

// IncrementDownloadCount increments the count for a file
func (s *FileService) IncrementDownloadCount(category, filename string) {
	key := filepath.Join(category, filename)
	
	s.mu.Lock()
	s.downloadCounts[key]++
	s.mu.Unlock()

	// Persist asynchronously to avoid blocking download
	// In a real high-scale app, we'd batch this. For this usage, it's fine.
	go s.saveStats()
}

// AcquireUploadSlot blocks until an upload slot is available
func (s *FileService) AcquireUploadSlot() {
	s.uploadSem <- struct{}{}
}

// ReleaseUploadSlot releases an upload slot
func (s *FileService) ReleaseUploadSlot() {
	<-s.uploadSem
}

// AcquireDownloadSlot blocks until a download slot is available
func (s *FileService) AcquireDownloadSlot() {
	s.downloadSem <- struct{}{}
}

// ReleaseDownloadSlot releases a download slot
func (s *FileService) ReleaseDownloadSlot() {
	<-s.downloadSem
}

// InitializeStorage creates all required directories
func (s *FileService) InitializeStorage() error {
	baseDir := s.cfg.Storage.UploadDir

	// Create temp directory
	tempDir := filepath.Join(baseDir, s.cfg.Storage.TempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Create category directories
	for catName, cat := range s.cfg.Categories {
		if cat.Enabled {
			catDir := filepath.Join(baseDir, catName)
			if err := os.MkdirAll(catDir, 0755); err != nil {
				return fmt.Errorf("failed to create category directory %s: %w", catName, err)
			}
		}
	}

	return nil
}

// ListFiles returns all files from enabled categories
func (s *FileService) ListFiles() ([]models.FileInfo, error) {
	// 1. Try Fast Path (Read Lock)
	s.mu.RLock()
	if s.cacheValid {
		// Clone cache and inject live counters
		result := make([]models.FileInfo, len(s.cachedFiles))
		copy(result, s.cachedFiles)
		
		for i := range result {
			key := filepath.Join(result[i].Category, result[i].Filename)
			result[i].Downloads = s.downloadCounts[key]
		}
		s.mu.RUnlock()
		return result, nil
	}
	s.mu.RUnlock()

	// 2. Slow Path (Write Lock - Rebuild Cache)
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check (in case another goroutine beat us)
	if s.cacheValid {
		result := make([]models.FileInfo, len(s.cachedFiles))
		copy(result, s.cachedFiles)
		for i := range result {
			key := filepath.Join(result[i].Category, result[i].Filename)
			result[i].Downloads = s.downloadCounts[key]
		}
		return result, nil
	}

	// Rebuild Cache from Disk
	var files []models.FileInfo
	baseDir := s.cfg.Storage.UploadDir

	for catName, cat := range s.cfg.Categories {
		if !cat.Enabled {
			continue
		}

		catDir := filepath.Join(baseDir, catName)
		entries, err := os.ReadDir(catDir)
		if err != nil {
			continue // Directory might not exist yet
		}

		for _, e := range entries {
			if e.IsDir() {
				continue
			}

			// Check allowed extensions
			ext := filepath.Ext(e.Name())
			if !s.cfg.IsAllowedExtension(ext) {
				continue
			}

			info, err := e.Info()
			if err != nil {
				continue
			}

			files = append(files, models.FileInfo{
				Category:  catName,
				Filename:  e.Name(),
				Size:      formatSize(info.Size()),
				SizeBytes: info.Size(),
				UpdatedAt: info.ModTime().Format("2006-01-02 15:04"),
				// Downloads populated dynamically
			})
		}
	}

	// Sort by modification time (newest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].UpdatedAt > files[j].UpdatedAt
	})

	// Update Cache
	s.cachedFiles = files
	s.cacheValid = true

	// Return result with populated counts
	result := make([]models.FileInfo, len(files))
	copy(result, files)
	for i := range result {
		key := filepath.Join(result[i].Category, result[i].Filename)
		result[i].Downloads = s.downloadCounts[key]
	}

	return result, nil
}

// ListFilesByCategory returns files for a specific category
func (s *FileService) ListFilesByCategory(category string) ([]models.FileInfo, error) {
	allFiles, err := s.ListFiles()
	
	// Invalidate Cache since we are adding a file
	s.cacheValid = false
	if err != nil {
		return nil, err
	}

	var filtered []models.FileInfo
	for _, f := range allFiles {
		if f.Category == category {
			filtered = append(filtered, f)
		}
	}

	return filtered, nil
}

// SaveFile saves an uploaded file with atomic write and enforces file limits
func (s *FileService) SaveFile(category, filename string, reader io.Reader) error {
	// NO GLOBAL LOCK during I/O!
	// We only lock when swapping the file into the public directory.

	baseDir := s.cfg.Storage.UploadDir
	tempDir := filepath.Join(baseDir, s.cfg.Storage.TempDir)
	finalDir := filepath.Join(baseDir, category)

	// 1. Create temp file
	tempFile, err := os.CreateTemp(tempDir, "upload-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath) // Cleanup on failure

	// 2. Stream data to temp file (HEAVY I/O - UNLOCKED)
	if _, err := io.Copy(tempFile, reader); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to write file: %w", err)
	}
	tempFile.Close()

	// 3. ENTER CRITICAL SECTION
	s.mu.Lock()
	defer s.mu.Unlock()

	// 4. Enforce file limit for category
	if err := s.enforceFileLimit(category); err != nil {
		return fmt.Errorf("failed to enforce file limit: %w", err)
	}

	// 5. Move to final destination
	finalPath := filepath.Join(finalDir, filename)
	if err := os.Rename(tempPath, finalPath); err != nil {
		// Cross-device fallback
		if copyErr := s.manualMove(tempPath, finalPath); copyErr != nil {
			return fmt.Errorf("failed to save file: %w", copyErr)
		}
	}

	return nil
}

// enforceFileLimit removes oldest files if limit exceeded
func (s *FileService) enforceFileLimit(category string) error {
	cat, exists := s.cfg.Categories[category]
	if !exists {
		return fmt.Errorf("category %s not found", category)
	}

	baseDir := s.cfg.Storage.UploadDir
	catDir := filepath.Join(baseDir, category)

	entries, err := os.ReadDir(catDir)
	if err != nil {
		return nil // Directory doesn't exist yet
	}

	// Get file info with mod times
	type fileWithTime struct {
		name    string
		modTime int64
	}

	var files []fileWithTime
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileWithTime{
			name:    e.Name(),
			modTime: info.ModTime().Unix(),
		})
	}

	// Sort by mod time (oldest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime < files[j].modTime
	})

	// Remove oldest files until we're under limit (leaving room for new file)
	maxFiles := cat.MaxFiles
	for len(files) >= maxFiles {
		oldest := files[0]
		oldPath := filepath.Join(catDir, oldest.name)
		if err := os.Remove(oldPath); err != nil {
			return fmt.Errorf("failed to remove old file %s: %w", oldest.name, err)
		}
		files = files[1:]
	}

	return nil
}

// DeleteFile removes a file from storage
func (s *FileService) DeleteFile(category, filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	// Invalidate Cache
	s.cacheValid = false

	// Sanitize to prevent directory traversal
	safeFilename := filepath.Base(filename)
	filePath := filepath.Join(s.cfg.Storage.UploadDir, category, safeFilename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("file not found")
	}

	return os.Remove(filePath)
}

// GetFilePath returns the full path to a file (for downloads)
func (s *FileService) GetFilePath(category, filename string) (string, error) {
	safeFilename := filepath.Base(filename)
	filePath := filepath.Join(s.cfg.Storage.UploadDir, category, safeFilename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found")
	}

	return filePath, nil
}

// GetCategoryStats returns statistics for all categories
func (s *FileService) GetCategoryStats() []models.CategoryInfo {
	var stats []models.CategoryInfo

	for catName, cat := range s.cfg.Categories {
		if !cat.Enabled {
			continue
		}

		files, _ := s.ListFilesByCategory(catName)
		stats = append(stats, models.CategoryInfo{
			Name:        catName,
			DisplayName: cat.DisplayName,
			Description: cat.Description,
			MaxFiles:    cat.MaxFiles,
			FileCount:   len(files),
		})
	}

	return stats
}

// manualMove copies file then removes source (for cross-device moves)
func (s *FileService) manualMove(source, dest string) error {
	inputFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer inputFile.Close()

	outputFile, err := os.Create(dest)
	if err != nil {
		return err
	}

	if _, err := io.Copy(outputFile, inputFile); err != nil {
		outputFile.Close()
		return err
	}

	if err := outputFile.Sync(); err != nil {
		outputFile.Close()
		return err
	}

	if err := outputFile.Close(); err != nil {
		return err
	}

	return os.Remove(source)
}

// ValidateZipMagicBytes checks if file starts with ZIP magic bytes
func ValidateZipMagicBytes(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	// ZIP magic: 0x50 0x4B 0x03 0x04
	return header[0] == 0x50 && header[1] == 0x4B && header[2] == 0x03 && header[3] == 0x04
}

// formatSize converts bytes to human readable format
func formatSize(bytes int64) string {
	if bytes >= 1024*1024*1024 {
		return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
	} else if bytes >= 1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	} else if bytes >= 1024 {
		return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%d B", bytes)
}

// SanitizeFilename cleans a filename to prevent security issues
func SanitizeFilename(filename string) string {
	// Take only base name to prevent directory traversal
	safe := filepath.Base(filename)
	// Remove any path separators that might have snuck through
	safe = strings.ReplaceAll(safe, "/", "")
	safe = strings.ReplaceAll(safe, "\\", "")
	return safe
}
