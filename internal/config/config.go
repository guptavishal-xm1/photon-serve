package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Config is the root configuration structure
type Config struct {
	Server      ServerConfig      `json:"server"`
	Storage     StorageConfig     `json:"storage"`
	Categories  map[string]Category `json:"categories"`
	Security    SecurityConfig    `json:"security"`
	Concurrency ConcurrencyConfig `json:"concurrency"`
	Text        TextConfig        `json:"text"`
	AllowedExts []string          `json:"allowed_extensions"`
	Logging     LoggingConfig     `json:"logging"`
}

type ServerConfig struct {
	Port                 string `json:"port"`
	ReadTimeoutMinutes   int    `json:"read_timeout_minutes"`
	WriteTimeoutMinutes  int    `json:"write_timeout_minutes"`
	IdleTimeoutSeconds   int    `json:"idle_timeout_seconds"`
	ShutdownTimeoutSecs  int    `json:"shutdown_timeout_seconds"`
}

type StorageConfig struct {
	UploadDir      string `json:"upload_dir"`
	TempDir        string `json:"temp_dir"`
	MaxUploadSizeGB int   `json:"max_upload_size_gb"`
	DirPermissions string `json:"dir_permissions"`
}

type Category struct {
	Enabled     bool   `json:"enabled"`
	MaxFiles    int    `json:"max_files"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

type SecurityConfig struct {
	APIKeyEnv     string          `json:"api_key_env"`
	DefaultAPIKey string          `json:"default_api_key"`
	RateLimit     RateLimitConfig `json:"rate_limit"`
}

type RateLimitConfig struct {
	Enabled           bool `json:"enabled"`
	RequestsPerMinute int  `json:"requests_per_minute"`
	BurstSize         int  `json:"burst_size"`
}

type ConcurrencyConfig struct {
	MaxConcurrentDownloads int `json:"max_concurrent_downloads"`
	MaxConcurrentUploads   int `json:"max_concurrent_uploads"`
	DownloadBufferSizeKB   int `json:"download_buffer_size_kb"`
	WorkerPoolSize         int `json:"worker_pool_size"`
}

type TextConfig struct {
	AppName       string `json:"app_name"`
	AppTitle      string `json:"app_title"`
	AppSubtitle   string `json:"app_subtitle"`
	DeviceName    string `json:"device_name"`
	AdminTitle    string `json:"admin_title"`
	UploadSuccess string `json:"upload_success"`
	UploadFailed  string `json:"upload_failed"`
	FileTooLarge  string `json:"file_too_large"`
	InvalidFile   string `json:"invalid_file"`
	Unauthorized  string `json:"unauthorized"`
	NoFilesFound  string `json:"no_files_found"`
	CopySuccess   string `json:"copy_success"`
	CopyFailed    string `json:"copy_failed"`
	ServerError   string `json:"server_error"`
}

type LoggingConfig struct {
	Level               string `json:"level"`
	Format              string `json:"format"`
	EnableRequestLogging bool  `json:"enable_request_logging"`
}

// Global config instance with thread-safe access
var (
	instance *Config
	once     sync.Once
	mu       sync.RWMutex
)

// Load reads the configuration from a JSON file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Override with environment variables
	cfg.applyEnvOverrides()

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	mu.Lock()
	instance = &cfg
	mu.Unlock()

	return &cfg, nil
}

// Get returns the current configuration (thread-safe)
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return instance
}

// applyEnvOverrides allows environment variables to override config values
func (c *Config) applyEnvOverrides() {
	// Port override
	if port := os.Getenv("PORT"); port != "" {
		c.Server.Port = port
	}

	// Upload directory override
	if uploadDir := os.Getenv("UPLOAD_DIR"); uploadDir != "" {
		c.Storage.UploadDir = uploadDir
	}

	// API Key from environment (required for production)
	if apiKey := os.Getenv(c.Security.APIKeyEnv); apiKey != "" {
		c.Security.DefaultAPIKey = apiKey
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Server.Port == "" {
		return fmt.Errorf("server port is required")
	}

	if c.Storage.UploadDir == "" {
		return fmt.Errorf("upload directory is required")
	}

	if len(c.Categories) == 0 {
		return fmt.Errorf("at least one category must be defined")
	}

	for name, cat := range c.Categories {
		if cat.MaxFiles < 1 {
			return fmt.Errorf("category %s must allow at least 1 file", name)
		}
	}

	if c.Concurrency.MaxConcurrentDownloads < 1 {
		c.Concurrency.MaxConcurrentDownloads = 100
	}

	if c.Concurrency.MaxConcurrentUploads < 1 {
		c.Concurrency.MaxConcurrentUploads = 20
	}

	return nil
}

// GetMaxUploadSize returns max upload size in bytes
func (c *Config) GetMaxUploadSize() int64 {
	return int64(c.Storage.MaxUploadSizeGB) * 1024 * 1024 * 1024
}

// GetEnabledCategories returns list of enabled category names
func (c *Config) GetEnabledCategories() []string {
	var cats []string
	for name, cat := range c.Categories {
		if cat.Enabled {
			cats = append(cats, name)
		}
	}
	return cats
}

// IsValidCategory checks if a category name is valid and enabled
func (c *Config) IsValidCategory(name string) bool {
	cat, exists := c.Categories[name]
	return exists && cat.Enabled
}

// IsAllowedExtension checks if file extension is allowed
func (c *Config) IsAllowedExtension(ext string) bool {
	for _, allowed := range c.AllowedExts {
		if allowed == ext {
			return true
		}
	}
	return false
}
