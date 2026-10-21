package models

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestWhitelistIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range All() {
		if seen[m.ID] {
			t.Fatalf("duplicate id %s", m.ID)
		}
		seen[m.ID] = true
		if b, err := hex.DecodeString(m.SHA256); err != nil || len(b) != 32 {
			t.Errorf("%s: bad sha256", m.ID)
		}
		if !strings.HasPrefix(m.URL, "https://") || !strings.HasSuffix(m.URL, m.File) {
			t.Errorf("%s: url must be https and end with the file name", m.ID)
		}
		if m.SizeBytes <= 0 || m.MinVRAMMB <= 0 || m.ContextSize <= 0 {
			t.Errorf("%s: sizes must be positive", m.ID)
		}
	}
}

func TestBest(t *testing.T) {
	if _, ok := Best(1000, ""); ok {
		t.Fatal("nothing should fit into 1 GB")
	}
	if m, _ := Best(8192, ""); m.ID != "qwen3-8b" {
		t.Fatalf("8 GB: got %s", m.ID)
	}
	if m, _ := Best(4500, ""); m.ID != "qwen3-4b" {
		t.Fatalf("4.5 GB: got %s", m.ID)
	}
	if m, _ := Best(8192, "qwen3-0.6b"); m.ID != "qwen3-0.6b" {
		t.Fatalf("preferred model ignored: %s", m.ID)
	}
	if m, _ := Best(2000, "qwen3-8b"); m.ID != "qwen3-0.6b" {
		t.Fatalf("preferred model that does not fit must fall back: %s", m.ID)
	}
}
