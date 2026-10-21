package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sqo4/uley/client/config"
	"github.com/sqo4/uley/client/download"
	"github.com/sqo4/uley/client/engine"
	"github.com/sqo4/uley/coordinator"
	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
)

type fakeMonitor struct {
	mu         sync.Mutex
	idle       time.Duration
	fullscreen bool
}

func (m *fakeMonitor) IdleTime() (time.Duration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idle, true
}
func (m *fakeMonitor) Fullscreen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fullscreen
}
func (m *fakeMonitor) OnBattery() bool { return false }
func (m *fakeMonitor) set(f func(*fakeMonitor)) {
	m.mu.Lock()
	f(m)
	m.mu.Unlock()
}

type hive struct {
	srv   *httptest.Server
	store *coordinator.Store
}

func newHive(t *testing.T) *hive {
	t.Helper()
	store, err := coordinator.OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s := coordinator.NewServer(store, coordinator.Config{PollMax: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	srv := httptest.NewServer(s)
	t.Cleanup(func() { cancel(); srv.Close(); store.Close() })
	return &hive{srv: srv, store: store}
}

func (h *hive) submit(t *testing.T, prompt string) string {
	t.Helper()
	body := `{"model":"qwen3-0.6b","prompt":` + mustJSON(prompt) + `}`
	resp, err := http.Post(h.srv.URL+"/api/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var task protocol.Task
	json.NewDecoder(resp.Body).Decode(&task)
	if task.ID == "" {
		t.Fatalf("submit failed: %s", resp.Status)
	}
	return task.ID
}

func (h *hive) wait(t *testing.T, id string) protocol.Task {
	t.Helper()
	resp, err := http.Get(h.srv.URL + "/api/v1/tasks/" + id + "?wait=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var task protocol.Task
	json.NewDecoder(resp.Body).Decode(&task)
	return task
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newAgent(t *testing.T, coord string, consent bool, mon *fakeMonitor, eng engine.Engine) (*Agent, *config.Store) {
	t.Helper()
	cfg, err := config.Open(t.TempDir(), coord)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Update(func(s *config.Settings) {
		s.MaxGPUPercent = 100
		if consent {
			s.Consent = config.ConsentVersion
		}
	})
	a := &Agent{
		Config: cfg, Engine: eng, Monitor: mon, Version: "test",
		DetectGPU: func() []protocol.GPU { return []protocol.GPU{{Name: "Emu", Vendor: "amd", VRAMMB: 2400}} },
		Fetch: func(ctx context.Context, m models.Model, p download.Progress) (string, error) {
			return "/fake/" + m.File, nil
		},
		Tick: 20 * time.Millisecond, HeartbeatEvery: 50 * time.Millisecond,
	}
	return a, cfg
}

func runAgent(t *testing.T, a *Agent) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestEndToEnd(t *testing.T) {
	h := newHive(t)
	mon := &fakeMonitor{idle: time.Hour}
	a, cfg := newAgent(t, h.srv.URL, true, mon, &engine.Fake{})
	runAgent(t, a)

	id := h.submit(t, "Сколько будет 2+2?")
	task := h.wait(t, id)
	if task.Status != protocol.TaskDone || !strings.Contains(task.Output, "Сколько будет 2+2?") {
		t.Fatalf("task not done: %+v", task)
	}
	eventually(t, "counter", func() bool { return a.Status().JobsDone == 1 })
	if cfg.Counters().JobsDone != 1 {
		t.Fatal("jobs counter not persisted")
	}
	// 2400 MB * 70% = 1680 MB budget: only the 0.6B model fits.
	if st := a.Status(); st.Model != "qwen3-0.6b" || st.State != protocol.StateWorking {
		t.Fatalf("bad status %+v", st)
	}
	eventually(t, "node visible in stats", func() bool {
		st, _ := h.store.Stats()
		return st.NodesOnline == 1 && st.ModelsOnline["qwen3-0.6b"] == 1
	})
}

func TestNothingHappensWithoutConsent(t *testing.T) {
	h := newHive(t)
	eng := &engine.Fake{}
	a, cfg := newAgent(t, h.srv.URL, false, &fakeMonitor{idle: time.Hour}, eng)
	runAgent(t, a)
	time.Sleep(300 * time.Millisecond)
	if cfg.Identity().Token != "" {
		t.Fatal("must not even register before consent")
	}
	if eng.Loaded() != "" || a.Status().State != protocol.StatePaused {
		t.Fatal("must not load a model before consent")
	}
	cfg.Update(func(s *config.Settings) { s.Consent = config.ConsentVersion })
	a.Kick()
	eventually(t, "registration after consent", func() bool { return cfg.Identity().Token != "" })
}

func TestPausesForGamesAndUserActivity(t *testing.T) {
	h := newHive(t)
	mon := &fakeMonitor{idle: time.Hour}
	eng := &engine.Fake{Delay: 5 * time.Second}
	a, _ := newAgent(t, h.srv.URL, true, mon, eng)
	runAgent(t, a)

	id := h.submit(t, "long job")
	eventually(t, "job started", func() bool { return a.Status().Busy })

	// The user starts a fullscreen game: the job must stop and VRAM be freed.
	mon.set(func(m *fakeMonitor) { m.fullscreen = true })
	eventually(t, "paused for the game", func() bool {
		st := a.Status()
		return st.State == protocol.StatePaused && !st.Busy && eng.Loaded() == ""
	})
	if task := h.wait(t, id); task.Status == protocol.TaskDone {
		t.Fatal("interrupted job must not count as done")
	}

	// Game closed, but the user is still at the keyboard: keep waiting.
	mon.set(func(m *fakeMonitor) { m.fullscreen = false; m.idle = time.Second })
	time.Sleep(200 * time.Millisecond)
	if st := a.Status(); st.State != protocol.StatePaused || !strings.Contains(st.Reason, "простоя") {
		t.Fatalf("must wait for idle, got %+v", st)
	}

	// The user walks away: the task is picked up again and finished.
	mon.set(func(m *fakeMonitor) { m.idle = time.Hour })
	eventually(t, "requeued task done", func() bool { return h.wait(t, id).Status == protocol.TaskDone })
}

func TestValidateJob(t *testing.T) {
	ok := protocol.Job{Model: "qwen3-0.6b", Messages: []protocol.Message{{Role: "user", Content: "hi"}}, MaxTokens: 1 << 20, Temperature: 9}
	req, err := validateJob(ok, "qwen3-0.6b")
	if err != nil || req.MaxTokens != protocol.MaxTokensLimit || req.Temperature != 2 {
		t.Fatalf("limits not enforced: %+v %v", req, err)
	}
	bad := ok
	bad.Model = "qwen3-8b"
	if _, err := validateJob(bad, "qwen3-0.6b"); err == nil {
		t.Fatal("job for another model must be rejected")
	}
	bad = ok
	bad.Messages = []protocol.Message{{Role: "shell", Content: "rm -rf /"}}
	if _, err := validateJob(bad, "qwen3-0.6b"); err == nil {
		t.Fatal("unknown role must be rejected")
	}
}

func TestCoordinatorMustUseHTTPS(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://hive.example.org": true,
		"http://127.0.0.1:8080":    true,
		"http://localhost:8080":    true,
		"http://hive.example.org":  false,
		"ftp://hive.example.org":   false,
		"hive.example.org":         false,
	} {
		if err := CheckCoordinatorURL(raw, false); (err == nil) != ok {
			t.Errorf("%s: got %v", raw, err)
		}
	}
}
