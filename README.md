# ROM Server - Production Ready

A loosely coupled, production-ready ROM distribution server built with Go.

## Project Structure

```
├── cmd/
│   └── server/
│       └── main.go           # Application entry point
├── internal/
│   ├── config/
│   │   └── config.go         # Configuration management
│   ├── handlers/
│   │   └── handlers.go       # HTTP handlers
│   ├── middleware/
│   │   └── middleware.go     # Auth, rate limiting, logging
│   ├── models/
│   │   └── models.go         # Data models & DTOs
│   └── services/
│       └── file_service.go   # Business logic & file operations
├── static/
│   ├── download.html         # Public download page
│   └── index.html            # Admin upload page
├── config.json               # Configuration file (customize this!)
├── go.mod                    # Go module definition
└── README.md                 # This file
```

## Features

### ✅ Loose Coupling
- **Config Package**: All settings externalized to `config.json`
- **Service Layer**: Business logic separated from HTTP handlers
- **Middleware Stack**: Auth, rate limiting, logging as composable middleware
- **Handlers**: Clean HTTP handlers with dependency injection

### ✅ Configurable File Limits
Edit `config.json` to control how many files each category can hold:

```json
"categories": {
  "vanilla": {
    "enabled": true,
    "max_files": 3,  // ← Change this!
    "display_name": "Vanilla (Pure)"
  }
}
```

### ✅ Optimized for 100+ Concurrent Users
- **Semaphore-based concurrency control** for uploads and downloads
- **Rate limiting** with token bucket algorithm
- **Connection pooling** via Go's http.Server
- **Configurable worker pools**

### ✅ External Text Configuration
All UI text is configurable in `config.json`:

```json
"text": {
  "app_name": "Lunaris AOSP",
  "app_title": "Lunaris AOSP — Downloads",
  "upload_success": "Upload successful",
  "no_files_found": "No builds found"
}
```

## Quick Start

### 1. Build
```bash
go build -o rom-server ./cmd/server
```

### 2. Configure
Edit `config.json` to customize:
- Server port and timeouts
- File storage location
- Category names and file limits
- Rate limiting settings
- All UI text strings

### 3. Set API Key (Required for Production!)
```bash
# Linux/Mac
export API_KEY="your-secure-api-key-here"

# Windows PowerShell
$env:API_KEY = "your-secure-api-key-here"
```

### 4. Run
```bash
./rom-server -config config.json
```

## Configuration Reference

### Server Settings
| Setting | Default | Description |
|---------|---------|-------------|
| `server.port` | `8080` | HTTP port |
| `server.read_timeout_minutes` | `60` | Max time for request body read |
| `server.write_timeout_minutes` | `60` | Max time for response write |
| `server.shutdown_timeout_seconds` | `30` | Graceful shutdown timeout |

### Concurrency Settings
| Setting | Default | Description |
|---------|---------|-------------|
| `concurrency.max_concurrent_downloads` | `100` | Max simultaneous downloads |
| `concurrency.max_concurrent_uploads` | `20` | Max simultaneous uploads |
| `concurrency.worker_pool_size` | `50` | Worker pool size |

### Rate Limiting
| Setting | Default | Description |
|---------|---------|-------------|
| `security.rate_limit.enabled` | `true` | Enable rate limiting |
| `security.rate_limit.requests_per_minute` | `60` | Requests allowed per minute |
| `security.rate_limit.burst_size` | `10` | Burst allowance |

## API Endpoints

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/` | No | Public download page |
| GET | `/admin` | No | Admin upload page |
| GET | `/health` | No | Health check |
| GET | `/api/config` | No | Get public configuration |
| GET | `/list` | No | List all files |
| POST | `/upload` | Yes | Upload a file |
| DELETE | `/delete?category=X&filename=Y` | Yes | Delete a file |
| GET | `/downloads/{category}/{filename}` | No | Download a file |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `API_KEY` | Admin API key (REQUIRED in production) |
| `PORT` | Override server port |
| `UPLOAD_DIR` | Override upload directory |

## Production Deployment

### Systemd Service (Linux)
```ini
[Unit]
Description=ROM Server
After=network.target

[Service]
Type=simple
User=romserver
Environment=API_KEY=your-secure-key
ExecStart=/opt/rom-server/rom-server -config /opt/rom-server/config.json
Restart=always

[Install]
WantedBy=multi-user.target
```

### Nginx Reverse Proxy
```nginx
server {
    listen 80;
    server_name downloads.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        
        # Large file uploads
        client_max_body_size 5G;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

## Adding New Categories

1. Edit `config.json`:
```json
"categories": {
  "vanilla": { ... },
  "gapps": { ... },
  "kernels": {               // ← Add new category
    "enabled": true,
    "max_files": 5,
    "display_name": "Custom Kernels",
    "description": "Custom kernel builds"
  }
}
```

2. Restart the server - directories are created automatically!

## License

MIT
