// «Сообщество»: регистрация → анкета → фото и проверка → лента → мэтч → чат.
// Человечек появляется только в эмоциональных местах: приветствие, ожидание проверки, пустая лента, мэтч.
(() => {
  'use strict';
  const GOALS = { mentor: 'Готов менторить', mentee: 'Ищу ментора', team: 'Ищу команду', cofounder: 'Ищу сооснователя', internship: 'Стажировка', idea: 'Обсудить идею', network: 'Просто познакомиться' };
  const view = document.getElementById('view'), tabs = document.getElementById('tabs'), dlg = document.getElementById('dlg');
  let me = null, chatTimer = null;

  /* ---------- helpers ---------- */
  const api = async (url, body) => {
    const r = await fetch(url, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'fetch' }, body: JSON.stringify(body) });
    let j = {}; try { j = await r.json(); } catch (_) {}
    if (!r.ok) throw new Error(j.error || 'Что-то пошло не так');
    return j;
  };
  const el = (tag, props = {}, ...kids) => {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (v == null || v === false) continue;
      if (k === 'class') e.className = v; else if (k === 'text') e.textContent = v; else if (k.startsWith('on')) e.addEventListener(k.slice(2), v); else e.setAttribute(k, v === true ? '' : v);
    }
    for (const c of kids.flat()) if (c != null && c !== false) e.append(c.nodeType ? c : document.createTextNode(String(c)));
    return e;
  };
  const svgNS = 'http://www.w3.org/2000/svg';
  const figure = (paths, w = 130, h = 170) => { const s = document.createElementNS(svgNS, 'svg'); s.setAttribute('class', 'fig'); s.setAttribute('width', w); s.setAttribute('height', h); s.setAttribute('viewBox', '0 0 100 130'); s.setAttribute('aria-hidden', 'true'); s.innerHTML = '<g>' + paths + '</g>'; return s; };
  // the stick figure's poses (static markup, no user data)
  const POSE = {
    wave: '<circle cx="50" cy="24" r="12" fill="#F6F3EC"/><path d="M45 22v1M55 22v1"/><path d="M44.5 28q5.5 5 11 0"/><path d="M50 36v42"/><path d="M50 46l-16 14"/><path d="M50 46l14-10l6-16"/><path d="M50 78l-12 38M50 78l12 38"/><path d="M74 14q3 3 0 7M79 10q5 7 0 14" stroke="#FF5B2E" stroke-width="2"/>',
    lens: '<circle cx="40" cy="26" r="12" fill="#F6F3EC"/><path d="M35 24h3M43 24h3"/><path d="M37 31h6"/><path d="M40 38l4 40"/><path d="M41 50l20-6l14 2"/><path d="M41 50l-12 14l-4 12"/><path d="M44 78l-12 38M44 78l12 38"/><circle cx="86" cy="44" r="9" fill="rgba(255,255,255,.6)" stroke-width="2.4"/><path d="M79 50l-5 5" stroke-width="3"/>',
    sleep: '<circle cx="50" cy="28" r="12" fill="#F6F3EC"/><path d="M44 27h4M52 27h4"/><path d="M47 33h6"/><path d="M50 40v38"/><path d="M50 50l-14 10l-2 12M50 50l14 10l2 12"/><path d="M50 78l-12 38M50 78l12 38"/><path d="M66 10h8l-8 8h8M78 0h6l-6 6h6" stroke="#55524B" stroke-width="2"/>',
    joy: '<circle cx="50" cy="22" r="12" fill="#F6F3EC"/><path d="M45 19q1.5-2 3 0M52 19q1.5-2 3 0"/><path d="M44 26q6 7 12 0"/><path d="M50 34v38"/><path d="M50 44l-16-16l-4-12M50 44l16-16l4-12"/><path d="M50 72l-12 34M50 72l12 34"/><path d="M18 4l-4-6M84 4l4-6M10 22h-8M90 22h8" stroke="#FF5B2E" stroke-width="2.4"/>'
  };
  const say = (text, rot = -1) => el('div', { class: 'say', style: `transform:rotate(${rot}deg)` }, text);
  const initials = n => (n || '?').split(/\s+/).map(w => w[0]).slice(0, 2).join('').toUpperCase();
  const avatar = (u, cls = 'avatar') => el('div', { class: cls }, u.photo ? el('img', { src: u.photo, alt: '' }) : initials(u.name));
  const errBox = () => el('p', { class: 'err', role: 'alert' });
  const steps = n => el('div', { class: 'steps', 'aria-label': `Шаг ${n} из 3` }, [1, 2, 3].map(i => el('i', { class: i <= n ? 'on' : '' })));
  function show(...nodes) { clearInterval(chatTimer); document.querySelector('.composer')?.remove(); view.replaceChildren(el('div', { class: 'screen' }, ...nodes)); view.focus({ preventScroll: true }); scrollTo({ top: 0 }); }
  function setTabs(active) { tabs.hidden = !(me && me.status === 'verified'); for (const a of tabs.querySelectorAll('a')) { if (a.dataset.tab === active) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); } }
  async function refreshMe() { me = (await api('/api/me')).user; return me; }
  const go = h => { if (location.hash === h) route(); else location.hash = h; };

  // resize on the client: smaller upload and strips EXIF (location, device) from photos
  function toJpeg(file, max = 1000) {
    return new Promise((res, rej) => {
      if (!/^image\//.test(file.type)) return rej(new Error('Нужна картинка'));
      const img = new Image(), url = URL.createObjectURL(file);
      img.onload = () => { const k = Math.min(1, max / Math.max(img.width, img.height)); const c = document.createElement('canvas'); c.width = Math.round(img.width * k); c.height = Math.round(img.height * k); c.getContext('2d').drawImage(img, 0, 0, c.width, c.height); URL.revokeObjectURL(url); res(c.toDataURL('image/jpeg', 0.85)); };
      img.onerror = () => { URL.revokeObjectURL(url); rej(new Error('Не получилось открыть картинку')); };
      img.src = url;
    });
  }

  /* ---------- screens ---------- */
  function welcome() {
    setTabs(null);
    show(el('div', { class: 'screen center', style: 'padding-top:16px' },
      say('Привет! Здесь знакомятся студенты и предприниматели. Я буду рядом в важные моменты.'),
      figure(POSE.wave, 150, 190),
      el('h1', { style: 'font-size:32px', text: 'Знакомства, а не фриланс' }),
      el('p', { class: 'muted', style: 'max-width:380px', text: 'Найдите ментора, команду, сооснователя или просто интересного собеседника. Проект добавлять не обязательно. Каждую анкету проверяет человек.' }),
      el('a', { class: 'btn p', href: '#/register', style: 'width:100%', text: 'Присоединиться' }),
      el('a', { class: 'btn ghost', href: '#/login', style: 'width:100%', text: 'У меня уже есть аккаунт' }),
      el('a', { href: '/learn', class: 'hand', style: 'font-size:22px;color:var(--soft)', text: 'или сначала пройти обучалку по ИИ →' })));
  }

  function authForm(mode) {
    setTabs(null);
    const err = errBox();
    const f = el('form', { class: 'screen', novalidate: true },
      el('h1', { style: 'font-size:30px', text: mode === 'register' ? 'Регистрация' : 'Вход' }),
      el('label', { class: 'f' }, 'Почта', el('input', { type: 'email', name: 'email', autocomplete: 'email', required: true })),
      el('label', { class: 'f' }, 'Пароль', el('input', { type: 'password', name: 'password', autocomplete: mode === 'register' ? 'new-password' : 'current-password', minlength: '8', required: true })),
      mode === 'register' ? el('label', { class: 'row', style: 'font-size:14px;align-items:flex-start' }, el('input', { type: 'checkbox', name: 'consent', style: 'width:20px;height:20px;margin-top:2px' }), el('span', {}, 'Согласен на обработку персональных данных для работы сервиса. Документ для проверки удаляется сразу после неё.')) : null,
      err,
      el('button', { class: 'btn p', type: 'submit', text: mode === 'register' ? 'Создать аккаунт' : 'Войти' }),
      el('a', { href: mode === 'register' ? '#/login' : '#/register', style: 'text-align:center', text: mode === 'register' ? 'Уже есть аккаунт? Войти' : 'Нет аккаунта? Зарегистрироваться' }));
    f.addEventListener('submit', async e => {
      e.preventDefault(); err.textContent = '';
      if (mode === 'register' && !f.consent.checked) { err.textContent = 'Нужно согласие на обработку данных'; return; }
      try { await api('/api/' + mode, { email: f.email.value, password: f.password.value }); await refreshMe(); go(nextFor(me)); } catch (x) { err.textContent = x.message; }
    });
    show(f);
  }

  function profileForm(editing) {
    setTabs(editing ? 'me' : null);
    const u = me, goals = new Set(u.goals || []); let role = u.role || 'student';
    const err = errBox();
    const roleBtns = [['student', 'Студент'], ['founder', 'Предприниматель']].map(([v, t]) => el('button', { type: 'button', class: 'chip', 'aria-pressed': String(role === v), style: 'flex:1;justify-content:center;min-height:48px', text: t, onclick: () => { role = v; roleBtns.forEach((b, i) => b.setAttribute('aria-pressed', String(['student', 'founder'][i] === v))); org.querySelector('span').textContent = v === 'student' ? 'Вуз, курс, направление' : 'Компания и чем занимаетесь'; } }));
    const org = el('label', { class: 'f' }, el('span', { text: role === 'student' ? 'Вуз, курс, направление' : 'Компания и чем занимаетесь' }), el('input', { type: 'text', name: 'org', maxlength: '120', value: u.org || '' }));
    const goalChips = Object.entries(GOALS).map(([k, t]) => el('button', { type: 'button', class: 'chip', 'aria-pressed': String(goals.has(k)), text: t, onclick: e => { goals.has(k) ? goals.delete(k) : goals.add(k); e.currentTarget.setAttribute('aria-pressed', String(goals.has(k))); } }));
    const p = u.project || {};
    const proj = el('details', { open: u.project ? true : null }, el('summary', { style: 'cursor:pointer;font-weight:600', text: 'Добавить проект (необязательно)' }),
      el('div', { class: 'screen', style: 'margin-top:12px' },
        el('label', { class: 'f' }, 'Название', el('input', { type: 'text', name: 'ptitle', maxlength: '80', value: p.title || '' })),
        el('label', { class: 'f' }, 'Пара слов о проекте', el('textarea', { name: 'pdesc', maxlength: '400' }, p.desc || '')),
        el('label', { class: 'f' }, 'Ссылка', el('input', { type: 'url', name: 'plink', maxlength: '300', value: p.link || '', placeholder: 'https://' }))));
    const f = el('form', { class: 'screen', novalidate: true },
      editing ? el('a', { href: '#/me', text: '← Профиль' }) : steps(1),
      el('h1', { style: 'font-size:28px', text: editing ? 'Анкета' : 'Расскажите о себе' }),
      el('div', { class: 'row' }, roleBtns),
      el('label', { class: 'f' }, 'Имя и фамилия', el('input', { type: 'text', name: 'name', maxlength: '60', autocomplete: 'name', value: u.name || '' })),
      org,
      el('div', { class: 'screen', style: 'gap:8px' }, el('span', { style: 'font-size:14px;font-weight:600;color:var(--soft)', text: 'Что ищете (можно несколько)' }), el('div', { class: 'chips' }, goalChips)),
      el('label', { class: 'f' }, 'Интересы через запятую', el('input', { type: 'text', name: 'interests', maxlength: '200', value: (u.interests || []).join(', '), placeholder: 'ИИ, маркетинг, стартапы' })),
      el('label', { class: 'f' }, 'О себе', el('textarea', { name: 'about', maxlength: '600', placeholder: 'Чем занимаетесь, что вам интересно, чем можете помочь' }, u.about || '')),
      proj, err,
      el('button', { class: 'btn p', type: 'submit', text: editing ? 'Сохранить' : 'Дальше' }));
    f.addEventListener('submit', async e => {
      e.preventDefault(); err.textContent = '';
      try {
        await api('/api/profile', { role, name: f.name.value, org: f.org.value, goals: [...goals], interests: f.interests.value.split(',').map(s => s.trim()).filter(Boolean), about: f.about.value, project: f.ptitle.value.trim() ? { title: f.ptitle.value, desc: f.pdesc.value, link: f.plink.value } : null });
        await refreshMe(); go(editing ? '#/me' : nextFor(me));
      } catch (x) { err.textContent = x.message; }
    });
    show(f);
  }

  function verifyForm() {
    setTabs(null);
    const err = errBox();
    const slot = (kind, title, hint) => {
      const prev = kind === 'photo' && me.photo ? el('img', { src: me.photo, alt: '' }) : null;
      const input = el('input', { type: 'file', accept: 'image/*', ...(kind === 'doc' ? { capture: 'user' } : {}) });
      const lab = el('label', {}, prev, el('span', { text: title }), el('span', { class: 'muted', style: 'font-weight:400;font-size:12px', text: hint }), input);
      if (kind === 'doc' && me.hasDoc) lab.append(el('span', { class: 'badge-ok', text: '✓ загружено' }));
      input.addEventListener('change', async () => {
        const file = input.files[0]; if (!file) return; err.textContent = '';
        try {
          const data = await toJpeg(file); await api('/api/upload', { kind, data });
          lab.querySelector('img')?.remove(); lab.prepend(el('img', { src: data, alt: '' })); await refreshMe(); check();
        } catch (x) { err.textContent = x.message; }
      });
      return lab;
    };
    const submit = el('button', { class: 'btn p', type: 'button', text: 'Отправить на проверку', onclick: async () => { try { await api('/api/submit', {}); await refreshMe(); go('#/pending'); } catch (x) { err.textContent = x.message; } } });
    const check = () => { submit.disabled = !(me.photo && me.hasDoc); };
    show(steps(2), el('h1', { style: 'font-size:28px', text: 'Фото и подтверждение' }),
      el('p', { class: 'muted', text: 'Здесь только реальные люди. Фото профиля увидят участники. Второе фото — селфи со студенческим или паспортом (данные можно прикрыть, кроме имени и фото).' }),
      el('div', { class: 'upl' }, slot('photo', 'Фото профиля', 'лицо хорошо видно'), slot('doc', 'Селфи с документом', 'видит только модератор')),
      el('div', { class: 'note' }, 'Документ хранится отдельно, его видит только модератор, и он удаляется сразу после проверки. В профиле остаётся только галочка ✓.'),
      err, submit, el('button', { class: 'link-btn', type: 'button', text: '← Изменить анкету', onclick: () => go('#/profile') }));
    check();
  }

  function pending() {
    setTabs(null);
    if (me.status === 'rejected') {
      show(el('div', { class: 'screen center' }, figure(POSE.lens), el('h1', { style: 'font-size:28px', text: 'Нужно кое-что поправить' }),
        el('div', { class: 'card flat', style: 'text-align:left' }, el('b', { text: 'Комментарий модератора: ' }), me.rejectReason || ''),
        el('a', { class: 'btn p', href: '#/profile', style: 'width:100%', text: 'Исправить анкету' }), el('a', { class: 'btn', href: '#/verify', style: 'width:100%', text: 'Загрузить фото заново' })));
      return;
    }
    show(el('div', { class: 'screen center', style: 'padding-top:12px' },
      steps(3), figure(POSE.lens, 160, 200),
      el('h1', { style: 'font-size:28px', text: 'Анкета на проверке' }),
      el('p', { class: 'muted', text: 'Обычно это занимает до 24 часов. Страница обновится сама, как только всё будет готово.' }),
      say('Пока ждём — может, пройдёте обучалку по ИИ?', 1),
      el('a', { class: 'btn', href: '/learn', style: 'width:100%', text: 'Открыть обучалку' }),
      el('button', { class: 'link-btn', type: 'button', text: 'Изменить анкету', onclick: () => go('#/profile') })));
    clearInterval(chatTimer);
    chatTimer = setInterval(async () => { try { await refreshMe(); if (me.status !== 'pending') go(nextFor(me)); } catch (_) {} }, 15000);
  }

  async function feed() {
    setTabs('feed');
    let data; try { data = await api('/api/feed'); } catch (x) { return show(el('p', { class: 'err', text: x.message })); }
    const card = data.cards[0];
    if (!card) {
      show(el('div', { class: 'screen center', style: 'padding-top:20px' }, figure(POSE.sleep, 150, 180),
        el('h1', { style: 'font-size:28px', text: data.left ? 'Пока никого нового' : 'На сегодня всё' }),
        el('p', { class: 'muted', text: data.left ? 'Новые проверенные участники появляются каждый день. Загляните позже.' : 'Мы показываем 10 анкет в день, чтобы вы смотрели внимательно. Новые — завтра.' }),
        el('a', { class: 'btn', href: '#/matches', text: 'Мои мэтчи' })));
      return;
    }
    const swipe = async like => {
      try { const r = await api('/api/swipe', { id: card.id, like }); if (r.match) matchDialog(card, r.matchId); else feed(); } catch (x) { alert(x.message); }
    };
    show(el('div', { class: 'row', style: 'justify-content:space-between' }, el('h1', { style: 'font-size:24px', text: 'Знакомства' }), el('span', { class: 'muted', style: 'font-size:13px', text: `осталось ${data.left} на сегодня` })),
      profileCard(card),
      el('div', { class: 'acts' },
        el('button', { class: 'round', type: 'button', 'aria-label': 'Пропустить', onclick: () => swipe(false) }, svgIcon('<path d="M6 6l12 12M18 6L6 18"/>')),
        el('button', { class: 'round like', type: 'button', 'aria-label': 'Интересно', onclick: () => swipe(true) }, svgIcon('<path d="M12 20s-7-4.4-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 10c0 5.6-7 10-7 10z" fill="#fff"/>'))));
  }
  const svgIcon = d => { const s = document.createElementNS(svgNS, 'svg'); s.setAttribute('width', '28'); s.setAttribute('height', '28'); s.setAttribute('viewBox', '0 0 24 24'); s.setAttribute('fill', 'none'); s.setAttribute('stroke', '#1C1B19'); s.setAttribute('stroke-width', '2.4'); s.setAttribute('stroke-linecap', 'round'); s.setAttribute('stroke-linejoin', 'round'); s.setAttribute('aria-hidden', 'true'); s.innerHTML = d; return s; };

  function profileCard(u) {
    return el('article', { class: 'pcard' },
      el('div', { class: 'photo' }, avatar(u), u.verified ? el('span', { class: 'badge-ok', text: '✓ проверен' }) : null),
      el('div', { class: 'info' },
        el('h2', { style: 'font-size:24px', text: u.name }),
        el('p', { class: 'muted', text: (u.role === 'founder' ? 'Предприниматель · ' : 'Студент · ') + (u.org || '') }),
        u.common && u.common.length ? el('p', { class: 'common', text: 'Совпадение: ' + u.common.map(g => GOALS[g].toLowerCase()).join(', ') }) : null,
        el('div', { class: 'chips' }, (u.goals || []).map(g => el('span', { class: 'tag', style: 'background:var(--marker)', text: GOALS[g] || g })), (u.interests || []).map(i => el('span', { class: 'tag', text: i }))),
        u.about ? el('p', { text: u.about }) : null,
        u.project ? el('div', { class: 'card flat', style: 'padding:12px' }, el('b', { text: 'Проект: ' + u.project.title }), u.project.desc ? el('p', { class: 'muted', style: 'font-size:14px', text: u.project.desc }) : null, u.project.link ? el('a', { href: u.project.link, target: '_blank', rel: 'noopener noreferrer nofollow', text: 'ссылка' }) : null) : null));
  }

  function matchDialog(u, matchId) {
    const topic = (u.common && u.common.length) ? `У вас совпало: «${GOALS[u.common[0]].toLowerCase()}». С этого и начните!` : 'Спросите, над чем человек сейчас работает.';
    dlg.replaceChildren(el('div', { class: 'screen center' },
      el('p', { class: 'hand', style: 'font-size:46px;line-height:1;color:var(--accent-ink);transform:rotate(-3deg)', text: 'Это мэтч!' }),
      figure(POSE.joy, 120, 150),
      el('p', { text: `${u.name.split(' ')[0]} тоже хочет познакомиться.` }),
      say(topic, 1),
      el('a', { class: 'btn p', href: '#/chat/' + matchId, style: 'width:100%', text: 'Написать', onclick: () => dlg.close() }),
      el('button', { class: 'btn ghost', type: 'button', style: 'width:100%', text: 'Смотреть дальше', onclick: () => { dlg.close(); feed(); } })));
    dlg.showModal();
  }

  async function matches() {
    setTabs('matches');
    const { matches: list } = await api('/api/matches');
    if (!list.length) return show(el('h1', { style: 'font-size:24px', text: 'Мэтчи' }), el('div', { class: 'card flat', style: 'text-align:center' }, el('p', { class: 'muted', text: 'Пока пусто. Когда интерес будет взаимным, человек появится здесь.' }), el('a', { class: 'btn p', href: '#/feed', style: 'margin-top:12px', text: 'К знакомствам' })));
    show(el('h1', { style: 'font-size:24px', text: 'Мэтчи' }), list.map(m => el('a', { class: 'mrow', href: '#/chat/' + m.id }, avatar(m.user, 'av'),
      el('span', { style: 'display:flex;flex-direction:column;min-width:0' }, el('b', { text: m.user.name }), el('span', { class: 'muted', style: 'font-size:14px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis', text: m.last ? (m.last.mine ? 'Вы: ' : '') + m.last.text : 'Напишите первым' })))));
  }

  async function chat(id) {
    setTabs('matches');
    let last = 0, other = null;
    const box = el('div', { class: 'chat' });
    const add = msgs => { for (const m of msgs) { box.append(el('div', { class: 'msg' + (m.mine ? ' mine' : ''), text: m.text })); last = m.id; } if (msgs.length) scrollTo({ top: document.body.scrollHeight }); };
    try { const r = await api('/api/messages?match=' + id); other = r.other; add(r.messages); } catch (x) { return show(el('p', { class: 'err', text: x.message })); }
    const menu = el('details', { style: 'position:relative' }, el('summary', { style: 'cursor:pointer;list-style:none;font-size:22px;padding:4px 10px', 'aria-label': 'Ещё', text: '⋯' }),
      el('div', { class: 'card flat', style: 'position:absolute;right:0;top:36px;z-index:2;display:flex;flex-direction:column;gap:6px;padding:10px;width:200px' },
        el('button', { class: 'link-btn', type: 'button', text: 'Пожаловаться', onclick: () => report(other) }),
        el('button', { class: 'link-btn', type: 'button', text: 'Заблокировать', onclick: async () => { if (confirm('Заблокировать? Вы больше не увидите друг друга.')) { await api('/api/block', { id: other.id }); go('#/matches'); } } })));
    show(el('div', { class: 'row', style: 'justify-content:space-between' }, el('a', { href: '#/matches', text: '← Мэтчи' }), menu),
      el('div', { class: 'row' }, avatar(other, 'av'), el('div', {}, el('b', { text: other.name }), el('div', { class: 'muted', style: 'font-size:13px', text: other.org || '' }))),
      box.children.length ? null : el('div', { class: 'note' }, 'Хороший старт: расскажите, почему вам интересно познакомиться, и предложите созвон на 15 минут.'),
      box);
    const input = el('input', { type: 'text', maxlength: '2000', placeholder: 'Сообщение', 'aria-label': 'Сообщение' });
    const form = el('form', {}, input, el('button', { class: 'btn p', type: 'submit', text: 'Отправить' }));
    form.addEventListener('submit', async e => { e.preventDefault(); const text = input.value.trim(); if (!text) return; input.value = ''; try { await api('/api/messages', { match: Number(id), text }); await poll(); } catch (x) { alert(x.message); } });
    document.body.append(el('div', { class: 'composer' }, form));
    const poll = async () => { try { const r = await api(`/api/messages?match=${id}&after=${last}`); add(r.messages); } catch (_) {} };
    chatTimer = setInterval(poll, 4000);
  }

  function report(u) {
    const ta = el('textarea', { maxlength: '500', placeholder: 'Что случилось?', 'aria-label': 'Причина жалобы' }), err = errBox();
    dlg.replaceChildren(el('div', { class: 'screen' }, el('h2', { style: 'font-size:22px', text: 'Жалоба' }), el('p', { class: 'muted', style: 'font-size:14px', text: 'Модератор посмотрит её. Человек будет скрыт от вас.' }), ta, err,
      el('button', { class: 'btn p', type: 'button', text: 'Отправить', onclick: async () => { try { await api('/api/report', { id: u.id, reason: ta.value }); dlg.close(); go('#/matches'); } catch (x) { err.textContent = x.message; } } }),
      el('button', { class: 'btn ghost', type: 'button', text: 'Отмена', onclick: () => dlg.close() })));
    dlg.showModal();
  }

  function profile() {
    setTabs('me');
    show(el('h1', { style: 'font-size:24px', text: 'Мой профиль' }), profileCard({ ...me, common: null }),
      el('a', { class: 'btn', href: '#/profile-edit', text: 'Редактировать анкету' }),
      el('a', { class: 'btn', href: '#/verify', text: 'Сменить фото' }),
      el('a', { class: 'btn', href: '/learn', text: `Обучалка: пройдено уроков ${(me.progress || []).length}` }),
      el('button', { class: 'link-btn', type: 'button', text: 'Выйти', onclick: async () => { await api('/api/logout', {}); me = null; go('#/'); } }));
  }

  /* ---------- routing ---------- */
  function nextFor(u) {
    if (!u) return '#/';
    if (!u.name) return '#/profile';
    if (u.status === 'draft') return '#/verify';
    if (u.status === 'pending' || u.status === 'rejected') return '#/pending';
    return '#/feed';
  }
  async function route() {
    try { await refreshMe(); } catch (_) { me = null; }
    const h = location.hash || '#/';
    if (!me) return h === '#/register' ? authForm('register') : h === '#/login' ? authForm('login') : welcome();
    const verified = me.status === 'verified';
    if (h === '#/profile' || h === '#/profile-edit') return profileForm(h === '#/profile-edit' || verified);
    if (h === '#/verify' && me.name) return verifyForm();
    if (!verified) { const n = nextFor(me); return n === '#/profile' ? profileForm(false) : n === '#/verify' ? verifyForm() : pending(); }
    const m = /^#\/chat\/(\d+)$/.exec(h);
    if (m) return chat(m[1]);
    if (h === '#/matches') return matches();
    if (h === '#/me') return profile();
    return feed();
  }
  addEventListener('hashchange', route);
  route();
})();
