// Демо-режим: тот же API, что у server.js, но в браузере, на localStorage. Нужен, чтобы открыть MVP по ссылке без сервера.
// Модератор «одобряет» анкету сам через 8 секунд; демо-участники отвечают взаимностью и на сообщения.
(() => {
  const KEY = 'demo:db:v1', GOALS = ['mentor', 'mentee', 'team', 'cofounder', 'internship', 'idea', 'network'];
  const now = () => Date.now();
  const seed = () => ({
    seq: 100, me: null, admin: false,
    users: [
      ['founder', 'Игорь Петров', 'Основатель сервиса доставки, СПб', 'Пять лет строю бизнес. Хочу помогать студентам и искать с ними новые идеи.', ['mentor', 'team', 'idea'], ['ИИ в логистике', 'продажи']],
      ['student', 'Мария Козлова', 'СПбГУ, 4 курс, прикладная математика', 'Пишу диплом про рекомендательные системы. Ищу команду на хакатон.', ['team', 'mentee', 'internship'], ['ML', 'Python', 'хакатоны']],
      ['founder', 'Олег Смирнов', 'Сеть кофеен «Зерно»', 'Хочу автоматизировать заказы и маркетинг с помощью ИИ, ищу толковых ребят.', ['internship', 'idea', 'network'], ['маркетинг', 'ИИ-боты']],
      ['student', 'Дмитрий Орлов', 'ИТМО, 2 курс', 'Делаю Telegram-ботов, хочу в стартап сооснователем.', ['cofounder', 'mentee', 'team'], ['боты', 'стартапы', 'Python']]
    ].map(([role, name, org, about, goals, interests], i) => ({ id: i + 1, demo: true, email: 'demo' + i, pass: '', status: 'verified', role, name, org, about, goals, interests, project: null, photo: null, doc: null, progress: [], created: now() })),
    swipes: [], matches: [], messages: [], blocks: [], reports: [], leads: []
  });
  let db; try { db = JSON.parse(localStorage.getItem(KEY)) || seed(); } catch (_) { db = seed(); }
  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(db)); } catch (_) { /* quota: photos are large */ } };
  const id = () => ++db.seq;
  class E extends Error { constructor(c, m) { super(m); this.code = c; } }
  const fail = (c, m) => { throw new E(c, m); };
  const meU = () => db.users.find(u => u.id === db.me) || null;
  const needUser = () => meU() || fail(401, 'Нужно войти');
  const needVerified = () => { const u = needUser(); if (u.status !== 'verified') fail(403, 'Анкета ещё не проверена'); return u; };
  const needAdmin = () => db.admin || fail(401, 'Нужен вход администратора');
  const pub = (u, viewer) => { const p = { id: u.id, role: u.role, name: u.name, org: u.org, about: u.about, goals: u.goals, interests: u.interests, project: u.project, photo: u.photo, verified: u.status === 'verified' };
    if (viewer === u.id) Object.assign(p, { email: u.email, status: u.status, rejectReason: u.reject, hasDoc: !!u.doc, progress: u.progress }); return p; };
  const blocked = (a, b) => db.blocks.some(x => (x.f === a && x.t === b) || (x.f === b && x.t === a));
  const today = () => { const d = new Date(); d.setHours(0, 0, 0, 0); return d.getTime(); };
  const tick = () => { const u = meU(); if (u && u.status === 'pending' && u.pendingAt && now() - u.pendingAt > 8000) { u.status = 'verified'; u.doc = null; save(); } };
  const pairs = { mentor: 'mentee', mentee: 'mentor' };
  const REPLIES = ['Привет! Рад знакомству 🙂', 'Давай созвонимся — завтра в 18:00 удобно?', 'Звучит интересно, расскажи подробнее!', 'Отличная идея, я в деле.'];

  const api = {
    'POST /api/register'(b) { const email = String(b.email || '').trim().toLowerCase(); if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) fail(400, 'Проверьте почту'); if (String(b.password || '').length < 8) fail(400, 'Пароль — минимум 8 символов'); if (db.users.some(u => u.email === email)) fail(409, 'Такая почта уже зарегистрирована');
      const u = { id: id(), email, pass: String(b.password), status: 'draft', role: null, name: null, org: null, about: '', goals: [], interests: [], project: null, photo: null, doc: null, progress: [], created: now() }; db.users.push(u); db.me = u.id; return { ok: true }; },
    'POST /api/login'(b) { const u = db.users.find(x => x.email === String(b.email || '').trim().toLowerCase() && x.pass === String(b.password || '')); if (!u) fail(401, 'Неверная почта или пароль'); db.me = u.id; return { ok: true }; },
    'POST /api/logout'() { db.me = null; return { ok: true }; },
    'GET /api/me'() { tick(); const u = meU(); return { user: u ? pub(u, u.id) : null }; },
    'POST /api/profile'(b) { const u = needUser(); if (!['student', 'founder'].includes(b.role) || !String(b.name || '').trim() || !String(b.org || '').trim()) fail(400, 'Заполните роль, имя и место учёбы или работы');
      const goals = (b.goals || []).filter(g => GOALS.includes(g)); if (!goals.length) fail(400, 'Выберите хотя бы одну цель');
      Object.assign(u, { role: b.role, name: String(b.name).trim().slice(0, 60), org: String(b.org).trim().slice(0, 120), about: String(b.about || '').slice(0, 600), goals, interests: (b.interests || []).slice(0, 8), project: b.project && b.project.title ? b.project : null }); return { ok: true }; },
    'POST /api/upload'(b) { const u = needUser(); if (!/^data:image\//.test(b.data || '')) fail(400, 'Нужна картинка'); if (b.kind === 'doc') u.doc = 'demo'; else u.photo = b.data; return { ok: true }; },
    'POST /api/submit'() { const u = needUser(); if (!u.name || !u.photo || !u.doc) fail(400, 'Нужны анкета, фото и подтверждающее фото с документом'); u.status = 'pending'; u.pendingAt = now(); u.reject = null; return { ok: true }; },
    'GET /api/feed'() { const u = needVerified(); const used = db.swipes.filter(s => s.f === u.id && s.at >= today()).length, left = Math.max(0, 10 - used);
      const mine = new Set(u.goals);
      const cards = db.users.filter(x => x.status === 'verified' && x.id !== u.id && !db.swipes.some(s => s.f === u.id && s.t === x.id) && !blocked(u.id, x.id))
        .map(x => { let s = 0; for (const g of x.goals) { if (mine.has(g)) s += 2; if (pairs[g] && mine.has(pairs[g])) s += 3; } return { x, s }; }).sort((a, b) => b.s - a.s).slice(0, left)
        .map(({ x }) => ({ ...pub(x, u.id), common: x.goals.filter(g => mine.has(g) || (pairs[g] && mine.has(pairs[g]))) }));
      return { left, cards }; },
    'POST /api/swipe'(b) { const u = needVerified(), to = Number(b.id); db.swipes.push({ f: u.id, t: to, like: !!b.like, at: now() });
      const other = db.users.find(x => x.id === to);
      if (b.like && other && (other.demo || db.swipes.some(s => s.f === to && s.t === u.id && s.like))) { let m = db.matches.find(m => (m.a === u.id && m.b === to) || (m.a === to && m.b === u.id)); if (!m) { m = { id: id(), a: u.id, b: to, at: now() }; db.matches.push(m); } return { match: true, matchId: m.id }; }
      return { match: false }; },
    'GET /api/matches'() { const u = needVerified();
      return { matches: db.matches.filter(m => m.a === u.id || m.b === u.id).map(m => { const o = db.users.find(x => x.id === (m.a === u.id ? m.b : m.a)); if (!o || blocked(u.id, o.id)) return null; const ms = db.messages.filter(x => x.m === m.id), last = ms[ms.length - 1];
        return { id: m.id, at: m.at, user: pub(o, u.id), last: last ? { text: last.text, mine: last.f === u.id, at: last.at } : null }; }).filter(Boolean).reverse() }; },
    'GET /api/messages'(b, q) { const u = needVerified(), m = db.matches.find(x => x.id === Number(q.get('match')) && (x.a === u.id || x.b === u.id)) || fail(404, 'Нет такого мэтча');
      const after = Number(q.get('after')) || 0, o = db.users.find(x => x.id === (m.a === u.id ? m.b : m.a));
      return { other: pub(o, u.id), messages: db.messages.filter(x => x.m === m.id && x.id > after).map(x => ({ id: x.id, mine: x.f === u.id, text: x.text, at: x.at })) }; },
    'POST /api/messages'(b) { const u = needVerified(), m = db.matches.find(x => x.id === Number(b.match) && (x.a === u.id || x.b === u.id)) || fail(404, 'Нет такого мэтча');
      const text = String(b.text || '').trim().slice(0, 2000); if (!text) fail(400, 'Пустое сообщение');
      db.messages.push({ id: id(), m: m.id, f: u.id, text, at: now() });
      const o = db.users.find(x => x.id === (m.a === u.id ? m.b : m.a));
      if (o && o.demo) setTimeout(() => { const n = db.messages.filter(x => x.m === m.id && x.f === o.id).length; db.messages.push({ id: id(), m: m.id, f: o.id, text: REPLIES[n % REPLIES.length], at: now() }); save(); }, 2500);
      return { id: db.seq }; },
    'POST /api/report'(b) { const u = needUser(); if (String(b.reason || '').trim().length < 3) fail(400, 'Опишите причину'); db.reports.push({ id: id(), f: u.id, t: Number(b.id), reason: String(b.reason), at: now(), done: false }); db.blocks.push({ f: u.id, t: Number(b.id) }); return { ok: true }; },
    'POST /api/block'(b) { const u = needUser(); db.blocks.push({ f: u.id, t: Number(b.id) }); return { ok: true }; },
    'POST /api/progress'(b) { const u = needUser(); u.progress = [...new Set([...(u.progress || []), ...(b.done || [])])]; return { done: u.progress }; },
    'POST /api/lead'(b) { if (!String(b.name || '').trim() || !String(b.contact || '').trim() || String(b.text || '').trim().length < 5) fail(400, 'Заполните имя, контакт и пару слов о задаче');
      db.leads.push({ id: id(), kind: b.kind, name: b.name, contact: b.contact, text: b.text, code: b.code || null, at: now(), done: 0 }); return { ok: true }; },
    'POST /api/admin/login'(b) { if (String(b.token) !== 'demo') fail(401, 'Неверный ключ (в демо: demo)'); db.admin = true; return { ok: true }; },
    'GET /api/admin/queue'() { needAdmin(); const stats = {}; for (const u of db.users) stats[u.status] = (stats[u.status] || 0) + 1;
      return { queue: db.users.filter(u => u.status === 'pending').map(u => ({ ...pub(u, u.id), doc: u.photo, created: u.created })), stats: Object.entries(stats).map(([status, n]) => ({ status, n })) }; },
    'POST /api/admin/decide'(b) { needAdmin(); const u = db.users.find(x => x.id === Number(b.id)) || fail(404, 'Нет пользователя'); u.doc = null; if (b.approve) u.status = 'verified'; else { u.status = 'rejected'; u.reject = b.reason || 'Анкета не прошла проверку'; } return { ok: true }; },
    'GET /api/admin/reports'() { needAdmin(); return { reports: db.reports.filter(r => !r.done).map(r => ({ id: r.id, reason: r.reason, at: r.at, to_id: r.t, from_name: (db.users.find(x => x.id === r.f) || {}).name, to_name: (db.users.find(x => x.id === r.t) || {}).name })) }; },
    'POST /api/admin/resolve'(b) { needAdmin(); const r = db.reports.find(x => x.id === Number(b.id)); if (r) { r.done = true; if (b.ban) { const u = db.users.find(x => x.id === r.t); if (u) u.status = 'banned'; } } return { ok: true }; },
    'GET /api/admin/leads'() { needAdmin(); return { leads: [...db.leads].reverse() }; },
    'POST /api/admin/lead-done'(b) { needAdmin(); const l = db.leads.find(x => x.id === Number(b.id)); if (l) l.done = 1; return { ok: true }; }
  };

  const realFetch = window.fetch.bind(window);
  window.fetch = async (input, opts = {}) => {
    const url = typeof input === 'string' ? input : input.url;
    if (!/^\/api\//.test(url)) return realFetch(input, opts);
    const u = new URL(url, 'http://demo'), method = (opts.method || 'GET').toUpperCase(), h = api[method + ' ' + u.pathname];
    await new Promise(r => setTimeout(r, 120));
    let status = 200, body;
    try { if (!h) fail(404, 'Not found'); body = h(opts.body ? JSON.parse(opts.body) : {}, u.searchParams); save(); }
    catch (e) { status = e.code || 500; body = { error: e.message }; }
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  };
  window.__demoReset = () => { localStorage.removeItem(KEY); location.reload(); };
})();
