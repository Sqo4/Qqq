package ui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sqo4/uley/client/agent"
	"github.com/sqo4/uley/client/config"
)

func start(t *testing.T) (*Server, *config.Store) {
	cfg, _ := config.Open(t.TempDir(), "https://hive.example")
	s := &Server{Agent: &agent.Agent{Config: cfg}, Config: cfg, Version: "test"}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, cfg
}

func TestPanelNeedsSecret(t *testing.T) {
	s, cfg := start(t)
	base := "http://" + s.addr
	resp, _ := http.Get(base + "/api/state")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("state without secret: %d", resp.StatusCode)
	}
	// Even with the secret cookie, a POST without the custom header fails.
	req, _ := http.NewRequest("POST", base+"/api/consent", strings.NewReader(`{"accept":true}`))
	req.AddCookie(&http.Cookie{Name: "uley", Value: s.secret})
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden || cfg.Get().Consent != 0 {
		t.Fatal("cross-site style POST must be rejected")
	}
	req, _ = http.NewRequest("POST", base+"/api/consent", strings.NewReader(`{"accept":true}`))
	req.AddCookie(&http.Cookie{Name: "uley", Value: s.secret})
	req.Header.Set("X-Uley", "1")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK || cfg.Get().Consent != config.ConsentVersion {
		t.Fatalf("consent not stored: %d", resp.StatusCode)
	}
	if FindRunning(cfg.Dir()) == "" {
		t.Fatal("second instance must find the first one")
	}
}

func TestRejectsForeignHost(t *testing.T) {
	s, _ := start(t)
	req, _ := http.NewRequest("GET", "http://"+s.addr+"/api/ping", nil)
	req.Host = "evil.example:80"
	req.AddCookie(&http.Cookie{Name: "uley", Value: s.secret})
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("DNS rebinding request must be rejected")
	}
}

func TestSettingsValidation(t *testing.T) {
	s, cfg := start(t)
	post := func(body string) int {
		req, _ := http.NewRequest("POST", "http://"+s.addr+"/api/settings", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "uley", Value: s.secret})
		req.Header.Set("X-Uley", "1")
		resp, _ := http.DefaultClient.Do(req)
		return resp.StatusCode
	}
	if post(`{"coordinator":"http://evil.example"}`) != http.StatusBadRequest {
		t.Fatal("plain http coordinator must be refused")
	}
	if post(`{"model":"some-random-model"}`) != http.StatusBadRequest {
		t.Fatal("unknown model must be refused")
	}
	if post(`{"max_gpu_percent":40,"paused":true}`) != http.StatusOK {
		t.Fatal("valid settings refused")
	}
	if got := cfg.Get(); got.MaxGPUPercent != 40 || !got.Paused {
		t.Fatalf("not saved: %+v", got)
	}
}
