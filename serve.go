// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// serve is a simple file server that runs on your Tailscale network.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tsnet"
)

var (
	port     = flag.String("port", "8080", "port to listen on (local mode only)")
	hostname = flag.String("hostname", "", "hostname to use on tailnet")
	dataDir  = flag.String("dir", "./.serve", "directory to store tailscale state")
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
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			for {
				st, err := lc.Status(ctx)
				if err == nil && st.BackendState == "Running" {
					dnsName := strings.TrimSuffix(st.Self.DNSName, ".")
					log.Printf("serving . at https://%s", dnsName)
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

	// Serve the current directory with access logging
	fs := http.FileServer(http.Dir("."))
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if *local {
				log.Printf("access: %s", r.URL.Path)
				fs.ServeHTTP(w, r)
				return
			}

			who, err := whoIs(r.Context(), r.RemoteAddr)
			if err != nil {
				log.Printf("access: unknown user (%v) %s", err, r.URL.Path)
			} else {
				log.Printf("access: %s (%s) %s",
					who.UserProfile.LoginName,
					firstLabel(who.Node.ComputedName),
					r.URL.Path)
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
