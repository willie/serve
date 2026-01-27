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

**Flags:**
- `-local` - Force local HTTP mode
- `-port` - Port for local mode (remembered in `.serve/port`)
- `-hostname` - Tailnet hostname (default: current directory name)
- `-proxy` - Reverse proxy to URL instead of serving files
- `-index` - Default file for directories (default: `README.md`, empty to disable)
- `-dir` - State directory (default: `.serve`)

**Key behaviors:**
- `serveMarkdown` - Renders `.md` files as HTML (GFM), `?raw` for source, `?list` for directory listing when index is set
- `ensureGitignore` - Auto-adds `.serve/` to `.gitignore` if in a git repo
- `logFilter` - Suppresses tsnet noise, throttles auth prompts
- `openBrowser` - Auto-opens browser when ready (macOS `open`)

**`.serve/` directory:**
- Tailscale state (`tailscaled.state`)
- `port` - Remembered port for local mode
- `proxy` - Remembered proxy URL
- `index` - Remembered index file setting
- `custom.css` - Optional CSS injected into markdown preview
