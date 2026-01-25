// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirstLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"foo.bar.baz", "foo"},
		{"hostname", "hostname"},
		{"", ""},
		{"a.b", "a"},
		{".leading", ""},
		{"trailing.", "trailing"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := firstLabel(tt.input)
			if got != tt.want {
				t.Errorf("firstLabel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPrettyPath(t *testing.T) {
	// Save and restore working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// Test with a path under home directory
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home directory")
	}

	// Create a temp dir under home
	tmpDir, err := os.MkdirTemp(home, "serve-test-")
	if err != nil {
		t.Skip("cannot create temp dir under home")
	}
	defer os.RemoveAll(tmpDir)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	got := prettyPath()
	if !strings.HasPrefix(got, "~") {
		t.Errorf("prettyPath() = %q, want path starting with ~", got)
	}

	// Test with a path not under home (use /tmp or similar)
	systemTmp := os.TempDir()
	// Only test if system tmp is not under home
	if !strings.HasPrefix(systemTmp, home) {
		tmpDir2, err := os.MkdirTemp("", "serve-test-outside-")
		if err == nil {
			defer os.RemoveAll(tmpDir2)
			if err := os.Chdir(tmpDir2); err == nil {
				got := prettyPath()
				if strings.HasPrefix(got, "~") {
					t.Errorf("prettyPath() = %q, should not start with ~ for path outside home", got)
				}
			}
		}
	}
}

func TestIsFlagSet(t *testing.T) {
	// Create a new FlagSet to avoid polluting the global flags
	oldCommandLine := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	defer func() { flag.CommandLine = oldCommandLine }()

	testFlag := flag.String("testflag", "default", "test flag")
	otherFlag := flag.String("otherflag", "default", "other flag")

	// Parse with only testflag set
	flag.CommandLine.Parse([]string{"-testflag=value"})

	if !isFlagSet("testflag") {
		t.Error("isFlagSet(\"testflag\") = false, want true")
	}

	if isFlagSet("otherflag") {
		t.Error("isFlagSet(\"otherflag\") = true, want false")
	}

	if isFlagSet("nonexistent") {
		t.Error("isFlagSet(\"nonexistent\") = true, want false")
	}

	// Use the flags to avoid unused variable errors
	_ = testFlag
	_ = otherFlag
}

func TestLogFilter(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldPass bool
	}{
		// Access logs (should pass)
		{"local access log", "2006/01/02 15:04:05 /path/to/file", true},
		{"unknown user access", "2006/01/02 15:04:05 ? /path/to/file", true},
		{"tailscale access", "2006/01/02 15:04:05 user@example.com (device) /path", true},

		// Whitelisted messages (should pass)
		{"server URL http", "2006/01/02 15:04:05 serving at http://localhost:8080", true},
		{"server URL https", "2006/01/02 15:04:05 serving at https://host.tail.ts.net", true},
		{"bind error", "2006/01/02 15:04:05 bind: address already in use", true},
		{"error message", "2006/01/02 15:04:05 some error occurred", true},
		{"fail message", "2006/01/02 15:04:05 failed to connect", true},

		// Suppressed messages (should not pass)
		{"tsnet noise", "2006/01/02 15:04:05 netcheck: UDP is blocked", false},
		{"random log", "2006/01/02 15:04:05 some random log message", false},
		{"short message", "short", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &logFilter{}
			var buf bytes.Buffer

			// Temporarily redirect stderr
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			n, err := f.Write([]byte(tt.input))
			w.Close()
			buf.ReadFrom(r)
			os.Stderr = oldStderr

			if err != nil {
				t.Errorf("Write() error = %v", err)
			}
			if n != len(tt.input) {
				t.Errorf("Write() n = %d, want %d", n, len(tt.input))
			}

			passed := buf.Len() > 0
			if passed != tt.shouldPass {
				t.Errorf("Write(%q) passed = %v, want %v", tt.input, passed, tt.shouldPass)
			}
		})
	}
}

func TestLogFilterAuthThrottle(t *testing.T) {
	f := &logFilter{}
	authMsg := "2006/01/02 15:04:05 To start this tsnet server, visit: https://..."

	// Temporarily redirect stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// First auth message should pass
	f.Write([]byte(authMsg))
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stderr = oldStderr

	if buf.Len() == 0 {
		t.Error("First auth message should pass through")
	}

	// Immediate second auth message should be throttled
	r, w, _ = os.Pipe()
	os.Stderr = w
	f.Write([]byte(authMsg))
	w.Close()
	buf.Reset()
	buf.ReadFrom(r)
	os.Stderr = oldStderr

	if buf.Len() > 0 {
		t.Error("Second immediate auth message should be throttled")
	}

	// Simulate time passing (manually set lastAuth to past)
	f.mu.Lock()
	f.lastAuth = time.Now().Add(-2 * time.Minute)
	f.mu.Unlock()

	// After timeout, auth message should pass again
	r, w, _ = os.Pipe()
	os.Stderr = w
	f.Write([]byte(authMsg))
	w.Close()
	buf.Reset()
	buf.ReadFrom(r)
	os.Stderr = oldStderr

	if buf.Len() == 0 {
		t.Error("Auth message after timeout should pass through")
	}
}

func TestServeMarkdown(t *testing.T) {
	// Create a temp directory with test files
	tmpDir, err := os.MkdirTemp("", "serve-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create test markdown file
	mdContent := "# Hello\n\nThis is **bold** text."
	if err := os.WriteFile("test.md", []byte(mdContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory with README.md
	if err := os.MkdirAll("subdir", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("subdir/README.md", []byte("# Subdir"), 0644); err != nil {
		t.Fatal(err)
	}

	// Save original index value and restore after test
	origIndex := *index
	defer func() { *index = origIndex }()
	*index = "README.md"

	tests := []struct {
		name           string
		path           string
		query          string
		wantHandled    bool
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:         "render markdown",
			path:         "/test.md",
			wantHandled:  true,
			wantContains: []string{"<h1>Hello</h1>", "<strong>bold</strong>", "text/html"},
		},
		{
			name:        "raw markdown",
			path:        "/test.md",
			query:       "raw",
			wantHandled: false, // Should not handle, let file server do it
		},
		{
			name:        "non-markdown file",
			path:        "/test.txt",
			wantHandled: false,
		},
		{
			name:        "nonexistent file",
			path:        "/nonexistent.md",
			wantHandled: false,
		},
		{
			name:        "directory traversal attempt",
			path:        "/../../../etc/passwd.md",
			wantHandled: false,
		},
		{
			name:         "case insensitive extension",
			path:         "/test.MD",
			wantHandled:  false, // File doesn't exist with .MD extension
		},
		{
			name:         "index file browse link",
			path:         "/subdir/README.md",
			wantHandled:  true,
			wantContains: []string{"?list", "Browse"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.query != "" {
				req.URL.RawQuery = tt.query
			}
			w := httptest.NewRecorder()

			handled := serveMarkdown(w, req, tt.path)

			if handled != tt.wantHandled {
				t.Errorf("serveMarkdown() handled = %v, want %v", handled, tt.wantHandled)
			}

			if handled {
				body := w.Body.String()
				for _, want := range tt.wantContains {
					if !strings.Contains(body, want) {
						t.Errorf("response body should contain %q", want)
					}
				}
				for _, notWant := range tt.wantNotContain {
					if strings.Contains(body, notWant) {
						t.Errorf("response body should not contain %q", notWant)
					}
				}
			}
		})
	}
}

func TestServeMarkdownBrowsePath(t *testing.T) {
	// Create a temp directory with test files
	tmpDir, err := os.MkdirTemp("", "serve-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create test files
	os.WriteFile("README.md", []byte("# Root"), 0644)
	os.WriteFile("other.md", []byte("# Other"), 0644)
	os.MkdirAll("docs", 0755)
	os.WriteFile("docs/README.md", []byte("# Docs"), 0644)
	os.WriteFile("docs/guide.md", []byte("# Guide"), 0644)

	origIndex := *index
	defer func() { *index = origIndex }()

	tests := []struct {
		name       string
		indexFlag  string
		path       string
		wantBrowse string
	}{
		{
			name:       "index file shows list link",
			indexFlag:  "README.md",
			path:       "/README.md",
			wantBrowse: "/?list",
		},
		{
			name:       "non-index shows index link",
			indexFlag:  "README.md",
			path:       "/other.md",
			wantBrowse: "/README.md",
		},
		{
			name:       "subdir index shows list",
			indexFlag:  "README.md",
			path:       "/docs/README.md",
			wantBrowse: "/docs/?list",
		},
		{
			name:       "subdir non-index shows index",
			indexFlag:  "README.md",
			path:       "/docs/guide.md",
			wantBrowse: "/docs/README.md",
		},
		{
			name:       "no index configured",
			indexFlag:  "",
			path:       "/other.md",
			wantBrowse: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			*index = tt.indexFlag

			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			handled := serveMarkdown(w, req, tt.path)
			if !handled {
				t.Fatal("expected markdown to be handled")
			}

			body := w.Body.String()
			expectedHref := `href="` + tt.wantBrowse + `"`
			if !strings.Contains(body, expectedHref) {
				t.Errorf("expected browse link %q in body, got: %s", expectedHref, body)
			}
		})
	}
}

func TestEnsureGitignore(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "serve-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	t.Run("not a git repo", func(t *testing.T) {
		dir := filepath.Join(tmpDir, "not-git")
		os.MkdirAll(dir, 0755)
		os.Chdir(dir)

		ensureGitignore()

		// Should not create .gitignore
		if _, err := os.Stat(".gitignore"); !os.IsNotExist(err) {
			t.Error(".gitignore should not be created in non-git directory")
		}
	})

	t.Run("git repo without gitignore", func(t *testing.T) {
		dir := filepath.Join(tmpDir, "new-git")
		os.MkdirAll(filepath.Join(dir, ".git"), 0755)
		os.Chdir(dir)

		ensureGitignore()

		content, err := os.ReadFile(".gitignore")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), ".serve/") {
			t.Error(".gitignore should contain .serve/")
		}
	})

	t.Run("gitignore already has entry", func(t *testing.T) {
		dir := filepath.Join(tmpDir, "has-entry")
		os.MkdirAll(filepath.Join(dir, ".git"), 0755)
		os.Chdir(dir)
		os.WriteFile(".gitignore", []byte(".serve/\n"), 0644)

		ensureGitignore()

		content, err := os.ReadFile(".gitignore")
		if err != nil {
			t.Fatal(err)
		}
		// Should not duplicate
		if strings.Count(string(content), ".serve/") > 1 {
			t.Error(".serve/ entry should not be duplicated")
		}
	})

	t.Run("gitignore has commented entry", func(t *testing.T) {
		dir := filepath.Join(tmpDir, "commented")
		os.MkdirAll(filepath.Join(dir, ".git"), 0755)
		os.Chdir(dir)
		os.WriteFile(".gitignore", []byte("# .serve/\n"), 0644)

		ensureGitignore()

		content, err := os.ReadFile(".gitignore")
		if err != nil {
			t.Fatal(err)
		}
		// Should not add another entry since commented one exists
		if strings.Count(string(content), ".serve/") > 1 {
			t.Error("should not add entry when commented one exists")
		}
	})

	t.Run("gitignore without trailing newline", func(t *testing.T) {
		dir := filepath.Join(tmpDir, "no-newline")
		os.MkdirAll(filepath.Join(dir, ".git"), 0755)
		os.Chdir(dir)
		os.WriteFile(".gitignore", []byte("node_modules/"), 0644) // No trailing newline

		ensureGitignore()

		content, err := os.ReadFile(".gitignore")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), ".serve/") {
			t.Error(".gitignore should contain .serve/")
		}
		// Check that the new entry is on its own line
		lines := strings.Split(string(content), "\n")
		foundServe := false
		for _, line := range lines {
			if strings.TrimSpace(line) == ".serve/" {
				foundServe = true
				break
			}
		}
		if !foundServe {
			t.Error(".serve/ should be on its own line")
		}
	})
}

func TestOpenBrowser(t *testing.T) {
	// This test just verifies openBrowser doesn't panic
	// We can't easily test that it actually opens a browser
	t.Run("does not panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("openBrowser panicked: %v", r)
			}
		}()
		// Use an invalid URL to ensure we don't actually open anything
		// but the function should still not panic
		openBrowser("http://localhost:99999")
	})
}

func TestMarkdownTemplate(t *testing.T) {
	// Test that the template renders correctly
	var buf bytes.Buffer
	err := mdTemplate.Execute(&buf, struct {
		Title      string
		BaseCSS    string
		Content    string
		CustomCSS  string
		BrowsePath string
	}{
		Title:      "Test Title",
		BaseCSS:    "body { color: black; }",
		Content:    "<p>Test content</p>",
		CustomCSS:  ".custom { color: red; }",
		BrowsePath: "/browse",
	})

	if err != nil {
		t.Fatalf("template execution failed: %v", err)
	}

	output := buf.String()

	checks := []string{
		"<title>Test Title</title>",
		"body { color: black; }",
		"<p>Test content</p>",
		".custom { color: red; }",
		`href="/browse"`,
		"<!DOCTYPE html>",
		"markdown-body",
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("template output should contain %q", check)
		}
	}
}

func TestHandlerIntegration(t *testing.T) {
	// Create a temp directory with test files
	tmpDir, err := os.MkdirTemp("", "serve-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create test files
	os.WriteFile("index.html", []byte("<html>Hello</html>"), 0644)
	os.WriteFile("README.md", []byte("# README"), 0644)
	os.MkdirAll("docs", 0755)
	os.WriteFile("docs/README.md", []byte("# Docs"), 0644)

	// Save original values
	origIndex := *index
	origProxy := *proxy
	origLocal := *local
	defer func() {
		*index = origIndex
		*proxy = origProxy
		*local = origLocal
	}()

	*index = "README.md"
	*proxy = ""
	*local = true

	// Create the handler similar to main()
	fs := http.FileServer(http.Dir("."))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})

	tests := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "serve HTML file",
			path:         "/index.html",
			wantStatus:   http.StatusOK,
			wantContains: "<html>Hello</html>",
		},
		{
			name:         "serve markdown as HTML",
			path:         "/README.md",
			wantStatus:   http.StatusOK,
			wantContains: "<h1>README</h1>",
		},
		{
			name:         "directory serves index",
			path:         "/",
			wantStatus:   http.StatusOK,
			wantContains: "<h1>README</h1>",
		},
		{
			name:         "subdirectory serves index",
			path:         "/docs/",
			wantStatus:   http.StatusOK,
			wantContains: "<h1>Docs</h1>",
		},
		{
			name:       "directory with list param",
			path:       "/?list",
			wantStatus: http.StatusOK,
			// File server will show directory listing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			if tt.wantContains != "" && !strings.Contains(w.Body.String(), tt.wantContains) {
				t.Errorf("body should contain %q, got: %s", tt.wantContains, w.Body.String())
			}
		})
	}
}

func TestCustomCSS(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "serve-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create .serve directory with custom CSS
	os.MkdirAll(".serve", 0755)
	os.WriteFile(".serve/custom.css", []byte(".test { color: blue; }"), 0644)

	// Create test markdown
	os.WriteFile("test.md", []byte("# Test"), 0644)

	// Save and set custom CSS
	origCustomCSS := customCSS
	defer func() { customCSS = origCustomCSS }()

	// Load custom CSS like main() does
	if css, err := os.ReadFile(".serve/custom.css"); err == nil {
		customCSS = string(css)
	}

	req := httptest.NewRequest("GET", "/test.md", nil)
	w := httptest.NewRecorder()

	handled := serveMarkdown(w, req, "/test.md")
	if !handled {
		t.Fatal("expected markdown to be handled")
	}

	if !strings.Contains(w.Body.String(), ".test { color: blue; }") {
		t.Error("custom CSS should be included in output")
	}
}

func TestMarkdownCSS(t *testing.T) {
	// Verify the embedded CSS is loaded
	if markdownCSS == "" {
		t.Error("markdownCSS should not be empty")
	}

	// Check it contains expected GitHub markdown styles
	if !strings.Contains(markdownCSS, "markdown") {
		t.Error("markdownCSS should contain markdown-related styles")
	}
}
