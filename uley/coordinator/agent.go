package coordinator

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	_ "time/tzdata" // the server may run in a container without zoneinfo

	"github.com/sqo4/uley/internal/protocol"
)

// Улей as an agent: it remembers people, keeps reusable skills, searches
// past conversations and runs scheduled tasks. The model only decides which
// tool to call; the tools below are this file's own Go code running on the
// coordinator. Nothing a model writes is ever executed as code.

const agentSchema = `
CREATE TABLE IF NOT EXISTS memories (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id    TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL DEFAULT '',
	text       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS memories_scope ON memories(user_id, project_id);
CREATE TABLE IF NOT EXISTS skills (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     TEXT NOT NULL DEFAULT '',
	project_id  TEXT NOT NULL DEFAULT '',
	name        TEXT NOT NULL,
	description TEXT NOT NULL,
	body        TEXT NOT NULL,
	uses        INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	UNIQUE(user_id, project_id, name)
);
CREATE TABLE IF NOT EXISTS schedules (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id    TEXT NOT NULL,
	chat_id    TEXT NOT NULL,
	prompt     TEXT NOT NULL,
	kind       TEXT NOT NULL,   -- once, daily, weekly, interval
	at_minute  INTEGER NOT NULL DEFAULT 0, -- minutes after local midnight
	weekday    INTEGER NOT NULL DEFAULT 0, -- 1 = Monday … 7 = Sunday
	every_min  INTEGER NOT NULL DEFAULT 0,
	next_run   INTEGER NOT NULL,
	enabled    INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS schedules_due ON schedules(enabled, next_run);
CREATE TABLE IF NOT EXISTS agent_runs (
	message_id INTEGER PRIMARY KEY,
	user_id    TEXT NOT NULL,
	chat_id    TEXT NOT NULL,
	messages   TEXT NOT NULL,
	step       INTEGER NOT NULL DEFAULT 0
);`

const (
	agentMaxSteps     = 6
	maxMemories       = 200
	maxSkills         = 100
	maxSchedules      = 20
	memoriesInPrompt  = 60
	maxMemoryLen      = 500
	maxSkillLen       = 8000
	minIntervalMinute = 60
)

// scope is whose memory and skills a conversation uses: a person's own in
// personal chats, the group's in a project chat.
type scope struct {
	userID    string
	projectID string
}

func scopeOf(c Chat, u User) scope {
	if c.Kind == ChatProject {
		return scope{projectID: c.ProjectID}
	}
	return scope{userID: u.ID}
}

// ---- memory ----

type Memory struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) Remember(sc scope, text string) (Memory, error) {
	text = cleanText(text, maxMemoryLen)
	if text == "" {
		return Memory{}, ErrInvalidInput
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE user_id=? AND project_id=?`, sc.userID, sc.projectID).Scan(&n)
	if n >= maxMemories {
		// Forget the oldest fact to make room, like a notebook that is full.
		s.db.Exec(`DELETE FROM memories WHERE id=(SELECT MIN(id) FROM memories WHERE user_id=? AND project_id=?)`, sc.userID, sc.projectID)
	}
	m := Memory{Text: text, CreatedAt: s.now().Unix()}
	res, err := s.db.Exec(`INSERT INTO memories(user_id, project_id, text, created_at) VALUES(?,?,?,?)`, sc.userID, sc.projectID, text, m.CreatedAt)
	if err != nil {
		return m, err
	}
	m.ID, _ = res.LastInsertId()
	return m, nil
}

func (s *Store) Memories(sc scope, limit int) ([]Memory, error) {
	rows, err := s.db.Query(`SELECT id, text, created_at FROM memories WHERE user_id=? AND project_id=? ORDER BY id DESC LIMIT ?`,
		sc.userID, sc.projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		rows.Scan(&m.ID, &m.Text, &m.CreatedAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Forget(sc scope, id int64) error {
	res, err := s.db.Exec(`DELETE FROM memories WHERE id=? AND user_id=? AND project_id=?`, id, sc.userID, sc.projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- skills ----

type Skill struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Uses        int    `json:"uses"`
}

func (s *Store) SaveSkill(sc scope, name, description, body string) (Skill, error) {
	name, description, body = cleanText(name, 60), cleanText(description, 200), cleanText(body, maxSkillLen)
	if name == "" || body == "" {
		return Skill{}, fmt.Errorf("%w: у навыка должны быть название и инструкция", ErrInvalidInput)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM skills WHERE user_id=? AND project_id=?`, sc.userID, sc.projectID).Scan(&n)
	if n >= maxSkills {
		return Skill{}, fmt.Errorf("%w: не больше %d навыков", ErrInvalidInput, maxSkills)
	}
	_, err := s.db.Exec(`INSERT INTO skills(user_id, project_id, name, description, body, created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(user_id, project_id, name) DO UPDATE SET description=excluded.description, body=excluded.body`,
		sc.userID, sc.projectID, name, description, body, s.now().Unix())
	if err != nil {
		return Skill{}, err
	}
	return s.GetSkill(sc, name)
}

func (s *Store) GetSkill(sc scope, name string) (Skill, error) {
	// SQLite's NOCASE knows only Latin letters, so match names in Go.
	rows, err := s.db.Query(`SELECT id, name, description, body, uses FROM skills WHERE user_id=? AND project_id=?`, sc.userID, sc.projectID)
	if err != nil {
		return Skill{}, err
	}
	defer rows.Close()
	want := strings.ToLower(strings.TrimSpace(name))
	for rows.Next() {
		var k Skill
		rows.Scan(&k.ID, &k.Name, &k.Description, &k.Body, &k.Uses)
		if strings.ToLower(k.Name) == want {
			return k, nil
		}
	}
	return Skill{}, ErrNotFound
}

func (s *Store) UseSkill(sc scope, name string) (Skill, error) {
	k, err := s.GetSkill(sc, name)
	if err == nil {
		s.db.Exec(`UPDATE skills SET uses=uses+1 WHERE id=?`, k.ID)
	}
	return k, err
}

func (s *Store) Skills(sc scope) ([]Skill, error) {
	rows, err := s.db.Query(`SELECT id, name, description, uses FROM skills WHERE user_id=? AND project_id=? ORDER BY uses DESC, name`,
		sc.userID, sc.projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		var k Skill
		rows.Scan(&k.ID, &k.Name, &k.Description, &k.Uses)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSkill(sc scope, id int64) error {
	res, err := s.db.Exec(`DELETE FROM skills WHERE id=? AND user_id=? AND project_id=?`, id, sc.userID, sc.projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- search in past conversations ----

type Snippet struct {
	ChatTitle string `json:"chat"`
	Author    string `json:"author"`
	Text      string `json:"text"`
	When      string `json:"when"`
}

// SearchHistory finds messages containing every word of the query in chats
// the user can read (their personal chats and their projects).
func (s *Store) SearchHistory(userID, query string, loc *time.Location) ([]Snippet, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, ErrInvalidInput
	}
	if len(words) > 6 {
		words = words[:6]
	}
	q := `SELECT c.title, COALESCE(u.name, 'Улей'), m.content, m.created_at FROM messages m
		JOIN chats c ON c.id=m.chat_id LEFT JOIN users u ON u.id=m.user_id
		WHERE m.status='done' AND (c.owner_id=? OR c.project_id IN (SELECT project_id FROM project_members WHERE user_id=?))`
	args := []any{userID, userID}
	for _, w := range words {
		q += ` AND lower(m.content) LIKE ?`
		args = append(args, "%"+w+"%")
	}
	q += ` ORDER BY m.id DESC LIMIT 6`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snippet
	for rows.Next() {
		var sn Snippet
		var at int64
		rows.Scan(&sn.ChatTitle, &sn.Author, &sn.Text, &at)
		if r := []rune(sn.Text); len(r) > 400 {
			sn.Text = string(r[:400]) + "…"
		}
		sn.When = time.Unix(at, 0).In(loc).Format("02.01.2006 15:04")
		out = append(out, sn)
	}
	return out, rows.Err()
}

// ---- schedules ----

type Schedule struct {
	ID       int64  `json:"id"`
	ChatID   string `json:"chat_id"`
	Prompt   string `json:"prompt"`
	Kind     string `json:"kind"`
	When     string `json:"when"` // human readable
	NextRun  int64  `json:"next_run"`
	AtMinute int    `json:"-"`
	Weekday  int    `json:"-"`
	EveryMin int    `json:"-"`
}

var weekdayNames = []string{"", "понедельник", "вторник", "среду", "четверг", "пятницу", "субботу", "воскресенье"}

func (sc Schedule) describe(loc *time.Location) string {
	hm := fmt.Sprintf("%02d:%02d", sc.AtMinute/60, sc.AtMinute%60)
	switch sc.Kind {
	case "daily":
		return "каждый день в " + hm
	case "weekly":
		return "каждую неделю в " + weekdayNames[sc.Weekday] + " в " + hm
	case "interval":
		if sc.EveryMin%60 == 0 {
			return fmt.Sprintf("каждые %d ч", sc.EveryMin/60)
		}
		return fmt.Sprintf("каждые %d мин", sc.EveryMin)
	}
	return "один раз, " + time.Unix(sc.NextRun, 0).In(loc).Format("02.01.2006 в 15:04")
}

// nextRun computes the next start strictly after `after`.
func (sc Schedule) nextRun(after time.Time, loc *time.Location) time.Time {
	t := after.In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	at := day.Add(time.Duration(sc.AtMinute) * time.Minute)
	switch sc.Kind {
	case "daily":
		for !at.After(t) {
			at = at.AddDate(0, 0, 1)
		}
		return at
	case "weekly":
		wd := int(at.Weekday())
		if wd == 0 {
			wd = 7
		}
		at = at.AddDate(0, 0, (sc.Weekday-wd+7)%7)
		for !at.After(t) {
			at = at.AddDate(0, 0, 7)
		}
		return at
	case "interval":
		return after.Add(time.Duration(sc.EveryMin) * time.Minute)
	}
	return time.Time{}
}

func (s *Store) AddSchedule(userID, chatID, prompt string, sc Schedule, first time.Time) (Schedule, error) {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE user_id=? AND enabled=1`, userID).Scan(&n)
	if n >= maxSchedules {
		return sc, fmt.Errorf("%w: не больше %d заданий по расписанию", ErrInvalidInput, maxSchedules)
	}
	sc.Prompt, sc.ChatID, sc.NextRun = cleanText(prompt, 2000), chatID, first.Unix()
	res, err := s.db.Exec(`INSERT INTO schedules(user_id, chat_id, prompt, kind, at_minute, weekday, every_min, next_run, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		userID, chatID, sc.Prompt, sc.Kind, sc.AtMinute, sc.Weekday, sc.EveryMin, sc.NextRun, s.now().Unix())
	if err != nil {
		return sc, err
	}
	sc.ID, _ = res.LastInsertId()
	return sc, nil
}

func scanSchedules(rows *sql.Rows, loc *time.Location) ([]Schedule, error) {
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		var sc Schedule
		if err := rows.Scan(&sc.ID, &sc.ChatID, &sc.Prompt, &sc.Kind, &sc.AtMinute, &sc.Weekday, &sc.EveryMin, &sc.NextRun); err != nil {
			return nil, err
		}
		sc.When = sc.describe(loc)
		out = append(out, sc)
	}
	return out, rows.Err()
}

const scheduleCols = `id, chat_id, prompt, kind, at_minute, weekday, every_min, next_run`

func (s *Store) Schedules(userID string, loc *time.Location) ([]Schedule, error) {
	rows, err := s.db.Query(`SELECT `+scheduleCols+` FROM schedules WHERE user_id=? AND enabled=1 ORDER BY next_run`, userID)
	if err != nil {
		return nil, err
	}
	return scanSchedules(rows, loc)
}

func (s *Store) CancelSchedule(userID string, id int64) error {
	res, err := s.db.Exec(`UPDATE schedules SET enabled=0 WHERE id=? AND user_id=? AND enabled=1`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type dueSchedule struct {
	Schedule
	userID string
}

// DueSchedules returns enabled schedules whose time has come and moves each
// to its next run (or switches a one-off off).
func (s *Store) DueSchedules(loc *time.Location) ([]dueSchedule, error) {
	now := s.now()
	rows, err := s.db.Query(`SELECT user_id, `+scheduleCols+` FROM schedules WHERE enabled=1 AND next_run<=?`, now.Unix())
	if err != nil {
		return nil, err
	}
	var due []dueSchedule
	for rows.Next() {
		var d dueSchedule
		if err := rows.Scan(&d.userID, &d.ID, &d.ChatID, &d.Prompt, &d.Kind, &d.AtMinute, &d.Weekday, &d.EveryMin, &d.NextRun); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, d)
	}
	rows.Close()
	for _, d := range due {
		if d.Kind == "once" {
			s.db.Exec(`UPDATE schedules SET enabled=0 WHERE id=?`, d.ID)
			continue
		}
		next := d.nextRun(now, loc)
		s.db.Exec(`UPDATE schedules SET next_run=? WHERE id=?`, next.Unix(), d.ID)
	}
	return due, nil
}

// ---- agent runs ----

func (s *Store) saveRun(msgID int64, userID, chatID string, msgs []protocol.Message, step int) error {
	raw, _ := json.Marshal(msgs)
	_, err := s.db.Exec(`INSERT INTO agent_runs(message_id, user_id, chat_id, messages, step) VALUES(?,?,?,?,?)
		ON CONFLICT(message_id) DO UPDATE SET messages=excluded.messages, step=excluded.step`, msgID, userID, chatID, string(raw), step)
	return err
}

type finishedRun struct {
	msgID   int64
	userID  string
	chatID  string
	step    int
	msgs    []protocol.Message
	task    protocol.Task
	payload taskPayload
}

// finishedRuns lists agent runs whose current model step has completed.
func (s *Store) finishedRuns() ([]finishedRun, error) {
	rows, err := s.db.Query(`SELECT r.message_id, r.user_id, r.chat_id, r.step, r.messages, m.task_id FROM agent_runs r
		JOIN messages m ON m.id=r.message_id JOIN tasks t ON t.id=m.task_id
		WHERE t.status IN ('done','failed')`)
	if err != nil {
		return nil, err
	}
	var out []finishedRun
	var taskIDs []string
	for rows.Next() {
		var f finishedRun
		var raw, taskID string
		if err := rows.Scan(&f.msgID, &f.userID, &f.chatID, &f.step, &raw, &taskID); err != nil {
			rows.Close()
			return nil, err
		}
		json.Unmarshal([]byte(raw), &f.msgs)
		out = append(out, f)
		taskIDs = append(taskIDs, taskID)
	}
	rows.Close()
	for i, id := range taskIDs {
		out[i].task, _ = s.GetTask(id)
	}
	return out, nil
}

func (s *Store) endRun(msgID int64, status, content string) (Message, error) {
	s.db.Exec(`DELETE FROM agent_runs WHERE message_id=?`, msgID)
	if _, err := s.db.Exec(`UPDATE messages SET status=?, content=? WHERE id=?`, status, content, msgID); err != nil {
		return Message{}, err
	}
	return s.GetMessage(msgID)
}

func (s *Store) setProgress(msgID int64, taskID, note string) (Message, error) {
	if _, err := s.db.Exec(`UPDATE messages SET task_id=?, content=? WHERE id=?`, taskID, note, msgID); err != nil {
		return Message{}, err
	}
	return s.GetMessage(msgID)
}

func logErr(what string, err error) {
	if err != nil {
		log.Printf("%s: %v", what, err)
	}
}
