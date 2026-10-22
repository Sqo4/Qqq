// Package datadir tells where Улей keeps its big files (models and the
// weights a chain sends). The installer lets people pick a disk with room,
// because the system disk is often full.
package datadir

import (
	"os"
	"path/filepath"
	"strings"
)

// FileName is written by the installer next to the programs.
const FileName = "data-dir.txt"

// Get returns the chosen folder, or "" for the default. ULEY_DATA_DIR wins
// over the installer's choice.
func Get() string {
	if d := os.Getenv("ULEY_DATA_DIR"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return read(filepath.Join(filepath.Dir(exe), FileName))
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Inno Setup may write a UTF-8 byte order mark.
	d := strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff"))
	if !filepath.IsAbs(d) {
		return ""
	}
	return d
}

// IsDiskFull reports whether err looks like "no space left on the disk".
func IsDiskFull(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not enough space") || strings.Contains(s, "no space left") ||
		strings.Contains(s, "disk full") || strings.Contains(s, "недостаточно места")
}
