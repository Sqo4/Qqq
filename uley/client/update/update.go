// Package update checks for new client versions. An update is used only if
// its manifest carries a valid Ed25519 signature from the Uley release key
// and the downloaded installer matches the signed SHA-256.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/sqo4/uley/internal/signing"
)

type Checker struct {
	// ManifestURL may contain {os} and {arch}.
	ManifestURL string
	PublicKey   string // base64 Ed25519; empty disables updates
	Current     string
	HTTP        *http.Client
}

// Newer compares dotted versions like "0.3.1" (a leading "v" is ignored).
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// Check returns a verified manifest for a newer version, or nil.
func (c *Checker) Check(ctx context.Context) (*signing.Manifest, error) {
	if c.PublicKey == "" || c.ManifestURL == "" || c.Current == "dev" {
		return nil, nil
	}
	u := strings.NewReplacer("{os}", runtime.GOOS, "{arch}", runtime.GOARCH).Replace(c.ManifestURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update manifest: %s", resp.Status)
	}
	var m signing.Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&m); err != nil {
		return nil, err
	}
	if err := signing.Verify(m, c.PublicKey); err != nil {
		return nil, err
	}
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH || !strings.HasPrefix(m.URL, "https://") {
		return nil, errors.New("update manifest is for another platform")
	}
	if !Newer(m.Version, c.Current) {
		return nil, nil
	}
	return &m, nil
}

// Download fetches the installer and checks it against the signed hash.
func (c *Checker) Download(ctx context.Context, m *signing.Manifest, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download update: %s", resp.Status)
	}
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, filepath.Base(m.URL))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 2<<30))
	f.Close()
	if err != nil {
		os.Remove(path)
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(m.SHA256) {
		os.Remove(path)
		return "", errors.New("update file does not match the signed checksum")
	}
	return path, nil
}
