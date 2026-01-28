# serve

A simple HTTP file server for quick local sharing or your Tailscale network. Automatic HTTPS, markdown preview, and access logging.

## Features

- **Zero config**: Just run `serve` to share the current directory
- **Markdown preview**: Renders `.md` files as HTML with GitHub styling (`?raw` for source)
- **Tailscale integration**: Accessible only on your tailnet with automatic HTTPS
- **Access logging**: Logs requests (with Tailscale user identity when applicable)
- **Custom CSS**: Drop `custom.css` in `.serve/` to customize markdown styling

## Installation

```bash
go install github.com/willie/serve@latest
```

## Usage

```bash
serve              # Local mode, finds available port
serve -port 9000   # Local mode on port 9000 (remembered)
serve -ts          # Tailscale mode
```

### Mode detection

With no flags, `serve` auto-detects based on existing config:
1. `.serve/tailscaled.state` exists → Tailscale mode
2. `.serve/port` exists → Local mode with saved port
3. Neither exists → Local mode, finds available port starting at 8080

Use `-local` or `-ts` to override auto-detection.

### Flags

| Flag | Description |
|------|-------------|
| `-local` | Force local HTTP mode |
| `-ts` | Force Tailscale mode |
| `-port <n>` | Port for local mode (saves to `.serve/port`) |
| `-hostname <name>` | Tailnet hostname (default: directory name) |
| `-proxy <url>` | Reverse proxy to URL instead of serving files |
| `-index <file>` | Default file for directories (default: `README.md`, empty to disable) |
| `-dir <path>` | State directory (default: `.serve`) |

### Markdown

When `-index` is set (default `README.md`), directory requests serve the index file if present. Use `?list` to see the directory listing, or `?raw` to view markdown source.

### Tailscale

On first run in Tailscale mode, authenticate via the printed URL. The server will be available at `https://<hostname>.<tailnet>.ts.net`.
