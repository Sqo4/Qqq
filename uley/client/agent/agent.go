// Package agent is the heart of the client: it decides when the computer
// may work, keeps the right model loaded, takes jobs from the coordinator
// and runs them. Everything the user can see in the tray comes from here.
package agent

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
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sqo4/uley/client/config"
	"github.com/sqo4/uley/client/download"
	"github.com/sqo4/uley/client/engine"
	"github.com/sqo4/uley/client/gpu"
	"github.com/sqo4/uley/client/sysmon"
	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
	"github.com/sqo4/uley/internal/rpctunnel"
)

type Agent struct {
	Config    *config.Store
	Engine    engine.Engine
	Monitor   sysmon.Monitor
	DetectGPU func() []protocol.GPU
	Version   string
	ModelsDir string
	HTTP      *http.Client
	// Fetch downloads a model; tests replace it.
	Fetch func(ctx context.Context, m models.Model, p download.Progress) (string, error)
	// AllowInsecure permits a plain-http coordinator on any host
	// (developer mode); loopback addresses are always allowed.
	AllowInsecure bool
	// Tick is how often pause rules are re-checked.
	Tick time.Duration
	// HeartbeatEvery is the heartbeat interval.
	HeartbeatEvery time.Duration
	// RPC lends the GPU to a chain running the hive's big model; nil if
	// this build has no rpc-server.
	RPC *engine.RPCServer

	once     sync.Once
	mu       sync.Mutex
	status   Status
	gpus     []protocol.GPU
	kick     chan struct{}
	hive     protocol.Hive
	hiveTime time.Time
}

// Status is a snapshot for the tray and the settings page.
type Status struct {
	State       string         `json:"state"`
	Reason      string         `json:"reason"`
	Model       string         `json:"model"`
	Download    float64        `json:"download"` // 0..1 while downloading, else -1
	JobsDone    int            `json:"jobs_done"`
	TokensOut   int64          `json:"tokens_out"`
	GPUs        []protocol.GPU `json:"gpus"`
	VRAMBudget  int            `json:"vram_budget_mb"`
	LastError   string         `json:"last_error,omitempty"`
	Connected   bool           `json:"connected"`
	IdleUnknown bool           `json:"idle_unknown"`
	Busy        bool           `json:"busy"`     // a job is running right now
	Mode        string         `json:"mode"`     // single or chain
	InChain     bool           `json:"in_chain"` // holds layers of the big model now
}

func (a *Agent) init() {
	if a.Tick == 0 {
		a.Tick = time.Second
	}
	if a.HeartbeatEvery == 0 {
		a.HeartbeatEvery = 30 * time.Second
	}
	if a.HTTP == nil {
		a.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if a.Fetch == nil {
		a.Fetch = func(ctx context.Context, m models.Model, p download.Progress) (string, error) {
			return download.Fetch(ctx, &http.Client{}, a.ModelsDir, m, p)
		}
	}
	a.kick = make(chan struct{}, 1)
	c := a.Config.Counters()
	a.status = Status{State: protocol.StatePaused, Download: -1, JobsDone: c.JobsDone, TokensOut: c.TokensOut}
}

func (a *Agent) Status() Status {
	a.once.Do(a.init)
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.status
	s.GPUs = append([]protocol.GPU(nil), a.gpus...)
	return s
}

func (a *Agent) update(f func(*Status)) {
	a.mu.Lock()
	f(&a.status)
	a.mu.Unlock()
}

// Kick makes the agent re-check its rules now, e.g. after the user pressed
// pause in the tray.
func (a *Agent) Kick() {
	a.once.Do(a.init)
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// pauseReason explains in plain words why we must not use the GPU now.
func (a *Agent) pauseReason(s config.Settings) string {
	if s.Consent < config.ConsentVersion {
		return "Ждём вашего согласия"
	}
	if s.Paused {
		return "Пауза"
	}
	if s.PauseOnBattery && a.Monitor.OnBattery() {
		return "Ноутбук работает от батареи"
	}
	if s.PauseOnFullscreen && a.Monitor.Fullscreen() {
		return "Запущена полноэкранная игра или видео"
	}
	if s.IdleOnly {
		idle, ok := a.Monitor.IdleTime()
		a.update(func(st *Status) { st.IdleUnknown = !ok })
		if ok && idle < time.Duration(s.IdleMinutes)*time.Minute {
			return fmt.Sprintf("Компьютером пользуются — начнём после %d мин простоя", s.IdleMinutes)
		}
	}
	return ""
}

// CheckCoordinatorURL enforces HTTPS for anything outside this computer
// and the local network.
func CheckCoordinatorURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("адрес координатора указан неверно")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		switch {
		case host == "localhost" || (ip != nil && ip.IsLoopback()) || allowInsecure:
			return nil
		case ip != nil && ip.IsPrivate(), strings.HasSuffix(host, ".local"):
			// A hive inside a classroom or dorm network (local build).
			return nil
		}
	}
	return errors.New("координатор в интернете должен быть доступен по https://")
}

// Run works until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	a.once.Do(a.init)
	a.refreshGPUs()
	go a.heartbeatLoop(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		err := a.step(ctx)
		if err == nil {
			backoff = time.Second
			continue
		}
		if ctx.Err() != nil {
			break
		}
		log.Printf("agent: %v", err)
		a.update(func(st *Status) { st.LastError = err.Error() })
		a.sleep(ctx, backoff)
		backoff = min(backoff*2, 2*time.Minute)
	}
	a.Engine.Unload()
}

func (a *Agent) refreshGPUs() {
	g := a.DetectGPU()
	a.mu.Lock()
	a.gpus = g
	a.mu.Unlock()
}

func (a *Agent) budget(s config.Settings) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return gpu.Usable(a.gpus) * s.MaxVRAMPercent / 100
}

// sleep waits for d, a kick or ctx.
func (a *Agent) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-a.kick:
	}
}

// pauseAware returns a context that is cancelled as soon as a pause rule
// fires, so a running job or download stops within a second.
func (a *Agent) pauseAware(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(a.Tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-a.kick:
			}
			if a.pauseReason(a.Config.Get()) != "" {
				cancel()
				return
			}
		}
	}()
	return ctx, cancel
}

// step does one round of work: wait out pauses, or prepare, or run a job.
func (a *Agent) step(ctx context.Context) error {
	s := a.Config.Get()
	if reason := a.pauseReason(s); reason != "" {
		if a.Engine.Loaded() != "" {
			a.Engine.Unload() // give the video memory back right away
		}
		if a.RPC != nil && a.RPC.Running() {
			a.leaveChain(ctx)
		}
		a.update(func(st *Status) {
			st.State, st.Reason, st.Model, st.Busy, st.InChain = protocol.StatePaused, reason, "", false, false
		})
		a.sleep(ctx, a.Tick)
		return nil
	}
	if err := CheckCoordinatorURL(s.Coordinator, a.AllowInsecure); err != nil {
		return err
	}
	if err := a.ensureRegistered(ctx, s); err != nil {
		return err
	}
	budget := a.budget(s)
	a.update(func(st *Status) { st.VRAMBudget = budget })
	hive, err := a.hiveInfo(ctx, s)
	if err != nil {
		return fmt.Errorf("hive info: %w", err)
	}
	a.update(func(st *Status) { st.Mode = hive.Mode })
	if hive.Mode == protocol.ModeChain {
		return a.chainStep(ctx, hive, budget)
	}
	if hive.Model != "" {
		s.Model = hive.Model // the hive serves one model
	}
	m, ok := models.Best(budget, s.Model)
	if ok && hive.Model != "" && m.ID != hive.Model {
		ok = false
	}
	if !ok {
		a.update(func(st *Status) {
			st.State, st.Reason = protocol.StateUnsupported, "Не нашли видеокарту с достаточным объёмом памяти"
		})
		a.sleep(ctx, time.Minute)
		return nil
	}

	work, cancel := a.pauseAware(ctx)
	defer cancel()

	if a.Engine.Loaded() != m.ID {
		a.update(func(st *Status) {
			st.State, st.Reason, st.Model = protocol.StatePreparing, "Скачиваем модель "+m.Name, ""
		})
		path, err := a.Fetch(work, m, func(done, total int64) {
			a.update(func(st *Status) { st.Download = float64(done) / float64(total) })
		})
		a.update(func(st *Status) { st.Download = -1 })
		if err != nil {
			if work.Err() != nil {
				return nil // paused mid-download; it resumes later
			}
			return fmt.Errorf("model download: %w", err)
		}
		a.update(func(st *Status) { st.Reason = "Загружаем модель в видеокарту" })
		if err := a.Engine.Load(work, m, path); err != nil {
			if work.Err() != nil {
				return nil
			}
			return fmt.Errorf("engine: %w", err)
		}
	}
	a.update(func(st *Status) {
		st.State, st.Reason, st.Model, st.LastError = protocol.StateWorking, "Ждём задачу", m.ID, ""
	})

	job, err := a.poll(work, m.ID)
	if err != nil || job == nil {
		if work.Err() != nil && ctx.Err() == nil {
			return nil
		}
		return err
	}
	return a.runJob(ctx, work, s, *job)
}

// hiveInfo asks the coordinator how the hive runs, at most once a minute.
func (a *Agent) hiveInfo(ctx context.Context, s config.Settings) (protocol.Hive, error) {
	a.mu.Lock()
	h, at := a.hive, a.hiveTime
	a.mu.Unlock()
	if time.Since(at) < time.Minute && h.Mode != "" {
		return h, nil
	}
	code, err := a.call(ctx, http.MethodGet, s.Coordinator, "", "/hive", nil, &h)
	if code == http.StatusNotFound {
		h, err = protocol.Hive{Mode: protocol.ModeSingle}, nil // older coordinator
	}
	if err != nil {
		return h, err
	}
	a.mu.Lock()
	a.hive, a.hiveTime = h, time.Now()
	a.mu.Unlock()
	return h, nil
}

// minChainMB: a smaller GPU is not worth a network hop in the chain.
const minChainMB = 1536

// chainStep lends the GPU to the chain until a pause rule fires.
func (a *Agent) chainStep(ctx context.Context, hive protocol.Hive, budget int) error {
	if a.RPC == nil || budget < minChainMB {
		a.update(func(st *Status) {
			st.State, st.Reason = protocol.StateUnsupported, "Видеокарта не подходит для общей модели улья"
		})
		a.sleep(ctx, time.Minute)
		return nil
	}
	if a.Engine.Loaded() != "" {
		a.Engine.Unload()
	}
	work, cancel := a.pauseAware(ctx)
	defer cancel()

	a.RPC.Device = bestDevice(a.Status().GPUs)
	local, err := a.RPC.Start()
	if err != nil {
		return err
	}
	a.update(func(st *Status) {
		st.State, st.Model, st.LastError = protocol.StateChain, hive.Model, ""
		st.Reason = "В запасе: ждём места в цепочке общей модели"
	})
	poll := func(pctx context.Context) (string, error) {
		if !a.RPC.Running() {
			return "", errors.New("rpc-server stopped")
		}
		var w protocol.ChainWait
		_, err := a.api(pctx, http.MethodGet, fmt.Sprintf("/chain/wait?budget=%d", budget), nil, &w)
		if err != nil {
			return "", err
		}
		a.update(func(st *Status) {
			st.InChain = w.InChain
			if w.InChain {
				st.Reason = "Держим часть общей модели улья"
			} else {
				st.Reason = "В запасе: ждём места в цепочке общей модели"
			}
		})
		return w.Dial, nil
	}
	dial := func(dctx context.Context, tok string) (net.Conn, error) {
		return rpctunnel.Dial(dctx, a.Config.Get().Coordinator, protocol.APIPrefix+"/chain/dial?t="+url.QueryEscape(tok),
			a.Config.Identity().Token, nil)
	}
	err = rpctunnel.Serve(work, local, poll, dial)
	paused := work.Err() != nil && ctx.Err() == nil
	a.leaveChain(ctx)
	if paused {
		return nil
	}
	return err
}

// leaveChain frees the GPU and tells the coordinator at once, so the chain
// is rebuilt without waiting for a timeout.
func (a *Agent) leaveChain(ctx context.Context) {
	a.RPC.Stop()
	a.update(func(st *Status) { st.InChain = false })
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	a.api(lctx, http.MethodPost, "/chain/leave", struct{}{}, nil)
}

func bestDevice(gpus []protocol.GPU) string {
	best, dev := 0, ""
	for _, g := range gpus {
		if g.Device != "" && g.VRAMMB > best {
			best, dev = g.VRAMMB, g.Device
		}
	}
	return dev
}

func (a *Agent) runJob(ctx, work context.Context, s config.Settings, job protocol.Job) error {
	a.update(func(st *Status) { st.Busy, st.Reason = true, "Выполняем задачу" })
	defer a.update(func(st *Status) { st.Busy = false })

	start := time.Now()
	res := protocol.JobResult{}
	req, verr := validateJob(job, a.Engine.Loaded())
	if verr != nil {
		res.Error = verr.Error()
	} else {
		jctx, cancel := context.WithDeadline(work, time.Unix(job.DeadlineUnix, 0))
		out, err := a.Engine.Generate(jctx, req)
		cancel()
		switch {
		case err != nil && work.Err() != nil:
			res.Error = "interrupted: the owner needs the computer"
		case err != nil:
			res.Error = err.Error()
		default:
			res.Output, res.TokensOut = out.Text, out.Tokens
			if len(res.Output) > protocol.MaxOutputBytes {
				res.Output = res.Output[:protocol.MaxOutputBytes]
			}
		}
	}
	elapsed := time.Since(start)
	res.DurationMS = elapsed.Milliseconds()

	// Report even when paused, so the job goes to someone else at once.
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := a.api(rctx, http.MethodPost, "/jobs/"+url.PathEscape(job.ID)+"/result", res, nil); err != nil {
		return fmt.Errorf("send result: %w", err)
	}
	if res.Error == "" {
		c := a.Config.AddJob(res.TokensOut)
		a.update(func(st *Status) { st.JobsDone, st.TokensOut = c.JobsDone, c.TokensOut })
	}
	// GPU load limit: rest in proportion to the time we worked.
	if s.MaxGPUPercent < 100 {
		rest := elapsed * time.Duration(100-s.MaxGPUPercent) / time.Duration(s.MaxGPUPercent)
		a.update(func(st *Status) { st.Reason = "Короткий отдых (лимит нагрузки)" })
		a.sleep(work, rest)
	}
	return nil
}

// validateJob re-checks everything the coordinator sent, so a broken or
// hostile coordinator cannot make the node do more than it agreed to.
func validateJob(j protocol.Job, loaded string) (engine.Request, error) {
	if _, ok := models.Get(j.Model); !ok || j.Model != loaded {
		return engine.Request{}, fmt.Errorf("model %q is not loaded here", j.Model)
	}
	if len(j.Messages) == 0 || len(j.Messages) > protocol.MaxMessages {
		return engine.Request{}, errors.New("bad message count")
	}
	total := 0
	for _, m := range j.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return engine.Request{}, fmt.Errorf("bad role %q", m.Role)
		}
		total += len(m.Content)
	}
	if total > protocol.MaxPromptBytes {
		return engine.Request{}, errors.New("prompt too long")
	}
	maxTok := j.MaxTokens
	if maxTok <= 0 || maxTok > protocol.MaxTokensLimit {
		maxTok = min(max(maxTok, protocol.DefaultMaxTokens), protocol.MaxTokensLimit)
	}
	temp := min(max(j.Temperature, 0), 2)
	return engine.Request{Messages: j.Messages, MaxTokens: maxTok, Temperature: temp, Seed: j.Seed}, nil
}

func (a *Agent) ensureRegistered(ctx context.Context, s config.Settings) error {
	id := a.Config.Identity()
	if id.Token != "" && id.Coordinator == s.Coordinator {
		return nil
	}
	name := s.NodeName
	if name == "" {
		// Never send the computer name: it often contains a real name.
		b := make([]byte, 3)
		rand.Read(b)
		name = "uley-" + hex.EncodeToString(b)
	}
	req := protocol.RegisterRequest{
		NodeName: name, OS: runtime.GOOS, Arch: runtime.GOARCH,
		ClientVersion: a.Version, GPUs: a.Status().GPUs,
	}
	var resp protocol.RegisterResponse
	if _, err := a.call(ctx, http.MethodPost, s.Coordinator, "", "/nodes/register", req, &resp); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	return a.Config.SetIdentity(config.Identity{Coordinator: s.Coordinator, NodeID: resp.NodeID, Token: resp.Token})
}

func (a *Agent) poll(ctx context.Context, model string) (*protocol.Job, error) {
	var job protocol.Job
	code, err := a.api(ctx, http.MethodPost, "/jobs/poll", protocol.PollRequest{Models: []string{model}, WaitSeconds: 25}, &job)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &job, nil
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	lastGPU := time.Now()
	wait := min(2*time.Second, a.HeartbeatEvery) // report soon after start
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = a.HeartbeatEvery
		if time.Since(lastGPU) > time.Hour {
			a.refreshGPUs()
			lastGPU = time.Now()
		}
		if a.Config.Identity().Token == "" {
			continue
		}
		st := a.Status()
		hb := protocol.HeartbeatRequest{
			State: st.State, StateReason: st.Reason, ClientVersion: a.Version,
			GPUs: st.GPUs, VRAMBudgetMB: st.VRAMBudget,
		}
		if (st.State == protocol.StateWorking || st.InChain) && st.Model != "" {
			hb.Models = []string{st.Model}
		}
		hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := a.api(hctx, http.MethodPost, "/nodes/heartbeat", hb, nil)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("heartbeat: %v", err)
		}
	}
}

// api calls the coordinator as this node.
func (a *Agent) api(ctx context.Context, method, path string, in, out any) (int, error) {
	s := a.Config.Get()
	if err := CheckCoordinatorURL(s.Coordinator, a.AllowInsecure); err != nil {
		return 0, err
	}
	code, err := a.call(ctx, method, s.Coordinator, a.Config.Identity().Token, path, in, out)
	if code == http.StatusUnauthorized {
		// The coordinator forgot us (e.g. its database was reset).
		a.Config.SetIdentity(config.Identity{})
	}
	return code, err
}

func (a *Agent) call(ctx context.Context, method, base, token, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+protocol.APIPrefix+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "uley-client/"+a.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		a.update(func(st *Status) { st.Connected = false })
		return 0, err
	}
	defer resp.Body.Close()
	a.update(func(st *Status) { st.Connected = true })
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return resp.StatusCode, nil
	case resp.StatusCode >= 300:
		var e protocol.ErrorResponse
		json.Unmarshal(raw, &e)
		if resp.StatusCode == http.StatusForbidden {
			e.Error = "координатор отключил этот узел: " + e.Error
		}
		return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("bad response: %w", err)
		}
	}
	return resp.StatusCode, nil
}
