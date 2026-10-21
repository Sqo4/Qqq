package coordinator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sqo4/uley/internal/protocol"
)

// fakeTelegram is a tiny Bot API: tests push user messages in and read
// what the bot sent out.
type fakeTelegram struct {
	mu      sync.Mutex
	updates []map[string]any
	nextID  int64
	sent    []string
}

func (f *fakeTelegram) say(chat int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.updates = append(f.updates, map[string]any{"update_id": f.nextID,
		"message": map[string]any{"text": text, "chat": map[string]any{"id": chat, "type": "private"}}})
}

func (f *fakeTelegram) outbox() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var p map[string]any
	json.NewDecoder(r.Body).Decode(&p)
	reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v}) }
	switch {
	case strings.HasSuffix(r.URL.Path, "/getMe"):
		reply(map[string]any{"username": "uley_hive_bot"})
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		offset := int64(p["offset"].(float64))
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			f.mu.Lock()
			var out []map[string]any
			for _, u := range f.updates {
				if u["update_id"].(int64) >= offset {
					out = append(out, u)
				}
			}
			f.mu.Unlock()
			if len(out) > 0 {
				reply(out)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		reply([]any{})
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		f.mu.Lock()
		f.sent = append(f.sent, p["text"].(string))
		f.mu.Unlock()
		reply(map[string]any{})
	default:
		reply(true)
	}
}

func waitSent(t *testing.T, f *fakeTelegram, contains string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range f.outbox() {
			if strings.Contains(m, contains) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("bot never sent %q; sent: %q", contains, f.outbox())
}

func TestTelegramBot(t *testing.T) {
	tg := &fakeTelegram{}
	api := httptest.NewServer(tg)
	defer api.Close()
	s, hs := newSocial(t, Config{TelegramToken: "123:abc", TelegramAPI: api.URL})
	vlad := signup(t, hs.URL, "vlad")

	// A stranger gets instructions, not the model.
	tg.say(555, "привет")
	waitSent(t, tg, "войдите на сайт Улья")

	var code struct{ Code, Bot, Link string }
	deadline := time.Now().Add(3 * time.Second)
	for code.Bot == "" && time.Now().Before(deadline) {
		vlad.do("POST", "/telegram/code", nil, &code)
		time.Sleep(50 * time.Millisecond)
	}
	if code.Bot != "uley_hive_bot" || !strings.HasPrefix(code.Link, "https://t.me/uley_hive_bot?start=") {
		t.Fatalf("bad code %+v", code)
	}
	tg.say(777, "/start "+code.Code)
	waitSent(t, tg, "Аккаунт Улья подключён")

	model := newFakeModel(t, s, func(j protocol.Job) protocol.JobResult { return text("Привет из Улья!") })
	tg.say(777, "Как дела?")
	deadline = time.Now().Add(5 * time.Second)
	for len(model.jobs) == 0 && time.Now().Before(deadline) {
		model.drain()
	}
	waitSent(t, tg, "Привет из Улья!")

	var st struct{ Linked bool }
	vlad.do("GET", "/telegram", nil, &st)
	if !st.Linked {
		t.Fatal("web app must show the link")
	}
	// The conversation is also visible on the site.
	var me struct{ Chats []Chat }
	vlad.do("GET", "/me", nil, &me)
	if len(me.Chats) == 0 || me.Chats[0].Title != "Telegram" {
		t.Fatalf("telegram chat not on the site: %+v", me.Chats)
	}
}
