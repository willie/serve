// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// The tshello server demonstrates how to use Tailscale as a library.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

var (
	port     = flag.String("port", "8080", "port to listen on (local mode only)")
	hostname = flag.String("hostname", "", "hostname to use on tailnet")
	dataDir  = flag.String("dir", "./tsnet-state", "directory to store tailscale state")
	local    = flag.Bool("local", false, "run in local mode")
)

func main() {
	flag.Parse()

	// Globally filter logs to suppress tsnet noise
	log.SetOutput(new(logFilter))

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

	if *local {
		listenAddr = ":" + *port
		ln, err = net.Listen("tcp", listenAddr)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("serving . at http://localhost%s ...", listenAddr)
		whoIs = func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
			return &apitype.WhoIsResponse{
				UserProfile: &tailcfg.UserProfile{
					LoginName: "local-user",
				},
				Node: &tailcfg.Node{
					ComputedName: "localhost",
				},
			}, nil
		}
	} else {
		// Production mode always enforces :443
		listenAddr = ":443"

		if err := os.MkdirAll(*dataDir, 0700); err != nil {
			log.Fatal(err)
		}

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
			ctx := context.Background()
			for {
				st, err := lc.Status(ctx)
				if err == nil && st.BackendState == "Running" {
					dnsName := strings.TrimSuffix(st.Self.DNSName, ".")
					scheme := "https" // Always HTTPS in prod now
					portStr := ""     // Standard port, no need to show

					log.Printf("serving . at %s://%s%s", scheme, dnsName, portStr)
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()

		ln = tls.NewListener(ln, &tls.Config{
			GetCertificate: lc.GetCertificate,
		})
	}
	defer ln.Close()

	if !*local && listenAddr == ":443" {
		// (Removed old dead code comments)
	}

	// Serve the current directory with access logging
	fs := http.FileServer(http.Dir("."))
	log.Fatal(http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := whoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			// In local mode or error cases, handle gracefully
			log.Printf("access: unknown user (%v) %s", err, r.URL.Path)
		} else {
			log.Printf("access: %s (%s) %s",
				who.UserProfile.LoginName,
				firstLabel(who.Node.ComputedName),
				r.URL.Path)
		}
		fs.ServeHTTP(w, r)
	})))
}

type logFilter struct {
	mu       sync.Mutex
	lastAuth time.Time
	auth     bool
}

func (f *logFilter) Write(p []byte) (n int, err error) {
	s := string(p)
	// Whitelist specific messages
	if strings.Contains(s, "serving . at") ||
		strings.Contains(s, "access: ") ||
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
