package coordinator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sqo4/uley/internal/protocol"
)

// fakeModel plays the GPU side: for every queued job it asks reply what to
// answer. reply sees the whole job, so a test can check the conversation.
type fakeModel struct {
	t     *testing.T
	s     *Server
	node  string
	reply func(j protocol.Job) protocol.JobResult
	jobs  []protocol.Job
}

func newFakeModel(t *testing.T, s *Server, reply func(j protocol.Job) protocol.JobResult) *fakeModel {
	reg, _ := s.store.RegisterNode(protocol.RegisterRequest{NodeName: "gpu", OS: "linux"})
	return &fakeModel{t: t, s: s, node: reg.NodeID, reply: reply}
}

// drain answers jobs until none arrive for a while.
func (f *fakeModel) drain() {
	f.t.Helper()
	idle := time.Now()
	for time.Since(idle) < 400*time.Millisecond {
		j, err := f.s.store.Assign(f.node, []string{"qwen3-0.6b"}, time.Minute)
		if err != nil {
			f.t.Fatal(err)
		}
		if j == nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		f.jobs = append(f.jobs, *j)
		f.s.store.Complete(f.node, j.ID, f.reply(*j))
		f.s.bell.ring()
		idle = time.Now()
	}
}

func call(name, args string) protocol.JobResult {
	b, _ := json.Marshal([]map[string]any{{"id": "c1", "type": "function", "function": map[string]string{"name": name, "arguments": args}}})
	return protocol.JobResult{ToolCalls: b, FinishReason: "tool_calls", TokensOut: 5, DurationMS: 10}
}

func text(t string) protocol.JobResult {
	return protocol.JobResult{Output: t, TokensOut: 3, DurationMS: 10}
}

func lastMessage(t *testing.T, c *apiClient, chatID string) Message {
	var out struct{ Messages []Message }
	c.do("GET", "/chats/"+chatID+"/messages", nil, &out)
	return out.Messages[len(out.Messages)-1]
}

func systemPrompt(j protocol.Job) string {
	if len(j.Messages) > 0 && j.Messages[0].Role == "system" {
		return j.Messages[0].Content
	}
	return ""
}

func TestAgentRemembersAcrossChats(t *testing.T) {
	s, hs := newSocial(t, Config{})
	vlad := signup(t, hs.URL, "vlad")
	model := newFakeModel(t, s, func(j protocol.Job) protocol.JobResult {
		last := j.Messages[len(j.Messages)-1]
		switch {
		case last.Role == "tool":
			return text("Запомнил, удачи с курсовой!")
		case strings.Contains(last.Content, "физфак"):
			return call("remember", `{"fact":"Учится на 2 курсе физфака СПбГУ"}`)
		}
		return text("Конечно.")
	})

	var c Chat
	vlad.do("POST", "/chats", map[string]string{}, &c)
	vlad.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "Я учусь на 2 курсе физфака"}, nil)
	model.drain()
	time.Sleep(100 * time.Millisecond)
	if m := lastMessage(t, vlad, c.ID); m.Status != MsgDone || m.Content != "Запомнил, удачи с курсовой!" {
		t.Fatalf("final answer: %+v", m)
	}
	if len(model.jobs) != 2 || len(model.jobs[0].Tools) == 0 {
		t.Fatalf("expected a tool step and a final step, got %d jobs", len(model.jobs))
	}
	tool := model.jobs[1].Messages[len(model.jobs[1].Messages)-1]
	if tool.Role != "tool" || !strings.Contains(tool.Content, "Запомнено") {
		t.Fatalf("tool result not fed back: %+v", tool)
	}

	// A brand new chat already knows the fact.
	var c2 Chat
	vlad.do("POST", "/chats", map[string]string{}, &c2)
	vlad.do("POST", "/chats/"+c2.ID+"/messages", map[string]any{"content": "Привет"}, nil)
	model.drain()
	if sp := systemPrompt(model.jobs[2]); !strings.Contains(sp, "физфака СПбГУ") {
		t.Fatalf("memory missing from the prompt: %s", sp)
	}
	// Someone else does not see it.
	eve := signup(t, hs.URL, "eve")
	var c3 Chat
	eve.do("POST", "/chats", map[string]string{}, &c3)
	eve.do("POST", "/chats/"+c3.ID+"/messages", map[string]any{"content": "Привет"}, nil)
	model.drain()
	if sp := systemPrompt(model.jobs[3]); strings.Contains(sp, "физфака") {
		t.Fatal("memory leaked to another person")
	}
}

func TestProjectSkillsAreShared(t *testing.T) {
	s, hs := newSocial(t, Config{})
	anna, boris := signup(t, hs.URL, "anna"), signup(t, hs.URL, "boris")
	var p Project
	anna.do("POST", "/projects", map[string]any{"name": "Лабы"}, &p)
	boris.do("POST", "/projects/join", map[string]string{"code": p.InviteCode}, nil)

	model := newFakeModel(t, s, func(j protocol.Job) protocol.JobResult {
		last := j.Messages[len(j.Messages)-1]
		if last.Role == "tool" {
			return text("Готово: " + last.Content)
		}
		if strings.Contains(last.Content, "сохрани") {
			return call("save_skill", `{"name":"Отчёт по лабе","description":"оформление отчёта","instructions":"1. Цель 2. Схема 3. Расчёт погрешностей"}`)
		}
		return call("use_skill", `{"name":"отчёт по лабе"}`)
	})
	anna.do("POST", "/chats/"+p.ChatID+"/messages", map[string]any{"content": "@улей сохрани навык оформления отчёта"}, nil)
	model.drain()
	boris.do("POST", "/chats/"+p.ChatID+"/messages", map[string]any{"content": "@улей оформи отчёт по лабе №3"}, nil)
	model.drain()
	time.Sleep(100 * time.Millisecond)
	if m := lastMessage(t, boris, p.ChatID); !strings.Contains(m.Content, "Расчёт погрешностей") {
		t.Fatalf("Boris could not use Anna's project skill: %+v", m)
	}
	if !strings.Contains(systemPrompt(model.jobs[len(model.jobs)-1]), "Отчёт по лабе") {
		t.Fatal("project skills must be listed in the prompt")
	}
}

func TestScheduledTaskRuns(t *testing.T) {
	s, hs := newSocial(t, Config{})
	c0 := &clock{t: time.Date(2026, 10, 21, 20, 0, 0, 0, s.loc())}
	s.store.now = c0.now
	vlad := signup(t, hs.URL, "vlad")
	model := newFakeModel(t, s, func(j protocol.Job) protocol.JobResult {
		last := j.Messages[len(j.Messages)-1]
		switch {
		case last.Role == "tool":
			return text(last.Content)
		case strings.Contains(last.Content, "каждое утро"):
			return call("schedule_task", `{"task":"Составь план на день","kind":"daily","time":"09:00"}`)
		}
		return text("План: 1) лаба 2) спорт")
	})
	var c Chat
	vlad.do("POST", "/chats", map[string]string{}, &c)
	vlad.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "каждое утро в 9 присылай план на день"}, nil)
	model.drain()
	time.Sleep(100 * time.Millisecond)
	if m := lastMessage(t, vlad, c.ID); !strings.Contains(m.Content, "22.10.2026 в 09:00") {
		t.Fatalf("schedule not confirmed: %+v", m)
	}

	c0.t = c0.t.Add(13*time.Hour + time.Minute) // 09:01 next morning
	for _, m := range s.runSchedules() {
		_ = m
	}
	model.drain()
	time.Sleep(100 * time.Millisecond)
	var out struct{ Messages []Message }
	vlad.do("GET", "/chats/"+c.ID+"/messages", nil, &out)
	n := len(out.Messages)
	if n < 2 || !strings.Contains(out.Messages[n-2].Content, "⏰ Задание по расписанию: Составь план") || out.Messages[n-1].Content != "План: 1) лаба 2) спорт" {
		t.Fatalf("scheduled run missing: %+v", out.Messages[max(0, n-3):])
	}
	list, _ := s.store.Schedules(func() string { u, _ := s.store.UserBySession(vlad.token); return u.ID }(), s.loc())
	if len(list) != 1 || !strings.Contains(list[0].When, "каждый день в 09:00") ||
		time.Unix(list[0].NextRun, 0).In(s.loc()).Format("02.01 15:04") != "23.10 09:00" {
		t.Fatalf("next run not moved to tomorrow: %+v", list)
	}
}

func TestSearchHistoryAndStepLimit(t *testing.T) {
	s, hs := newSocial(t, Config{})
	vlad := signup(t, hs.URL, "vlad")
	steps := 0
	model := newFakeModel(t, s, func(j protocol.Job) protocol.JobResult {
		last := j.Messages[len(j.Messages)-1]
		if last.Role == "user" && strings.Contains(last.Content, "маятник длиной") {
			return text("Ок")
		}
		if len(j.Tools) == 0 {
			return text("Итог: нашёл старое сообщение.")
		}
		steps++
		return call("search_history", `{"query":"маятник"}`) // a model stuck in a loop
	})
	var c Chat
	vlad.do("POST", "/chats", map[string]string{}, &c)
	vlad.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "Берём маятник длиной 1 м"}, nil)
	model.drain()
	vlad.do("POST", "/chats/"+c.ID+"/messages", map[string]any{"content": "Что мы решили раньше?"}, nil)
	model.drain()
	time.Sleep(100 * time.Millisecond)
	found := false
	for _, j := range model.jobs {
		for _, m := range j.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "маятник длиной 1 м") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("search did not find the old message")
	}
	if steps != agentMaxSteps-1 {
		t.Fatalf("tool steps must stop at %d, got %d", agentMaxSteps-1, steps)
	}
	if m := lastMessage(t, vlad, c.ID); m.Content != "Итог: нашёл старое сообщение." {
		t.Fatalf("final answer after the limit: %+v", m)
	}
}
