package download

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedDownloadAndNoOverwrite(t *testing.T) {
	body := []byte("verified mod file")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer server.Close()
	client := New()
	client.AllowURL = func(u *url.URL) bool { return u.String() == server.URL }
	file := File{URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Size: int64(len(body))}
	dest := filepath.Join(t.TempDir(), "mods", "test.jar")
	if err := client.Download(context.Background(), file, dest, nil); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dest, file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("user changes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.Download(context.Background(), file, dest, nil); err == nil {
		t.Fatal("overwrote user changes")
	}
	actual, _ := os.ReadFile(dest)
	if string(actual) != "user changes" {
		t.Fatal("changed existing file")
	}
}
func TestBadDigestNeverPublishes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "wrong content") }))
	defer server.Close()
	client := New()
	client.AllowURL = func(u *url.URL) bool { return u.String() == server.URL }
	dir := t.TempDir()
	dest := filepath.Join(dir, "bad.jar")
	if err := client.Download(context.Background(), File{URL: server.URL, SHA1: strings.Repeat("0", 40)}, dest, nil); err == nil {
		t.Fatal("accepted bad digest")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("published partial or temporary file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Download(ctx, File{URL: server.URL, SHA1: strings.Repeat("0", 40)}, dest, nil); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestPathsAndVendorURLs(t *testing.T) {
	for _, name := range []string{"../x", "/x", "x\\y", "C:/x", "mods/../x", "CON", "aux.txt", "mods/LPT1.jar", "a.", "a ", "a//b"} {
		if SafeRelative(name) {
			t.Fatal("unsafe path", name)
		}
	}
	if !SafeRelative("mods/fabric-api.jar") {
		t.Fatal("valid path denied")
	}
	for _, raw := range []string{"http://cdn.modrinth.com/x", "https://cdn.modrinth.com.evil.test/x", "https://user:pass@cdn.modrinth.com/x", "https://127.0.0.1/x", "https://cdn.modrinth.com:8443/x"} {
		u, _ := url.Parse(raw)
		if AllowedURL(u) {
			t.Fatal("unsafe URL", raw)
		}
	}
	for _, raw := range []string{"https://cdn.modrinth.com/x", "https://edge.forgecdn.net/x", "https://piston-data.mojang.com/x"} {
		u, _ := url.Parse(raw)
		if !AllowedURL(u) {
			t.Fatal("vendor denied", raw)
		}
	}
	client := New()
	if _, err := client.Fetch(context.Background(), "https://cdn.modrinth.com/x", 100, map[string]string{"x-api-key": "secret"}); err == nil {
		t.Fatal("sent provider key to CDN")
	}
}

func TestDestinationCreatedDuringDownloadIsPreserved(t *testing.T) {
	body := []byte("verified content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer server.Close()
	client := New()
	client.AllowURL = func(u *url.URL) bool { return u.String() == server.URL }
	file := File{URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(body))}
	dest := filepath.Join(t.TempDir(), "test.jar")
	err := client.Download(context.Background(), file, dest, func(p Progress) {
		if p.Stage == "Downloading" {
			if e := os.WriteFile(dest, []byte("concurrent file"), 0600); e != nil {
				t.Fatal(e)
			}
		}
	})
	if err == nil {
		t.Fatal("overwrote concurrent file")
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "concurrent file" {
		t.Fatal("changed concurrent file")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".download-*"))
	if len(leftovers) != 0 {
		t.Fatal("temporary files leaked")
	}
}
