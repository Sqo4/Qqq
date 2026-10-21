package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/sqo4/uley/internal/signing"
)

func TestNewer(t *testing.T) {
	if !Newer("0.2.0", "0.1.9") || !Newer("v1.0.0", "0.9.9") || Newer("0.1.0", "0.1.0") || Newer("0.1.0-rc1", "0.1.0") {
		t.Fatal("version compare broken")
	}
}

func TestCheckAndDownload(t *testing.T) {
	pub, priv, _ := signing.GenerateKey()
	payload := []byte("installer bytes")
	sum := sha256.Sum256(payload)
	var srv *httptest.Server
	var manifest []byte
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/m.json" {
			w.Write(manifest)
			return
		}
		w.Write(payload)
	}))
	defer srv.Close()
	m, _ := signing.Sign(signing.Manifest{Version: "9.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH,
		URL: srv.URL + "/Uley-Setup.exe", SHA256: hex.EncodeToString(sum[:])}, priv)
	manifest, _ = json.Marshal(m)

	c := &Checker{ManifestURL: srv.URL + "/m.json", PublicKey: pub, Current: "0.1.0", HTTP: srv.Client()}
	got, err := c.Check(context.Background())
	if err != nil || got == nil {
		t.Fatalf("expected update, got %v %v", got, err)
	}
	if _, err := c.Download(context.Background(), got, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	// Tampered manifest is refused.
	m.Version = "9.9.9"
	manifest, _ = json.Marshal(m)
	if _, err := c.Check(context.Background()); err == nil {
		t.Fatal("tampered manifest accepted")
	}
	// Tampered installer is refused.
	payload = []byte("evil")
	if _, err := c.Download(context.Background(), got, t.TempDir()); err == nil {
		t.Fatal("tampered installer accepted")
	}
}
