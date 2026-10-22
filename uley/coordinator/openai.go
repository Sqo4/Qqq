package coordinator

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
)

// This file makes the hive look like an OpenAI-compatible server, so agents
// such as Hermes Agent (MIT, Nous Research) can use the hive's model as
// their brain: chat completions with tool calling, streaming and /v1/models.
// The agent runs its tools on its owner's computer; volunteers only ever
// compute the model.

const maxOpenTasksPerUser = 6

var errTooBusy = errors.New("слишком много запросов сразу, дождитесь ответов на предыдущие")

// ---- personal API keys ----

const apiKeysSchema = `
CREATE TABLE IF NOT EXISTS api_keys (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id),
	name       TEXT NOT NULL,
	prefix     TEXT NOT NULL,
	key_hash   TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	last_used  INTEGER NOT NULL DEFAULT 0
);`

type APIKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Key       string `json:"key,omitempty"` // only right after creation
	CreatedAt int64  `json:"created_at"`
	LastUsed  int64  `json:"last_used"`
}

const maxKeysPerUser = 10

func (s *Store) CreateAPIKey(u User, name string) (APIKey, error) {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE user_id=?`, u.ID).Scan(&n)
	if n >= maxKeysPerUser {
		return APIKey{}, fmt.Errorf("%w: не больше %d ключей", ErrInvalidInput, maxKeysPerUser)
	}
	name = cleanText(name, 40)
	if name == "" {
		name = "Hermes"
	}
	b := make([]byte, 24)
	rand.Read(b)
	k := APIKey{ID: newID("k_"), Name: name, Key: "uley-" + hex.EncodeToString(b), CreatedAt: s.now().Unix()}
	k.Prefix = k.Key[:11] + "…"
	_, err := s.db.Exec(`INSERT INTO api_keys(id, user_id, name, prefix, key_hash, created_at) VALUES(?,?,?,?,?,?)`,
		k.ID, u.ID, k.Name, k.Prefix, hashToken(k.Key), k.CreatedAt)
	return k, err
}

func (s *Store) APIKeys(userID string) ([]APIKey, error) {
	rows, err := s.db.Query(`SELECT id, name, prefix, created_at, last_used FROM api_keys WHERE user_id=? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsed)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIKey(userID, id string) error {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UserByAPIKey returns the owner of a personal key.
func (s *Store) UserByAPIKey(key string) (string, error) {
	if !strings.HasPrefix(key, "uley-") {
		return "", ErrUnauthorized
	}
	var uid string
	err := s.db.QueryRow(`SELECT user_id FROM api_keys WHERE key_hash=?`, hashToken(key)).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err == nil {
		s.db.Exec(`UPDATE api_keys SET last_used=? WHERE key_hash=?`, s.now().Unix(), hashToken(key))
	}
	return uid, err
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.APIKeys(currentUser(r).ID)
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "model": s.hiveModel(), "context": s.contextPerRequest()})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	readJSON(r, &req)
	k, err := s.store.CreateAPIKey(currentUser(r), req.Name)
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIKey(currentUser(r).ID, r.PathValue("id")); err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- OpenAI-compatible endpoints ----

// contextPerRequest is how many tokens one conversation may use.
func (s *Server) contextPerRequest() int {
	if s.cfg.Chain != nil {
		return s.cfg.Chain.cfg.Model.ContextSize / max(s.cfg.Chain.cfg.Parallel, 1)
	}
	m, ok := models.Get(s.hiveModel())
	if !ok {
		return 4096
	}
	return m.ContextSize
}

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id": s.hiveModel(), "object": "model", "created": 0, "owned_by": "uley",
			"context_length": s.contextPerRequest(),
		}},
	})
}

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type oaiRequest struct {
	Model               string          `json:"model"`
	Messages            []oaiMessage    `json:"messages"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	Seed                int64           `json:"seed"`
	Stream              bool            `json:"stream"`
	Tools               json.RawMessage `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
}

// contentText flattens OpenAI content: a string, null, or a list of parts.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text":
			b.WriteString(p.Text)
		default:
			b.WriteString("[" + p.Type + " не поддерживается]")
		}
	}
	return b.String()
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req oaiRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	sub := protocol.SubmitRequest{Temperature: req.Temperature, Seed: req.Seed}
	if len(req.Tools) > 0 && string(req.Tools) != "null" && string(req.Tools) != "[]" {
		sub.Tools = req.Tools
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		sub.ToolChoice = req.ToolChoice
	}
	switch {
	case req.MaxCompletionTokens != nil:
		sub.MaxTokens = *req.MaxCompletionTokens
	case req.MaxTokens != nil:
		sub.MaxTokens = *req.MaxTokens
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "developer" {
			role = "system"
		}
		sub.Messages = append(sub.Messages, protocol.Message{
			Role: role, Content: contentText(m.Content), Name: m.Name, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID,
		})
	}
	// Any model name is accepted: the hive serves exactly one model.
	id, err := s.submitAs(apiUser(r), sub)
	if errors.Is(err, errTooBusy) {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	model := req.Model
	if model == "" {
		model = s.hiveModel()
	}
	if req.Stream {
		s.streamCompletion(w, r, id, model)
		return
	}
	t, err := s.waitTask(r.Context(), id, 15*time.Minute)
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
	w.Header().Set(aiMarkHeader, "true")
	writeJSON(w, http.StatusOK, completionBody(t, model))
}

func finishReason(t protocol.Task) string {
	switch {
	case len(t.ToolCalls) > 2:
		return "tool_calls"
	case t.FinishReason != "":
		return t.FinishReason
	}
	return "stop"
}

func completionBody(t protocol.Task, model string) map[string]any {
	msg := map[string]any{"role": "assistant", "content": t.Output}
	if len(t.ToolCalls) > 2 {
		msg["tool_calls"] = t.ToolCalls
		if t.Output == "" {
			msg["content"] = nil
		}
	}
	return map[string]any{
		"id": "chatcmpl-" + t.ID, "object": "chat.completion", "created": t.CreatedAt, "model": model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finishReason(t)}},
		"usage":   map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
		"uley":    map[string]any{"verify": t.Verify, "ai_generated": true, "mark": aiMark},
	}
}

// streamCompletion speaks the OpenAI streaming format. The hive answers in
// one piece, so while it works we send keep-alive comments, then the whole
// answer as one delta.
func (s *Server) streamCompletion(w http.ResponseWriter, r *http.Request, id, model string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set(aiMarkHeader, "true")
	created := time.Now().Unix()
	chunk := func(delta map[string]any, finish any) {
		b, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-" + id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	chunk(map[string]any{"role": "assistant", "content": ""}, nil)
	deadline := time.Now().Add(15 * time.Minute)
	var t protocol.Task
	for {
		var err error
		t, err = s.waitTask(r.Context(), id, 10*time.Second)
		if err != nil {
			return
		}
		if t.Status == protocol.TaskDone || t.Status == protocol.TaskFailed || time.Now().After(deadline) {
			break
		}
		fmt.Fprint(w, ": the hive is thinking\n\n")
		fl.Flush()
	}
	if t.Status != protocol.TaskDone {
		msg := "Улей не успел ответить"
		if t.Error != "" {
			msg += ": " + t.Error
		}
		b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "type": "server_error"}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
		fl.Flush()
		return
	}
	if t.Output != "" {
		chunk(map[string]any{"content": t.Output}, nil)
	}
	if len(t.ToolCalls) > 2 {
		var calls []map[string]any
		json.Unmarshal(t.ToolCalls, &calls)
		for i := range calls {
			calls[i]["index"] = i
		}
		chunk(map[string]any{"tool_calls": calls}, nil)
	}
	chunk(map[string]any{}, finishReason(t))
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}
