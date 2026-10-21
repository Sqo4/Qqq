package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqo4/uley/internal/protocol"
)

type apiClient struct {
	t     *testing.T
	base  string
	token string
}

func (c *apiClient) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+protocol.APIPrefix+path, rd)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func newSocial(t *testing.T, cfg Config) (*Server, *httptest.Server) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	cfg.HiveModel = "qwen3-0.6b"
	s := NewServer(store, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	hs := httptest.NewServer(s)
	t.Cleanup(func() { cancel(); hs.Close(); store.Close() })
	return s, hs
}

func signup(t *testing.T, base, login string) *apiClient {
	c := &apiClient{t: t, base: base}
	var out struct {
		Token string `json:"token"`
	}
	if code := c.do("POST", "/auth/register", map[string]string{"login": login, "name": strings.ToUpper(login), "password": "secret123"}, &out); code != 200 {
		t.Fatalf("register %s: %d", login, code)
	}
	c.token = out.Token
	return c
}

// answerAll plays a volunteer node: it answers every queued task.
func answerAll(t *testing.T, s *Server, text string) {
	reg, _ := s.store.RegisterNode(protocol.RegisterRequest{NodeName: "gpu", OS: "linux"})
	for {
		j, err := s.store.Assign(reg.NodeID, []string{"qwen3-0.6b"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		s.store.Complete(reg.NodeID, j.ID, protocol.JobResult{Output: text, TokensOut: 3, DurationMS: 100})
	}
	s.bell.ring()
}

func TestStudentsProject(t *testing.T) {
	s, hs := newSocial(t, Config{})
	anna, boris, outsider := signup(t, hs.URL, "anna"), signup(t, hs.URL, "boris"), signup(t, hs.URL, "eve")

	var p Project
	anna.do("POST", "/projects", map[string]any{"name": "Курсовая по физике", "description": "Моделируем маятник"}, &p)
	if p.InviteCode == "" || p.ChatID == "" {
		t.Fatalf("bad project %+v", p)
	}
	if code := outsider.do("GET", "/chats/"+p.ChatID+"/messages", nil, nil); code != http.StatusForbidden {
		t.Fatalf("outsider read the project chat: %d", code)
	}
	if code := boris.do("POST", "/projects/join", map[string]string{"project_id": p.ID}, nil); code != http.StatusForbidden {
		t.Fatalf("closed project joined without a code: %d", code)
	}
	if code := boris.do("POST", "/projects/join", map[string]string{"code": strings.ToLower(p.InviteCode)}, &p); code != 200 || p.MemberCount != 2 {
		t.Fatalf("join by code: %d %+v", code, p)
	}

	// Boris listens to live events while Anna asks the hive.
	req, _ := http.NewRequest("GET", hs.URL+protocol.APIPrefix+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+boris.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := bufio.NewScanner(resp.Body)

	var posted []Message
	anna.do("POST", "/chats/"+p.ChatID+"/messages", map[string]any{"content": "@улей какая формула периода маятника?"}, &posted)
	if len(posted) != 2 || posted[1].Status != MsgPending {
		t.Fatalf("mention must queue an AI answer: %+v", posted)
	}
	// The hive sees the project context and who wrote what.
	var payload string
	s.store.db.QueryRow(`SELECT payload FROM tasks`).Scan(&payload)
	if !strings.Contains(payload, "Курсовая по физике") || !strings.Contains(payload, "ANNA: @улей") {
		t.Fatalf("project context missing from prompt: %s", payload)
	}
	answerAll(t, s, "T = 2π√(l/g)")

	deadline := time.After(5 * time.Second)
	got := make(chan string)
	go func() {
		for events.Scan() {
			line := events.Text()
			if strings.HasPrefix(line, "data:") && strings.Contains(line, "2π") && strings.Contains(line, `"status":"done"`) {
				got <- line
				return
			}
		}
	}()
	select {
	case <-got:
	case <-deadline:
		t.Fatal("Boris did not get the answer live")
	}

	var msgs struct{ Messages []Message }
	boris.do("GET", "/chats/"+p.ChatID+"/messages", nil, &msgs)
	last := msgs.Messages[len(msgs.Messages)-1]
	if last.AuthorName != "Улей" || last.Content != "T = 2π√(l/g)" {
		t.Fatalf("answer not stored: %+v", last)
	}
}

func TestPersonalChatsArePrivate(t *testing.T) {
	s, hs := newSocial(t, Config{})
	anna, eve := signup(t, hs.URL, "anna"), signup(t, hs.URL, "eve")
	var c Chat
	anna.do("POST", "/chats", map[string]string{}, &c)
	anna.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "Привет, улей! Помоги с эссе"}, nil)
	if code := eve.do("GET", "/chats/"+c.ID+"/messages", nil, nil); code != http.StatusForbidden {
		t.Fatalf("someone else's personal chat must be closed: %d", code)
	}
	answerAll(t, s, "Конечно!")
	time.Sleep(200 * time.Millisecond)
	var me struct{ Chats []Chat }
	anna.do("GET", "/me", nil, &me)
	if len(me.Chats) != 1 || !strings.HasPrefix(me.Chats[0].Title, "Привет, улей") {
		t.Fatalf("chat not auto-titled: %+v", me.Chats)
	}
	// Only two AI questions may wait at a time.
	anna.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "1"}, nil)
	anna.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "2"}, nil)
	if code := anna.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "3"}, nil); code != http.StatusTooManyRequests {
		t.Fatalf("flood not limited: %d", code)
	}
}

func TestShareProjectInLobby(t *testing.T) {
	_, hs := newSocial(t, Config{})
	anna, boris := signup(t, hs.URL, "anna"), signup(t, hs.URL, "boris")
	var p Project
	anna.do("POST", "/projects", map[string]any{"name": "Игра на Godot", "open": true}, &p)
	if code := boris.do("POST", "/chats/lobby/messages", map[string]any{"share_project_id": p.ID}, nil); code != http.StatusForbidden {
		t.Fatalf("sharing someone else's project: %d", code)
	}
	anna.do("POST", "/chats/lobby/messages", map[string]any{"content": "Ищем двоих в команду!", "share_project_id": p.ID}, nil)
	var lobby struct{ Messages []Message }
	boris.do("GET", "/chats/lobby/messages", nil, &lobby)
	card := lobby.Messages[len(lobby.Messages)-1].Project
	if card == nil || card.Name != "Игра на Godot" || card.InviteCode != "" {
		t.Fatalf("bad card (invite code must stay secret): %+v", card)
	}
	if code := boris.do("POST", "/projects/join", map[string]string{"project_id": card.ID}, nil); code != 200 {
		t.Fatalf("open project join: %d", code)
	}
}

func TestSignupKeyAndCSRF(t *testing.T) {
	_, hs := newSocial(t, Config{SignupKey: "SPBU-2026"})
	c := &apiClient{t: t, base: hs.URL}
	if code := c.do("POST", "/auth/register", map[string]string{"login": "x123", "password": "secret123"}, nil); code != http.StatusForbidden {
		t.Fatalf("signup without the key: %d", code)
	}
	if code := c.do("POST", "/auth/register", map[string]string{"login": "x123", "password": "secret123", "signup_key": "SPBU-2026"}, nil); code != 200 {
		t.Fatalf("signup with the key: %d", code)
	}
	// A cookie alone (what a foreign site could make the browser send) is not enough to write.
	var out struct{ Token string }
	c.do("POST", "/auth/login", map[string]string{"login": "x123", "password": "secret123"}, &out)
	req, _ := http.NewRequest("POST", hs.URL+protocol.APIPrefix+"/chats", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: out.Token})
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write without X-Uley: %d", resp.StatusCode)
	}
	if code := c.do("POST", "/auth/login", map[string]string{"login": "x123", "password": "wrong"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", code)
	}
}
