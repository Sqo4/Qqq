package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/sqo4/uley/client/procutil"
	"github.com/sqo4/uley/internal/models"
)

// Llama drives a llama-server child process.
type Llama struct {
	Bin    string // path to llama-server
	LogDir string

	mu     sync.Mutex
	cmd    *exec.Cmd
	exited chan struct{}
	model  string
	base   string
	key    string
	http   *http.Client
}

// FindLlamaServer looks for the engine next to the client binary (that is
// where the installer puts it), then in ULEY_LLAMA_SERVER, then in PATH.
func FindLlamaServer() string {
	name := "llama-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p := os.Getenv("ULEY_LLAMA_SERVER"); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, p := range []string{
			filepath.Join(dir, "engine", name),
			filepath.Join(dir, "..", "Resources", "engine", name), // macOS .app bundle
			filepath.Join(dir, "..", "lib", "uley", "engine", name), // .deb layout
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (e *Llama) Loaded() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.model
}

func (e *Llama) Load(ctx context.Context, m models.Model, path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.model == m.ID && e.cmd != nil {
		select {
		case <-e.exited:
		default:
			return nil
		}
	}
	e.stopLocked()
	if e.Bin == "" {
		return errors.New("llama-server not found, reinstall Uley")
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	k := make([]byte, 16)
	rand.Read(k)
	e.key = hex.EncodeToString(k)
	e.base = "http://127.0.0.1:" + strconv.Itoa(port)
	e.http = &http.Client{}

	args := []string{
		"-m", path,
		"--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--api-key", e.key,
		"-c", strconv.Itoa(m.ContextSize),
		"-ngl", "999", // the model was chosen to fit into the VRAM budget
		"--parallel", "1",
		"--jinja",
	}
	cmd := exec.Command(e.Bin, args...)
	procutil.Background(cmd)
	var logw io.Writer = io.Discard
	if e.LogDir != "" {
		os.MkdirAll(e.LogDir, 0o700)
		if f, err := os.Create(filepath.Join(e.LogDir, "engine.log")); err == nil {
			logw = f
			defer f.Close() // the child keeps its own handle
		}
	}
	cmd.Stdout, cmd.Stderr = logw, logw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}
	procutil.AfterStart(cmd.Process.Pid)
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	e.cmd, e.exited = cmd, exited

	// Wait until the model is loaded into the GPU.
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			e.cmd = nil
			return errors.New("engine exited while loading the model, see engine.log")
		case <-ctx.Done():
			e.stopLocked()
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		resp, err := e.http.Get(e.base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				e.model = m.ID
				return nil
			}
		}
	}
	e.stopLocked()
	return errors.New("engine did not become ready in 5 minutes")
}

func (e *Llama) Unload() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked()
}

func (e *Llama) stopLocked() {
	if e.cmd != nil && e.cmd.Process != nil {
		e.cmd.Process.Kill()
		select {
		case <-e.exited:
		case <-time.After(10 * time.Second):
		}
	}
	e.cmd, e.model = nil, ""
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (e *Llama) Generate(ctx context.Context, req Request) (Result, error) {
	e.mu.Lock()
	base, key, client, loaded := e.base, e.key, e.http, e.model
	e.mu.Unlock()
	if loaded == "" {
		return Result{}, errors.New("no model loaded")
	}
	body, _ := json.Marshal(map[string]any{
		"messages":             req.Messages,
		"max_tokens":           req.MaxTokens,
		"temperature":          req.Temperature,
		"seed":                 req.Seed,
		"stream":               false,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(hreq)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("engine: %s: %.200s", resp.Status, raw)
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil || len(cr.Choices) == 0 {
		return Result{}, errors.New("engine: unexpected response")
	}
	return Result{Text: stripThinking(cr.Choices[0].Message.Content), Tokens: cr.Usage.CompletionTokens}, nil
}
