package coordinator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
	"github.com/sqo4/uley/internal/rpctunnel"
)

// ChainConfig describes the big model the hive runs as a chain.
type ChainConfig struct {
	Model     models.Model
	ModelPath string // the GGUF file on the coordinator
	HeadBin   string // llama-server that drives the chain
	LogDir    string
	// Parallel is how many chats one chain answers at once. Batching costs
	// almost nothing extra here: most time goes into network round trips.
	Parallel int
	// MinWorkerMB: smaller GPUs are not worth a network hop.
	MinWorkerMB int
	LoadTimeout time.Duration
	// WorkerTTL: a worker that has not polled for this long is gone.
	WorkerTTL time.Duration
}

func (c *ChainConfig) defaults() {
	if c.Parallel == 0 {
		c.Parallel = 4
	}
	if c.MinWorkerMB == 0 {
		c.MinWorkerMB = 1536
	}
	if c.LoadTimeout == 0 {
		c.LoadTimeout = 2 * time.Hour // the first load sends the weights out
	}
	if c.WorkerTTL == 0 {
		c.WorkerTTL = 45 * time.Second
	}
}

type chainWorker struct {
	id       string
	budgetMB int
	seen     time.Time
	served   bool // was in a chain before, so its weight cache is warm
}

type liveChain struct {
	members []string
	cmd     *exec.Cmd
	exited  chan struct{}
	base    string
	key     string
	ready   bool
	cancel  context.CancelFunc
	started time.Time
}

// ChainManager assembles volunteer GPUs into a chain that runs one big
// model, watches it and rebuilds it when someone leaves.
type ChainManager struct {
	cfg     ChainConfig
	store   *Store
	relay   *rpctunnel.Relay
	bell    *notifier
	timeout time.Duration
	nodeID  string // the chain acts as one virtual node in the task queue

	mu      sync.Mutex
	workers map[string]*chainWorker
	chain   *liveChain
	message string
}

func NewChainManager(store *Store, cfg ChainConfig) (*ChainManager, error) {
	cfg.defaults()
	if !cfg.Model.Chain {
		return nil, fmt.Errorf("%s is not a chain model", cfg.Model.ID)
	}
	reg, err := store.RegisterNode(protocol.RegisterRequest{NodeName: "Цепочка " + cfg.Model.Name, OS: "chain", Arch: "-", ClientVersion: "-"})
	if err != nil {
		return nil, err
	}
	return &ChainManager{
		cfg: cfg, store: store, relay: rpctunnel.NewRelay(), nodeID: reg.NodeID,
		workers: map[string]*chainWorker{}, message: "Ждём участников",
	}, nil
}

// Touch records a worker's poll and returns whether it is in the chain.
func (m *ChainManager) Touch(nodeID string, budgetMB int) (bool, error) {
	if _, err := m.relay.Attach(nodeID); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.workers[nodeID]
	if !ok {
		w = &chainWorker{id: nodeID}
		m.workers[nodeID] = w
	}
	w.budgetMB, w.seen = budgetMB, time.Now()
	return m.chain != nil && contains(m.chain.members, nodeID), nil
}

// Leave is called when a worker pauses (its owner started a game).
func (m *ChainManager) Leave(nodeID string) {
	m.mu.Lock()
	delete(m.workers, nodeID)
	broken := m.chain != nil && contains(m.chain.members, nodeID)
	m.mu.Unlock()
	m.relay.Detach(nodeID)
	if broken {
		m.breakChain("участник поставил паузу, пересобираем цепочку")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Run supervises the chain until ctx ends.
func (m *ChainManager) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.breakChain("координатор остановлен")
			return
		case <-t.C:
		}
		m.tick(ctx)
	}
}

func (m *ChainManager) tick(ctx context.Context) {
	now := time.Now()
	var gone []string
	m.mu.Lock()
	for id, w := range m.workers {
		if now.Sub(w.seen) > m.cfg.WorkerTTL {
			gone = append(gone, id)
			delete(m.workers, id)
		}
	}
	ch := m.chain
	m.mu.Unlock()
	for _, id := range gone {
		m.relay.Detach(id)
		if ch != nil && contains(ch.members, id) {
			m.breakChain("участник пропал из сети, пересобираем цепочку")
			ch = nil
		}
	}
	if ch != nil {
		select {
		case <-ch.exited:
			m.breakChain("движок цепочки остановился, пересобираем")
		default:
		}
		return
	}
	m.mu.Lock()
	haveModel := m.cfg.ModelPath != ""
	m.mu.Unlock()
	if !haveModel {
		return
	}
	members, split, ok := m.pick()
	if !ok {
		return
	}
	m.start(ctx, members, split)
}

// pick chooses workers whose memory covers the model. Workers that served
// before go first: their weight cache saves hours of upload.
func (m *ChainManager) pick() ([]string, []int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cands []*chainWorker
	have := 0
	for _, w := range m.workers {
		if w.budgetMB >= m.cfg.MinWorkerMB {
			cands = append(cands, w)
			have += w.budgetMB
		}
	}
	need := m.cfg.Model.MinVRAMMB
	if have < need {
		m.message = fmt.Sprintf("Ждём участников: есть %.1f из %.1f ГБ видеопамяти", float64(have)/1024, float64(need)/1024)
		return nil, nil, false
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].served != cands[j].served {
			return cands[i].served
		}
		if cands[i].budgetMB != cands[j].budgetMB {
			return cands[i].budgetMB > cands[j].budgetMB
		}
		return cands[i].id < cands[j].id
	})
	var members []string
	var split []int
	sum := 0
	for _, w := range cands {
		if sum >= need {
			break
		}
		members = append(members, w.id)
		split = append(split, w.budgetMB)
		sum += w.budgetMB
	}
	return members, split, true
}

// SetModelPath is called once the model file is downloaded and verified.
func (m *ChainManager) SetModelPath(p string) {
	m.mu.Lock()
	m.cfg.ModelPath = p
	m.message = "Модель готова, ждём участников"
	m.mu.Unlock()
}

// SetMessage shows progress (e.g. the server downloading the model).
func (m *ChainManager) SetMessage(msg string) {
	m.mu.Lock()
	m.message = msg
	m.mu.Unlock()
}

func randKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (m *ChainManager) start(ctx context.Context, members []string, split []int) {
	var addrs []string
	for _, id := range members {
		a, err := m.relay.Attach(id)
		if err != nil {
			log.Printf("chain: attach %s: %v", id, err)
			return
		}
		addrs = append(addrs, a)
	}
	port, err := freeLocalPort()
	if err != nil {
		log.Printf("chain: %v", err)
		return
	}
	ts := make([]string, len(split))
	for i, v := range split {
		ts[i] = strconv.Itoa(v)
	}
	c := &liveChain{members: members, exited: make(chan struct{}), base: "http://127.0.0.1:" + strconv.Itoa(port), key: randKey(), started: time.Now()}
	m.mu.Lock()
	modelPath := m.cfg.ModelPath
	m.mu.Unlock()
	args := []string{
		"-m", modelPath,
		"--rpc", strings.Join(addrs, ","),
		"-ngl", "999",
		"--tensor-split", strings.Join(ts, ","),
		"-c", strconv.Itoa(m.cfg.Model.ContextSize),
		"--parallel", strconv.Itoa(m.cfg.Parallel),
		"--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--api-key", c.key,
		"--jinja",
	}
	cmd := exec.Command(m.cfg.HeadBin, args...)
	var logw io.Writer = io.Discard
	if m.cfg.LogDir != "" {
		os.MkdirAll(m.cfg.LogDir, 0o700)
		if f, err := os.Create(filepath.Join(m.cfg.LogDir, "chain-head.log")); err == nil {
			defer f.Close()
			logw = f
		}
	}
	cmd.Stdout, cmd.Stderr = logw, logw
	if err := cmd.Start(); err != nil {
		log.Printf("chain: start head: %v", err)
		m.mu.Lock()
		m.message = "Не удалось запустить движок цепочки: " + err.Error()
		m.mu.Unlock()
		return
	}
	c.cmd = cmd
	go func() { cmd.Wait(); close(c.exited) }()
	cctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	m.mu.Lock()
	m.chain = c
	m.message = fmt.Sprintf("Загружаем модель в цепочку из %d участников", len(members))
	for _, id := range members {
		if w, ok := m.workers[id]; ok {
			w.served = true
		}
	}
	m.mu.Unlock()
	log.Printf("chain: starting with %d workers %v split %v", len(members), members, split)
	go m.waitReady(cctx, c)
}

func (m *ChainManager) waitReady(ctx context.Context, c *liveChain) {
	deadline := time.Now().Add(m.cfg.LoadTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-c.exited:
			return
		case <-time.After(time.Second):
		}
		resp, err := http.Get(c.base + "/health")
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			m.mu.Lock()
			c.ready = true
			m.message = fmt.Sprintf("Модель работает: цепочка из %d участников", len(c.members))
			m.mu.Unlock()
			log.Printf("chain: ready after %v", time.Since(c.started).Round(time.Second))
			for i := 0; i < m.cfg.Parallel; i++ {
				go m.consume(ctx, c)
			}
			return
		}
	}
	m.breakChain("цепочка не загрузилась вовремя")
}

// consume takes tasks for the big model from the queue and runs them.
func (m *ChainManager) consume(ctx context.Context, c *liveChain) {
	for ctx.Err() == nil {
		wake := m.bell.wait()
		job, err := m.store.Assign(m.nodeID, []string{m.cfg.Model.ID}, m.timeout)
		if err != nil {
			log.Printf("chain: assign: %v", err)
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-time.After(5 * time.Second):
			}
			continue
		}
		res := m.generate(ctx, c, *job)
		if err := m.store.Complete(m.nodeID, job.ID, res); err != nil {
			log.Printf("chain: complete: %v", err)
		}
		m.bell.ring()
	}
}

func (m *ChainManager) generate(ctx context.Context, c *liveChain, job protocol.Job) protocol.JobResult {
	start := time.Now()
	jctx, cancel := context.WithDeadline(ctx, time.Unix(job.DeadlineUnix, 0))
	defer cancel()
	body, _ := json.Marshal(map[string]any{
		"messages": job.Messages, "max_tokens": job.MaxTokens, "temperature": job.Temperature, "seed": job.Seed,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	})
	req, _ := http.NewRequestWithContext(jctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return protocol.JobResult{Error: "chain: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &cr) != nil || len(cr.Choices) == 0 {
		return protocol.JobResult{Error: fmt.Sprintf("chain: %s %.200s", resp.Status, raw)}
	}
	return protocol.JobResult{
		Output: stripThink(cr.Choices[0].Message.Content), TokensOut: cr.Usage.CompletionTokens,
		DurationMS: time.Since(start).Milliseconds(),
	}
}

func stripThink(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<think>") {
		if i := strings.Index(s, "</think>"); i >= 0 {
			s = strings.TrimSpace(s[i+len("</think>"):])
		}
	}
	return s
}

func (m *ChainManager) breakChain(reason string) {
	m.mu.Lock()
	c := m.chain
	m.chain = nil
	if c != nil {
		m.message = reason
	}
	m.mu.Unlock()
	if c == nil {
		return
	}
	log.Printf("chain: stopped: %s", reason)
	c.cancel()
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
		<-c.exited
	}
}

// Stats is the chain part of the live dashboard.
func (m *ChainManager) Stats() protocol.ChainStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := protocol.ChainStats{Model: m.cfg.Model.ID, State: "waiting", Message: m.message, NeedMB: m.cfg.Model.MinVRAMMB}
	for _, w := range m.workers {
		st.HaveMB += w.budgetMB
	}
	st.Spares = len(m.workers)
	if m.chain != nil {
		st.Members = len(m.chain.members)
		st.Spares -= st.Members
		st.State = "loading"
		if m.chain.ready {
			st.State, st.ChainsUp = "ready", 1
		}
	}
	return st
}

var errNotChain = errors.New("this hive does not run a chain model")
