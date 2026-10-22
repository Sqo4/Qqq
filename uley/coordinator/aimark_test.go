package coordinator

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sqo4/uley/internal/protocol"
)

func TestAIContentIsMarked(t *testing.T) {
	s, hs := newSocial(t, Config{})
	vlad := signup(t, hs.URL, "vlad")
	model := newFakeModel(t, s, func(j protocol.Job) protocol.JobResult { return text("Период маятника T = 2π√(l/g)") })
	var c Chat
	vlad.do("POST", "/chats", map[string]string{}, &c)
	vlad.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "Формула маятника?"}, nil)
	model.drain()
	time.Sleep(100 * time.Millisecond)

	req, _ := http.NewRequest("GET", hs.URL+"/api/v1/chats/"+c.ID+"/export", nil)
	req.Header.Set("Authorization", "Bearer "+vlad.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	md := string(body)
	if !strings.Contains(md, "**🐝 Улей · Создано ИИ**") || !strings.Contains(md, "**VLAD**") ||
		strings.Contains(md, "**VLAD · Создано ИИ") || resp.Header.Get("X-AI-Generated") == "" {
		t.Fatalf("export must mark AI answers and only them:\n%s", md)
	}
	if !strings.Contains(s.aiFooter(), "Создано ИИ") {
		t.Fatal("footer for Telegram must carry the mark")
	}
}
