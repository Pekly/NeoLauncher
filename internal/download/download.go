// Package download verifies vendor downloads and confines archive destinations.
package download

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const MaxFileBytes int64 = 512 << 20
const UserAgent = "NeoLauncher/0.2.0 (https://github.com/Pekly/NeoLauncher)"

type Progress struct {
	Stage      string `json:"stage"`
	Name       string `json:"name"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"total_bytes"`
}
type Reporter func(Progress)
type File struct {
	URL             string `json:"url"`
	Path            string `json:"path"`
	SHA1            string `json:"sha1,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	SHA512          string `json:"sha512,omitempty"`
	Size            int64  `json:"size"`
	AllowOverwrite  bool   `json:"-"`
	AllowUnverified bool   `json:"-"`
}
type HTTPError struct {
	Status int
	Host   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("download from %s failed (HTTP %d)", e.Host, e.Status)
}

type Client struct {
	HTTP     *http.Client
	AllowURL func(*url.URL) bool
}

func New() *Client {
	c := &Client{AllowURL: AllowedURL}
	c.HTTP = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second, MaxResponseHeaderBytes: 64 << 10}}
	c.HTTP.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !c.permitted(req.URL) {
			return errors.New("download redirect is not allowed")
		}
		if req.URL.Hostname() != "api.curseforge.com" {
			req.Header.Del("x-api-key")
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		return nil
	}
	return c
}
func AllowedURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range []string{"piston-meta.mojang.com", "piston-data.mojang.com", "launchermeta.mojang.com", "launcher.mojang.com", "libraries.minecraft.net", "resources.download.minecraft.net", "meta.fabricmc.net", "maven.fabricmc.net", "api.modrinth.com", "cdn.modrinth.com", "api.curseforge.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "raw.githubusercontent.com"} {
		if host == allowed {
			return true
		}
	}
	return strings.HasSuffix(host, ".forgecdn.net")
}
func (c *Client) permitted(u *url.URL) bool {
	if c.AllowURL != nil {
		return c.AllowURL(u)
	}
	return AllowedURL(u)
}
func (c *Client) request(ctx context.Context, raw string, headers map[string]string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || !c.permitted(u) {
		return nil, errors.New("download URL is not allowed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid download URL")
	}
	req.Header.Set("User-Agent", UserAgent)
	for key, value := range headers {
		if strings.EqualFold(key, "x-api-key") && u.Hostname() != "api.curseforge.com" {
			return nil, errors.New("API key cannot be sent to this host")
		}
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") {
			return nil, errors.New("credentials are not allowed on downloads")
		}
		req.Header.Set(key, value)
	}
	client := c.HTTP
	if client == nil {
		client = New().HTTP
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("could not reach %s", u.Hostname())
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, &HTTPError{response.StatusCode, u.Hostname()}
	}
	return response, nil
}
func (c *Client) Fetch(ctx context.Context, raw string, maxBytes int64, headers map[string]string) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > MaxFileBytes {
		return nil, errors.New("invalid download size limit")
	}
	response, err := c.request(ctx, raw, headers)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > maxBytes {
		return nil, errors.New("download exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("download exceeds size limit")
	}
	return data, nil
}
func (c *Client) JSON(ctx context.Context, raw string, dest any, headers map[string]string) error {
	data, err := c.Fetch(ctx, raw, 32<<20, headers)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, dest); err != nil {
		return errors.New("provider returned invalid metadata")
	}
	return nil
}
func SafeRelative(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00\r\n*?\"<>|") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return false
		}
	}
	return true
}
func SafeJoin(root, name string) (string, error) {
	if !SafeRelative(name) {
		return "", errors.New("unsafe file path")
	}
	if info, err := os.Lstat(root); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return "", errors.New("install root must be a real directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	target := root
	for _, part := range strings.Split(name, "/") {
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symbolic links are not allowed in install paths")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return target, nil
}
func fileHashes(f File) ([]hash.Hash, []string, error) {
	var hashes []hash.Hash
	var expected []string
	for _, value := range []struct {
		text string
		size int
		make func() hash.Hash
	}{{f.SHA1, 20, sha1.New}, {f.SHA256, 32, sha256.New}, {f.SHA512, 64, sha512.New}} {
		if value.text == "" {
			continue
		}
		decoded, err := hex.DecodeString(value.text)
		if err != nil || len(decoded) != value.size {
			return nil, nil, errors.New("invalid file checksum")
		}
		hashes = append(hashes, value.make())
		expected = append(expected, strings.ToLower(value.text))
	}
	if len(hashes) == 0 {
		u, _ := url.Parse(f.URL)
		if !f.AllowUnverified || u == nil || u.Hostname() != "meta.fabricmc.net" {
			return nil, nil, errors.New("download has no published checksum")
		}
	}
	return hashes, expected, nil
}
func Verify(path string, f File) error {
	hashes, expected, err := fileHashes(f)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("download target is not a regular file")
	}
	if info.Size() > MaxFileBytes || (f.Size > 0 && info.Size() != f.Size) {
		return errors.New("file size does not match")
	}
	writers := make([]io.Writer, len(hashes))
	for i, h := range hashes {
		writers[i] = h
	}
	if _, err = io.Copy(io.MultiWriter(writers...), file); err != nil {
		return err
	}
	for i, h := range hashes {
		if hex.EncodeToString(h.Sum(nil)) != expected[i] {
			return errors.New("file checksum does not match")
		}
	}
	return nil
}
func (c *Client) Download(ctx context.Context, f File, dest string, report Reporter) error {
	if f.Size < 0 || f.Size > MaxFileBytes {
		return errors.New("download exceeds size limit")
	}
	if _, _, err := fileHashes(f); err != nil {
		return err
	}
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("download destination is not a regular file")
		}
		if err = Verify(dest, f); err == nil {
			return nil
		}
		if !f.AllowOverwrite {
			return errors.New("a different file already exists; it was not overwritten")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	response, err := c.request(ctx, f.URL, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength > MaxFileBytes {
		return errors.New("download exceeds size limit")
	}
	file, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	progress := &progressWriter{writer: file, report: report, name: filepath.Base(dest), total: f.Size}
	n, copyErr := io.Copy(progress, io.LimitReader(response.Body, MaxFileBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > MaxFileBytes {
		return errors.New("download exceeds size limit")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = Verify(temp, f); err != nil {
		return err
	}
	if !f.AllowOverwrite {
		// Creating a hard link publishes the verified file atomically without
		// replacing a destination created by another writer.
		if err = os.Link(temp, dest); err != nil {
			return fmt.Errorf("download destination could not be published without overwriting: %w", err)
		}
	} else if err = os.Rename(temp, dest); err != nil {
		return err
	}
	if report != nil {
		report(Progress{Stage: "Downloaded", Name: filepath.Base(dest), Done: 1, Total: 1, Bytes: n, TotalBytes: f.Size})
	}
	return nil
}

type progressWriter struct {
	writer       io.Writer
	report       Reporter
	name         string
	bytes, total int64
	last         time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.bytes += int64(n)
	if w.report != nil && time.Since(w.last) > 250*time.Millisecond {
		w.last = time.Now()
		w.report(Progress{Stage: "Downloading", Name: w.name, Bytes: w.bytes, TotalBytes: w.total})
	}
	return n, err
}
