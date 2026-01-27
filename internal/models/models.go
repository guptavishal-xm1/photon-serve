package models

import "time"

// FileInfo represents a file in the storage
type FileInfo struct {
	Category  string `json:"category"`
	Filename  string `json:"filename"`
	Size      string `json:"size"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at"`
	Downloads int64  `json:"downloads"`
}

// UploadRequest represents an upload request
type UploadRequest struct {
	Category string
	Filename string
	Size     int64
}

// UploadResponse represents the response after upload
type UploadResponse struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Filename string `json:"filename,omitempty"`
	Category string `json:"category,omitempty"`
}

// CategoryInfo represents category details for API
type CategoryInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	MaxFiles    int    `json:"max_files"`
	FileCount   int    `json:"file_count"`
}

// ConfigResponse represents public configuration for frontend
type ConfigResponse struct {
	AppName     string         `json:"app_name"`
	AppTitle    string         `json:"app_title"`
	AppSubtitle string         `json:"app_subtitle"`
	DeviceName  string         `json:"device_name"`
	Categories  []CategoryInfo `json:"categories"`
	Text        TextMessages   `json:"text"`
}

// TextMessages contains all UI text messages
type TextMessages struct {
	UploadSuccess string `json:"upload_success"`
	UploadFailed  string `json:"upload_failed"`
	NoFilesFound  string `json:"no_files_found"`
	CopySuccess   string `json:"copy_success"`
	CopyFailed    string `json:"copy_failed"`
}

// HealthResponse for health check endpoint
type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Version   string    `json:"version"`
}

// ErrorResponse for standardized error responses
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Details string `json:"details,omitempty"`
}

// ListResponse wraps file list with metadata
type ListResponse struct {
	Files      []FileInfo `json:"files"`
	TotalCount int        `json:"total_count"`
}
