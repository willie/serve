# serve

`serve` is a simple, secure file server built on [Tailscale](https://tailscale.com) and Go. It allows you to easily share files from your local machine to your private Tailscale network.

## Features

- **Private & Secure**: Accessible only over your Tailscale network.
- **Automatic HTTPS**: Provisions valid SSL certificates automatically via Let's Encrypt + Tailscale MagicDNS.
- **Zero Configuration**: Defaults to serving the current directory.
- **Access Logging**: Logs which Tailscale user accessed which file.
- **Local Dev Mode**: Includes a development mode for testing without Tailscale.

## Installation

```bash
go install github.com/willie/serve@latest
```

## Usage

### Production (Tailscale)

Run the server in the directory you want to share:

```bash
# Serves the current directory on https://<machine-name>.<tailnet>.ts.net
serve
```

On the first run, it will print a Tailscale authentication URL. Once authenticated, it will bind to port `:443` and print your MagicDNS URL.

**Options:**

- `-hostname <name>`: Hostname on your tailnet (default: current directory name).
- `-dir <path>`: Directory to store Tailscale state (default `./tsnet-state`).

### Local Mode

Run without connecting to Tailscale:

```bash
serve -local
```

This binds to `:8080` by default (to distinguish from prod `:443`) and uses a placeholder identity for access logs.

**Local Options:**

- `-local`: Enable local mode.
- `-port <number>`: Port to listen on (default `8080`, local mode only).
