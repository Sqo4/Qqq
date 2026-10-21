// Package ui serves the consent and settings page on 127.0.0.1. It is the
// client's only window: it opens in the user's browser, is reachable from
// this computer only, and needs a random secret that changes every start.
package ui

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/sqo4/uley/client/agent"
	"github.com/sqo4/uley/client/config"
	"github.com/sqo4/uley/internal/models"
)

//go:embed page.html
var page []byte

type Server struct {
	Agent   *agent.Agent
	Config  *config.Store
	Version string
	// Autostart switches launching with the OS; nil if unsupported.
	Autostart func(on bool) error
	// OnUpdate installs a pending update, if any.
	OnUpdate func() error
	// UpdateAvailable returns the newer version or "".
	UpdateAvailable func() string

	secret string
	addr   string
	srv    *http.Server
}

// lockFile lets a second copy of the client find the first one.
type lockFile struct {
	Port   int    `json:"port"`
	Secret string `json:"secret"`
}

func lockPath(dir string) string { return filepath.Join(dir, "ui.json") }

// FindRunning returns the panel URL of an already running client, or "".
func FindRunning(dir string) string {
	b, err := os.ReadFile(lockPath(dir))
	if err != nil {
		return ""
	}
	var lf lockFile
	if json.Unmarshal(b, &lf) != nil || lf.Port == 0 {
		return ""
	}
	base := "http://127.0.0.1:" + strconv.Itoa(lf.Port)
	req, _ := http.NewRequest(http.MethodGet, base+"/api/ping", nil)
	req.AddCookie(&http.Cookie{Name: "uley", Value: lf.Secret})
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return base + "/?k=" + lf.Secret
}

func (s *Server) Start() error {
	b := make([]byte, 24)
	rand.Read(b)
	s.secret = hex.EncodeToString(b)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /api/ping", s.auth(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	mux.HandleFunc("GET /api/state", s.auth(s.handleState))
	mux.HandleFunc("POST /api/consent", s.auth(s.handleConsent))
	mux.HandleFunc("POST /api/settings", s.auth(s.handleSettings))
	mux.HandleFunc("POST /api/update", s.auth(s.handleUpdate))
	s.srv = &http.Server{Handler: s.guard(mux), ReadHeaderTimeout: 5 * time.Second}
	go s.srv.Serve(ln)
	lf, _ := json.Marshal(lockFile{Port: ln.Addr().(*net.TCPAddr).Port, Secret: s.secret})
	return os.WriteFile(lockPath(s.Config.Dir()), lf, 0o600)
}

func (s *Server) Close() {
	os.Remove(lockPath(s.Config.Dir()))
	if s.srv != nil {
		s.srv.Close()
	}
}

// URL opens the panel and logs the browser in.
func (s *Server) URL() string { return "http://" + s.addr + "/?k=" + s.secret }

// guard rejects requests addressed to any other host name, which stops
// DNS-rebinding attacks from web pages.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) okSecret(v string) bool {
	return subtle.ConstantTimeCompare([]byte(v), []byte(s.secret)) == 1
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("uley")
		if err != nil || !s.okSecret(c.Value) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// A custom header cannot be sent cross-site without CORS approval.
		if r.Method == http.MethodPost && r.Header.Get("X-Uley") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if k := r.URL.Query().Get("k"); k != "" {
		if !s.okSecret(k) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "uley", Value: s.secret, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie("uley"); err != nil || !s.okSecret(c.Value) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("Откройте панель из значка Улья в трее."))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

type modelInfo struct {
	models.Model
	Fits bool `json:"fits"`
}

type state struct {
	Version       string          `json:"version"`
	ConsentNeeded bool            `json:"consent_needed"`
	Settings      config.Settings `json:"settings"`
	Status        agent.Status    `json:"status"`
	Models        []modelInfo     `json:"models"`
	CanAutostart  bool            `json:"can_autostart"`
	Update        string          `json:"update,omitempty"`
}

func (s *Server) snapshot() state {
	set := s.Config.Get()
	st := s.Agent.Status()
	full := 0
	for _, g := range st.GPUs {
		full = max(full, g.VRAMMB)
	}
	budget := full * set.MaxVRAMPercent / 100
	var ms []modelInfo
	for _, m := range models.All() {
		ms = append(ms, modelInfo{Model: m, Fits: m.MinVRAMMB <= budget})
	}
	out := state{
		Version: s.Version, ConsentNeeded: set.Consent < config.ConsentVersion,
		Settings: set, Status: st, Models: ms, CanAutostart: s.Autostart != nil,
	}
	if s.UpdateAvailable != nil {
		out.Update = s.UpdateAvailable()
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.snapshot()) }

func (s *Server) handleConsent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Accept bool `json:"accept"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.Config.Update(func(c *config.Settings) {
		if req.Accept {
			c.Consent = config.ConsentVersion
			c.Paused = false
		} else {
			c.Consent = 0 // withdrawing consent stops everything
		}
	})
	s.Agent.Kick()
	writeJSON(w, s.snapshot())
}

// settingsPatch lists what the page may change. Consent is not here: it
// has its own endpoint with its own button.
type settingsPatch struct {
	Paused            *bool   `json:"paused"`
	Autostart         *bool   `json:"autostart"`
	IdleOnly          *bool   `json:"idle_only"`
	IdleMinutes       *int    `json:"idle_minutes"`
	PauseOnFullscreen *bool   `json:"pause_on_fullscreen"`
	PauseOnBattery    *bool   `json:"pause_on_battery"`
	MaxGPUPercent     *int    `json:"max_gpu_percent"`
	MaxVRAMPercent    *int    `json:"max_vram_percent"`
	Model             *string `json:"model"`
	NodeName          *string `json:"node_name"`
	Coordinator       *string `json:"coordinator"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var p settingsPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if p.Model != nil && *p.Model != "" {
		if _, ok := models.Get(*p.Model); !ok {
			http.Error(w, "unknown model", http.StatusBadRequest)
			return
		}
	}
	if p.Coordinator != nil {
		if err := agent.CheckCoordinatorURL(*p.Coordinator, s.Agent.AllowInsecure); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if p.Autostart != nil && s.Autostart != nil {
		if err := s.Autostart(*p.Autostart); err != nil {
			http.Error(w, "не удалось изменить автозапуск: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.Config.Update(func(c *config.Settings) {
		set := func(dst *bool, v *bool) {
			if v != nil {
				*dst = *v
			}
		}
		setI := func(dst *int, v *int) {
			if v != nil {
				*dst = *v
			}
		}
		set(&c.Paused, p.Paused)
		set(&c.Autostart, p.Autostart)
		set(&c.IdleOnly, p.IdleOnly)
		set(&c.PauseOnFullscreen, p.PauseOnFullscreen)
		set(&c.PauseOnBattery, p.PauseOnBattery)
		setI(&c.IdleMinutes, p.IdleMinutes)
		setI(&c.MaxGPUPercent, p.MaxGPUPercent)
		setI(&c.MaxVRAMPercent, p.MaxVRAMPercent)
		if p.Model != nil {
			c.Model = *p.Model
		}
		if p.NodeName != nil {
			c.NodeName = *p.NodeName
		}
		if p.Coordinator != nil {
			c.Coordinator = *p.Coordinator
		}
	})
	s.Agent.Kick()
	writeJSON(w, s.snapshot())
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.OnUpdate == nil {
		http.Error(w, "updates are not configured", http.StatusNotFound)
		return
	}
	if err := s.OnUpdate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// OpenBrowser shows url in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return errors.New("не удалось открыть браузер: " + err.Error())
	}
	go cmd.Wait()
	return nil
}
