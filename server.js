'use strict';
// Сайт Владимира: главная с человечком, кейсы, обучалка по ИИ и «Сообщество» — знакомства студентов и предпринимателей.
// Без внешних зависимостей: node:http + встроенный node:sqlite (Node 22.13+).
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { DatabaseSync } = require('node:sqlite');

const PORT = Number(process.env.PORT) || 8080;
const DATA = path.resolve(process.env.DATA_DIR || path.join(__dirname, 'data'));
const PUBLIC = path.join(__dirname, 'public');
const ADMIN_TOKEN = process.env.ADMIN_TOKEN || '';
const SECURE_COOKIES = process.env.SECURE_COOKIES === '1';
const DAILY_CARDS = 10;
const MAX_UPLOAD = 1.5 * 1024 * 1024;

fs.mkdirSync(path.join(DATA, 'photos'), { recursive: true });
fs.mkdirSync(path.join(DATA, 'docs'), { recursive: true });

/* ---------- database ---------- */
const db = new DatabaseSync(path.join(DATA, 'app.db'));
db.exec(`
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  email TEXT UNIQUE NOT NULL,
  pass TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',      -- draft | pending | verified | rejected | banned
  reject_reason TEXT,
  role TEXT,                                  -- student | founder
  name TEXT, org TEXT, about TEXT,
  goals TEXT NOT NULL DEFAULT '[]', interests TEXT NOT NULL DEFAULT '[]',
  project TEXT,                               -- JSON {title, desc, link} или NULL: проект не обязателен
  photo TEXT, doc TEXT,                       -- имена файлов; документ удаляется после проверки
  progress TEXT NOT NULL DEFAULT '[]',
  created INTEGER NOT NULL, verified_at INTEGER
);
CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, user_id INTEGER, admin INTEGER NOT NULL DEFAULT 0, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS swipes (from_id INTEGER NOT NULL, to_id INTEGER NOT NULL, liked INTEGER NOT NULL, at INTEGER NOT NULL, PRIMARY KEY (from_id, to_id));
CREATE TABLE IF NOT EXISTS matches (id INTEGER PRIMARY KEY, a INTEGER NOT NULL, b INTEGER NOT NULL, at INTEGER NOT NULL, UNIQUE (a, b));
CREATE TABLE IF NOT EXISTS messages (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, from_id INTEGER NOT NULL, text TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS blocks (from_id INTEGER NOT NULL, to_id INTEGER NOT NULL, PRIMARY KEY (from_id, to_id));
CREATE TABLE IF NOT EXISTS reports (id INTEGER PRIMARY KEY, from_id INTEGER NOT NULL, to_id INTEGER NOT NULL, reason TEXT NOT NULL, at INTEGER NOT NULL, resolved INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS leads (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, contact TEXT NOT NULL, text TEXT NOT NULL, code TEXT, at INTEGER NOT NULL, done INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS messages_match ON messages (match_id, id);
`);
const q = sql => db.prepare(sql);
const now = () => Date.now();

/* ---------- helpers ---------- */
const GOALS = ['mentor', 'mentee', 'team', 'cofounder', 'internship', 'idea', 'network'];
const ROLES = ['student', 'founder'];

function hashPass(pw) {
  const salt = crypto.randomBytes(16);
  const h = crypto.scryptSync(pw, salt, 32, { N: 16384, r: 8, p: 1 });
  return 'scrypt$' + salt.toString('hex') + '$' + h.toString('hex');
}
function checkPass(pw, stored) {
  const [, salt, hash] = String(stored).split('$');
  if (!salt || !hash) return false;
  const h = crypto.scryptSync(pw, Buffer.from(salt, 'hex'), 32, { N: 16384, r: 8, p: 1 });
  return crypto.timingSafeEqual(h, Buffer.from(hash, 'hex'));
}
const str = (v, max, min = 0) => { if (typeof v !== 'string') return null; v = v.trim(); return v.length >= min && v.length <= max ? v : null; };
const list = (v, allowed, max) => Array.isArray(v) ? [...new Set(v.filter(x => typeof x === 'string' && (!allowed || allowed.includes(x)) && x.length <= 40))].slice(0, max) : [];
const safeLink = v => { const s = str(v, 300); if (!s) return ''; try { const u = new URL(s); return /^https?:$/.test(u.protocol) ? u.href : ''; } catch { return ''; } };

class HttpError extends Error { constructor(code, msg) { super(msg); this.code = code; } }
const fail = (code, msg) => { throw new HttpError(code, msg); };

// simple in-memory rate limit: key -> [timestamps]
const hits = new Map();
function limit(key, max, windowMs) {
  const t = now(), arr = (hits.get(key) || []).filter(x => t - x < windowMs);
  if (arr.length >= max) fail(429, 'Слишком много попыток, попробуйте позже');
  arr.push(t); hits.set(key, arr);
}
setInterval(() => { const t = now(); for (const [k, a] of hits) if (!a.some(x => t - x < 3600e3)) hits.delete(k); q('DELETE FROM sessions WHERE expires < ?').run(t); }, 600e3).unref();

/* ---------- sessions ---------- */
function cookies(req) { const o = {}; for (const p of (req.headers.cookie || '').split(';')) { const i = p.indexOf('='); if (i > 0) o[p.slice(0, i).trim()] = decodeURIComponent(p.slice(i + 1).trim()); } return o; }
// members and the moderator use separate cookies, so signing in to moderation never signs a member out
function session(req, admin = false) {
  const t = cookies(req)[admin ? 'asid' : 'sid']; if (!t || t.length !== 64) return null;
  const s = q('SELECT * FROM sessions WHERE token = ? AND expires > ? AND admin = ?').get(t, now(), admin ? 1 : 0);
  return s || null;
}
function startSession(res, userId, admin = 0) {
  const token = crypto.randomBytes(32).toString('hex'), expires = now() + 30 * 864e5;
  q('INSERT INTO sessions (token, user_id, admin, expires) VALUES (?, ?, ?, ?)').run(token, userId, admin, expires);
  res.setHeader('Set-Cookie', `${admin ? 'asid' : 'sid'}=${token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${30 * 86400}${SECURE_COOKIES ? '; Secure' : ''}`);
}
function currentUser(req) {
  const s = session(req); if (!s || !s.user_id) return null;
  const u = q('SELECT * FROM users WHERE id = ?').get(s.user_id);
  return u && u.status !== 'banned' ? u : null;
}
const needUser = req => currentUser(req) || fail(401, 'Нужно войти');
const needVerified = req => { const u = needUser(req); if (u.status !== 'verified') fail(403, 'Анкета ещё не проверена'); return u; };
const needAdmin = req => { const s = session(req, true); if (!s) fail(401, 'Нужен вход администратора'); return s; };

function publicProfile(u, viewerId) {
  const p = { id: u.id, role: u.role, name: u.name, org: u.org, about: u.about, goals: JSON.parse(u.goals), interests: JSON.parse(u.interests), project: u.project ? JSON.parse(u.project) : null, photo: u.photo ? `/media/photo/${u.id}?v=${u.photo.slice(0, 8)}` : null, verified: u.status === 'verified' };
  if (viewerId === u.id) Object.assign(p, { email: u.email, status: u.status, rejectReason: u.reject_reason, hasDoc: !!u.doc, progress: JSON.parse(u.progress) });
  return p;
}

/* ---------- uploads: images only, sniffed by magic bytes ---------- */
function decodeImage(dataUrl) {
  const m = /^data:image\/(jpeg|png|webp);base64,([A-Za-z0-9+/=]+)$/.exec(String(dataUrl || ''));
  if (!m) fail(400, 'Нужна картинка JPEG, PNG или WebP');
  const buf = Buffer.from(m[2], 'base64');
  if (buf.length > MAX_UPLOAD) fail(413, 'Файл слишком большой');
  const jpg = buf[0] === 0xff && buf[1] === 0xd8, png = buf.slice(0, 4).toString('hex') === '89504e47', webp = buf.slice(0, 4).toString() === 'RIFF' && buf.slice(8, 12).toString() === 'WEBP';
  if (!jpg && !png && !webp) fail(400, 'Файл не похож на картинку');
  return { buf, ext: jpg ? 'jpg' : png ? 'png' : 'webp' };
}
function removeFile(dir, name) { if (name && /^[a-f0-9-]+\.(jpg|png|webp)$/.test(name)) fs.rm(path.join(DATA, dir, name), { force: true }, () => {}); }

/* ---------- API ---------- */
const api = {
  'POST /api/register'(req, body, res) {
    limit('auth:' + req.ip, 10, 15 * 60e3);
    const email = str(body.email, 120, 5), pass = str(body.password, 200, 8);
    if (!email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) fail(400, 'Проверьте почту');
    if (!pass) fail(400, 'Пароль — минимум 8 символов');
    if (q('SELECT 1 FROM users WHERE email = ?').get(email.toLowerCase())) fail(409, 'Такая почта уже зарегистрирована');
    const r = q('INSERT INTO users (email, pass, created) VALUES (?, ?, ?)').run(email.toLowerCase(), hashPass(pass), now());
    startSession(res, Number(r.lastInsertRowid));
    return { ok: true };
  },
  'POST /api/login'(req, body, res) {
    limit('auth:' + req.ip, 10, 15 * 60e3);
    const u = q('SELECT * FROM users WHERE email = ?').get(String(body.email || '').trim().toLowerCase());
    if (!u || !checkPass(String(body.password || ''), u.pass)) fail(401, 'Неверная почта или пароль');
    if (u.status === 'banned') fail(403, 'Аккаунт заблокирован');
    startSession(res, u.id);
    return { ok: true };
  },
  'POST /api/logout'(req, body, res) {
    const s = session(req); if (s) q('DELETE FROM sessions WHERE token = ?').run(s.token);
    res.setHeader('Set-Cookie', 'sid=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0');
    return { ok: true };
  },
  'GET /api/me'(req) { const u = currentUser(req); return { user: u ? publicProfile(u, u.id) : null }; },
  'POST /api/profile'(req, body) {
    const u = needUser(req);
    const role = ROLES.includes(body.role) ? body.role : null, name = str(body.name, 60, 2), org = str(body.org, 120, 2), about = str(body.about, 600) ?? '';
    if (!role || !name || !org) fail(400, 'Заполните роль, имя и место учёбы или работы');
    const goals = list(body.goals, GOALS, 7); if (!goals.length) fail(400, 'Выберите хотя бы одну цель');
    const interests = list(body.interests, null, 8).map(s => s.slice(0, 30));
    let project = null;
    if (body.project && str(body.project.title, 80, 2)) project = JSON.stringify({ title: str(body.project.title, 80), desc: str(body.project.desc, 400) ?? '', link: safeLink(body.project.link) });
    // правка анкеты после проверки отправляет её на повторную проверку только если меняется имя или роль
    const reverify = u.status === 'verified' && (u.name !== name || u.role !== role);
    q('UPDATE users SET role=?, name=?, org=?, about=?, goals=?, interests=?, project=?, status=? WHERE id=?')
      .run(role, name, org, about, JSON.stringify(goals), JSON.stringify(interests), project, reverify ? 'pending' : u.status, u.id);
    return { ok: true };
  },
  'POST /api/upload'(req, body) {
    const u = needUser(req); limit('up:' + u.id, 20, 60 * 60e3);
    const kind = body.kind === 'doc' ? 'doc' : 'photo', { buf, ext } = decodeImage(body.data);
    const name = crypto.randomUUID() + '.' + ext;
    fs.writeFileSync(path.join(DATA, kind === 'doc' ? 'docs' : 'photos', name), buf, { mode: 0o600 });
    if (kind === 'doc') { removeFile('docs', u.doc); q('UPDATE users SET doc=? WHERE id=?').run(name, u.id); }
    else { removeFile('photos', u.photo); q('UPDATE users SET photo=? WHERE id=?').run(name, u.id); }
    return { ok: true };
  },
  'POST /api/submit'(req) {
    const u = needUser(req);
    if (!u.name || !u.photo || !u.doc) fail(400, 'Нужны анкета, фото и подтверждающее фото с документом');
    if (!['draft', 'rejected'].includes(u.status)) fail(400, 'Анкета уже отправлена');
    q("UPDATE users SET status='pending', reject_reason=NULL WHERE id=?").run(u.id);
    return { ok: true };
  },
  'GET /api/feed'(req) {
    const u = needVerified(req);
    const dayStart = new Date(); dayStart.setHours(0, 0, 0, 0);
    const used = q('SELECT COUNT(*) n FROM swipes WHERE from_id=? AND at>=?').get(u.id, dayStart.getTime()).n;
    const left = Math.max(0, DAILY_CARDS - used);
    if (!left) return { left: 0, cards: [] };
    const rows = q(`SELECT * FROM users WHERE status='verified' AND id<>?
      AND id NOT IN (SELECT to_id FROM swipes WHERE from_id=?)
      AND id NOT IN (SELECT to_id FROM blocks WHERE from_id=?) AND id NOT IN (SELECT from_id FROM blocks WHERE to_id=?)`).all(u.id, u.id, u.id, u.id);
    const mine = new Set(JSON.parse(u.goals)), myInt = new Set(JSON.parse(u.interests).map(s => s.toLowerCase()));
    const pairs = { mentor: 'mentee', mentee: 'mentor' };
    const score = r => { let s = 0; const g = JSON.parse(r.goals); for (const x of g) { if (mine.has(x)) s += 2; if (pairs[x] && mine.has(pairs[x])) s += 3; } for (const i of JSON.parse(r.interests)) if (myInt.has(i.toLowerCase())) s += 1; if (r.role !== u.role) s += 1; return s + Math.random() * 0.5; };
    const cards = rows.map(r => ({ r, s: score(r) })).sort((a, b) => b.s - a.s).slice(0, left).map(({ r }) => {
      const p = publicProfile(r, u.id); p.common = JSON.parse(r.goals).filter(x => mine.has(x) || (pairs[x] && mine.has(pairs[x]))); return p; });
    return { left, cards };
  },
  'POST /api/swipe'(req, body) {
    const u = needVerified(req), to = Number(body.id), liked = body.like ? 1 : 0;
    const t = q("SELECT id FROM users WHERE id=? AND status='verified'").get(to); if (!t || to === u.id) fail(404, 'Нет такого профиля');
    const dayStart = new Date(); dayStart.setHours(0, 0, 0, 0);
    if (q('SELECT COUNT(*) n FROM swipes WHERE from_id=? AND at>=?').get(u.id, dayStart.getTime()).n >= DAILY_CARDS) fail(429, 'На сегодня всё — новые знакомства завтра');
    q('INSERT OR IGNORE INTO swipes (from_id, to_id, liked, at) VALUES (?, ?, ?, ?)').run(u.id, to, liked, now());
    // демо-профили (SEED_DEMO=1) отвечают взаимностью, чтобы можно было увидеть мэтч и чат
    if (liked && process.env.SEED_DEMO === '1' && /@demo\.local$/.test(q('SELECT email FROM users WHERE id=?').get(to).email)) q('INSERT OR IGNORE INTO swipes (from_id, to_id, liked, at) VALUES (?, ?, 1, ?)').run(to, u.id, now());
    if (liked && q('SELECT 1 FROM swipes WHERE from_id=? AND to_id=? AND liked=1').get(to, u.id)) {
      const a = Math.min(u.id, to), b = Math.max(u.id, to);
      q('INSERT OR IGNORE INTO matches (a, b, at) VALUES (?, ?, ?)').run(a, b, now());
      const m = q('SELECT id FROM matches WHERE a=? AND b=?').get(a, b);
      return { match: true, matchId: m.id };
    }
    return { match: false };
  },
  'GET /api/matches'(req) {
    const u = needVerified(req);
    const rows = q(`SELECT m.id mid, m.at, u.* FROM matches m JOIN users u ON u.id = CASE WHEN m.a=? THEN m.b ELSE m.a END
      WHERE (m.a=? OR m.b=?) AND u.status='verified'
      AND u.id NOT IN (SELECT to_id FROM blocks WHERE from_id=?) AND u.id NOT IN (SELECT from_id FROM blocks WHERE to_id=?) ORDER BY m.at DESC`).all(u.id, u.id, u.id, u.id, u.id);
    return { matches: rows.map(r => { const last = q('SELECT text, from_id, at FROM messages WHERE match_id=? ORDER BY id DESC LIMIT 1').get(r.mid); return { id: r.mid, at: r.at, user: publicProfile(r, u.id), last: last ? { text: last.text, mine: last.from_id === u.id, at: last.at } : null }; }) };
  },
  'GET /api/messages'(req, body, res, params) {
    const u = needVerified(req), m = myMatch(u.id, Number(params.get('match')));
    const after = Number(params.get('after')) || 0;
    const rows = q('SELECT id, from_id, text, at FROM messages WHERE match_id=? AND id>? ORDER BY id LIMIT 200').all(m.id, after);
    const other = q('SELECT * FROM users WHERE id=?').get(m.a === u.id ? m.b : m.a);
    return { other: publicProfile(other, u.id), messages: rows.map(r => ({ id: r.id, mine: r.from_id === u.id, text: r.text, at: r.at })) };
  },
  'POST /api/messages'(req, body) {
    const u = needVerified(req), m = myMatch(u.id, Number(body.match));
    limit('msg:' + u.id, 30, 60e3);
    const text = str(body.text, 2000, 1); if (!text) fail(400, 'Пустое сообщение');
    const r = q('INSERT INTO messages (match_id, from_id, text, at) VALUES (?, ?, ?, ?)').run(m.id, u.id, text, now());
    return { id: Number(r.lastInsertRowid) };
  },
  'POST /api/report'(req, body) {
    const u = needUser(req), to = Number(body.id), reason = str(body.reason, 500, 3);
    if (!reason) fail(400, 'Опишите причину'); limit('rep:' + u.id, 10, 60 * 60e3);
    q('INSERT INTO reports (from_id, to_id, reason, at) VALUES (?, ?, ?, ?)').run(u.id, to, reason, now());
    q('INSERT OR IGNORE INTO blocks (from_id, to_id) VALUES (?, ?)').run(u.id, to);
    return { ok: true };
  },
  'POST /api/block'(req, body) { const u = needUser(req); q('INSERT OR IGNORE INTO blocks (from_id, to_id) VALUES (?, ?)').run(u.id, Number(body.id)); return { ok: true }; },
  'POST /api/progress'(req, body) {
    const u = needUser(req), done = list(body.done, null, 100).filter(x => /^[a-z0-9-]{1,20}$/.test(x));
    const merged = [...new Set([...JSON.parse(u.progress), ...done])].slice(0, 100);
    q('UPDATE users SET progress=? WHERE id=?').run(JSON.stringify(merged), u.id);
    return { done: merged };
  },
  'POST /api/lead'(req, body) {
    limit('lead:' + req.ip, 5, 60 * 60e3);
    const kind = ['project', 'learn', 'partner', 'other'].includes(body.kind) ? body.kind : 'other';
    const name = str(body.name, 80, 2), contact = str(body.contact, 120, 3), text = str(body.text, 2000, 5), code = str(body.code, 20) || null;
    if (!name || !contact || !text) fail(400, 'Заполните имя, контакт и пару слов о задаче');
    q('INSERT INTO leads (kind, name, contact, text, code, at) VALUES (?, ?, ?, ?, ?, ?)').run(kind, name, contact, text, code, now());
    return { ok: true };
  },
  /* ---- moderation ---- */
  'POST /api/admin/login'(req, body, res) {
    limit('admin:' + req.ip, 5, 15 * 60e3);
    const t = String(body.token || '');
    if (!ADMIN_TOKEN || t.length !== ADMIN_TOKEN.length || !crypto.timingSafeEqual(Buffer.from(t), Buffer.from(ADMIN_TOKEN))) fail(401, 'Неверный ключ');
    startSession(res, null, 1); return { ok: true };
  },
  'GET /api/admin/queue'(req) {
    needAdmin(req);
    const rows = q("SELECT * FROM users WHERE status='pending' ORDER BY created").all();
    return { queue: rows.map(u => ({ ...publicProfile(u, u.id), doc: u.doc ? `/media/doc/${u.id}?v=${u.doc.slice(0, 8)}` : null, created: u.created })),
      stats: q("SELECT status, COUNT(*) n FROM users GROUP BY status").all() };
  },
  'POST /api/admin/decide'(req, body) {
    needAdmin(req); const u = q('SELECT * FROM users WHERE id=?').get(Number(body.id)); if (!u) fail(404, 'Нет пользователя');
    removeFile('docs', u.doc);   // документ больше не нужен ни в одном из исходов
    if (body.approve) q("UPDATE users SET status='verified', doc=NULL, verified_at=?, reject_reason=NULL WHERE id=?").run(now(), u.id);
    else q("UPDATE users SET status='rejected', doc=NULL, reject_reason=? WHERE id=?").run(str(body.reason, 300) || 'Анкета не прошла проверку', u.id);
    return { ok: true };
  },
  'GET /api/admin/leads'(req) { needAdmin(req); return { leads: q('SELECT * FROM leads ORDER BY done, at DESC LIMIT 200').all() }; },
  'POST /api/admin/lead-done'(req, body) { needAdmin(req); q('UPDATE leads SET done=1 WHERE id=?').run(Number(body.id)); return { ok: true }; },
  'GET /api/admin/reports'(req) {
    needAdmin(req);
    return { reports: q(`SELECT r.id, r.reason, r.at, r.to_id, f.name from_name, t.name to_name, t.status to_status FROM reports r
      JOIN users f ON f.id=r.from_id JOIN users t ON t.id=r.to_id WHERE r.resolved=0 ORDER BY r.at DESC`).all() };
  },
  'POST /api/admin/resolve'(req, body) {
    needAdmin(req); const r = q('SELECT * FROM reports WHERE id=?').get(Number(body.id)); if (!r) fail(404, 'Нет жалобы');
    if (body.ban) { q("UPDATE users SET status='banned' WHERE id=?").run(r.to_id); q('DELETE FROM sessions WHERE user_id=?').run(r.to_id); }
    q('UPDATE reports SET resolved=1 WHERE id=?').run(r.id); return { ok: true };
  }
};
function myMatch(uid, id) { const m = q('SELECT * FROM matches WHERE id=?').get(id); if (!m || (m.a !== uid && m.b !== uid)) fail(404, 'Нет такого мэтча'); return m; }

/* ---------- media: photos only to signed-in members, documents only to the admin ---------- */
function media(req, res, kind, id) {
  const u = q('SELECT * FROM users WHERE id=?').get(id); const s = session(req, true);
  const file = u && (kind === 'doc' ? u.doc : u.photo);
  if (!file) return send(res, 404, 'Not found');
  if (kind === 'doc') { if (!s) return send(res, 403, 'Forbidden'); }
  else { const me = currentUser(req); const ok = !!s || (me && (me.id === u.id || me.status === 'verified') && u.status !== 'banned'); if (!ok) return send(res, 403, 'Forbidden'); }
  const ext = path.extname(file).slice(1);
  res.writeHead(200, { 'Content-Type': ext === 'jpg' ? 'image/jpeg' : 'image/' + ext, 'Cache-Control': 'private, max-age=3600' });
  fs.createReadStream(path.join(DATA, kind === 'doc' ? 'docs' : 'photos', file)).pipe(res);
}

/* ---------- static ---------- */
const TYPES = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg', '.ico': 'image/x-icon', '.webmanifest': 'application/manifest+json' };
const PAGES = { '/': 'index.html', '/cases': 'cases.html', '/learn': 'learn.html', '/app': 'app.html', '/admin': 'admin.html' };
function serveStatic(req, res, pathname) {
  const rel = PAGES[pathname] || pathname.slice(1);
  const file = path.normalize(path.join(PUBLIC, rel));
  if (!file.startsWith(PUBLIC + path.sep)) return send(res, 404, 'Not found');
  fs.stat(file, (err, st) => {
    if (err || !st.isFile()) return send(res, 404, 'Not found');
    const type = TYPES[path.extname(file)] || 'application/octet-stream';
    res.writeHead(200, { 'Content-Type': type, 'Cache-Control': type.startsWith('text/html') ? 'no-cache' : 'public, max-age=3600' });
    fs.createReadStream(file).pipe(res);
  });
}

function send(res, code, body, type = 'text/plain; charset=utf-8') { res.writeHead(code, { 'Content-Type': type }); res.end(body); }
const SECURITY_HEADERS = {
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'same-origin',
  'X-Frame-Options': 'SAMEORIGIN',
  'Permissions-Policy': 'camera=(self), microphone=(), geolocation=()',
  'Content-Security-Policy': "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data: blob:; media-src 'self' data:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'self'"
};

const server = http.createServer((req, res) => {
  for (const [k, v] of Object.entries(SECURITY_HEADERS)) res.setHeader(k, v);
  req.ip = (process.env.TRUST_PROXY === '1' && String(req.headers['x-forwarded-for'] || '').split(',')[0].trim()) || req.socket.remoteAddress;
  const url = new URL(req.url, 'http://x'), pathname = decodeURIComponent(url.pathname);
  const mm = /^\/media\/(photo|doc)\/(\d+)$/.exec(pathname);
  if (req.method === 'GET' && mm) return media(req, res, mm[1], Number(mm[2]));
  if (!pathname.startsWith('/api/')) return req.method === 'GET' || req.method === 'HEAD' ? serveStatic(req, res, pathname) : send(res, 405, 'Method not allowed');
  const handler = api[req.method + ' ' + pathname];
  if (!handler) return send(res, 404, JSON.stringify({ error: 'Not found' }), 'application/json');
  // CSRF: state-changing requests must come from our own pages
  if (req.method === 'POST') {
    const origin = req.headers.origin, host = req.headers.host;
    if (req.headers['x-requested-with'] !== 'fetch' || (origin && new URL(origin).host !== host)) return send(res, 403, JSON.stringify({ error: 'Bad origin' }), 'application/json');
  }
  let size = 0; const chunks = [];
  req.on('data', c => { size += c.length; if (size > MAX_UPLOAD * 1.4 + 4096) { req.destroy(); } else chunks.push(c); });
  req.on('end', () => {
    try {
      let body = {};
      if (chunks.length) { try { body = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { fail(400, 'Bad JSON'); } }
      if (typeof body !== 'object' || body === null) body = {};
      const out = handler(req, body, res, url.searchParams);
      send(res, 200, JSON.stringify(out), 'application/json; charset=utf-8');
    } catch (e) {
      if (!(e instanceof HttpError)) console.error(e);
      send(res, e.code || 500, JSON.stringify({ error: e instanceof HttpError ? e.message : 'Ошибка сервера' }), 'application/json; charset=utf-8');
    }
  });
});

/* ---------- demo data for local testing: SEED_DEMO=1 ---------- */
if (process.env.SEED_DEMO === '1' && !q('SELECT 1 FROM users LIMIT 1').get()) {
  const demo = [
    ['founder', 'Игорь Петров', 'Основатель сервиса доставки, СПб', 'Пять лет строю бизнес. Хочу помогать студентам и искать с ними новые идеи.', ['mentor', 'team', 'idea'], ['ИИ в логистике', 'продажи']],
    ['student', 'Мария Козлова', 'СПбГУ, 4 курс, прикладная математика', 'Пишу диплом про рекомендательные системы. Ищу команду на хакатон.', ['team', 'mentee', 'internship'], ['ML', 'Python', 'хакатоны']],
    ['founder', 'Олег Смирнов', 'Сеть кофеен «Зерно»', 'Хочу автоматизировать заказы и маркетинг с помощью ИИ, ищу толковых ребят.', ['internship', 'idea', 'network'], ['маркетинг', 'ИИ-боты']],
    ['student', 'Дмитрий Орлов', 'ИТМО, 2 курс', 'Делаю Telegram-ботов, хочу в стартап сооснователем.', ['cofounder', 'mentee', 'team'], ['боты', 'стартапы', 'Python']]
  ];
  for (const [role, name, org, about, goals, interests] of demo) {
    q("INSERT INTO users (email, pass, status, role, name, org, about, goals, interests, created, verified_at) VALUES (?, ?, 'verified', ?, ?, ?, ?, ?, ?, ?, ?)")
      .run(name.split(' ')[0].toLowerCase() + '@demo.local', hashPass(crypto.randomBytes(12).toString('hex')), role, name, org, about, JSON.stringify(goals), JSON.stringify(interests), now(), now());
  }
  console.log('Demo profiles created');
}

server.listen(PORT, () => console.log(`http://localhost:${PORT}` + (ADMIN_TOKEN ? '' : '  (ADMIN_TOKEN не задан — модерация отключена)')));
