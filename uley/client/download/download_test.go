package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sqo4/uley/internal/models"
)

func serve(t *testing.T, data []byte) (*httptest.Server, *int) {
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.ServeContent(w, r, "m.gguf", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func model(url string, data []byte) models.Model {
	h := sha256.Sum256(data)
	return models.Model{ID: "t", File: "m.gguf", URL: url, SHA256: hex.EncodeToString(h[:]), SizeBytes: int64(len(data))}
}

func TestFetchResumeAndVerify(t *testing.T) {
	data := bytes.Repeat([]byte("uley"), 300000)
	srv, hits := serve(t, data)
	dir := t.TempDir()
	m := model(srv.URL+"/m.gguf", data)

	// Simulate an interrupted download.
	os.WriteFile(Path(dir, m)+".part", data[:500000], 0o600)
	var last int64
	p, err := Fetch(context.Background(), srv.Client(), dir, m, func(done, total int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, data) || last != m.SizeBytes {
		t.Fatal("file content or progress wrong")
	}
	if !Ready(dir, m) {
		t.Fatal("must be ready after download")
	}
	Fetch(context.Background(), srv.Client(), dir, m, nil)
	if *hits != 1 {
		t.Fatalf("verified model must not be downloaded again, hits=%d", *hits)
	}
}

func TestFetchRejectsWrongHash(t *testing.T) {
	data := []byte("real model")
	srv, _ := serve(t, data)
	dir := t.TempDir()
	m := model(srv.URL+"/m.gguf", []byte("other bytes"))
	m.SizeBytes = int64(len(data))
	if _, err := Fetch(context.Background(), srv.Client(), dir, m, nil); err != ErrHashMismatch {
		t.Fatalf("want hash mismatch, got %v", err)
	}
	if _, err := os.Stat(Path(dir, m)); err == nil {
		t.Fatal("bad file must not be kept")
	}
}

func TestFetchRefusesHTTP(t *testing.T) {
	m := models.Model{File: "x", URL: "http://example.org/x", SHA256: "00", SizeBytes: 1}
	if _, err := Fetch(context.Background(), http.DefaultClient, t.TempDir(), m, nil); err == nil {
		t.Fatal("plain http must be refused")
	}
}
