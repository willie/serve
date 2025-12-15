# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
# Build
go build

# Run locally (dev mode, port 8080)
go run . -local

# Run production (requires Tailscale auth, port 443)
go run .
```

## Architecture

This is a single-file Go application (`serve.go`) that serves files over Tailscale's private network using `tsnet`.

**Two modes:**
- **Production**: Uses `tsnet.Server` to join Tailnet, auto-provisions TLS via Let's Encrypt, listens on `:443`, logs Tailscale user identity per request
- **Local** (`-local`): Plain HTTP on configurable port (default `:8080`), no Tailscale, logs path only

**Key components:**
- `tsnet.Server` - Embeds Tailscale daemon as a library
- `logFilter` - Custom `io.Writer` that suppresses tsnet noise, throttles auth prompts
- `whoIs` - Resolves remote address to Tailscale user identity for access logging

**Tailscale state** is stored in `.serve/` directory (configurable via `-dir`).
