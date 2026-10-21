package coordinator

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sqo4/uley/internal/protocol"
)

// agentTools describes Улей's own tools to the model (OpenAI format).
var agentTools = json.RawMessage(`[
{"type":"function","function":{"name":"remember","description":"Запомнить важный и долговременный факт о собеседнике или проекте (учёба, цели, предпочтения, договорённости). Не запоминай мелочи из одного разговора.","parameters":{"type":"object","properties":{"fact":{"type":"string","description":"Факт одной фразой"}},"required":["fact"]}}},
{"type":"function","function":{"name":"forget","description":"Забыть факт по его номеру из списка памяти (если он устарел или человек просит забыть).","parameters":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}}},
{"type":"function","function":{"name":"save_skill","description":"Сохранить навык — подробную инструкцию, как выполнять повторяющуюся задачу (например, как оформлять отчёт). Сохраняй после сложной задачи, которую попросят повторять, или когда об этом просят.","parameters":{"type":"object","properties":{"name":{"type":"string","description":"Короткое название"},"description":{"type":"string","description":"Когда применять, одной фразой"},"instructions":{"type":"string","description":"Пошаговая инструкция"}},"required":["name","description","instructions"]}}},
{"type":"function","function":{"name":"use_skill","description":"Открыть сохранённый навык по названию, чтобы следовать его инструкции.","parameters":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}},
{"type":"function","function":{"name":"search_history","description":"Найти в прошлых разговорах собеседника сообщения по ключевым словам.","parameters":{"type":"object","properties":{"query":{"type":"string","description":"Ключевые слова"}},"required":["query"]}}},
{"type":"function","function":{"name":"schedule_task","description":"Поставить задание, которое Улей выполнит сам в нужное время и пришлёт результат в этот чат (напоминание, ежедневная сводка и т.п.).","parameters":{"type":"object","properties":{"task":{"type":"string","description":"Что сделать, как просьба к себе"},"kind":{"type":"string","enum":["once","daily","weekly","interval"]},"time":{"type":"string","description":"Время ЧЧ:ММ (для once, daily, weekly)"},"date":{"type":"string","description":"Дата ГГГГ-ММ-ДД (для once)"},"weekday":{"type":"integer","description":"День недели 1=пн … 7=вс (для weekly)"},"every_hours":{"type":"number","description":"Интервал в часах (для interval), не меньше 1"}},"required":["task","kind"]}}},
{"type":"function","function":{"name":"list_scheduled","description":"Показать задания по расписанию собеседника.","parameters":{"type":"object","properties":{}}}},
{"type":"function","function":{"name":"cancel_scheduled","description":"Отменить задание по расписанию по номеру.","parameters":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}}}
]`)

var toolNotes = map[string]string{
	"remember": "🧠 запоминаю", "forget": "🧹 забываю", "save_skill": "🛠 сохраняю навык", "use_skill": "📘 открываю навык",
	"search_history": "🔎 ищу в прошлых разговорах", "schedule_task": "⏰ ставлю задание", "list_scheduled": "📋 смотрю задания",
	"cancel_scheduled": "🗑 отменяю задание",
}

func (s *Server) loc() *time.Location {
	if s.cfg.Location != nil {
		return s.cfg.Location
	}
	return time.UTC
}

// agentSystem adds what the agent knows and can do to the system prompt.
func (s *Server) agentSystem(c Chat, u User) string {
	sc := scopeOf(c, u)
	now := s.store.now().In(s.loc())
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nСейчас %s, %s (%s).", weekdayNames[isoWeekday(now)], now.Format("02.01.2006 15:04"), s.loc())
	whose := "о собеседнике (" + u.Name + ")"
	if sc.projectID != "" {
		whose = "о проекте и его участниках"
	}
	mem, _ := s.store.Memories(sc, memoriesInPrompt)
	if len(mem) > 0 {
		b.WriteString("\nЧто ты помнишь " + whose + " (номер — текст):")
		for i := len(mem) - 1; i >= 0; i-- {
			fmt.Fprintf(&b, "\n[%d] %s", mem[i].ID, mem[i].Text)
		}
	}
	skills, _ := s.store.Skills(sc)
	if len(skills) > 0 {
		b.WriteString("\nСохранённые навыки (открывай через use_skill, если подходят):")
		for _, k := range skills {
			fmt.Fprintf(&b, "\n- %s: %s", k.Name, k.Description)
		}
	}
	b.WriteString("\nУ тебя есть инструменты: память (remember/forget), навыки (save_skill/use_skill), поиск по прошлым разговорам, задания по расписанию. " +
		"Пользуйся ими, когда это действительно помогает, и не рассказывай о них без нужды. Запоминай только долговременное.")
	return b.String()
}

func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// startAgent turns a pending answer into an agent run.
func (s *Server) startAgent(ai Message, c Chat, u User, msgs []protocol.Message) error {
	if err := s.store.saveRun(ai.ID, u.ID, c.ID, msgs, 0); err != nil {
		return err
	}
	taskID, err := s.store.CreateTaskFor(u.ID, s.hiveModel(), agentPayload(msgs, true), 1)
	if err != nil {
		return err
	}
	return s.store.SetMessageTask(ai.ID, taskID)
}

func agentPayload(msgs []protocol.Message, withTools bool) taskPayload {
	p := taskPayload{Messages: msgs, MaxTokens: 1536, Temperature: 0.6}
	if withTools {
		p.Tools, p.ToolChoice = agentTools, json.RawMessage(`"auto"`)
	}
	return p
}

type toolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// advanceAgents moves every agent run whose model step finished: it runs
// the tools the model asked for and starts the next step, or publishes the
// final answer. It returns the messages that changed.
func (s *Server) advanceAgents() []Message {
	runs, err := s.store.finishedRuns()
	logErr("agent runs", err)
	var changed []Message
	for _, f := range runs {
		var m Message
		var err error
		var calls []toolCall
		if len(f.task.ToolCalls) > 2 {
			json.Unmarshal(f.task.ToolCalls, &calls)
		}
		switch {
		case f.task.Status != protocol.TaskDone:
			m, err = s.store.endRun(f.msgID, MsgFailed, "Не получилось ответить: "+f.task.Error)
		case len(calls) == 0:
			out := f.task.Output
			if strings.TrimSpace(out) == "" {
				out = "Готово."
			}
			m, err = s.store.endRun(f.msgID, MsgDone, out)
		default:
			m, err = s.runToolsStep(f, calls)
		}
		if err != nil {
			logErr("agent step", err)
			continue
		}
		changed = append(changed, m)
	}
	return changed
}

func (s *Server) runToolsStep(f finishedRun, calls []toolCall) (Message, error) {
	c, err := s.store.ChatAccess(f.chatID, f.userID)
	if err != nil {
		return s.store.endRun(f.msgID, MsgFailed, "Чат больше недоступен")
	}
	u, err := s.store.GetUser(f.userID)
	if err != nil {
		return s.store.endRun(f.msgID, MsgFailed, "Пользователь не найден")
	}
	msgs := append(f.msgs, protocol.Message{Role: "assistant", Content: f.task.Output, ToolCalls: f.task.ToolCalls})
	var notes []string
	for i, call := range calls {
		if call.ID == "" {
			call.ID = "call_" + strconv.Itoa(f.step) + "_" + strconv.Itoa(i)
		}
		result := s.runTool(c, u, call.Function.Name, call.Function.Arguments)
		msgs = append(msgs, protocol.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: result})
		if n, ok := toolNotes[call.Function.Name]; ok {
			notes = append(notes, n)
		}
	}
	step := f.step + 1
	// The last step goes without tools, so the model has to answer.
	taskID, err := s.store.CreateTaskFor(u.ID, s.hiveModel(), agentPayload(msgs, step < agentMaxSteps-1), 1)
	if err != nil {
		return Message{}, err
	}
	if err := s.store.saveRun(f.msgID, u.ID, c.ID, msgs, step); err != nil {
		return Message{}, err
	}
	s.bell.ring()
	return s.store.setProgress(f.msgID, taskID, strings.Join(notes, ", "))
}

// runTool executes one of Улей's own tools and returns text for the model.
func (s *Server) runTool(c Chat, u User, name, rawArgs string) string {
	var a struct {
		Fact         string  `json:"fact"`
		ID           any     `json:"id"`
		Name         string  `json:"name"`
		Description  string  `json:"description"`
		Instructions string  `json:"instructions"`
		Query        string  `json:"query"`
		Task         string  `json:"task"`
		Kind         string  `json:"kind"`
		Time         string  `json:"time"`
		Date         string  `json:"date"`
		Weekday      int     `json:"weekday"`
		EveryHours   float64 `json:"every_hours"`
	}
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return "Ошибка: аргументы не в формате JSON"
		}
	}
	sc := scopeOf(c, u)
	id := func() int64 {
		switch v := a.ID.(type) {
		case float64:
			return int64(v)
		case string:
			n, _ := strconv.ParseInt(strings.Trim(v, "[]# "), 10, 64)
			return n
		}
		return 0
	}
	switch name {
	case "remember":
		m, err := s.store.Remember(sc, a.Fact)
		if err != nil {
			return "Ошибка: " + err.Error()
		}
		return fmt.Sprintf("Запомнено под номером %d.", m.ID)
	case "forget":
		if err := s.store.Forget(sc, id()); err != nil {
			return "Такого факта нет."
		}
		return "Забыто."
	case "save_skill":
		k, err := s.store.SaveSkill(sc, a.Name, a.Description, a.Instructions)
		if err != nil {
			return "Ошибка: " + err.Error()
		}
		return "Навык «" + k.Name + "» сохранён."
	case "use_skill":
		k, err := s.store.UseSkill(sc, a.Name)
		if err != nil {
			return "Навыка с таким названием нет."
		}
		return "Навык «" + k.Name + "»:\n" + k.Body
	case "search_history":
		found, err := s.store.SearchHistory(u.ID, a.Query, s.loc())
		if err != nil || len(found) == 0 {
			return "Ничего не найдено."
		}
		b, _ := json.Marshal(found)
		return string(b)
	case "schedule_task":
		sch, err := s.scheduleFromArgs(a.Task, a.Kind, a.Time, a.Date, a.Weekday, a.EveryHours, u, c)
		if err != nil {
			return "Ошибка: " + err.Error()
		}
		return fmt.Sprintf("Задание №%d поставлено: %s. Первый запуск %s.", sch.ID, sch.When,
			time.Unix(sch.NextRun, 0).In(s.loc()).Format("02.01.2006 в 15:04"))
	case "list_scheduled":
		list, _ := s.store.Schedules(u.ID, s.loc())
		if len(list) == 0 {
			return "Заданий по расписанию нет."
		}
		var b strings.Builder
		for _, x := range list {
			fmt.Fprintf(&b, "№%d — %s: %s\n", x.ID, x.When, x.Prompt)
		}
		return b.String()
	case "cancel_scheduled":
		if err := s.store.CancelSchedule(u.ID, id()); err != nil {
			return "Такого задания нет."
		}
		return "Задание отменено."
	}
	return "Неизвестный инструмент " + name
}

// scheduleFromArgs validates what the model asked for and stores it.
func (s *Server) scheduleFromArgs(task, kind, hm, date string, weekday int, everyHours float64, u User, c Chat) (Schedule, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return Schedule{}, fmt.Errorf("не сказано, что делать")
	}
	sc := Schedule{Kind: kind}
	parseHM := func() error {
		t, err := time.Parse("15:04", strings.TrimSpace(hm))
		if err != nil {
			return fmt.Errorf("время нужно в виде ЧЧ:ММ")
		}
		sc.AtMinute = t.Hour()*60 + t.Minute()
		return nil
	}
	now := s.store.now()
	var first time.Time
	switch kind {
	case "once":
		if err := parseHM(); err != nil {
			return sc, err
		}
		d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), s.loc())
		if err != nil {
			return sc, fmt.Errorf("дата нужна в виде ГГГГ-ММ-ДД")
		}
		first = d.Add(time.Duration(sc.AtMinute) * time.Minute)
		if !first.After(now) {
			return sc, fmt.Errorf("это время уже прошло")
		}
	case "daily":
		if err := parseHM(); err != nil {
			return sc, err
		}
		first = sc.nextRun(now, s.loc())
	case "weekly":
		if err := parseHM(); err != nil {
			return sc, err
		}
		if weekday < 1 || weekday > 7 {
			return sc, fmt.Errorf("день недели — число от 1 (пн) до 7 (вс)")
		}
		sc.Weekday = weekday
		first = sc.nextRun(now, s.loc())
	case "interval":
		sc.EveryMin = int(everyHours * 60)
		if sc.EveryMin < minIntervalMinute {
			return sc, fmt.Errorf("интервал не меньше часа")
		}
		first = sc.nextRun(now, s.loc())
	default:
		return sc, fmt.Errorf("kind: once, daily, weekly или interval")
	}
	created, err := s.store.AddSchedule(u.ID, c.ID, task, sc, first)
	if err != nil {
		return created, err
	}
	created.When = created.describe(s.loc())
	return created, nil
}

// runSchedules starts every task whose time has come: the request appears
// in its chat on behalf of its author and Улей answers it as usual.
func (s *Server) runSchedules() []Message {
	due, err := s.store.DueSchedules(s.loc())
	logErr("schedules", err)
	var changed []Message
	for _, d := range due {
		u, err := s.store.GetUser(d.userID)
		if err != nil {
			continue
		}
		c, err := s.store.ChatAccess(d.ChatID, u.ID)
		if err != nil {
			s.store.CancelSchedule(u.ID, d.ID) // the chat is gone or the user left the project
			continue
		}
		q, err := s.store.AddMessage(Message{ChatID: c.ID, UserID: u.ID, AuthorName: u.Name, Role: "user", Status: MsgDone,
			Content: "⏰ Задание по расписанию: " + d.Prompt})
		if err != nil {
			continue
		}
		changed = append(changed, q)
		ai, err := s.askAI(c, u)
		if err == nil {
			changed = append(changed, ai)
		}
	}
	return changed
}
