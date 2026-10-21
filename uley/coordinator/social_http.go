package coordinator

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sqo4/uley/internal/models"
	"github.com/sqo4/uley/internal/protocol"
)

const sessionCookie = "uley_session"

// hub pushes live events (new messages, hive stats) to open browsers.
type hub struct {
	mu   sync.Mutex
	subs map[*subscriber]bool
}

type subscriber struct {
	userID string // "" = anonymous, stats only
	ch     chan []byte
}

func newHub() *hub { return &hub{subs: map[*subscriber]bool{}} }

func (h *hub) add(userID string) *subscriber {
	s := &subscriber{userID: userID, ch: make(chan []byte, 64)}
	h.mu.Lock()
	h.subs[s] = true
	h.mu.Unlock()
	return s
}

func (h *hub) remove(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

func sseFrame(event string, data any) []byte {
	b, _ := json.Marshal(data)
	return []byte("event: " + event + "\ndata: " + string(b) + "\n\n")
}

// send delivers to logged-in users in audience (nil = all logged-in users)
// or, with everyone set, to anonymous viewers too.
func (h *hub) send(event string, data any, audience []string, everyone bool) {
	frame := sseFrame(event, data)
	allow := map[string]bool{}
	for _, id := range audience {
		allow[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		switch {
		case everyone:
		case s.userID == "":
			continue
		case audience != nil && !allow[s.userID]:
			continue
		}
		select {
		case s.ch <- frame:
		default: // slow browser: drop, it refreshes on reconnect
		}
	}
}

func (h *hub) counts() (users int, viewers int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	for s := range h.subs {
		viewers++
		if s.userID != "" {
			seen[s.userID] = true
		}
	}
	return len(seen), viewers
}

func (s *Server) registerSocial() {
	p := protocol.APIPrefix
	s.mux.HandleFunc("POST "+p+"/auth/register", s.handleRegisterUser)
	s.mux.HandleFunc("POST "+p+"/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST "+p+"/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET "+p+"/me", s.userAuth(s.handleMe))
	s.mux.HandleFunc("POST "+p+"/chats", s.userAuth(s.handleCreateChat))
	s.mux.HandleFunc("PATCH "+p+"/chats/{id}", s.userAuth(s.handleRenameChat))
	s.mux.HandleFunc("DELETE "+p+"/chats/{id}", s.userAuth(s.handleDeleteChat))
	s.mux.HandleFunc("GET "+p+"/chats/{id}/messages", s.userAuth(s.handleMessages))
	s.mux.HandleFunc("POST "+p+"/chats/{id}/messages", s.userAuth(s.handlePostMessage))
	s.mux.HandleFunc("POST "+p+"/projects", s.userAuth(s.handleCreateProject))
	s.mux.HandleFunc("GET "+p+"/projects/{id}", s.userAuth(s.handleGetProject))
	s.mux.HandleFunc("POST "+p+"/projects/join", s.userAuth(s.handleJoinProject))
	s.mux.HandleFunc("POST "+p+"/projects/{id}/leave", s.userAuth(s.handleLeaveProject))
	s.mux.HandleFunc("GET "+p+"/keys", s.userAuth(s.handleListKeys))
	s.mux.HandleFunc("POST "+p+"/keys", s.userAuth(s.handleCreateKey))
	s.mux.HandleFunc("DELETE "+p+"/keys/{id}", s.userAuth(s.handleDeleteKey))
	s.mux.HandleFunc("GET "+p+"/events", s.userAuth(s.handleEvents))
	s.mux.HandleFunc("GET "+p+"/stats/stream", s.handleStatsStream)
}

type userKey struct{}

func currentUser(r *http.Request) User { return r.Context().Value(userKey{}).(User) }

// userAuth accepts a session cookie (browsers) or a Bearer session token.
// Cookie-authenticated writes must carry X-Uley: 1, which a foreign site
// cannot add without CORS approval (CSRF protection).
func (s *Server) userAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, viaCookie := bearer(r), false
		if token == "" {
			if c, err := r.Cookie(sessionCookie); err == nil {
				token, viaCookie = c.Value, true
			}
		}
		u, err := s.store.UserBySession(token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "нужно войти")
			return
		}
		if viaCookie && r.Method != http.MethodGet && r.Header.Get("X-Uley") != "1" {
			writeErr(w, http.StatusForbidden, "missing X-Uley header")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	}
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil, MaxAge: int(sessionTTL.Seconds()),
	})
}

func userErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrLoginTaken), errors.Is(err, ErrInvalidInput), errors.Is(err, ErrProjectFull):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrBadLogin), errors.Is(err, ErrUnauthorized):
		writeErr(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrForbidden):
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		log.Printf("social: %v", err)
		writeErr(w, http.StatusInternalServerError, "ошибка сервера")
	}
}

func (s *Server) handleRegisterUser(w http.ResponseWriter, r *http.Request) {
	if !s.regs.allow("user:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "слишком много регистраций с этого адреса")
		return
	}
	var req struct {
		Login     string `json:"login"`
		Name      string `json:"name"`
		Password  string `json:"password"`
		SignupKey string `json:"signup_key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.cfg.SignupKey != "" && subtle.ConstantTimeCompare([]byte(req.SignupKey), []byte(s.cfg.SignupKey)) != 1 {
		writeErr(w, http.StatusForbidden, "нужен правильный код регистрации")
		return
	}
	if _, err := s.store.CreateUser(req.Login, req.Name, req.Password); err != nil {
		userErr(w, err)
		return
	}
	u, token, err := s.store.Login(req.Login, req.Password)
	if err != nil {
		userErr(w, err)
		return
	}
	s.setSession(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "token": token})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.logins.allow(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток, подождите")
		return
	}
	var req struct{ Login, Password string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	u, token, err := s.store.Login(req.Login, req.Password)
	if err != nil {
		userErr(w, err)
		return
	}
	s.setSession(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "token": token})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.Logout(c.Value)
	}
	if t := bearer(r); t != "" {
		s.store.Logout(t)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	chats, err := s.store.PersonalChats(u.ID)
	if err != nil {
		userErr(w, err)
		return
	}
	projects, err := s.store.UserProjects(u.ID)
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": u, "chats": chats, "projects": projects,
		"lobby": Chat{ID: LobbyID, Kind: ChatLobby, Title: "Общий чат"}, "model": s.hiveModel(),
	})
}

func (s *Server) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	var req struct{ Title string }
	readJSON(r, &req)
	c, err := s.store.CreateChat(currentUser(r), req.Title)
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleRenameChat(w http.ResponseWriter, r *http.Request) {
	var req struct{ Title string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.store.RenameChat(currentUser(r), r.PathValue("id"), req.Title); err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteChat(currentUser(r), r.PathValue("id")); err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	c, err := s.store.ChatAccess(r.PathValue("id"), u.ID)
	if err != nil {
		userErr(w, err)
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	msgs, err := s.store.Messages(c.ID, u.ID, before, 50)
	if err != nil {
		userErr(w, err)
		return
	}
	resp := map[string]any{"chat": c, "messages": msgs}
	if c.Kind == ChatProject {
		if p, err := s.store.GetProject(c.ProjectID, u.ID); err == nil {
			resp["project"] = p
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// RE2's \b only knows ASCII letters, so the end of the mention is spelled out.
var mentionRe = regexp.MustCompile(`(?i)@(улей|uley|ai|ии)(\P{L}|$)`)

func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	c, err := s.store.ChatAccess(r.PathValue("id"), u.ID)
	if err != nil {
		userErr(w, err)
		return
	}
	var req struct {
		Content        string `json:"content"`
		AskAI          bool   `json:"ask_ai"`
		ShareProjectID string `json:"share_project_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	msg := Message{ChatID: c.ID, UserID: u.ID, AuthorName: u.Name, Role: "user", Status: MsgDone, Content: cleanText(req.Content, MaxMessageLen)}
	if req.ShareProjectID != "" {
		if c.Kind != ChatLobby {
			writeErr(w, http.StatusBadRequest, "проектом можно поделиться только в общем чате")
			return
		}
		p, err := s.store.GetProject(req.ShareProjectID, u.ID)
		if err != nil || p.InviteCode == "" {
			writeErr(w, http.StatusForbidden, "делиться можно только своими проектами")
			return
		}
		p.Members, p.InviteCode = nil, ""
		msg.Project = &p
	}
	if msg.Content == "" && msg.Project == nil {
		writeErr(w, http.StatusBadRequest, "пустое сообщение")
		return
	}
	askAI := c.Kind == ChatPersonal || req.AskAI || mentionRe.MatchString(msg.Content)
	if askAI && s.store.PendingAIMessages(u.ID) >= 2 {
		writeErr(w, http.StatusTooManyRequests, "Улей ещё отвечает на ваши прошлые вопросы, подождите")
		return
	}
	msg, err = s.store.AddMessage(msg)
	if err != nil {
		userErr(w, err)
		return
	}
	if c.Kind == ChatPersonal && c.Title == "Новый чат" {
		title := []rune(msg.Content)
		if len(title) > 40 {
			title = append(title[:40], '…')
		}
		s.store.RenameChat(u, c.ID, string(title))
	}
	audience, _ := s.store.ChatAudience(c.ID)
	s.events.send("message", msg, audience, false)
	out := []Message{msg}
	if askAI {
		ai, err := s.askAI(c, u)
		if err != nil {
			userErr(w, err)
			return
		}
		s.events.send("message", ai, audience, false)
		out = append(out, ai)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) hiveModel() string {
	if s.cfg.HiveModel != "" {
		return s.cfg.HiveModel
	}
	return models.All()[0].ID
}

// askAI queues an answer from the hive model with the chat as context.
func (s *Server) askAI(c Chat, u User) (Message, error) {
	history, err := s.store.Messages(c.ID, u.ID, 0, 30)
	if err != nil {
		return Message{}, err
	}
	system := "Ты — Улей, большая языковая модель, которая работает на видеокартах добровольцев. Отвечай по существу, на языке собеседника."
	group := c.Kind != ChatPersonal
	switch c.Kind {
	case ChatProject:
		p, err := s.store.GetProject(c.ProjectID, u.ID)
		if err == nil {
			var names []string
			for _, m := range p.Members {
				names = append(names, m.Name)
			}
			system += fmt.Sprintf(" Это общий чат проекта «%s». Описание проекта: %s. Участники: %s. Сообщения участников подписаны их именами. Помогай группе работать над проектом.",
				p.Name, p.Description, strings.Join(names, ", "))
		}
	case ChatLobby:
		system += " Это общий чат всех участников Улья, сообщения подписаны именами."
	}
	var msgs []protocol.Message
	total := 0
	for i := len(history) - 1; i >= 0; i-- { // newest first, to keep the latest
		m := history[i]
		if m.Status != MsgDone || m.Content == "" {
			continue
		}
		content, role := m.Content, "user"
		if m.UserID == "" {
			role = "assistant"
		} else if group {
			content = m.AuthorName + ": " + content
		}
		if total+len(content) > protocol.MaxPromptBytes-len(system)-1024 {
			break
		}
		total += len(content)
		msgs = append([]protocol.Message{{Role: role, Content: content}}, msgs...)
	}
	for len(msgs) > 0 && msgs[0].Role == "assistant" {
		msgs = msgs[1:]
	}
	agentic := c.Kind != ChatLobby
	if agentic {
		system += s.agentSystem(c, u)
	}
	msgs = append([]protocol.Message{{Role: "system", Content: system}}, msgs...)

	ai, err := s.store.AddMessage(Message{ChatID: c.ID, Role: "assistant", AuthorName: "Улей", Status: MsgPending, AskedBy: u.ID})
	if err != nil {
		return ai, err
	}
	if agentic {
		if err := s.startAgent(ai, c, u, msgs); err != nil {
			return ai, err
		}
		s.bell.ring()
		return ai, nil
	}
	taskID, err := s.store.CreateTask(s.hiveModel(), taskPayload{Messages: msgs, MaxTokens: 1024, Temperature: 0.7}, 1)
	if err != nil {
		return ai, err
	}
	if err := s.store.SetMessageTask(ai.ID, taskID); err != nil {
		return ai, err
	}
	s.bell.ring()
	return ai, nil
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name, Description string
		Open              bool
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	p, err := s.store.CreateProject(currentUser(r), req.Name, req.Description, req.Open)
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(r.PathValue("id"), currentUser(r).ID)
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleJoinProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code      string `json:"code"`
		ProjectID string `json:"project_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	u := currentUser(r)
	p, err := s.store.JoinProject(u, req.Code, req.ProjectID)
	if err != nil {
		userErr(w, err)
		return
	}
	note, _ := s.store.AddMessage(Message{ChatID: p.ChatID, UserID: u.ID, AuthorName: u.Name, Role: "user", Status: MsgDone,
		Content: "присоединился(-ась) к проекту 👋"})
	audience, _ := s.store.ChatAudience(p.ChatID)
	s.events.send("message", note, audience, false)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleLeaveProject(w http.ResponseWriter, r *http.Request) {
	if err := s.store.LeaveProject(currentUser(r), r.PathValue("id")); err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, currentUser(r).ID)
}

func (s *Server) handleStatsStream(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, "")
}

// stream is a Server-Sent Events connection: stats every 2 s, and for
// logged-in users new and updated chat messages as they happen.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, userID string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	sub := s.events.add(userID)
	defer s.events.remove(sub)
	if st, err := s.liveStats(); err == nil {
		w.Write(sseFrame("stats", st))
	}
	fl.Flush()
	keep := time.NewTicker(25 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-sub.ch:
			w.Write(frame)
			fl.Flush()
		case <-keep.C:
			w.Write([]byte(": ping\n\n"))
			fl.Flush()
		}
	}
}

func (s *Server) liveStats() (protocol.Stats, error) {
	st, err := s.Stats()
	if err == nil {
		st.UsersOnline, _ = s.events.counts()
	}
	return st, err
}

// runLive broadcasts stats and finished AI answers until ctx ends.
func (s *Server) runLive(ctx context.Context) {
	stats := time.NewTicker(2 * time.Second)
	defer stats.Stop()
	var lastSchedules time.Time
	wake := s.bell.wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stats.C:
			if _, viewers := s.events.counts(); viewers > 0 {
				if st, err := s.liveStats(); err == nil {
					s.events.send("stats", st, nil, true)
				}
			}
		case <-wake:
		}
		// Subscribe before working, so a ring during the work is not lost.
		wake = s.bell.wait()
		changed, err := s.store.FinishAnswers()
		if err != nil {
			log.Printf("answers: %v", err)
		}
		changed = append(changed, s.advanceAgents()...)
		if time.Since(lastSchedules) >= 20*time.Second {
			lastSchedules = time.Now()
			changed = append(changed, s.runSchedules()...)
		}
		for _, m := range changed {
			audience, err := s.store.ChatAudience(m.ChatID)
			if err == nil {
				s.events.send("message", m, audience, false)
			}
		}
	}
}
