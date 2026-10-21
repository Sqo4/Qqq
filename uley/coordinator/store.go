package coordinator

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sqo4/uley/internal/protocol"
	_ "modernc.org/sqlite"
)

// Store keeps all coordinator state in SQLite. The connection pool is
// limited to one connection, so every statement is serialised; that is
// plenty for an MVP and keeps the dispatch logic free of races.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id          TEXT PRIMARY KEY,
	token_hash  TEXT NOT NULL UNIQUE,
	name        TEXT NOT NULL,
	os          TEXT NOT NULL,
	arch        TEXT NOT NULL,
	version     TEXT NOT NULL,
	gpus        TEXT NOT NULL DEFAULT '[]',
	vram_mb     INTEGER NOT NULL DEFAULT 0,
	state       TEXT NOT NULL DEFAULT 'paused',
	models      TEXT NOT NULL DEFAULT '[]',
	created_at  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	jobs_done   INTEGER NOT NULL DEFAULT 0,
	jobs_failed INTEGER NOT NULL DEFAULT 0,
	tokens_out  INTEGER NOT NULL DEFAULT 0,
	trust       REAL NOT NULL DEFAULT 1.0,
	banned      INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS tasks (
	id           TEXT PRIMARY KEY,
	model        TEXT NOT NULL,
	payload      TEXT NOT NULL,
	status       TEXT NOT NULL,
	replicas     INTEGER NOT NULL DEFAULT 1,
	failures     INTEGER NOT NULL DEFAULT 0,
	output       TEXT NOT NULL DEFAULT '',
	error        TEXT NOT NULL DEFAULT '',
	verify       TEXT NOT NULL DEFAULT 'none',
	created_at   INTEGER NOT NULL,
	completed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status, created_at);
CREATE TABLE IF NOT EXISTS assignments (
	id          TEXT PRIMARY KEY,
	task_id     TEXT NOT NULL REFERENCES tasks(id),
	node_id     TEXT NOT NULL REFERENCES nodes(id),
	status      TEXT NOT NULL,
	output      TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	tokens_out  INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	assigned_at INTEGER NOT NULL,
	deadline    INTEGER NOT NULL,
	finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS assignments_task ON assignments(task_id);
CREATE INDEX IF NOT EXISTS assignments_status ON assignments(status, deadline);
`

// Assignment statuses.
const (
	asRunning = "running"
	asDone    = "done"
	asFailed  = "failed"
	asExpired = "expired"
)

func OpenStore(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + socialSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func maxVRAM(gpus []protocol.GPU) int {
	m := 0
	for _, g := range gpus {
		if g.VRAMMB > m {
			m = g.VRAMMB
		}
	}
	return m
}

func (s *Store) RegisterNode(req protocol.RegisterRequest) (protocol.RegisterResponse, error) {
	id, token := newID("n_"), newToken()
	gpus, _ := json.Marshal(req.GPUs)
	now := s.now().Unix()
	_, err := s.db.Exec(`INSERT INTO nodes(id, token_hash, name, os, arch, version, gpus, vram_mb, created_at, last_seen)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		id, hashToken(token), req.NodeName, req.OS, req.Arch, req.ClientVersion, string(gpus), maxVRAM(req.GPUs), now, now)
	return protocol.RegisterResponse{NodeID: id, Token: token}, err
}

var ErrUnauthorized = errors.New("unauthorized")

// NodeByToken returns the id of the node owning token.
func (s *Store) NodeByToken(token string) (id string, banned bool, err error) {
	err = s.db.QueryRow(`SELECT id, banned FROM nodes WHERE token_hash=?`, hashToken(token)).Scan(&id, &banned)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrUnauthorized
	}
	return id, banned, err
}

func (s *Store) Heartbeat(nodeID string, hb protocol.HeartbeatRequest) error {
	gpus, _ := json.Marshal(hb.GPUs)
	ms, _ := json.Marshal(hb.Models)
	_, err := s.db.Exec(`UPDATE nodes SET state=?, version=?, gpus=?, vram_mb=?, models=?, last_seen=? WHERE id=?`,
		hb.State, hb.ClientVersion, string(gpus), maxVRAM(hb.GPUs), string(ms), s.now().Unix(), nodeID)
	return err
}

func (s *Store) Touch(nodeID string) error {
	_, err := s.db.Exec(`UPDATE nodes SET last_seen=? WHERE id=?`, s.now().Unix(), nodeID)
	return err
}

type taskPayload struct {
	Messages    []protocol.Message `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature"`
	Seed        int64              `json:"seed"`
}

// CreateTask queues a task. replicas > 1 means the same task is computed by
// that many different nodes and the answers are compared.
func (s *Store) CreateTask(model string, p taskPayload, replicas int) (string, error) {
	id := newID("t_")
	raw, _ := json.Marshal(p)
	_, err := s.db.Exec(`INSERT INTO tasks(id, model, payload, status, replicas, created_at) VALUES(?,?,?,?,?,?)`,
		id, model, string(raw), protocol.TaskQueued, replicas, s.now().Unix())
	return id, err
}

func (s *Store) GetTask(id string) (protocol.Task, error) {
	var t protocol.Task
	err := s.db.QueryRow(`SELECT id, status, model, output, error, verify, created_at, completed_at FROM tasks WHERE id=?`, id).
		Scan(&t.ID, &t.Status, &t.Model, &t.Output, &t.Error, &t.Verify, &t.CreatedAt, &t.CompletedAt)
	return t, err
}

// Assign hands the oldest suitable task to nodeID, or returns nil. A node
// never gets two replicas of the same task; it may retry a task it failed
// (e.g. paused because its owner started a game).
func (s *Store) Assign(nodeID string, models []string, timeout time.Duration) (*protocol.Job, error) {
	if len(models) == 0 {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	args := []any{protocol.TaskQueued, protocol.TaskRunning}
	for _, m := range models {
		args = append(args, m)
	}
	args = append(args, nodeID)
	q := `SELECT t.id, t.model, t.payload FROM tasks t
		WHERE t.status IN (?,?) AND t.model IN (` + strings.TrimSuffix(strings.Repeat("?,", len(models)), ",") + `)
		AND (SELECT COUNT(*) FROM assignments a WHERE a.task_id=t.id AND a.status IN ('running','done')) < t.replicas
		AND NOT EXISTS (SELECT 1 FROM assignments a WHERE a.task_id=t.id AND a.node_id=? AND a.status IN ('running','done'))
		ORDER BY t.created_at, t.rowid LIMIT 1`
	var taskID, model, payload string
	err = tx.QueryRow(q, args...).Scan(&taskID, &model, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p taskPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, err
	}
	now := s.now()
	deadline := now.Add(timeout).Unix()
	aid := newID("a_")
	if _, err := tx.Exec(`INSERT INTO assignments(id, task_id, node_id, status, assigned_at, deadline) VALUES(?,?,?,?,?,?)`,
		aid, taskID, nodeID, asRunning, now.Unix(), deadline); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE tasks SET status=? WHERE id=?`, protocol.TaskRunning, taskID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE nodes SET last_seen=? WHERE id=?`, now.Unix(), nodeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &protocol.Job{
		ID: aid, TaskID: taskID, Model: model, Messages: p.Messages,
		MaxTokens: p.MaxTokens, Temperature: p.Temperature, Seed: p.Seed, DeadlineUnix: deadline,
	}, nil
}

var ErrNotYours = errors.New("assignment not found or not owned by this node")

// maxFailures is how many failed or expired assignments a task survives.
const maxFailures = 3

// Complete stores a node's result and, when enough replicas are in,
// finalises the task.
func (s *Store) Complete(nodeID, assignmentID string, r protocol.JobResult) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var taskID string
	err = tx.QueryRow(`SELECT task_id FROM assignments WHERE id=? AND node_id=? AND status=?`, assignmentID, nodeID, asRunning).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotYours
	}
	if err != nil {
		return err
	}
	now := s.now().Unix()
	if r.Error != "" {
		if _, err := tx.Exec(`UPDATE assignments SET status=?, error=?, finished_at=? WHERE id=?`, asFailed, r.Error, now, assignmentID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE nodes SET jobs_failed=jobs_failed+1 WHERE id=?`, nodeID); err != nil {
			return err
		}
		if err := failTaskTx(tx, taskID, r.Error, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE assignments SET status=?, output=?, tokens_out=?, duration_ms=?, finished_at=? WHERE id=?`,
		asDone, r.Output, r.TokensOut, r.DurationMS, now, assignmentID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE nodes SET jobs_done=jobs_done+1, tokens_out=tokens_out+?, last_seen=? WHERE id=?`, r.TokensOut, now, nodeID); err != nil {
		return err
	}
	if err := settleTx(tx, taskID, now, false); err != nil {
		return err
	}
	return tx.Commit()
}

// failTaskTx counts a failure; after maxFailures the task fails for good,
// otherwise it goes back to the queue.
func failTaskTx(tx *sql.Tx, taskID, reason string, now int64) error {
	var failures int
	var status string
	if err := tx.QueryRow(`SELECT failures+1, status FROM tasks WHERE id=?`, taskID).Scan(&failures, &status); err != nil {
		return err
	}
	if status == protocol.TaskDone || status == protocol.TaskFailed {
		return nil
	}
	if failures >= maxFailures {
		var done int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM assignments WHERE task_id=? AND status=?`, taskID, asDone).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			// A replica already answered: keep that answer, unverified.
			if _, err := tx.Exec(`UPDATE tasks SET failures=? WHERE id=?`, failures, taskID); err != nil {
				return err
			}
			return settleTx(tx, taskID, now, true)
		}
		_, err := tx.Exec(`UPDATE tasks SET failures=?, status=?, error=?, completed_at=? WHERE id=?`,
			failures, protocol.TaskFailed, reason, now, taskID)
		return err
	}
	_, err := tx.Exec(`UPDATE tasks SET failures=? WHERE id=?`, failures, taskID)
	return err
}

type replicaResult struct {
	nodeID string
	output string
}

// settleTx finalises a task when its replicas are in. With force set (the
// second replica never showed up) a single answer is accepted as unverified.
func settleTx(tx *sql.Tx, taskID string, now int64, force bool) error {
	var replicas int
	var status string
	if err := tx.QueryRow(`SELECT replicas, status FROM tasks WHERE id=?`, taskID).Scan(&replicas, &status); err != nil {
		return err
	}
	if status == protocol.TaskDone || status == protocol.TaskFailed {
		return nil
	}
	rows, err := tx.Query(`SELECT node_id, output FROM assignments WHERE task_id=? AND status=? ORDER BY finished_at, rowid`, taskID, asDone)
	if err != nil {
		return err
	}
	var results []replicaResult
	for rows.Next() {
		var r replicaResult
		if err := rows.Scan(&r.nodeID, &r.output); err != nil {
			rows.Close()
			return err
		}
		results = append(results, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	finish := func(output, verify string) error {
		_, err := tx.Exec(`UPDATE tasks SET status=?, output=?, verify=?, completed_at=? WHERE id=?`,
			protocol.TaskDone, output, verify, now, taskID)
		return err
	}

	switch {
	case len(results) == 0:
		return nil
	case replicas == 1:
		return finish(results[0].output, protocol.VerifyNone)
	case len(results) < replicas && !force:
		return nil
	case len(results) == 1:
		return finish(results[0].output, protocol.VerifyPartial)
	}

	// Find the biggest group of answers that agree with each other.
	bestIdx, bestVotes := 0, 0
	for i := range results {
		votes := 0
		for j := range results {
			if Similar(results[i].output, results[j].output) {
				votes++
			}
		}
		if votes > bestVotes {
			bestIdx, bestVotes = i, votes
		}
	}
	if bestVotes >= 2 {
		winner := results[bestIdx].output
		for _, r := range results {
			delta := 0.02
			if !Similar(winner, r.output) {
				delta = -0.2
			}
			if err := adjustTrustTx(tx, r.nodeID, delta); err != nil {
				return err
			}
		}
		return finish(winner, protocol.VerifyOK)
	}
	if replicas < 3 && !force {
		// Two different answers: ask a third node to break the tie.
		_, err := tx.Exec(`UPDATE tasks SET replicas=3 WHERE id=?`, taskID)
		return err
	}
	return finish(results[0].output, protocol.VerifyMismatch)
}

// Nodes whose trust falls below banTrust stop getting work.
const banTrust = 0.3

func adjustTrustTx(tx *sql.Tx, nodeID string, delta float64) error {
	_, err := tx.Exec(`UPDATE nodes SET trust=MIN(1.0, MAX(0.0, trust+?)) WHERE id=?`, delta, nodeID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE nodes SET banned=1 WHERE id=? AND trust<?`, nodeID, banTrust)
	return err
}

// Sweep expires overdue assignments and settles replicated tasks whose
// extra replica has been waiting longer than verifyWait. It returns true
// when tasks went back to the queue.
func (s *Store) Sweep(verifyWait time.Duration) (requeued bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := s.now().Unix()

	rows, err := tx.Query(`SELECT id, task_id, node_id FROM assignments WHERE status=? AND deadline<?`, asRunning, now)
	if err != nil {
		return false, err
	}
	type expired struct{ id, task, node string }
	var list []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.id, &e.task, &e.node); err != nil {
			rows.Close()
			return false, err
		}
		list = append(list, e)
	}
	rows.Close()
	for _, e := range list {
		if _, err := tx.Exec(`UPDATE assignments SET status=?, error='timeout', finished_at=? WHERE id=?`, asExpired, now, e.id); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`UPDATE nodes SET jobs_failed=jobs_failed+1 WHERE id=?`, e.node); err != nil {
			return false, err
		}
		if err := failTaskTx(tx, e.task, "timeout on every node it was given to", now); err != nil {
			return false, err
		}
		requeued = true
	}

	// Replicated tasks that already have an answer but no second node came.
	cutoff := now - int64(verifyWait.Seconds())
	rows, err = tx.Query(`SELECT t.id FROM tasks t WHERE t.status=? AND t.replicas>1
		AND NOT EXISTS (SELECT 1 FROM assignments a WHERE a.task_id=t.id AND a.status='running')
		AND EXISTS (SELECT 1 FROM assignments a WHERE a.task_id=t.id AND a.status='done')
		AND (SELECT MAX(finished_at) FROM assignments a WHERE a.task_id=t.id AND a.status='done') < ?`,
		protocol.TaskRunning, cutoff)
	if err != nil {
		return false, err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		stale = append(stale, id)
	}
	rows.Close()
	for _, id := range stale {
		if err := settleTx(tx, id, now, true); err != nil {
			return false, err
		}
	}
	return requeued, tx.Commit()
}

// WantedModels returns models that have work waiting.
func (s *Store) WantedModels() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT model FROM tasks WHERE status IN (?,?)`, protocol.TaskQueued, protocol.TaskRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// NodeLabel returns a node's public name and its main GPU for the dashboard.
func (s *Store) NodeLabel(id string) (name, gpu string) {
	var raw string
	s.db.QueryRow(`SELECT name, gpus FROM nodes WHERE id=?`, id).Scan(&name, &raw)
	var gpus []protocol.GPU
	json.Unmarshal([]byte(raw), &gpus)
	best := 0
	for _, g := range gpus {
		if g.VRAMMB >= best {
			best, gpu = g.VRAMMB, g.Name
		}
	}
	return name, gpu
}

// NodeLoad returns a node's measured speed (tokens/s per request) and how
// many of its assignments are running now.
func (s *Store) NodeLoad(nodeID string) (speed float64, running int) {
	s.db.QueryRow(`SELECT COALESCE(SUM(tokens_out)*1000.0/SUM(duration_ms),0) FROM assignments
		WHERE node_id=? AND status='done' AND duration_ms>0 AND finished_at>?`, nodeID, s.now().Unix()-86400).Scan(&speed)
	s.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE node_id=? AND status='running'`, nodeID).Scan(&running)
	return speed, running
}

// onlineWindow is how long a node counts as online after its last contact.
const onlineWindow = 90 * time.Second

func (s *Store) Stats() (protocol.Stats, error) {
	st := protocol.Stats{GPUVendors: map[string]int{}, ModelsOnline: map[string]int{}}
	since := s.now().Add(-onlineWindow).Unix()
	// Measured speed of every node over the last day, in tokens/s.
	speed := map[string]float64{}
	srows, err := s.db.Query(`SELECT node_id, SUM(tokens_out)*1000.0/SUM(duration_ms) FROM assignments
		WHERE status='done' AND duration_ms>0 AND finished_at>? GROUP BY node_id`, s.now().Unix()-86400)
	if err != nil {
		return st, err
	}
	for srows.Next() {
		var id string
		var v float64
		srows.Scan(&id, &v)
		speed[id] = v
	}
	srows.Close()
	busy := map[string]bool{}
	brows, err := s.db.Query(`SELECT DISTINCT node_id FROM assignments WHERE status='running'`)
	if err != nil {
		return st, err
	}
	for brows.Next() {
		var id string
		brows.Scan(&id)
		busy[id] = true
	}
	brows.Close()
	avg := 0.0
	for _, v := range speed {
		avg += v / float64(len(speed))
	}

	rows, err := s.db.Query(`SELECT id, state, vram_mb, gpus, models FROM nodes WHERE last_seen>=? AND banned=0 AND os!='chain'`, since)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var id, state, gpusRaw, modelsRaw string
		var vram int
		if err := rows.Scan(&id, &state, &vram, &gpusRaw, &modelsRaw); err != nil {
			rows.Close()
			return st, err
		}
		st.NodesOnline++
		st.VRAMOnlineMB += vram
		if state == protocol.StateChain {
			st.NodesWorking++ // its share is counted with the chain
		} else if state == protocol.StateWorking || busy[id] {
			st.NodesWorking++
			v, ok := speed[id]
			if !ok {
				v = avg // not measured yet: assume an average node
			}
			st.CapacityTPS += v
			if busy[id] {
				st.NodesBusy++
				st.UsedTPS += v
			}
		}
		var gpus []protocol.GPU
		json.Unmarshal([]byte(gpusRaw), &gpus)
		for _, g := range gpus {
			st.GPUVendors[g.Vendor]++
		}
		var ms []string
		json.Unmarshal([]byte(modelsRaw), &ms)
		for _, m := range ms {
			st.ModelsOnline[m]++
		}
	}
	rows.Close()
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE os!='chain'`).Scan(&st.NodesTotal); err != nil {
		return st, err
	}
	err = s.db.QueryRow(`SELECT
		COALESCE(SUM(status='queued'),0), COALESCE(SUM(status='running'),0),
		COALESCE(SUM(status='done'),0), COALESCE(SUM(status='failed'),0) FROM tasks`).
		Scan(&st.TasksQueued, &st.TasksRunning, &st.TasksDone, &st.TasksFailed)
	if err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(tokens_out),0) FROM nodes`).Scan(&st.TokensTotal); err != nil {
		return st, err
	}
	window := int64(600)
	var recent int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(tokens_out),0) FROM assignments WHERE status='done' AND finished_at>=?`,
		s.now().Unix()-window).Scan(&recent); err != nil {
		return st, err
	}
	st.TokensPerSec = float64(recent) / float64(window)
	if st.CapacityTPS > 0 {
		st.Utilization = min(st.UsedTPS/st.CapacityTPS, 1)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&st.UsersTotal)
	s.db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&st.ProjectsTotal)
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE created_at>?`, s.now().Unix()-86400).Scan(&st.MessagesToday)
	return st, nil
}

// Similar decides whether two answers to the same greedy (temperature 0)
// prompt agree. Different GPUs round floats differently, so long answers
// can drift apart near the end; we require identical text or a long common
// prefix of words.
func Similar(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) == 0 || len(wb) == 0 {
		return len(wa) == len(wb)
	}
	n := min(len(wa), len(wb))
	common := 0
	for common < n && wa[common] == wb[common] {
		common++
	}
	if common == len(wa) && common == len(wb) {
		return true
	}
	return float64(common)/float64(n) >= 0.6 && common >= min(n, 8)
}
