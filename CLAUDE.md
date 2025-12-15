# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
go build           # Build binary
go run . -local    # Local HTTP server
go run .           # Tailscale HTTPS server
```

## Architecture

Single-file Go app (`serve.go`) serving files via HTTP, optionally over Tailscale.

**Two modes:**
- **Tailscale** (default): `tsnet.Server` joins tailnet, auto-provisions TLS on `:443`, logs user identity
- **Local** (`-local`): Plain HTTP on saved port (default `:8080`), logs path only

**Key components:**
- `tsnet.Server` - Embeds Tailscale daemon as library
- `logFilter` - Suppresses tsnet noise, throttles auth prompts
- `serveMarkdown` - Renders `.md` files as HTML (GFM), `?raw` for source
- `openBrowser` - Auto-opens browser when ready (macOS `open`)

**`.serve/` directory:**
- Tailscale state
- `port` - Remembered port for local mode
- `custom.css` - Optional CSS injected into markdown preview
