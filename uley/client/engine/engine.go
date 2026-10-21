// Package engine runs language models. The real engine is llama.cpp's
// llama-server, started as a child process that listens on 127.0.0.1 only
// and is protected by a random key. The engine only ever receives text
// prompts; it never runs code from the network.
package engine

import (
	"context"
	"strings"

	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
)

type Request struct {
	Messages    []protocol.Message
	MaxTokens   int
	Temperature float64
	Seed        int64
}

type Result struct {
	Text   string
	Tokens int
}

type Engine interface {
	// Load makes model m (stored at path) ready. Loading the model that is
	// already loaded is a no-op.
	Load(ctx context.Context, m models.Model, path string) error
	// Loaded returns the id of the loaded model or "".
	Loaded() string
	Generate(ctx context.Context, req Request) (Result, error)
	// Unload stops the engine and frees video memory.
	Unload()
}

// stripThinking removes an empty or full <think> block some models emit.
func stripThinking(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<think>") {
		if i := strings.Index(s, "</think>"); i >= 0 {
			s = strings.TrimSpace(s[i+len("</think>"):])
		}
	}
	return s
}
