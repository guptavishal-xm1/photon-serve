package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rom-server/internal/config"
	"rom-server/internal/handlers"
	"rom-server/internal/middleware"
	"rom-server/internal/services"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "config.json", "Path to configuration file")
	flag.Parse()

	// Initialize logger
	logger := log.New(os.Stdout, "", log.LstdFlags)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	// Update logger format from config
	logger.SetPrefix(cfg.Logging.Format)

	// Security warning for default API key
	if cfg.Security.DefaultAPIKey == "changeme" {
		logger.Println("WARNING: Using default API Key! Set API_KEY environment variable for production.")
	}

	// Initialize services
	fileService := services.NewFileService(cfg)
	
	// Initialize storage directories
	if err := fileService.InitializeStorage(); err != nil {
		logger.Fatalf("Failed to initialize storage: %v", err)
	}

	// Initialize handlers
	h := handlers.NewHandlers(cfg, fileService, logger)

	// Create auth middleware
	authMiddleware := middleware.Auth(cfg, logger)

	// Setup router
	mux := http.NewServeMux()

	// Public endpoints
	mux.HandleFunc("/", serveStaticFile("static/download.html"))
	mux.HandleFunc("/admin", serveStaticFile("static/index.html"))
	mux.HandleFunc("/health", h.Health)
	mux.HandleFunc("/api/config", h.GetConfig)
	mux.HandleFunc("/list", h.ListFiles)
	
	// Static assets (favicon, images, etc.)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/favicon.png")
	})
	
	// Protected endpoints (require API key)
	mux.HandleFunc("/upload", authMiddleware(h.Upload))
	mux.HandleFunc("/delete", authMiddleware(h.Delete))

	// File downloads with concurrency control
	mux.Handle("/downloads/", h.ServeDownload(cfg.Storage.UploadDir))

	// Apply middleware chain
	var handler http.Handler = mux
	handler = middleware.CORS(handler)
	handler = middleware.RateLimit(cfg, logger)(handler)
	handler = middleware.RequestLogger(logger, cfg.Logging.EnableRequestLogging)(handler)
	handler = middleware.SecurityHeaders(handler)

	// Configure server with optimized settings for concurrent users
	srv := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           handler,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutMinutes) * time.Minute,
		WriteTimeout:      time.Duration(cfg.Server.WriteTimeoutMinutes) * time.Minute,
		IdleTimeout:       time.Duration(cfg.Server.IdleTimeoutSeconds) * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB max header size
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start server in background
	go func() {
		logger.Printf("Server starting on :%s", cfg.Server.Port)
		logger.Printf("Storage path: %s", cfg.Storage.UploadDir)
		logger.Printf("Max concurrent downloads: %d", cfg.Concurrency.MaxConcurrentDownloads)
		logger.Printf("Max concurrent uploads: %d", cfg.Concurrency.MaxConcurrentUploads)
		
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Server error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.Server.ShutdownTimeoutSecs)*time.Second,
	)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatalf("Server forced to shutdown: %v", err)
	}

	logger.Println("Server exited cleanly")
}

// serveStaticFile returns a handler that serves a specific static file
func serveStaticFile(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/admin" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}
}
