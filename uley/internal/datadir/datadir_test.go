package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	want := filepath.Join(dir, "Uley data")
	os.WriteFile(p, []byte("\ufeff"+want+"\r\n"), 0o600)
	if got := read(p); got != want {
		t.Fatalf("got %q", got)
	}
	os.WriteFile(p, []byte("relative\\path"), 0o600)
	if got := read(p); got != "" {
		t.Fatalf("relative path accepted: %q", got)
	}
	if read(filepath.Join(dir, "missing")) != "" {
		t.Fatal("missing file")
	}
}

func TestIsDiskFull(t *testing.T) {
	if !IsDiskFull(errors.New(`write C:\x.part: There is not enough space on the disk.`)) {
		t.Fatal("windows message")
	}
	if IsDiskFull(errors.New("connection reset")) {
		t.Fatal("false positive")
	}
}
