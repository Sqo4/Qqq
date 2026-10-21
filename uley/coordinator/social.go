package coordinator

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// People (users) are separate from computers (nodes): a person chats and
// works on projects, a node lends its GPU.

const socialSchema = `
CREATE TABLE IF NOT EXISTS users (
	id         TEXT PRIMARY KEY,
	login      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	name       TEXT NOT NULL,
	pass_hash  TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id),
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	owner_id    TEXT NOT NULL REFERENCES users(id),
	invite_code TEXT NOT NULL UNIQUE,
	open        INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS project_members (
	project_id TEXT NOT NULL REFERENCES projects(id),
	user_id    TEXT NOT NULL REFERENCES users(id),
	joined_at  INTEGER NOT NULL,
	PRIMARY KEY (project_id, user_id)
);
CREATE TABLE IF NOT EXISTS chats (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,           -- personal, project, lobby
	owner_id   TEXT NOT NULL DEFAULT '', -- personal chats
	project_id TEXT NOT NULL DEFAULT '', -- project chats
	title      TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS chats_owner ON chats(owner_id, updated_at);
CREATE TABLE IF NOT EXISTS messages (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id     TEXT NOT NULL REFERENCES chats(id),
	user_id     TEXT NOT NULL DEFAULT '', -- '' for the AI
	role        TEXT NOT NULL,            -- user, assistant
	content     TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'done', -- done, pending, failed
	task_id     TEXT NOT NULL DEFAULT '',
	project_ref TEXT NOT NULL DEFAULT '', -- a project shared in the lobby
	asked_by    TEXT NOT NULL DEFAULT '', -- AI answers: who asked
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_chat ON messages(chat_id, id);
CREATE INDEX IF NOT EXISTS messages_pending ON messages(status) WHERE status='pending';
INSERT OR IGNORE INTO chats(id, kind, title, created_at, updated_at) VALUES('lobby', 'lobby', 'Общий чат', 0, 0);
` + apiKeysSchema + agentSchema

const (
	ChatPersonal = "personal"
	ChatProject  = "project"
	ChatLobby    = "lobby"
	LobbyID      = "lobby"

	MsgDone    = "done"
	MsgPending = "pending"
	MsgFailed  = "failed"

	sessionTTL     = 30 * 24 * time.Hour
	MaxMessageLen  = 8000
	MaxProjectSize = 100
)

type User struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	OwnerID     string `json:"owner_id"`
	InviteCode  string `json:"invite_code,omitempty"` // members only
	Open        bool   `json:"open"`
	ChatID      string `json:"chat_id"`
	Members     []User `json:"members,omitempty"`
	MemberCount int    `json:"member_count"`
}

type Chat struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	ProjectID string `json:"project_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

type Message struct {
	ID         int64    `json:"id"`
	ChatID     string   `json:"chat_id"`
	UserID     string   `json:"user_id,omitempty"`
	AuthorName string   `json:"author"`
	Role       string   `json:"role"`
	Content    string   `json:"content"`
	Status     string   `json:"status"`
	Project    *Project `json:"project,omitempty"` // shared project card
	CreatedAt  int64    `json:"created_at"`
	AskedBy    string   `json:"-"`
}

var (
	ErrLoginTaken   = errors.New("этот логин уже занят")
	ErrBadLogin     = errors.New("неверный логин или пароль")
	ErrNotFound     = errors.New("не найдено")
	ErrForbidden    = errors.New("нет доступа")
	ErrProjectFull  = errors.New("в проекте уже максимум участников")
	ErrInvalidInput = errors.New("неверные данные")
)

// dummyHash keeps a login with an unknown name as slow as a real one.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("uley"), bcrypt.DefaultCost)

func validLogin(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func cleanText(s string, max int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

func (s *Store) CreateUser(login, name, password string) (User, error) {
	login = strings.TrimSpace(login)
	name = cleanText(name, 40)
	if !validLogin(login) {
		return User{}, fmt.Errorf("%w: логин 3–32 символа, латиница, цифры, _ - .", ErrInvalidInput)
	}
	if len(password) < 6 || len(password) > 72 {
		return User{}, fmt.Errorf("%w: пароль от 6 до 72 символов", ErrInvalidInput)
	}
	if name == "" {
		name = login
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	u := User{ID: newID("u_"), Login: login, Name: name}
	now := s.now().Unix()
	_, err = s.db.Exec(`INSERT INTO users(id, login, name, pass_hash, created_at, last_seen) VALUES(?,?,?,?,?,?)`,
		u.ID, u.Login, u.Name, string(hash), now, now)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return User{}, ErrLoginTaken
	}
	return u, err
}

// Login checks the password and returns a new session token.
func (s *Store) Login(login, password string) (User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRow(`SELECT id, login, name, pass_hash FROM users WHERE login=?`, strings.TrimSpace(login)).
		Scan(&u.ID, &u.Login, &u.Name, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) // same timing as a real check
		return User{}, "", ErrBadLogin
	}
	if err != nil {
		return User{}, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, "", ErrBadLogin
	}
	token := newToken()
	if _, err := s.db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at) VALUES(?,?,?)`,
		hashToken(token), u.ID, s.now().Add(sessionTTL).Unix()); err != nil {
		return User{}, "", err
	}
	return u, token, nil
}

func (s *Store) Logout(token string) {
	s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
}

func (s *Store) UserBySession(token string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT u.id, u.login, u.name FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires_at>?`, hashToken(token), s.now().Unix()).Scan(&u.ID, &u.Login, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrUnauthorized
	}
	if err == nil {
		s.db.Exec(`UPDATE users SET last_seen=? WHERE id=?`, s.now().Unix(), u.ID)
	}
	return u, err
}

func inviteCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no look-alikes
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func (s *Store) CreateProject(owner User, name, description string, open bool) (Project, error) {
	name, description = cleanText(name, 80), cleanText(description, 1000)
	if name == "" {
		return Project{}, fmt.Errorf("%w: нужно название проекта", ErrInvalidInput)
	}
	p := Project{ID: newID("p_"), Name: name, Description: description, OwnerID: owner.ID, InviteCode: inviteCode(), Open: open}
	p.ChatID = "c_" + p.ID
	now := s.now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO projects(id, name, description, owner_id, invite_code, open, created_at) VALUES(?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Description, p.OwnerID, p.InviteCode, p.Open, now); err != nil {
		return p, err
	}
	if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, joined_at) VALUES(?,?,?)`, p.ID, owner.ID, now); err != nil {
		return p, err
	}
	if _, err := tx.Exec(`INSERT INTO chats(id, kind, project_id, title, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		p.ChatID, ChatProject, p.ID, p.Name, now, now); err != nil {
		return p, err
	}
	p.Members, p.MemberCount = []User{owner}, 1
	return p, tx.Commit()
}

func (s *Store) isMember(projectID, userID string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM project_members WHERE project_id=? AND user_id=?`, projectID, userID).Scan(&one) == nil
}

// GetProject returns a project; the invite code and members only to members.
func (s *Store) GetProject(id, viewerID string) (Project, error) {
	var p Project
	err := s.db.QueryRow(`SELECT id, name, description, owner_id, invite_code, open,
		(SELECT COUNT(*) FROM project_members m WHERE m.project_id=projects.id) FROM projects WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID, &p.InviteCode, &p.Open, &p.MemberCount)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.ChatID = "c_" + p.ID
	if !s.isMember(p.ID, viewerID) {
		p.InviteCode = ""
		return p, nil
	}
	rows, err := s.db.Query(`SELECT u.id, u.login, u.name FROM project_members m JOIN users u ON u.id=m.user_id
		WHERE m.project_id=? ORDER BY m.joined_at`, p.ID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		rows.Scan(&u.ID, &u.Login, &u.Name)
		p.Members = append(p.Members, u)
	}
	return p, rows.Err()
}

// JoinProject adds the user by invite code, or by id if the project is open.
func (s *Store) JoinProject(u User, code, projectID string) (Project, error) {
	var id string
	var open bool
	var err error
	if code != "" {
		err = s.db.QueryRow(`SELECT id, open FROM projects WHERE invite_code=?`, strings.ToUpper(strings.TrimSpace(code))).Scan(&id, &open)
	} else {
		err = s.db.QueryRow(`SELECT id, open FROM projects WHERE id=?`, projectID).Scan(&id, &open)
		if err == nil && !open {
			return Project{}, fmt.Errorf("%w: проект закрытый, попросите код приглашения у автора", ErrForbidden)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM project_members WHERE project_id=?`, id).Scan(&n)
	if n >= MaxProjectSize && !s.isMember(id, u.ID) {
		return Project{}, ErrProjectFull
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO project_members(project_id, user_id, joined_at) VALUES(?,?,?)`,
		id, u.ID, s.now().Unix()); err != nil {
		return Project{}, err
	}
	return s.GetProject(id, u.ID)
}

func (s *Store) LeaveProject(u User, projectID string) error {
	_, err := s.db.Exec(`DELETE FROM project_members WHERE project_id=? AND user_id=?`, projectID, u.ID)
	return err
}

func (s *Store) UserProjects(userID string) ([]Project, error) {
	rows, err := s.db.Query(`SELECT p.id FROM projects p JOIN project_members m ON m.project_id=p.id
		WHERE m.user_id=? ORDER BY p.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := []Project{}
	for _, id := range ids {
		p, err := s.GetProject(id, userID)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) CreateChat(u User, title string) (Chat, error) {
	title = cleanText(title, 80)
	if title == "" {
		title = "Новый чат"
	}
	now := s.now().Unix()
	c := Chat{ID: newID("c_"), Kind: ChatPersonal, Title: title, UpdatedAt: now}
	_, err := s.db.Exec(`INSERT INTO chats(id, kind, owner_id, title, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		c.ID, c.Kind, u.ID, c.Title, now, now)
	return c, err
}

func (s *Store) RenameChat(u User, chatID, title string) error {
	title = cleanText(title, 80)
	if title == "" {
		return ErrInvalidInput
	}
	res, err := s.db.Exec(`UPDATE chats SET title=? WHERE id=? AND kind=? AND owner_id=?`, title, chatID, ChatPersonal, u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteChat(u User, chatID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM chats WHERE id=? AND kind=? AND owner_id=?`, chatID, ChatPersonal, u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE chat_id=?`, chatID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PersonalChats(userID string) ([]Chat, error) {
	rows, err := s.db.Query(`SELECT id, kind, title, project_id, updated_at FROM chats WHERE kind=? AND owner_id=? ORDER BY updated_at DESC`,
		ChatPersonal, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		rows.Scan(&c.ID, &c.Kind, &c.Title, &c.ProjectID, &c.UpdatedAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChatAccess returns the chat if userID may read and write it.
func (s *Store) ChatAccess(chatID, userID string) (Chat, error) {
	var c Chat
	var owner string
	err := s.db.QueryRow(`SELECT id, kind, title, project_id, owner_id, updated_at FROM chats WHERE id=?`, chatID).
		Scan(&c.ID, &c.Kind, &c.Title, &c.ProjectID, &owner, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	switch c.Kind {
	case ChatLobby:
		return c, nil
	case ChatPersonal:
		if owner == userID {
			return c, nil
		}
	case ChatProject:
		if s.isMember(c.ProjectID, userID) {
			return c, nil
		}
	}
	return c, ErrForbidden
}

func (s *Store) AddMessage(m Message) (Message, error) {
	m.CreatedAt = s.now().Unix()
	projectRef := ""
	if m.Project != nil {
		projectRef = m.Project.ID
	}
	res, err := s.db.Exec(`INSERT INTO messages(chat_id, user_id, role, content, status, project_ref, asked_by, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		m.ChatID, m.UserID, m.Role, m.Content, m.Status, projectRef, m.AskedBy, m.CreatedAt)
	if err != nil {
		return m, err
	}
	m.ID, _ = res.LastInsertId()
	s.db.Exec(`UPDATE chats SET updated_at=? WHERE id=?`, m.CreatedAt, m.ChatID)
	return m, nil
}

func (s *Store) SetMessageTask(id int64, taskID string) error {
	_, err := s.db.Exec(`UPDATE messages SET task_id=? WHERE id=?`, taskID, id)
	return err
}

const msgColumns = `m.id, m.chat_id, m.user_id, COALESCE(u.name, 'Улей'), m.role, m.content, m.status, m.project_ref, m.created_at`

func (s *Store) scanMessages(rows *sql.Rows, viewerID string) ([]Message, error) {
	out := []Message{}
	var refs []string
	for rows.Next() {
		var m Message
		var ref string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.UserID, &m.AuthorName, &m.Role, &m.Content, &m.Status, &ref, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if m.UserID == "" {
			m.AuthorName = "Улей"
		}
		out = append(out, m)
		refs = append(refs, ref)
	}
	rows.Close() // the store has one connection: close before the next query
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, ref := range refs {
		if ref == "" {
			continue
		}
		if p, err := s.GetProject(ref, viewerID); err == nil {
			p.Members, p.InviteCode = nil, ""
			out[i].Project = &p
		}
	}
	return out, nil
}

// Messages returns up to limit messages before beforeID (0 = newest), oldest first.
func (s *Store) Messages(chatID, viewerID string, beforeID int64, limit int) ([]Message, error) {
	if beforeID <= 0 {
		beforeID = 1 << 62
	}
	rows, err := s.db.Query(`SELECT * FROM (SELECT `+msgColumns+` FROM messages m LEFT JOIN users u ON u.id=m.user_id
		WHERE m.chat_id=? AND m.id<? ORDER BY m.id DESC LIMIT ?) ORDER BY id`, chatID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return s.scanMessages(rows, viewerID)
}

func (s *Store) GetMessage(id int64) (Message, error) {
	rows, err := s.db.Query(`SELECT `+msgColumns+` FROM messages m LEFT JOIN users u ON u.id=m.user_id WHERE m.id=?`, id)
	if err != nil {
		return Message{}, err
	}
	ms, err := s.scanMessages(rows, "")
	if err != nil || len(ms) == 0 {
		return Message{}, ErrNotFound
	}
	return ms[0], nil
}

func (s *Store) PendingAIMessages(userID string) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE status='pending' AND asked_by=?`, userID).Scan(&n)
	return n
}

// FinishAnswers copies finished task outputs into their pending chat
// messages and returns the messages that changed.
func (s *Store) FinishAnswers() ([]Message, error) {
	rows, err := s.db.Query(`SELECT m.id, t.status, t.output, t.error FROM messages m JOIN tasks t ON t.id=m.task_id
		WHERE m.status='pending' AND t.status IN ('done','failed') AND m.id NOT IN (SELECT message_id FROM agent_runs)`)
	if err != nil {
		return nil, err
	}
	type fin struct {
		id                    int64
		status, output, error string
	}
	var list []fin
	for rows.Next() {
		var f fin
		rows.Scan(&f.id, &f.status, &f.output, &f.error)
		list = append(list, f)
	}
	rows.Close()
	var changed []Message
	for _, f := range list {
		status, content := MsgDone, f.output
		if f.status != "done" {
			status, content = MsgFailed, "Не получилось ответить: "+f.error
		}
		if _, err := s.db.Exec(`UPDATE messages SET status=?, content=? WHERE id=?`, status, content, f.id); err != nil {
			return changed, err
		}
		if m, err := s.GetMessage(f.id); err == nil {
			changed = append(changed, m)
		}
	}
	return changed, nil
}

// ChatAudience lists users who see a chat; nil means everybody (lobby).
func (s *Store) ChatAudience(chatID string) ([]string, error) {
	var kind, owner, project string
	if err := s.db.QueryRow(`SELECT kind, owner_id, project_id FROM chats WHERE id=?`, chatID).Scan(&kind, &owner, &project); err != nil {
		return nil, err
	}
	switch kind {
	case ChatLobby:
		return nil, nil
	case ChatPersonal:
		return []string{owner}, nil
	}
	rows, err := s.db.Query(`SELECT user_id FROM project_members WHERE project_id=?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetUser(id string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, login, name FROM users WHERE id=?`, id).Scan(&u.ID, &u.Login, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}
