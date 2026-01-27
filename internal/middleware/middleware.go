package middleware

import (
	"crypto/subtle"
	"log"
	"net/http"
	"sync"
	"time"

	"rom-server/internal/config"
)

// SecurityHeaders adds security headers to all responses
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// Auth creates an authentication middleware
func Auth(cfg *config.Config, logger *log.Logger) func(http.HandlerFunc) http.HandlerFunc {
	apiKey := cfg.Security.DefaultAPIKey

	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Get key from header (preferred) or query parameter (never read body)
			userKey := r.Header.Get("X-API-Key")
			if userKey == "" {
				userKey = r.URL.Query().Get("key")
			}

			// Constant time comparison to prevent timing attacks
			if subtle.ConstantTimeCompare([]byte(userKey), []byte(apiKey)) != 1 {
				if logger != nil {
					logger.Printf("Unauthorized access attempt from %s", r.RemoteAddr)
				}
				http.Error(w, cfg.Text.Unauthorized, http.StatusUnauthorized)
				return
			}

			next(w, r)
		}
	}
}

// RateLimiter implements a token bucket rate limiter
type RateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*clientBucket
	rate     int           // Tokens per interval
	burst    int           // Max burst size
	interval time.Duration // Token refill interval
	cleanup  time.Duration // Cleanup interval for old entries
}

type clientBucket struct {
	tokens     int
	lastRefill time.Time
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(requestsPerMinute, burstSize int) *RateLimiter {
	rl := &RateLimiter{
		clients:  make(map[string]*clientBucket),
		rate:     requestsPerMinute,
		burst:    burstSize,
		interval: time.Minute,
		cleanup:  5 * time.Minute,
	}

	// Start cleanup goroutine
	go rl.cleanupLoop()

	return rl
}

// Allow checks if a request from the given IP should be allowed
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	bucket, exists := rl.clients[ip]

	if !exists {
		rl.clients[ip] = &clientBucket{
			tokens:     rl.burst - 1, // Use one token for this request
			lastRefill: now,
		}
		return true
	}

	// Refill tokens based on time passed
	elapsed := now.Sub(bucket.lastRefill)
	tokensToAdd := int(elapsed.Minutes() * float64(rl.rate))
	
	if tokensToAdd > 0 {
		bucket.tokens = min(bucket.tokens+tokensToAdd, rl.burst)
		bucket.lastRefill = now
	}

	// Check if we have tokens available
	if bucket.tokens > 0 {
		bucket.tokens--
		return true
	}

	return false
}

// cleanupLoop removes old entries periodically
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-rl.cleanup)
		for ip, bucket := range rl.clients {
			if bucket.lastRefill.Before(cutoff) {
				delete(rl.clients, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimit creates a rate limiting middleware
func RateLimit(cfg *config.Config, logger *log.Logger) func(http.Handler) http.Handler {
	if !cfg.Security.RateLimit.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}

	limiter := NewRateLimiter(
		cfg.Security.RateLimit.RequestsPerMinute,
		cfg.Security.RateLimit.BurstSize,
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := getClientIP(r)
			
			if !limiter.Allow(ip) {
				if logger != nil {
					logger.Printf("Rate limit exceeded for %s", ip)
				}
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger logs all incoming requests
func RequestLogger(logger *log.Logger, enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !enabled {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			
			// Wrap response writer to capture status code
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			
			next.ServeHTTP(wrapped, r)

			logger.Printf("%s %s %d %s %s",
				r.Method,
				r.URL.Path,
				wrapped.statusCode,
				time.Since(start),
				getClientIP(r),
			)
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// CORS adds CORS headers for API endpoints
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// getClientIP extracts the client IP from request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for reverse proxy)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	// Fall back to RemoteAddr
	return r.RemoteAddr
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
