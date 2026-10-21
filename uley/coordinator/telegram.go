package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A Telegram bot for the hive: people link their account once with a
// one-time code from the web app, then chat with Улей from Telegram and get
// the results of their scheduled tasks there. It uses the plain Bot API
// over HTTPS with long polling, so it needs no public webhook address.

const telegramSchema = `
CREATE TABLE IF NOT EXISTS telegram_links (
	user_id   TEXT PRIMARY KEY REFERENCES users(id),
	tg_chat   INTEGER NOT NULL UNIQUE,
	chat_id   TEXT NOT NULL,
	linked_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS telegram_codes (
	code       TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);`

type TelegramLink struct {
	UserID string
	TgChat int64
	ChatID string // the Улей chat the bot talks in
}

func (s *Store) TelegramCode(userID string) (string, error) {
	code := strings.ReplaceAll(inviteCode(), "-", "")
	s.db.Exec(`DELETE FROM telegram_codes WHERE user_id=? OR expires_at<?`, userID, s.now().Unix())
	_, err := s.db.Exec(`INSERT INTO telegram_codes(code, user_id, expires_at) VALUES(?,?,?)`, code, userID, s.now().Add(15*time.Minute).Unix())
	return code, err
}

// LinkTelegram consumes a code and connects a Telegram chat to its user.
func (s *Store) LinkTelegram(code string, tgChat int64) (User, error) {
	var uid string
	err := s.db.QueryRow(`SELECT user_id FROM telegram_codes WHERE code=? AND expires_at>=?`, strings.ToUpper(strings.TrimSpace(code)), s.now().Unix()).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u, err := s.GetUser(uid)
	if err != nil {
		return u, err
	}
	c, err := s.CreateChat(u, "Telegram")
	if err != nil {
		return u, err
	}
	s.db.Exec(`DELETE FROM telegram_codes WHERE code=?`, code)
	s.db.Exec(`DELETE FROM telegram_links WHERE tg_chat=? OR user_id=?`, tgChat, uid)
	_, err = s.db.Exec(`INSERT INTO telegram_links(user_id, tg_chat, chat_id, linked_at) VALUES(?,?,?,?)`, uid, tgChat, c.ID, s.now().Unix())
	return u, err
}

func (s *Store) TelegramByTg(tgChat int64) (TelegramLink, error) {
	l := TelegramLink{TgChat: tgChat}
	err := s.db.QueryRow(`SELECT user_id, chat_id FROM telegram_links WHERE tg_chat=?`, tgChat).Scan(&l.UserID, &l.ChatID)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

func (s *Store) TelegramByUser(userID string) (TelegramLink, error) {
	l := TelegramLink{UserID: userID}
	err := s.db.QueryRow(`SELECT tg_chat, chat_id FROM telegram_links WHERE user_id=?`, userID).Scan(&l.TgChat, &l.ChatID)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

func (s *Store) UnlinkTelegram(userID string) {
	s.db.Exec(`DELETE FROM telegram_links WHERE user_id=?`, userID)
}

func (s *Store) setTelegramChat(userID, chatID string) {
	s.db.Exec(`UPDATE telegram_links SET chat_id=? WHERE user_id=?`, chatID, userID)
}

// telegramBot talks to the Bot API.
type telegramBot struct {
	s        *Server
	api      string // https://api.telegram.org/bot<token>
	http     *http.Client
	username string

	mu      sync.Mutex
	forward map[int64]int64 // Улей message id → Telegram chat waiting for it
}

func newTelegramBot(s *Server) *telegramBot {
	base := s.cfg.TelegramAPI
	if base == "" {
		base = "https://api.telegram.org"
	}
	return &telegramBot{s: s, api: strings.TrimRight(base, "/") + "/bot" + s.cfg.TelegramToken,
		http: &http.Client{Timeout: 40 * time.Second}, forward: map[int64]int64{}}
}

func (b *telegramBot) call(ctx context.Context, method string, params, out any) error {
	body, _ := json.Marshal(params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.api+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (b *telegramBot) send(ctx context.Context, chat int64, text string) {
	for len(text) > 0 {
		part := text
		if r := []rune(part); len(r) > 4000 { // Telegram's limit is 4096
			part = string(r[:4000])
		}
		text = text[len(part):]
		if err := b.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": part}, nil); err != nil {
			log.Printf("telegram send: %v", err)
			return
		}
	}
}

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

func (b *telegramBot) run(ctx context.Context) {
	var me struct {
		Username string `json:"username"`
	}
	for ctx.Err() == nil {
		if err := b.call(ctx, "getMe", map[string]any{}, &me); err != nil {
			log.Printf("telegram: %v (retrying)", err)
			sleepCtx(ctx, 30*time.Second)
			continue
		}
		break
	}
	b.mu.Lock()
	b.username = me.Username
	b.mu.Unlock()
	log.Printf("telegram bot @%s is up", me.Username)
	var offset int64
	for ctx.Err() == nil {
		var updates []tgUpdate
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message"}}, &updates)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("telegram updates: %v", err)
				sleepCtx(ctx, 5*time.Second)
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message != nil && u.Message.Chat.Type == "private" && u.Message.Text != "" {
				b.handle(ctx, u.Message.Chat.ID, u.Message.Text)
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

const tgHelp = "Это бот Улья — распределённой нейросети на видеокартах добровольцев.\n\n" +
	"Просто пишите вопросы — отвечает большая модель Улья. Она помнит важное о вас и умеет ставить задания по расписанию («каждое утро в 9 присылай план на день»).\n\n" +
	"/new — начать новый разговор\n/help — эта справка"

func (b *telegramBot) handle(ctx context.Context, tgChat int64, text string) {
	text = strings.TrimSpace(text)
	if code, ok := strings.CutPrefix(text, "/start"); ok && strings.TrimSpace(code) != "" {
		u, err := b.s.store.LinkTelegram(strings.TrimSpace(code), tgChat)
		if err != nil {
			b.send(ctx, tgChat, "Код не подошёл или устарел. Получите новый на сайте Улья: «📱 Telegram».")
			return
		}
		b.send(ctx, tgChat, "Готово, "+u.Name+"! Аккаунт Улья подключён.\n\n"+tgHelp)
		return
	}
	link, err := b.s.store.TelegramByTg(tgChat)
	if err != nil {
		b.send(ctx, tgChat, "Чтобы пользоваться ботом, войдите на сайт Улья, нажмите «📱 Telegram» и отправьте сюда код подключения.")
		return
	}
	u, err := b.s.store.GetUser(link.UserID)
	if err != nil {
		return
	}
	switch text {
	case "/start", "/help":
		b.send(ctx, tgChat, tgHelp)
		return
	case "/new":
		c, err := b.s.store.CreateChat(u, "Telegram")
		if err == nil {
			b.s.store.setTelegramChat(u.ID, c.ID)
			b.send(ctx, tgChat, "Начали новый разговор.")
		}
		return
	}
	c, err := b.s.store.ChatAccess(link.ChatID, u.ID)
	if err != nil { // the chat was deleted on the site: start a new one
		c, err = b.s.store.CreateChat(u, "Telegram")
		if err != nil {
			return
		}
		b.s.store.setTelegramChat(u.ID, c.ID)
	}
	if b.s.store.PendingAIMessages(u.ID) >= 2 {
		b.send(ctx, tgChat, "Улей ещё отвечает на прошлые вопросы, подождите немного.")
		return
	}
	q, err := b.s.store.AddMessage(Message{ChatID: c.ID, UserID: u.ID, AuthorName: u.Name, Role: "user", Status: MsgDone, Content: cleanText(text, MaxMessageLen)})
	if err != nil {
		return
	}
	b.s.events.send("message", q, []string{u.ID}, false)
	ai, err := b.s.askAI(c, u)
	if err != nil {
		b.send(ctx, tgChat, "Не получилось отправить вопрос: "+err.Error())
		return
	}
	b.s.events.send("message", ai, []string{u.ID}, false)
	b.expect(ai.ID, tgChat)
	b.call(ctx, "sendChatAction", map[string]any{"chat_id": tgChat, "action": "typing"}, nil)
}

// expect marks an answer that must be delivered to Telegram when ready.
func (b *telegramBot) expect(msgID, tgChat int64) {
	b.mu.Lock()
	b.forward[msgID] = tgChat
	b.mu.Unlock()
}

// deliver sends finished answers the bot is waiting for.
func (b *telegramBot) deliver(ctx context.Context, changed []Message) {
	for _, m := range changed {
		if m.Status == MsgPending {
			continue
		}
		b.mu.Lock()
		tg, ok := b.forward[m.ID]
		delete(b.forward, m.ID)
		b.mu.Unlock()
		if ok {
			go b.send(ctx, tg, m.Content)
		}
	}
}

// ---- web app side ----

func (s *Server) handleTelegramState(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": s.tg != nil}
	if s.tg != nil {
		s.tg.mu.Lock()
		out["bot"] = s.tg.username
		s.tg.mu.Unlock()
		_, err := s.store.TelegramByUser(currentUser(r).ID)
		out["linked"] = err == nil
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTelegramCode(w http.ResponseWriter, r *http.Request) {
	if s.tg == nil {
		writeErr(w, http.StatusNotFound, "Telegram-бот на этом сервере не настроен")
		return
	}
	code, err := s.store.TelegramCode(currentUser(r).ID)
	if err != nil {
		userErr(w, err)
		return
	}
	s.tg.mu.Lock()
	bot := s.tg.username
	s.tg.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"code": code, "bot": bot, "link": "https://t.me/" + bot + "?start=" + code})
}

func (s *Server) handleTelegramUnlink(w http.ResponseWriter, r *http.Request) {
	s.store.UnlinkTelegram(currentUser(r).ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
