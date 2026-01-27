// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// serve is a simple file server that runs on your Tailscale network.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"flag"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tsnet"
)

//go:embed markdown.css
var markdownCSS string

var (
	port     = flag.String("port", "8080", "port to listen on (local mode only)")
	hostname = flag.String("hostname", "", "hostname to use on tailnet")
	dataDir  = flag.String("dir", "./.serve", "directory to store tailscale state")
	local    = flag.Bool("local", false, "run in local mode")
	ts       = flag.Bool("ts", false, "run in Tailscale mode")
	proxy    = flag.String("proxy", "", "proxy requests to this URL (e.g. http://127.0.0.1:8000)")
	index    = flag.String("index", "README.md", "default file to serve for directories (empty to disable)")
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
{{.BaseCSS}}
.markdown-body {
	box-sizing: border-box;
	min-width: 200px;
	max-width: 980px;
	margin: 0 auto;
	padding: 45px;
}
@media (max-width: 767px) {
	.markdown-body { padding: 15px; }
}
.controls {
	float: right;
	font-size: 14px;
}
.controls a {
	color: var(--fgColor-muted, #656d76);
	margin-left: 16px;
}
{{.CustomCSS}}
</style>
</head>
<body class="markdown-body">
<div class="controls">
<a href="{{.BrowsePath}}">Browse</a>
<a href="?raw">View raw</a>
</div>
{{.Content}}
</body>
</html>
`))

var customCSS string // loaded from .serve/custom.css if present

func main() {
	flag.Parse()
	ensureGitignore()

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

	// Load saved preferences if not explicitly set
	proxyFile := filepath.Join(*dataDir, "proxy")
	indexCfgFile := filepath.Join(*dataDir, "index")
	portFile := filepath.Join(*dataDir, "port")
	if !isFlagSet("proxy") {
		if saved, err := os.ReadFile(proxyFile); err == nil {
			*proxy = strings.TrimSpace(string(saved))
		}
	}
	if !isFlagSet("index") {
		if saved, err := os.ReadFile(indexCfgFile); err == nil {
			*index = strings.TrimSpace(string(saved))
		}
	}

	// Determine mode: local vs Tailscale
	// Priority: -local flag > -ts flag > existing TS state > existing port config > default local
	useLocalMode := false
	switch {
	case *local:
		useLocalMode = true
	case *ts:
		useLocalMode = false
	case hasTailscaleState(*dataDir):
		useLocalMode = false
	case hasLocalConfig(*dataDir):
		useLocalMode = true
	default:
		useLocalMode = true // Default to local mode
	}

	var ln net.Listener
	var whoIs func(context.Context, string) (*apitype.WhoIsResponse, error)
	var err error
	var listenAddr string
	var serverURL string

	desc := prettyPath()
	if *proxy != "" {
		desc = "proxy to " + *proxy
	}

	if useLocalMode {
		// Load saved port if not explicitly set
		if !isFlagSet("port") {
			if saved, err := os.ReadFile(portFile); err == nil {
				*port = strings.TrimSpace(string(saved))
			}
		}

		// Determine if we should save port (only when explicitly set or port config exists)
		savePort := isFlagSet("port") || hasLocalConfig(*dataDir)

		if !savePort {
			// Dynamic port finding for unconfigured local mode
			*port, ln, err = findAvailablePort()
			if err != nil {
				log.Fatal(err)
			}
		} else {
			listenAddr = ":" + *port
			ln, err = net.Listen("tcp", listenAddr)
			if err != nil {
				log.Fatal(err)
			}
		}
		listenAddr = ":" + *port

		// Save preferences only when explicitly set
		if isFlagSet("port") {
			os.WriteFile(portFile, []byte(*port), 0600)
		}
		if isFlagSet("proxy") {
			os.WriteFile(proxyFile, []byte(*proxy), 0600)
		}
		if isFlagSet("index") {
			os.WriteFile(indexCfgFile, []byte(*index), 0600)
		}

		serverURL = "http://localhost" + listenAddr
		log.Printf("%s at %s", desc, serverURL)
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
					log.Printf("%s at %s", desc, serverURL)
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

		// Save preferences only when explicitly set
		if isFlagSet("proxy") {
			os.WriteFile(proxyFile, []byte(*proxy), 0600)
		}
		if isFlagSet("index") {
			os.WriteFile(indexCfgFile, []byte(*index), 0600)
		}
	}
	defer ln.Close()

	// Open browser for local mode (Tailscale mode does it after ready)
	if useLocalMode {
		openBrowser(serverURL)
	}

	// Serve the current directory or proxy with access logging
	var rp *httputil.ReverseProxy
	if *proxy != "" {
		u, err := url.Parse(*proxy)
		if err != nil {
			log.Fatal(err)
		}
		rp = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(u)
				// pr.SetXForwarded() is NOT called to keep it "pure"
				// Additionally strip any proxy headers that might have been present in the original request
				pr.Out.Header.Del("X-Forwarded-For")
				pr.Out.Header.Del("X-Forwarded-Host")
				pr.Out.Header.Del("X-Forwarded-Proto")
			},
		}
	}
	fs := http.FileServer(http.Dir("."))
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if useLocalMode {
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

			if rp != nil {
				rp.ServeHTTP(w, r)
				return
			}

			path := r.URL.Path

			// Serve index file for directory requests unless ?list is present
			if strings.HasSuffix(path, "/") && *index != "" && !r.URL.Query().Has("list") {
				indexPath := filepath.Join(".", path, *index)
				if info, err := os.Stat(indexPath); err == nil && !info.IsDir() {
					path = filepath.Join(path, *index)
				}
			}

			// Render markdown files as HTML unless ?raw is requested
			if serveMarkdown(w, r, path) {
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
		if time.Since(f.lastAuth) > time.Minute {
			f.lastAuth = time.Now()
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
	if after, ok := strings.CutPrefix(wd, home); ok {
		return "~" + after
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

func hasTailscaleState(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, "tailscaled.state"))
	return err == nil
}

func hasLocalConfig(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, "port"))
	return err == nil
}

func findAvailablePort() (string, net.Listener, error) {
	for p := 8080; p < 9000; p++ {
		addr := ":" + strconv.Itoa(p)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return strconv.Itoa(p), ln, nil
		}
	}
	// Let OS pick if all ports busy
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", nil, err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port, ln, nil
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

	// Compute browse path
	dir := filepath.Dir(path)
	if dir == "." || dir == "/" {
		dir = ""
	}
	var browsePath string
	if *index != "" && filepath.Base(path) == *index {
		// Current file is the index file, browse shows directory listing
		browsePath = dir + "/?list"
	} else if *index != "" {
		// Check if index file exists in this directory
		indexPath := filepath.Join(".", dir, *index)
		if info, err := os.Stat(indexPath); err == nil && !info.IsDir() {
			browsePath = dir + "/" + *index
		} else {
			browsePath = dir + "/"
		}
	} else {
		browsePath = dir + "/"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	mdTemplate.Execute(w, struct {
		Title      string
		BaseCSS    template.CSS
		Content    template.HTML
		CustomCSS  template.CSS
		BrowsePath string
	}{
		Title:      filepath.Base(path),
		BaseCSS:    template.CSS(markdownCSS),
		Content:    template.HTML(buf.String()),
		CustomCSS:  template.CSS(customCSS),
		BrowsePath: browsePath,
	})
	return true
}

func ensureGitignore() {
	const entry = ".serve/"
	const comment = "# Comment the line below if you really want to commit .serve/"

	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		return // Not a git repo
	}

	data, err := os.ReadFile(".gitignore")
	if err != nil && !os.IsNotExist(err) {
		return
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == entry || (strings.HasPrefix(line, "#") && strings.TrimSpace(strings.TrimPrefix(line, "#")) == entry) {
			found = true
			break
		}
	}

	if found {
		return
	}

	// Append to .gitignore
	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	if len(data) > 0 && data[len(data)-1] != '\n' {
		f.WriteString("\n")
	}
	f.WriteString("\n" + comment + "\n" + entry + "\n")
}
