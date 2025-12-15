# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
go build           # Build binary
go run . -local    # Dev mode (HTTP, auto-opens browser)
go run .           # Prod mode (Tailscale HTTPS)
```

## Architecture

Single-file Go app (`serve.go`) serving files over Tailscale using `tsnet`.

**Two modes:**
- **Production**: `tsnet.Server` joins Tailnet, auto-provisions TLS, listens `:443`, logs user identity
- **Local** (`-local`): Plain HTTP on saved port (default `:8080`), logs path only

**Key components:**
- `tsnet.Server` - Embeds Tailscale daemon as library
- `logFilter` - Suppresses tsnet noise, throttles auth prompts
- `serveMarkdown` - Renders `.md` files as HTML (GFM), `?raw` for source
- `openBrowser` - Auto-opens browser when ready (macOS `open`)

**`.serve/` directory:**
- Tailscale state (prod mode)
- `port` - Remembered port for local mode
- `custom.css` - Optional CSS injected into markdown preview
