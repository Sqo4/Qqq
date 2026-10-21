// Package config stores the client settings in the user's config folder.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ConsentVersion is bumped whenever what the client does changes in a way
// the user should agree to again.
const ConsentVersion = 1

type Settings struct {
	Coordinator string `json:"coordinator"`
	NodeName    string `json:"node_name"`

	// Consent holds the ConsentVersion the user agreed to, 0 = never.
	Consent int `json:"consent"`

	Paused            bool   `json:"paused"`              // paused by the user from the tray
	Autostart         bool   `json:"autostart"`           // start with the OS, off by default
	IdleOnly          bool   `json:"idle_only"`           // work only when nobody uses the PC
	IdleMinutes       int    `json:"idle_minutes"`        // how long without input counts as idle
	PauseOnFullscreen bool   `json:"pause_on_fullscreen"` // pause while a fullscreen game runs
	PauseOnBattery    bool   `json:"pause_on_battery"`
	MaxGPUPercent     int    `json:"max_gpu_percent"`  // share of time the GPU may compute
	MaxVRAMPercent    int    `json:"max_vram_percent"` // share of video memory we may use
	Model             string `json:"model"`            // "" = pick automatically
}

// Identity is what the coordinator gave us on registration.
type Identity struct {
	Coordinator string `json:"coordinator"`
	NodeID      string `json:"node_id"`
	Token       string `json:"token"`
}

func Defaults(coordinator string) Settings {
	return Settings{
		Coordinator:       coordinator,
		IdleOnly:          true,
		IdleMinutes:       3,
		PauseOnFullscreen: true,
		PauseOnBattery:    true,
		MaxGPUPercent:     70,
		MaxVRAMPercent:    70,
	}
}

// Normalize keeps every value within sane bounds.
func (s *Settings) Normalize() {
	clamp := func(v, lo, hi int) int { return min(max(v, lo), hi) }
	s.IdleMinutes = clamp(s.IdleMinutes, 1, 120)
	s.MaxGPUPercent = clamp(s.MaxGPUPercent, 10, 100)
	s.MaxVRAMPercent = clamp(s.MaxVRAMPercent, 10, 95)
	if len(s.NodeName) > 64 {
		s.NodeName = s.NodeName[:64]
	}
}

// Store is a thread-safe settings file plus identity file.
type Store struct {
	dir string
	mu  sync.Mutex
	s   Settings
	id  Identity
}

// Dir is the folder holding settings, the node token and the models.
func Dir() (string, error) {
	if d := os.Getenv("ULEY_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Uley"), nil
}

func Open(dir, defaultCoordinator string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st := &Store{dir: dir, s: Defaults(defaultCoordinator)}
	if err := readJSON(filepath.Join(dir, "settings.json"), &st.s); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if st.s.Coordinator == "" {
		st.s.Coordinator = defaultCoordinator
	}
	st.s.Normalize()
	if err := readJSON(filepath.Join(dir, "identity.json"), &st.id); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return st, nil
}

func (st *Store) Dir() string { return st.dir }

func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s
}

// Update changes settings atomically and saves them.
func (st *Store) Update(f func(*Settings)) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.s
	f(&s)
	s.Normalize()
	if err := writeJSON(filepath.Join(st.dir, "settings.json"), s); err != nil {
		return st.s, err
	}
	st.s = s
	return s, nil
}

func (st *Store) Identity() Identity {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.id
}

func (st *Store) SetIdentity(id Identity) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := writeJSON(filepath.Join(st.dir, "identity.json"), id); err != nil {
		return err
	}
	st.id = id
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON writes via a temp file so a crash never leaves half a file.
// Files are readable by the user only: identity.json holds the node token.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Counters is what the tray shows as "jobs done".
type Counters struct {
	JobsDone  int   `json:"jobs_done"`
	TokensOut int64 `json:"tokens_out"`
}

func (st *Store) Counters() Counters {
	var c Counters
	readJSON(filepath.Join(st.dir, "counters.json"), &c)
	return c
}

func (st *Store) AddJob(tokens int) Counters {
	st.mu.Lock()
	defer st.mu.Unlock()
	var c Counters
	readJSON(filepath.Join(st.dir, "counters.json"), &c)
	c.JobsDone++
	c.TokensOut += int64(tokens)
	writeJSON(filepath.Join(st.dir, "counters.json"), c)
	return c
}
