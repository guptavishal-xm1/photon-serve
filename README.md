# Lunaris AOSP — ROM Download Portal

A lightweight, production-ready download portal for custom Android ROM builds with secure upload capabilities.

## Features

- **Dark theme** with modern UI
- **Mobile responsive** design
- **Search & filter** by category (Vanilla/GApps)
- **Zero-copy downloads** using Go's native file server
- **Secure uploads** with API key authentication
- **Automatic latest release** detection
- **Single-file HTML** portal (no build tools)

## Quick Start

```bash
# Clone and build
git clone <your-repo>
cd rom-ota
go build -o rom-server main.go

# Set environment (optional)
export PORT=8080
export API_KEY=your-secret-key

# Run
./rom-server
```

Visit `http://localhost:8080/` for public downloads  
Visit `http://localhost:8080/admin` for uploads

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Public download portal |
| `/admin` | GET | Upload dashboard |
| `/list` | GET | JSON list of all builds |
| `/upload` | POST | Upload new build (requires API key) |
| `/downloads/{category}/{file}` | GET | Download ROM file |

## Deployment

**Environment Variables:**
- `PORT` — Server port (default: `8080`)
- `API_KEY` — Required for uploads (default: `changeme`)
- `UPLOAD_DIR` — Storage path (default: `uploads`)

**Systemd example:**
```ini
[Unit]
Description=ROM Download Portal

[Service]
ExecStart=/path/to/rom-server
Environment="PORT=8080"
Environment="API_KEY=<secure-key>"
Restart=always

[Install]
WantedBy=multi-user.target
```

## Tech Stack

- **Backend:** Go (stdlib only)
- **Frontend:** Vanilla JS + Tailwind CSS (CDN)
- **Storage:** Local filesystem
