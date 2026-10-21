package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sqo4/uley/internal/models"
)

// Fake emulates a GPU for tests and demos: it "loads" instantly and
// answers deterministically, so two honest fake nodes always agree.
type Fake struct {
	Delay time.Duration // time per answer

	mu    sync.Mutex
	model string
}

func (f *Fake) Load(ctx context.Context, m models.Model, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.model = m.ID
	return nil
}

func (f *Fake) Loaded() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.model
}

func (f *Fake) Unload() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.model = ""
}

func (f *Fake) Generate(ctx context.Context, req Request) (Result, error) {
	model := f.Loaded()
	if model == "" {
		return Result{}, fmt.Errorf("no model loaded")
	}
	select {
	case <-time.After(f.Delay):
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	words := strings.Fields(fmt.Sprintf("[эмулятор %s] Вы спросили: %s", model, last))
	if len(words) > req.MaxTokens && req.MaxTokens > 0 {
		words = words[:req.MaxTokens]
	}
	return Result{Text: strings.Join(words, " "), Tokens: len(words)}, nil
}
