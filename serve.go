// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// serve is a simple file server that runs on your Tailscale network.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"flag"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tsnet"
)

var (
	port     = flag.String("port", "8080", "port to listen on (local mode only)")
	hostname = flag.String("hostname", "", "hostname to use on tailnet")
	dataDir  = flag.String("dir", "./.serve", "directory to store tailscale state")
	local    = flag.Bool("local", false, "run in local mode")
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
)

var mdTemplate = template.Must(template.New("markdown").Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body {
	max-width: 800px;
	margin: 40px auto;
	padding: 0 20px;
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
	line-height: 1.6;
	color: #24292f;
}
@media (prefers-color-scheme: dark) {
	body { background: #0d1117; color: #c9d1d9; }
	a { color: #58a6ff; }
	code, pre { background: #161b22; }
	pre { border-color: #30363d; }
}
pre {
	background: #f6f8fa;
	padding: 16px;
	overflow-x: auto;
	border-radius: 6px;
	border: 1px solid #d0d7de;
}
code {
	background: #f6f8fa;
	padding: 0.2em 0.4em;
	border-radius: 3px;
	font-size: 85%;
}
pre code {
	background: none;
	padding: 0;
}
blockquote {
	border-left: 4px solid #d0d7de;
	margin: 0;
	padding-left: 16px;
	color: #656d76;
}
table {
	border-collapse: collapse;
	width: 100%;
}
th, td {
	border: 1px solid #d0d7de;
	padding: 8px 12px;
	text-align: left;
}
img { max-width: 100%; }
.raw-link {
	float: right;
	font-size: 14px;
	color: #656d76;
}
{{.CustomCSS}}
</style>
</head>
<body>
<a class="raw-link" href="?raw">View raw</a>
{{.Content}}
</body>
</html>
`))

var customCSS string // loaded from .serve/custom.css if present

func main() {
	flag.Parse()

	// Globally filter logs to suppress tsnet noise
	log.SetOutput(new(logFilter))

	// Ensure .serve directory exists
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		log.Fatal(err)
	}

	// Load custom CSS if present
	if css, err := os.ReadFile(filepath.Join(*dataDir, "custom.css")); err == nil {
		customCSS = string(css)
	}

	if *hostname == "" {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		*hostname = filepath.Base(wd)
	}

	var ln net.Listener
	var whoIs func(context.Context, string) (*apitype.WhoIsResponse, error)
	var err error
	var listenAddr string
	var serverURL string

	if *local {
		// Load saved port if not explicitly set
		portFile := filepath.Join(*dataDir, "port")
		if !isFlagSet("port") {
			if saved, err := os.ReadFile(portFile); err == nil {
				*port = strings.TrimSpace(string(saved))
			}
		}

		listenAddr = ":" + *port
		ln, err = net.Listen("tcp", listenAddr)
		if err != nil {
			log.Fatal(err)
		}

		// Save port for next time
		os.WriteFile(portFile, []byte(*port), 0600)

		serverURL = "http://localhost" + listenAddr
		log.Printf("%s at %s", prettyPath(), serverURL)
	} else {
		// Tailscale mode uses :443
		listenAddr = ":443"

		s := &tsnet.Server{
			Hostname: *hostname,
			Dir:      *dataDir,
			// We rely on the global log filter to catch tsnet logs
		}
		defer s.Close()
		ln, err = s.Listen("tcp", listenAddr)
		if err != nil {
			log.Fatal(err)
		}

		lc, err := s.LocalClient()
		if err != nil {
			log.Fatal(err)
		}
		whoIs = lc.WhoIs

		go func() {
			// Wait for the backend to be running to print the URL
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			for {
				st, err := lc.Status(ctx)
				if err == nil && st.BackendState == "Running" {
					dnsName := strings.TrimSuffix(st.Self.DNSName, ".")
					serverURL = "https://" + dnsName
					log.Printf("%s at %s", prettyPath(), serverURL)
					openBrowser(serverURL)
					return
				}
				select {
				case <-ctx.Done():
					log.Printf("timeout waiting for tailscale to start")
					return
				case <-time.After(500 * time.Millisecond):
				}
			}
		}()

		ln = tls.NewListener(ln, &tls.Config{
			GetCertificate: lc.GetCertificate,
		})
	}
	defer ln.Close()

	// Open browser for local mode (Tailscale mode does it after ready)
	if *local {
		openBrowser(serverURL)
	}

	// Serve the current directory with access logging
	fs := http.FileServer(http.Dir("."))
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if *local {
				log.Print(r.URL.Path)
			} else {
				who, err := whoIs(r.Context(), r.RemoteAddr)
				if err != nil {
					log.Printf("? %s", r.URL.Path)
				} else {
					log.Printf("%s (%s) %s",
						who.UserProfile.LoginName,
						firstLabel(who.Node.ComputedName),
						r.URL.Path)
				}
			}

			// Render markdown files as HTML unless ?raw is requested
			if serveMarkdown(w, r, r.URL.Path) {
				return
			}
			fs.ServeHTTP(w, r)
		}),
	}

	// Graceful shutdown on interrupt
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Printf("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type logFilter struct {
	mu       sync.Mutex
	lastAuth time.Time
	auth     bool
}

func (f *logFilter) Write(p []byte) (n int, err error) {
	s := string(p)

	// Access log lines after timestamp (20 chars: "2006/01/02 15:04:05 ")
	// Local: "/path"
	// Tailscale: "user (device) /path" or "? /path"
	if len(s) > 20 {
		msg := s[20:]
		if msg[0] == '/' || // Local access
			strings.HasPrefix(msg, "? ") || // Unknown user
			strings.Contains(msg, ") /") { // Tailscale access
			return os.Stderr.Write(p)
		}
	}

	// Whitelist specific messages
	if strings.Contains(s, " at http") ||
		strings.Contains(s, "bind: ") || // Allow startup errors
		strings.Contains(s, "error") ||
		strings.Contains(s, "fail") {
		return os.Stderr.Write(p)
	}

	// Throttle auth prompt
	if strings.Contains(s, "To start this tsnet server") {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.auth || time.Since(f.lastAuth) > 1*time.Minute {
			f.lastAuth = time.Now()
			f.auth = true
			return os.Stderr.Write(p)
		}
		return len(p), nil
	}

	// Silence everything else
	return len(p), nil
}

func firstLabel(s string) string {
	s, _, _ = strings.Cut(s, ".")
	return s
}

func prettyPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return wd
	}
	if strings.HasPrefix(wd, home) {
		return "~" + strings.TrimPrefix(wd, home)
	}
	return wd
}

func isFlagSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func openBrowser(url string) {
	// macOS
	exec.Command("open", url).Start()
}

func serveMarkdown(w http.ResponseWriter, r *http.Request, path string) bool {
	// Only handle .md files
	if !strings.HasSuffix(strings.ToLower(path), ".md") {
		return false
	}

	// Serve raw if requested
	if r.URL.Query().Has("raw") {
		return false
	}

	// Clean path and prevent directory traversal
	clean := filepath.Clean(strings.TrimPrefix(path, "/"))
	if strings.HasPrefix(clean, "..") {
		return false
	}

	content, err := os.ReadFile(clean)
	if err != nil {
		return false // Let file server handle the error
	}

	var buf bytes.Buffer
	if err := md.Convert(content, &buf); err != nil {
		http.Error(w, "failed to render markdown", http.StatusInternalServerError)
		return true
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	mdTemplate.Execute(w, struct {
		Title     string
		Content   template.HTML
		CustomCSS template.CSS
	}{
		Title:     filepath.Base(path),
		Content:   template.HTML(buf.String()),
		CustomCSS: template.CSS(customCSS),
	})
	return true
}
