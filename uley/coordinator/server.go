// Package coordinator is the central server of the Uley hive: it registers
// volunteer nodes, queues inference tasks, hands them out, checks results
// and shows a public dashboard.
package coordinator

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
)

type Config struct {
	// APIKeys may submit tasks. If empty, submitting is open to anyone,
	// which is only acceptable for local testing.
	APIKeys []string
	// JoinKey, if set, must be presented by nodes on registration.
	JoinKey string
	// VerifyRate is the share of greedy (temperature 0) tasks that are
	// computed twice on different nodes to catch cheaters.
	VerifyRate float64
	JobTimeout time.Duration
	// VerifyWait is how long a finished replica waits for its twin before
	// the answer is accepted unverified.
	VerifyWait time.Duration
	PollMax    time.Duration
	// Web holds the dashboard and download pages.
	Web fs.FS
	// HiveModel is the one model the hive serves; tasks may omit it.
	HiveModel string
	// Chain, if set, runs HiveModel as a chain of volunteer GPUs.
	Chain *ChainManager
	// SignupKey, if set, is required to create an account (e.g. a
	// university running its own hive).
	SignupKey string
}

func (c *Config) defaults() {
	if c.JobTimeout == 0 {
		c.JobTimeout = 3 * time.Minute
	}
	if c.VerifyWait == 0 {
		c.VerifyWait = 2 * time.Minute
	}
	if c.PollMax == 0 {
		c.PollMax = 25 * time.Second
	}
}

type Server struct {
	store  *Store
	cfg    Config
	bell   *notifier
	mux    *http.ServeMux
	regs   *rateLimiter
	logins *rateLimiter
	events *hub
}

func NewServer(store *Store, cfg Config) *Server {
	cfg.defaults()
	s := &Server{store: store, cfg: cfg, bell: newNotifier(), mux: http.NewServeMux(), regs: newRateLimiter(20, time.Hour), logins: newRateLimiter(30, 10*time.Minute), events: newHub()}
	p := protocol.APIPrefix
	s.mux.HandleFunc("POST "+p+"/nodes/register", s.handleRegister)
	s.mux.HandleFunc("POST "+p+"/nodes/heartbeat", s.nodeAuth(s.handleHeartbeat))
	s.mux.HandleFunc("POST "+p+"/jobs/poll", s.nodeAuth(s.handlePoll))
	s.mux.HandleFunc("POST "+p+"/jobs/{id}/result", s.nodeAuth(s.handleResult))
	s.mux.HandleFunc("POST "+p+"/tasks", s.apiAuth(s.handleSubmit))
	s.mux.HandleFunc("GET "+p+"/tasks/{id}", s.apiAuth(s.handleGetTask))
	s.mux.HandleFunc("POST /v1/chat/completions", s.apiAuth(s.handleChatCompletions))
	s.mux.HandleFunc("GET "+p+"/stats", s.handleStats)
	s.mux.HandleFunc("GET "+p+"/models", s.handleModels)
	s.mux.HandleFunc("GET "+p+"/hive", s.handleHive)
	s.registerSocial()
	if cfg.Chain != nil {
		cfg.Chain.bell = s.bell
		cfg.Chain.timeout = max(cfg.JobTimeout, 15*time.Minute) // a chain writes slower
		s.mux.HandleFunc("GET "+p+"/chain/wait", s.nodeAuth(s.handleChainWait))
		s.mux.HandleFunc("GET "+p+"/chain/dial", s.nodeAuth(cfg.Chain.relay.HandleUpgrade))
		s.mux.HandleFunc("POST "+p+"/chain/leave", s.nodeAuth(s.handleChainLeave))
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	if cfg.Web != nil {
		s.mux.HandleFunc("GET /{$}", s.page("index.html"))
		s.mux.HandleFunc("GET /download", s.page("download.html"))
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	s.mux.ServeHTTP(w, r)
}

// Run sweeps expired assignments until ctx is done.
func (s *Server) Run(ctx context.Context) {
	if s.cfg.Chain != nil {
		go s.cfg.Chain.Run(ctx)
	}
	go s.runLive(ctx)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			requeued, err := s.store.Sweep(s.cfg.VerifyWait)
			if err != nil {
				log.Printf("sweep: %v", err)
			}
			if requeued {
				s.bell.ring()
			}
		}
	}
}

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(s.cfg.Web, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, protocol.ErrorResponse{Error: msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

type ctxKey struct{}

func (s *Server) nodeAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, banned, err := s.store.NodeByToken(bearer(r))
		if errors.Is(err, ErrUnauthorized) {
			writeErr(w, http.StatusUnauthorized, "unknown node token")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "store error")
			log.Printf("auth: %v", err)
			return
		}
		if banned {
			writeErr(w, http.StatusForbidden, "node is banned for returning wrong results")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	}
}

func nodeID(r *http.Request) string { return r.Context().Value(ctxKey{}).(string) }

func (s *Server) apiAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.APIKeys) > 0 {
			key := bearer(r)
			ok := false
			for _, k := range s.cfg.APIKeys {
				if key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 {
					ok = true
				}
			}
			if !ok {
				writeErr(w, http.StatusUnauthorized, "missing or wrong API key")
				return
			}
		}
		next(w, r)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func sanitizeGPUs(in []protocol.GPU) []protocol.GPU {
	if len(in) > 8 {
		in = in[:8]
	}
	out := make([]protocol.GPU, 0, len(in))
	for _, g := range in {
		g.Name, g.Vendor, g.Backend = clean(g.Name, 80), clean(g.Vendor, 16), clean(g.Backend, 16)
		if g.VRAMMB < 0 || g.VRAMMB > 1<<20 {
			g.VRAMMB = 0
		}
		out = append(out, g)
	}
	return out
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.regs.allow(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many registrations from this address")
		return
	}
	var req protocol.RegisterRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.cfg.JoinKey != "" && req.JoinKey != s.cfg.JoinKey {
		writeErr(w, http.StatusForbidden, "wrong join key")
		return
	}
	req.NodeName = clean(req.NodeName, 64)
	if req.NodeName == "" {
		req.NodeName = "node"
	}
	req.OS, req.Arch, req.ClientVersion = clean(req.OS, 16), clean(req.Arch, 16), clean(req.ClientVersion, 32)
	req.GPUs = sanitizeGPUs(req.GPUs)
	resp, err := s.store.RegisterNode(req)
	if err != nil {
		log.Printf("register: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	log.Printf("node registered: %s (%s/%s, %d GPU)", resp.NodeID, req.OS, req.Arch, len(req.GPUs))
	writeJSON(w, http.StatusOK, resp)
}

// knownModels drops anything that is not on the whitelist.
func knownModels(in []string) []string {
	var out []string
	for _, m := range in {
		if _, ok := models.Get(m); ok {
			out = append(out, m)
		}
	}
	return out
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb protocol.HeartbeatRequest
	if err := readJSON(r, &hb); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	hb.State, hb.ClientVersion = clean(hb.State, 16), clean(hb.ClientVersion, 32)
	hb.GPUs = sanitizeGPUs(hb.GPUs)
	hb.Models = knownModels(hb.Models)
	if err := s.store.Heartbeat(nodeID(r), hb); err != nil {
		log.Printf("heartbeat: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	wanted, _ := s.store.WantedModels()
	writeJSON(w, http.StatusOK, protocol.HeartbeatResponse{WantedModels: wanted})
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	var req protocol.PollRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	id := nodeID(r)
	ms := knownModels(req.Models)
	wait := min(time.Duration(req.WaitSeconds)*time.Second, s.cfg.PollMax)
	deadline := time.After(wait)
	s.store.Touch(id)
	for {
		ch := s.bell.wait() // subscribe before checking to not miss a ring
		job, err := s.store.Assign(id, ms, s.cfg.JobTimeout)
		if err != nil {
			log.Printf("assign: %v", err)
			writeErr(w, http.StatusInternalServerError, "store error")
			return
		}
		if job != nil {
			writeJSON(w, http.StatusOK, job)
			return
		}
		select {
		case <-ch:
		case <-deadline:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	var res protocol.JobResult
	if err := readJSON(r, &res); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(res.Output) > protocol.MaxOutputBytes {
		res.Output = res.Output[:protocol.MaxOutputBytes]
	}
	res.Error = clean(res.Error, 300)
	if res.TokensOut < 0 || res.TokensOut > protocol.MaxTokensLimit*2 {
		res.TokensOut = 0
	}
	err := s.store.Complete(nodeID(r), r.PathValue("id"), res)
	if errors.Is(err, ErrNotYours) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		log.Printf("complete: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	s.bell.ring()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// validate turns a submit request into a task payload.
func validate(req protocol.SubmitRequest) (taskPayload, error) {
	var p taskPayload
	if _, ok := models.Get(req.Model); !ok {
		return p, fmt.Errorf("unknown model %q, see %s/models", req.Model, protocol.APIPrefix)
	}
	msgs := req.Messages
	if req.Prompt != "" {
		msgs = append(msgs, protocol.Message{Role: "user", Content: req.Prompt})
	}
	if len(msgs) == 0 || len(msgs) > protocol.MaxMessages {
		return p, fmt.Errorf("need 1..%d messages", protocol.MaxMessages)
	}
	total := 0
	for _, m := range msgs {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return p, fmt.Errorf("bad role %q", m.Role)
		}
		total += len(m.Content)
	}
	if total > protocol.MaxPromptBytes {
		return p, fmt.Errorf("prompt is longer than %d bytes", protocol.MaxPromptBytes)
	}
	p.Messages = msgs
	p.MaxTokens = req.MaxTokens
	if p.MaxTokens <= 0 {
		p.MaxTokens = protocol.DefaultMaxTokens
	}
	p.MaxTokens = min(p.MaxTokens, protocol.MaxTokensLimit)
	p.Temperature = 0.7
	if req.Temperature != nil {
		p.Temperature = *req.Temperature
	}
	if p.Temperature < 0 || p.Temperature > 2 {
		return p, errors.New("temperature must be within 0..2")
	}
	p.Seed = req.Seed
	return p, nil
}

func (s *Server) submit(req protocol.SubmitRequest) (string, error) {
	if s.cfg.HiveModel != "" {
		if req.Model == "" {
			req.Model = s.cfg.HiveModel
		}
		if req.Model != s.cfg.HiveModel {
			return "", fmt.Errorf("this hive runs only %s", s.cfg.HiveModel)
		}
	}
	p, err := validate(req)
	if err != nil {
		return "", err
	}
	replicas := 1
	if p.Temperature == 0 && rand.Float64() < s.cfg.VerifyRate && s.cfg.Chain == nil {
		replicas = 2
	}
	id, err := s.store.CreateTask(req.Model, p, replicas)
	if err != nil {
		return "", err
	}
	s.bell.ring()
	return id, nil
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req protocol.SubmitRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	id, err := s.submit(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, _ := s.store.GetTask(id)
	writeJSON(w, http.StatusAccepted, t)
}

// waitTask blocks until the task is finished or wait elapses.
func (s *Server) waitTask(ctx context.Context, id string, wait time.Duration) (protocol.Task, error) {
	deadline := time.After(wait)
	for {
		ch := s.bell.wait()
		t, err := s.store.GetTask(id)
		if err != nil || t.Status == protocol.TaskDone || t.Status == protocol.TaskFailed {
			return t, err
		}
		select {
		case <-ch:
		case <-deadline:
			return t, nil
		case <-ctx.Done():
			return t, ctx.Err()
		}
	}
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	wait = min(max(wait, 0), 60)
	t, err := s.waitTask(r.Context(), r.PathValue("id"), time.Duration(wait)*time.Second)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such task")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleChatCompletions is a minimal OpenAI-compatible endpoint, so existing
// tools can talk to the hive. It blocks until the answer is ready.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req protocol.SubmitRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.Prompt = ""
	id, err := s.submit(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.waitTask(r.Context(), id, 5*time.Minute)
	if err != nil {
		return
	}
	switch t.Status {
	case protocol.TaskDone:
	case protocol.TaskFailed:
		writeErr(w, http.StatusBadGateway, "task failed: "+t.Error)
		return
	default:
		writeErr(w, http.StatusGatewayTimeout, "no free node answered in time, task "+t.ID+" is still queued")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": t.ID, "object": "chat.completion", "created": t.CreatedAt, "model": t.Model,
		"choices": []map[string]any{{
			"index": 0, "finish_reason": "stop",
			"message": protocol.Message{Role: "assistant", Content: t.Output},
		}},
		"uley_verify": t.Verify,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.Stats()
	if err != nil {
		log.Printf("stats: %v", err)
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, st)
}

// Stats is the live state of the hive, chain included.
func (s *Server) Stats() (protocol.Stats, error) {
	st, err := s.store.Stats()
	if err != nil {
		return st, err
	}
	st.HiveModel = s.cfg.HiveModel
	if c := s.cfg.Chain; c != nil {
		cs := c.Stats()
		st.Chain = &cs
		if cs.State == "ready" {
			speed, running := s.store.NodeLoad(c.nodeID)
			if speed == 0 {
				speed = 3 // not measured yet: a modest guess for an internet chain
			}
			// One chain answers Parallel chats at about the same speed each.
			st.CapacityTPS += speed * float64(c.cfg.Parallel)
			st.UsedTPS += speed * float64(min(running, c.cfg.Parallel))
		}
	}
	if st.CapacityTPS > 0 {
		st.Utilization = min(st.UsedTPS/st.CapacityTPS, 1)
	}
	return st, nil
}

func (s *Server) handleHive(w http.ResponseWriter, r *http.Request) {
	h := protocol.Hive{Mode: protocol.ModeSingle, Model: s.cfg.HiveModel}
	if s.cfg.Chain != nil {
		h.Mode = protocol.ModeChain
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleChainWait(w http.ResponseWriter, r *http.Request) {
	budget, _ := strconv.Atoi(r.URL.Query().Get("budget"))
	if budget < 0 || budget > 1<<20 {
		budget = 0
	}
	in, err := s.cfg.Chain.Touch(nodeID(r), budget)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.Touch(nodeID(r))
	tok, err := s.cfg.Chain.relay.WaitDial(r.Context(), nodeID(r), 20*time.Second)
	if err != nil {
		return
	}
	writeJSON(w, http.StatusOK, protocol.ChainWait{Dial: tok, InChain: in})
}

func (s *Server) handleChainLeave(w http.ResponseWriter, r *http.Request) {
	s.cfg.Chain.Leave(nodeID(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, models.All())
}

// notifier wakes every waiter when something changes in the queue.
type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

func newNotifier() *notifier { return &notifier{ch: make(chan struct{})} }

func (n *notifier) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

func (n *notifier) ring() {
	n.mu.Lock()
	close(n.ch)
	n.ch = make(chan struct{})
	n.mu.Unlock()
}

// rateLimiter allows n events per key per window.
type rateLimiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	events map[string][]time.Time
}

func newRateLimiter(n int, window time.Duration) *rateLimiter {
	return &rateLimiter{n: n, window: window, events: map[string][]time.Time{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.events) > 10000 {
		l.events = map[string][]time.Time{}
	}
	var kept []time.Time
	for _, t := range l.events[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.n {
		l.events[key] = kept
		return false
	}
	l.events[key] = append(kept, now)
	return true
}
