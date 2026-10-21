package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sqo4/uley/client/config"
	"github.com/sqo4/uley/client/engine"
	"github.com/sqo4/uley/coordinator"
	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
)

// TestChainEndToEnd runs the whole "one big model" path with real llama.cpp
// binaries: two volunteer agents lend their (CPU-emulated) GPUs, the
// coordinator assembles them into a chain, answers a question, rebuilds the
// chain after one volunteer starts a game, and answers again.
//
//	ULEY_LLAMA_BIN=/llama.cpp/build/bin ULEY_TEST_MODEL=/models/qwen3-0.6b.gguf go test -run ChainEndToEnd -v ./client/agent/
func TestChainEndToEnd(t *testing.T) {
	bin, modelPath := os.Getenv("ULEY_LLAMA_BIN"), os.Getenv("ULEY_TEST_MODEL")
	if bin == "" || modelPath == "" {
		t.Skip("ULEY_LLAMA_BIN and ULEY_TEST_MODEL not set")
	}
	store, err := coordinator.OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model := models.Model{ID: "test-chain", Name: "test", Chain: true, MinVRAMMB: 3000, ContextSize: 4096}
	chain, err := coordinator.NewChainManager(store, coordinator.ChainConfig{
		Model: model, ModelPath: modelPath, HeadBin: filepath.Join(bin, "llama-server"),
		LogDir: t.TempDir(), Parallel: 2, WorkerTTL: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := coordinator.NewServer(store, coordinator.Config{HiveModel: model.ID, Chain: chain, PollMax: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	hs := httptest.NewServer(srv)
	defer hs.Close()
	defer func() { cancel(); time.Sleep(time.Second) }() // stop agents before the server

	var monitors []*fakeMonitor
	for i := 0; i < 2; i++ {
		mon := &fakeMonitor{idle: time.Hour}
		monitors = append(monitors, mon)
		cfg, _ := config.Open(t.TempDir(), hs.URL)
		cfg.Update(func(s *config.Settings) { s.Consent = config.ConsentVersion; s.MaxGPUPercent = 100 })
		a := &Agent{
			Config: cfg, Engine: &engine.Fake{}, Monitor: mon, Version: "test",
			// 2286 MB * 70% = 1600 MB each: only both together cover 3000 MB.
			DetectGPU: func() []protocol.GPU { return []protocol.GPU{{Name: "Emu", Vendor: "amd", VRAMMB: 2286}} },
			RPC:       &engine.RPCServer{Bin: filepath.Join(bin, "ggml-rpc-server"), CacheDir: t.TempDir(), LogDir: t.TempDir()},
			Tick:      100 * time.Millisecond, HeartbeatEvery: time.Second,
		}
		go a.Run(ctx)
	}

	ask := func(q string) protocol.Task {
		id, err := store.CreateTask(model.ID, coordinator.TaskPayloadForTest(q), 1)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(4 * time.Minute)
		for time.Now().Before(deadline) {
			task, _ := store.GetTask(id)
			if task.Status == protocol.TaskDone || task.Status == protocol.TaskFailed {
				return task
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("no answer; chain: %+v", chain.Stats())
		return protocol.Task{}
	}

	task := ask("What is the capital of France? Answer in one word.")
	if task.Status != protocol.TaskDone || !strings.Contains(task.Output, "Paris") {
		t.Fatalf("bad answer %+v", task)
	}
	st, _ := srv.Stats()
	t.Logf("chain: %+v, capacity %.1f tok/s", *st.Chain, st.CapacityTPS)
	if st.Chain.Members != 2 || st.Chain.State != "ready" {
		t.Fatalf("expected a ready chain of two, got %+v", *st.Chain)
	}

	// Volunteer 1 starts a game: the chain must stop, then come back.
	monitors[0].set(func(m *fakeMonitor) { m.fullscreen = true })
	waitFor(t, "chain broken", func() bool { return chain.Stats().State == "waiting" })
	monitors[0].set(func(m *fakeMonitor) { m.fullscreen = false })

	task = ask("What is the capital of France? Answer in one word.")
	if task.Status != protocol.TaskDone || !strings.Contains(task.Output, "Paris") {
		t.Fatalf("bad answer after rebuild %+v", task)
	}
	t.Logf("rebuilt chain answered: %q", task.Output)
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
