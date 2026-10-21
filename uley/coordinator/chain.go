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
	"math"
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
	// Balance picks how layers are split: "fastest" (default) minimises the
	// time per word; "equal" gives every link the same time per step.
	Balance string
	// MinRounds: a link's speed counts once it answered this many rounds.
	MinRounds int64
	// RebalanceAfter: measure a fresh chain this long before reshaping it.
	RebalanceAfter time.Duration
	// RebalanceGap: never reshape more often than this.
	RebalanceGap time.Duration
	// MeasureEvery: how often link speeds are read.
	MeasureEvery time.Duration
}

func (c *ChainConfig) defaults() {
	if c.Parallel == 0 {
		c.Parallel = c.Model.Slots
	}
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
	if c.Balance == "" {
		c.Balance = BalanceFastest
	}
	if c.MinRounds == 0 {
		c.MinRounds = 50
	}
	if c.RebalanceAfter == 0 {
		c.RebalanceAfter = 2 * time.Minute
	}
	if c.RebalanceGap == 0 {
		c.RebalanceGap = 10 * time.Minute
	}
	if c.MeasureEvery == 0 {
		c.MeasureEvery = 15 * time.Second
	}
}

// Balance modes.
const (
	BalanceFastest = "fastest"
	BalanceEqual   = "equal"
)

type chainWorker struct {
	id       string
	budgetMB int
	rttMS    float64 // network round trip reported by the worker
	seen     time.Time
}

type liveChain struct {
	members []string
	layers  []int
	stepMS  []float64 // measured time per step of each link, 0 = not yet
	readyAt time.Time
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
	// speeds remembers each volunteer's compute time per layer, so a
	// returning computer gets its fair share right away.
	speeds        map[string]float64
	lastMeasure   time.Time
	lastRebalance time.Time
}

func NewChainManager(store *Store, cfg ChainConfig) (*ChainManager, error) {
	cfg.defaults()
	if !cfg.Model.Chain {
		return nil, fmt.Errorf("%s is not a chain model", cfg.Model.ID)
	}
	if cfg.Balance != "" && cfg.Balance != BalanceFastest && cfg.Balance != BalanceEqual {
		return nil, fmt.Errorf("unknown balance mode %q (fastest or equal)", cfg.Balance)
	}
	reg, err := store.RegisterNode(protocol.RegisterRequest{NodeName: "Цепочка " + cfg.Model.Name, OS: "chain", Arch: "-", ClientVersion: "-"})
	if err != nil {
		return nil, err
	}
	return &ChainManager{
		cfg: cfg, store: store, relay: rpctunnel.NewRelay(), nodeID: reg.NodeID,
		workers: map[string]*chainWorker{}, message: "Ждём участников", speeds: map[string]float64{},
	}, nil
}

// Touch records a worker's poll and returns whether it is in the chain.
func (m *ChainManager) Touch(nodeID string, budgetMB int, rttMS float64) (bool, error) {
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
	if rttMS > 0 {
		w.rttMS = rttMS
	}
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
			m.measure(ch)
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

// layersPerMB: how much video memory one layer of the model needs.
func (m *ChainManager) layerMB() int {
	n := max(m.cfg.Model.Layers, 1)
	return (m.cfg.Model.MinVRAMMB + n - 1) / n
}

// pick chooses the links and their share of layers from everybody online,
// using the speeds measured so far. Caller must not hold m.mu.
func (m *ChainManager) pick() ([]string, []int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, have := m.inputsLocked()
	need := m.cfg.Model.MinVRAMMB
	if have < need {
		m.message = fmt.Sprintf("Ждём участников: есть %.1f из %.1f ГБ видеопамяти", float64(have)/1024, float64(need)/1024)
		return nil, nil, false
	}
	layers, ok := m.allocate(in)
	if !ok {
		m.message = "Не хватает памяти участников даже для разбиения по слоям, ждём ещё"
		return nil, nil, false
	}
	var members []string
	var split []int
	for i, l := range layers {
		if l > 0 {
			members = append(members, in[i].ID)
			split = append(split, l)
		}
	}
	return members, split, true
}

func (m *ChainManager) inputsLocked() ([]BalanceInput, int) {
	var in []BalanceInput
	have := 0
	perLayer := m.layerMB()
	for _, w := range m.workers {
		if w.budgetMB < m.cfg.MinWorkerMB {
			continue
		}
		have += w.budgetMB
		in = append(in, BalanceInput{ID: w.id, MaxLayers: w.budgetMB / perLayer, LayerMS: m.speeds[w.id], NetMS: w.rttMS})
	}
	sort.Slice(in, func(i, j int) bool { return in[i].ID < in[j].ID }) // stable choices
	return in, have
}

func (m *ChainManager) allocate(in []BalanceInput) ([]int, bool) {
	total := max(m.cfg.Model.Layers, 1)
	if m.cfg.Balance == BalanceEqual {
		l, _, ok := Balance(in, total)
		return l, ok
	}
	l, _, ok := Fastest(in, total)
	return l, ok
}

// measure reads how long the head waits for every link, learns each
// volunteer's speed, and reshapes the chain when that would make words
// come out clearly faster.
func (m *ChainManager) measure(c *liveChain) {
	m.mu.Lock()
	if !c.ready || time.Since(m.lastMeasure) < m.cfg.MeasureEvery {
		m.mu.Unlock()
		return
	}
	m.lastMeasure = time.Now()
	m.mu.Unlock()

	steps := make([]float64, len(c.members))
	complete := true
	for i, id := range c.members {
		r, ok := m.relay.Meter(id)
		if !ok || r.Rounds < m.cfg.MinRounds {
			complete = false
			continue
		}
		steps[i] = float64(r.Wait.Microseconds()) / 1000 / float64(r.Rounds)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current := 0.0
	for i, id := range c.members {
		if steps[i] == 0 {
			continue
		}
		c.stepMS[i] = steps[i]
		current += steps[i]
		net := 0.0
		if w, ok := m.workers[id]; ok {
			net = w.rttMS
		}
		compute := math.Max(steps[i]-net, 0.05*steps[i])
		m.speeds[id] = compute / float64(c.layers[i])
	}
	if !complete || time.Since(c.readyAt) < m.cfg.RebalanceAfter || time.Since(m.lastRebalance) < m.cfg.RebalanceGap {
		return
	}
	in, _ := m.inputsLocked()
	layers, ok := m.allocate(in)
	if !ok {
		return
	}
	cost := measuredCosts(in)
	predicted := WordTime(in, layers, cost)
	if m.cfg.Balance == BalanceEqual {
		predicted = RoundTime(in, layers, cost) * float64(countPositive(layers))
	}
	if predicted >= 0.8*current {
		return // not worth a reload
	}
	if _, running := m.store.NodeLoad(m.nodeID); running > 0 {
		return // wait for a quiet moment, nobody is waiting for an answer then
	}
	m.lastRebalance = time.Now()
	log.Printf("chain: rebalancing, %.0f ms per word now, %.0f ms expected", current, predicted)
	go m.breakChain(fmt.Sprintf("Перераспределяем слои по скорости участников: было %.0f мс на слово, станет около %.0f", current, predicted))
}

func countPositive(v []int) int {
	n := 0
	for _, x := range v {
		if x > 0 {
			n++
		}
	}
	return n
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
	c := &liveChain{members: members, layers: split, stepMS: make([]float64, len(members)), exited: make(chan struct{}), base: "http://127.0.0.1:" + strconv.Itoa(port), key: randKey(), started: time.Now()}
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
	if t := m.cfg.Model.KVCacheType; t != "" {
		args = append(args, "--cache-type-k", t, "--cache-type-v", t) // halves the KV cache
	}
	if rs := m.cfg.Model.RopeScale; rs > 1 {
		// YaRN stretches the model's native context (e.g. 32k → 64k).
		args = append(args, "--rope-scaling", "yarn", "--rope-scale", strconv.FormatFloat(rs, 'f', -1, 64),
			"--yarn-orig-ctx", strconv.Itoa(m.cfg.Model.YarnOrigCtx))
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
			for _, id := range c.members {
				m.relay.ResetMeter(id) // the weight upload is not slowness
			}
			m.mu.Lock()
			c.ready, c.readyAt = true, time.Now()
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
	body, _ := json.Marshal(chatRequest(job))
	req, _ := http.NewRequestWithContext(jctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return protocol.JobResult{Error: "chain: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return protocol.JobResult{Error: fmt.Sprintf("chain: %s %.300s", resp.Status, raw)}
	}
	res, err := ParseChatResponse(raw)
	if err != nil {
		return protocol.JobResult{Error: "chain: " + err.Error()}
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

// chatRequest builds the llama-server request for a job. Tools are passed
// through as-is: the model may answer with tool calls, which go back to
// the caller's agent to execute.
func chatRequest(job protocol.Job) map[string]any {
	req := map[string]any{
		"messages": job.Messages, "max_tokens": job.MaxTokens, "temperature": job.Temperature, "seed": job.Seed,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	if len(job.Tools) > 0 {
		req["tools"] = job.Tools
		if len(job.ToolChoice) > 0 {
			req["tool_choice"] = job.ToolChoice
		}
	}
	return req
}

// ParseChatResponse turns an OpenAI-style completion into a job result.
func ParseChatResponse(raw []byte) (protocol.JobResult, error) {
	var cr struct {
		Choices []struct {
			Message struct {
				Content   *string         `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &cr); err != nil || len(cr.Choices) == 0 {
		return protocol.JobResult{}, errors.New("unexpected engine response")
	}
	ch := cr.Choices[0]
	res := protocol.JobResult{FinishReason: ch.FinishReason, TokensOut: cr.Usage.CompletionTokens}
	if ch.Message.Content != nil {
		res.Output = stripThink(*ch.Message.Content)
	}
	if len(ch.Message.ToolCalls) > 2 && string(ch.Message.ToolCalls) != "null" {
		res.ToolCalls = ch.Message.ToolCalls
	}
	return res, nil
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
	st := protocol.ChainStats{Model: m.cfg.Model.ID, State: "waiting", Message: m.message, NeedMB: m.cfg.Model.MinVRAMMB, Balance: m.cfg.Balance}
	inChain := map[string]int{}
	if c := m.chain; c != nil {
		st.Members = len(c.members)
		st.State = "loading"
		if c.ready {
			st.State, st.ChainsUp = "ready", 1
		}
		for i, id := range c.members {
			inChain[id] = i
			st.WordMS += c.stepMS[i]
			st.Links = append(st.Links, protocol.ChainLink{ID: id, Layers: c.layers[i], StepMS: c.stepMS[i], NetMS: m.rttLocked(id), InChain: true})
		}
	}
	ids := make([]string, 0, len(m.workers))
	for id, w := range m.workers {
		st.HaveMB += w.budgetMB
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := inChain[id]; !ok {
			st.Spares++
			st.Links = append(st.Links, protocol.ChainLink{ID: id, NetMS: m.workers[id].rttMS})
		}
	}
	return st
}

func (m *ChainManager) rttLocked(id string) float64 {
	if w, ok := m.workers[id]; ok {
		return w.rttMS
	}
	return 0
}

var errNotChain = errors.New("this hive does not run a chain model")
