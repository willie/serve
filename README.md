# serve

A simple HTTP file server for your Tailscale network. Share files from any directory with automatic HTTPS and access logging.

## Features

- **Tailscale integration**: Accessible only on your tailnet with automatic HTTPS via MagicDNS
- **Markdown preview**: Renders `.md` files as HTML with GitHub styling (`?raw` for source)
- **Access logging**: Logs which Tailscale user accessed which file
- **Custom CSS**: Drop `custom.css` in `.serve/` to customize markdown styling
- **Local mode**: Run without Tailscale for testing (`-local`)

## Installation

```bash
go install github.com/willie/serve@latest
```

## Usage

### Tailscale

```bash
serve
```

Serves the current directory at `https://<hostname>.<tailnet>.ts.net`. On first run, authenticate via the printed URL.

**Options:**
- `-hostname <name>`: Hostname on your tailnet (default: directory name)
- `-dir <path>`: State directory (default `./.serve`)

### Local

```bash
serve -local
```

Serves at `http://localhost:8080`. Port is remembered for next run.

**Options:**
- `-port <number>`: Port to listen on (default: `8080`, or last used port)
