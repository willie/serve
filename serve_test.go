package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbedImagesReplacesSrcNotAlt(t *testing.T) {
	dir := t.TempDir()
	pixel := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	if err := os.WriteFile(filepath.Join(dir, "pic.png"), pixel, 0600); err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString(pixel)

	// alt text repeats the src filename; the replacement must touch only src.
	in := []byte(`<img src="pic.png" alt="pic.png">`)
	out, err := embedImages(in, filepath.Join(dir, "doc.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `src="data:image/png;base64,`+want+`"`) {
		t.Errorf("src not embedded correctly: %s", got)
	}
	if !strings.Contains(got, `alt="pic.png"`) {
		t.Errorf("alt attribute was corrupted: %s", got)
	}
}

func TestEmbedImagesSkipsRemoteAndData(t *testing.T) {
	in := []byte(`<img src="https://example.com/a.png"> <img src="data:image/png;base64,AAAA">`)
	out, err := embedImages(in, "/tmp/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(in) {
		t.Errorf("remote/data URIs should be untouched, got: %s", out)
	}
}

func TestGetMimeType(t *testing.T) {
	cases := map[string]string{
		"a.png": "image/png",
		"a.JPG": "image/jpeg",
		"a.svg": "image/svg+xml",
		"a.bin": "application/octet-stream",
		"noext": "application/octet-stream",
	}
	for name, want := range cases {
		if got := getMimeType(name); got != want {
			t.Errorf("getMimeType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRewriteMarkdownLinks(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<a href="page.md">x</a>`, `<a href="page.html">x</a>`},
		{`<a href="sub/page.md#sec">x</a>`, `<a href="sub/page.html#sec">x</a>`},
		{`<a href="https://x.com/a.md">x</a>`, `<a href="https://x.com/a.md">x</a>`},
		{`<a href="#frag">x</a>`, `<a href="#frag">x</a>`},
		{`<a href="img.png">x</a>`, `<a href="img.png">x</a>`},
	}
	for _, c := range cases {
		if got := string(rewriteMarkdownLinks([]byte(c.in))); got != c.want {
			t.Errorf("rewriteMarkdownLinks(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGithubSlug(t *testing.T) {
	cases := map[string]string{
		"Hello World":      "hello-world",
		"Foo_Bar-Baz":      "foo_bar-baz",
		"What's New?":      "whats-new",
		"  spaces  here  ": "--spaces--here--",
	}
	for in, want := range cases {
		if got := githubSlug(in); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeFile writes content to dir/rel, creating parent directories, and
// optionally stamps a modification time when mod is non-zero.
func writeFile(t *testing.T, dir, rel, content string, mod time.Time) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

// readZip parses a zip from raw bytes into a name->entry map.
func readZip(t *testing.T, raw []byte) map[string]*zip.File {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		m[f.Name] = f
	}
	return m
}

func zipBody(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestServeExport(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	mod := time.Date(2024, 3, 15, 12, 30, 0, 0, time.Local)
	writeFile(t, dir, "README.md", "# Home\nSee [guide](docs/guide.md#sec) and [out](https://x.com/a.md).\n", mod)
	writeFile(t, dir, "docs/guide.md", "# Guide\nBack to [home](../README.md).\n", mod)
	writeFile(t, dir, "logo.png", "PNGBYTES", mod)
	writeFile(t, dir, ".git/config", "gitstuff", time.Time{})
	writeFile(t, dir, ".serve/state", "state", time.Time{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/?export", nil)
	if !serveExport(rec, req, "/") {
		t.Fatal("serveExport returned false")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}

	files := readZip(t, rec.Body.Bytes())

	// .md files become .html; assets are copied verbatim; vcs/state dirs excluded.
	for _, name := range []string{"README.html", "docs/guide.html", "logo.png"} {
		if files[name] == nil {
			t.Errorf("missing zip entry %q", name)
		}
	}
	for _, name := range []string{"README.md", "docs/guide.md", ".git/config", ".serve/state"} {
		if files[name] != nil {
			t.Errorf("unexpected zip entry %q", name)
		}
	}
	if got := zipBody(t, files["logo.png"]); got != "PNGBYTES" {
		t.Errorf("logo.png body = %q, want verbatim copy", got)
	}

	// Inter-page .md links are rewritten to .html (fragment preserved), external
	// links untouched.
	home := zipBody(t, files["README.html"])
	if !strings.Contains(home, `href="docs/guide.html#sec"`) {
		t.Errorf("link not rewritten in README.html: %s", home)
	}
	if !strings.Contains(home, `href="https://x.com/a.md"`) {
		t.Errorf("external link should be untouched: %s", home)
	}
	if !strings.Contains(zipBody(t, files["docs/guide.html"]), `href="../README.html"`) {
		t.Error("relative up-path link not rewritten in guide.html")
	}

	// Generated HTML carries the source .md's mod time, not the 1980 zero-date.
	if delta := files["README.html"].Modified.Sub(mod); delta < -2*time.Second || delta > 2*time.Second {
		t.Errorf("README.html mod time = %v, want ~%v", files["README.html"].Modified, mod)
	}
	if delta := files["logo.png"].Modified.Sub(mod); delta < -2*time.Second || delta > 2*time.Second {
		t.Errorf("logo.png mod time = %v, want ~%v", files["logo.png"].Modified, mod)
	}
}

func TestServeDirList(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "guide.md", "# Guide\n", time.Time{})
	writeFile(t, dir, "sub/deep.md", "# Deep\n", time.Time{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/?list", nil)
	if !serveDirList(rec, req, "/") {
		t.Fatal("serveDirList returned false")
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<a href="?export">Download HTML zip</a>`) {
		t.Errorf("export link missing from listing: %s", body)
	}
	if !strings.Contains(body, `<a href="guide.md">guide.md</a>`) {
		t.Errorf("file entry missing: %s", body)
	}
	if !strings.Contains(body, `<a href="sub/">sub/</a>`) {
		t.Errorf("subdir entry should have trailing slash: %s", body)
	}
}

func TestServeDirListDefersToIndexHTML(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "index.html", "<h1>custom</h1>", time.Time{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	if serveDirList(rec, req, "/") {
		t.Error("serveDirList should defer (return false) when index.html exists")
	}
}

func TestServeExportRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/?export", nil)
	if serveExport(rec, req, "/../") {
		t.Error("serveExport should reject a traversal path")
	}
}
