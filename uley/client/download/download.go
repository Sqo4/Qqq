// Package download fetches whitelisted model files with resume support and
// refuses to use any file whose SHA-256 does not match the whitelist.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sqo4/uley/internal/models"
)

// Progress is called with bytes done and total size.
type Progress func(done, total int64)

// Path is where model m is (or will be) stored.
func Path(dir string, m models.Model) string { return filepath.Join(dir, m.File) }

// Ready reports whether m is downloaded and was verified before.
func Ready(dir string, m models.Model) bool {
	ok, err := os.ReadFile(Path(dir, m) + ".verified")
	if err != nil || strings.TrimSpace(string(ok)) != m.SHA256 {
		return false
	}
	st, err := os.Stat(Path(dir, m))
	return err == nil && st.Size() == m.SizeBytes
}

var ErrHashMismatch = errors.New("downloaded file does not match the whitelisted SHA-256")

// Fetch downloads m into dir unless it is already there and verified.
func Fetch(ctx context.Context, client *http.Client, dir string, m models.Model, progress Progress) (string, error) {
	final := Path(dir, m)
	if Ready(dir, m) {
		return final, nil
	}
	if !strings.HasPrefix(m.URL, "https://") {
		return "", errors.New("refusing to download over plain http")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	part := final + ".part"
	if err := fetchPart(ctx, client, m, part, progress); err != nil {
		return "", err
	}
	if err := verify(part, m.SHA256); err != nil {
		os.Remove(part) // corrupted or tampered: start over next time
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	if err := os.WriteFile(final+".verified", []byte(m.SHA256), 0o600); err != nil {
		return "", err
	}
	return final, nil
}

func fetchPart(ctx context.Context, client *http.Client, m models.Model, part string, progress Progress) error {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	have, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if have > m.SizeBytes {
		f.Truncate(0)
		have, _ = f.Seek(0, io.SeekStart)
	}
	if have == m.SizeBytes {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		// Server ignored Range: start from scratch.
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		have = 0
	default:
		return fmt.Errorf("download %s: %s", m.File, resp.Status)
	}
	body := io.LimitReader(resp.Body, m.SizeBytes-have)
	buf := make([]byte, 1<<20)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			have += int64(n)
			if progress != nil {
				progress(have, m.SizeBytes)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if have != m.SizeBytes {
		return fmt.Errorf("download %s: got %d of %d bytes", m.File, have, m.SizeBytes)
	}
	return nil
}

func verify(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(want) {
		return ErrHashMismatch
	}
	return nil
}
