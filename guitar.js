/*
 * Интерактивная гитара.
 * Рисуется в SVG, звук синтезируется прямо в браузере (алгоритм Карплуса — Стронга),
 * поэтому никаких аудиофайлов не нужно.
 */
(() => {
  'use strict';

  /* ---------------- Музыка ---------------- */

  // Строй E A D G B E (MIDI-номера открытых струн), струна 0 — шестая (басовая).
  const OPEN_MIDI = [40, 45, 50, 55, 59, 64];
  const NOTE_NAMES = ['C', 'C#', 'D', 'D#', 'E', 'F', 'F#', 'G', 'G#', 'A', 'A#', 'B'];
  const noteName = midi => NOTE_NAMES[midi % 12];
  const midiToFreq = midi => 440 * Math.pow(2, (midi - 69) / 12);

  // Аппликатуры: от шестой струны к первой. -1 — струна заглушена, 0 — открытая.
  const CHORDS = [
    ['Am', [-1, 0, 2, 2, 1, 0]],
    ['Dm', [-1, -1, 0, 2, 3, 1]],
    ['E',  [0, 2, 2, 1, 0, 0]],
    ['C',  [-1, 3, 2, 0, 1, 0]],
    ['G',  [3, 2, 0, 0, 0, 3]],
    ['F',  [1, 3, 3, 2, 1, 1]],
    ['Em', [0, 2, 2, 0, 0, 0]],
    ['A',  [-1, 0, 2, 2, 2, 0]],
    ['D',  [-1, -1, 0, 2, 3, 2]],
    ['B7', [-1, 2, 1, 2, 0, 2]],
  ];
  const CHORD_KEYS = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '0'];
  const STRING_KEYS = ['KeyA', 'KeyS', 'KeyD', 'KeyF', 'KeyG', 'KeyH'];

  const state = [0, 0, 0, 0, 0, 0];

  /* ---------------- Геометрия ---------------- */

  const FRETS = 19;
  const NUT = 150;           // x порожка
  const SCALE = 1080;        // мензура
  const SADDLE = NUT + SCALE; // x нижнего порожка (сёдла)
  const CY = 240;            // центр грифа по y

  const fretX = n => NUT + SCALE * (1 - Math.pow(2, -n / 12));
  const spacing = x => 15 + Math.max(0, Math.min(1, (x - NUT) / SCALE)) * 6;
  const stringY = (i, x) => CY + (i - 2.5) * spacing(x);
  const neckHalf = x => 2.5 * spacing(x) + 11;
  const BOARD_END = fretX(FRETS) + 8;
  const LABEL_X = 1300;
  const fingerX = f => fretX(f) - (fretX(f) - fretX(f - 1)) * 0.38;

  const STRING_W = [3.4, 2.8, 2.3, 1.8, 1.3, 1.0];
  const WOUND = [true, true, true, true, false, false];

  /* ---------------- Построение SVG ---------------- */
  // Цвета отделки берутся из CSS-переменных --g-*, поэтому у каждого варианта оформления своя гитара.

  const NS = 'http://www.w3.org/2000/svg';
  const svg = document.getElementById('guitar');

  function el(tag, attrs, parent) {
    const e = document.createElementNS(NS, tag);
    for (const k in attrs) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }

  // Детерминированный «случай», чтобы рисунок древесины был одинаковым при каждой загрузке.
  let seed = 7;
  const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647);

  const BODY_PATH =
    `M 740,${CY} C 740,150 775,100 850,100 C 920,100 940,135 975,135 ` +
    `C 1020,135 1060,60 1190,60 C 1300,60 1360,140 1360,${CY} ` +
    `C 1360,340 1300,420 1190,420 C 1060,420 1020,345 975,345 ` +
    `C 940,345 920,380 850,380 C 775,380 740,330 740,${CY} Z`;

  const HEAD_PATH =
    `M ${NUT + 2},${CY - 47} L 136,${CY - 54} C 110,${CY - 60} 60,${CY - 66} 24,${CY - 64} ` +
    `C 12,${CY - 63} 6,${CY - 52} 8,${CY - 36} Q 14,${CY} 8,${CY + 36} ` +
    `C 6,${CY + 52} 12,${CY + 63} 24,${CY + 64} C 60,${CY + 66} 110,${CY + 60} 136,${CY + 54} ` +
    `L ${NUT + 2},${CY + 47} Z`;

  const GUARD_PATH =
    `M 992,${CY + 44} C 1034,${CY + 58} 1066,${CY + 102} 1054,${CY + 142} ` +
    `C 1038,${CY + 178} 958,${CY + 168} 944,${CY + 122} C 960,${CY + 110} 986,${CY + 80} 992,${CY + 44} Z`;

  const neckPoly = (pad, x1 = BOARD_END) => {
    const x0 = NUT;
    return `${x0},${CY - neckHalf(x0) - pad} ${x1},${CY - neckHalf(x1) - pad} ` +
           `${x1},${CY + neckHalf(x1) + pad} ${x0},${CY + neckHalf(x0) + pad}`;
  };

  const v = name => `var(--g-${name})`;
  const stop = (o, color, op = 1) => `<stop offset="${o}" style="stop-color:${color};stop-opacity:${op}"/>`;

  el('defs', {}, svg).innerHTML = `
    <linearGradient id="topBase" x1="0" y1="0" x2="0" y2="1">
      ${stop(0, v('top-b'))}${stop(0.5, v('top-a'))}${stop(1, v('top-b'))}
    </linearGradient>
    <radialGradient id="burst" gradientUnits="userSpaceOnUse" cx="1070" cy="${CY}" r="345">
      ${stop(0, v('burst'), 0)}${stop(0.5, v('burst'), 0)}${stop(0.78, v('burst'), 'var(--g-burst-op)')}${stop(1, v('edge'), 1)}
    </radialGradient>
    <radialGradient id="hole" cx=".42" cy=".38" r=".65">
      ${stop(0, '#1b0f07')}${stop(0.7, '#070403')}${stop(1, '#000')}
    </radialGradient>
    <linearGradient id="board" x1="0" y1="0" x2="0" y2="1">
      ${stop(0, v('board-b'))}${stop(0.5, v('board'))}${stop(1, v('board-b'))}
    </linearGradient>
    <linearGradient id="headGrad" x1="0" y1="0" x2="0" y2="1">
      ${stop(0, v('head-b'))}${stop(0.5, v('head'))}${stop(1, v('head-b'))}
    </linearGradient>
    <linearGradient id="fret" x1="0" y1="0" x2="1" y2="0">
      ${stop(0, '#7d7d7d')}${stop(0.45, '#ffffff')}${stop(0.6, '#d8d8d8')}${stop(1, '#6f6f6f')}
    </linearGradient>
    <radialGradient id="chrome" cx=".32" cy=".3" r=".75">
      ${stop(0, '#ffffff')}${stop(0.35, '#e2e2e2')}${stop(0.7, '#8e8e8e')}${stop(1, '#4a4a4a')}
    </radialGradient>
    <linearGradient id="chromeBar" x1="0" y1="0" x2="1" y2="0">
      ${stop(0, '#5c5c5c')}${stop(0.5, '#f2f2f2')}${stop(1, '#5c5c5c')}
    </linearGradient>
    <radialGradient id="pearl" cx=".35" cy=".35" r=".8">
      ${stop(0, '#ffffff')}${stop(0.35, '#f4eef6')}${stop(0.6, '#d6e7ef')}${stop(0.85, '#efe1cf')}${stop(1, '#b9b2c4')}
    </radialGradient>
    <linearGradient id="bone" x1="0" y1="0" x2="1" y2="0">
      ${stop(0, '#cfc4a8')}${stop(0.5, '#fbf6e8')}${stop(1, '#d8ceb4')}
    </linearGradient>
    <linearGradient id="gold" x1="0" y1="0" x2="1" y2="1">
      ${stop(0, '#fff1b8')}${stop(0.5, '#d9a93c')}${stop(1, '#8a6315')}
    </linearGradient>
    <radialGradient id="fingertip" cx=".4" cy=".35" r=".7">
      ${stop(0, '#ffe2c4')}${stop(0.7, '#f0a77a')}${stop(1, '#c36f45')}
    </radialGradient>
    <radialGradient id="gloss" cx=".5" cy=".5" r=".5">
      ${stop(0, '#ffffff', 0.55)}${stop(1, '#ffffff', 0)}
    </radialGradient>
    <filter id="boardGrain" x="0" y="0" width="100%" height="100%">
      <feTurbulence type="fractalNoise" baseFrequency="0.0035 0.32" numOctaves="3" seed="11"/>
      <feColorMatrix values="0 0 0 0 0.05  0 0 0 0 0.02  0 0 0 0 0.01  2.2 0 0 0 -0.9"/>
    </filter>
    <filter id="topTexture" x="0" y="0" width="100%" height="100%">
      <feTurbulence type="fractalNoise" baseFrequency="0.0018 0.22" numOctaves="2" seed="3"/>
      <feColorMatrix values="0 0 0 0 0.3  0 0 0 0 0.16  0 0 0 0 0.05  1.4 0 0 0 -0.55"/>
    </filter>
    <filter id="tortoise" x="0" y="0" width="100%" height="100%">
      <feTurbulence type="fractalNoise" baseFrequency="0.07 0.1" numOctaves="4" seed="21"/>
      <feColorMatrix values="0 0 0 0 0.55  0 0 0 0 0.22  0 0 0 0 0.04  3.6 0 0 0 -1.75"/>
    </filter>
    <filter id="shadow" x="-10%" y="-20%" width="120%" height="140%"><feGaussianBlur stdDeviation="10"/></filter>
    <filter id="soft" x="-20%" y="-20%" width="140%" height="140%"><feGaussianBlur stdDeviation="14"/></filter>
    <filter id="stringShadow" x="-2%" y="-20%" width="104%" height="140%"><feGaussianBlur stdDeviation="1.4"/></filter>
    <clipPath id="bodyClip"><path d="${BODY_PATH}"/></clipPath>
    <clipPath id="boardClip"><polygon points="${neckPoly(0)}"/></clipPath>
    <clipPath id="guardClip"><path d="${GUARD_PATH}"/></clipPath>
    <clipPath id="holeClip"><circle cx="935" cy="${CY}" r="56"/></clipPath>`;

  const root = el('g', { id: 'g-root' }, svg);

  // Тень от гитары на поверхности (статичная).
  const shadow = el('g', { transform: 'translate(12 20)', filter: 'url(#shadow)', style: 'opacity:var(--g-shadow-op)' }, root);
  el('path', { d: BODY_PATH, fill: '#000' }, shadow);
  el('polygon', { points: neckPoly(4), fill: '#000' }, shadow);
  el('path', { d: HEAD_PATH, fill: '#000' }, shadow);

  /* --- Корпус --- */
  el('path', { d: BODY_PATH, fill: 'url(#topBase)' }, root);
  const top = el('g', { 'clip-path': 'url(#bodyClip)' }, root);
  // волокна ели идут вдоль струн
  const grain = el('g', { style: `stroke:${v('grain')};opacity:var(--g-grain-op)`, fill: 'none' }, top);
  for (let y = 52; y < 430; y += 2.2 + rnd() * 5.5) {
    const w1 = (rnd() - 0.5) * 3, w2 = (rnd() - 0.5) * 3, w3 = (rnd() - 0.5) * 2;
    el('path', {
      d: `M 730,${y.toFixed(1)} C 900,${(y + w1).toFixed(1)} 1120,${(y + w2).toFixed(1)} 1370,${(y + w3).toFixed(1)}`,
      'stroke-width': (0.5 + rnd() * 1.3).toFixed(2), opacity: (0.25 + rnd() * 0.75).toFixed(2),
    }, grain);
  }
  el('rect', { x: 730, y: 50, width: 640, height: 380, filter: 'url(#topTexture)', style: 'opacity:var(--g-grain-op)' }, top);
  el('rect', { x: 730, y: 50, width: 640, height: 380, fill: 'url(#burst)' }, top);

  // Окантовка и пёрфлинг.
  const inset = (k) => `translate(1050 ${CY}) scale(${k}) translate(-1050 ${-CY})`;
  el('path', { d: BODY_PATH, fill: 'none', style: `stroke:${v('purfling')}`, 'stroke-width': 2.2, transform: inset(0.984) }, root);
  el('path', { d: BODY_PATH, fill: 'none', style: `stroke:${v('binding')}`, 'stroke-width': 6 }, root);
  el('path', { d: BODY_PATH, fill: 'none', stroke: 'rgba(0,0,0,.55)', 'stroke-width': 1.2 }, root);
  el('path', { d: BODY_PATH, fill: 'none', stroke: 'rgba(0,0,0,.25)', 'stroke-width': 1, transform: inset(0.974) }, root);

  // Накладка (черепаховая или однотонная).
  const guard = el('g', { style: 'opacity:var(--g-guard-op)' }, root);
  el('path', { d: GUARD_PATH, style: `fill:${v('guard')}` }, guard);
  el('rect', { x: 930, y: CY + 40, width: 140, height: 145, filter: 'url(#tortoise)', 'clip-path': 'url(#guardClip)', style: 'opacity:var(--g-tortoise)' }, guard);
  el('path', { d: GUARD_PATH, fill: 'none', stroke: 'rgba(255,255,255,.18)', 'stroke-width': 1 }, guard);

  // Розетка и резонаторное отверстие.
  const HOLE_X = 935, HOLE_R = 56;
  el('circle', { cx: HOLE_X, cy: CY, r: HOLE_R + 19, fill: 'none', stroke: '#140802', 'stroke-width': 1.6 }, root);
  el('circle', { cx: HOLE_X, cy: CY, r: HOLE_R + 15, fill: 'none', style: `stroke:${v('rosette')}`, 'stroke-width': 5 }, root);
  el('circle', { cx: HOLE_X, cy: CY, r: HOLE_R + 15, fill: 'none', stroke: '#2b1407', 'stroke-width': 5, 'stroke-dasharray': '1.6 1.6' }, root);
  el('circle', { cx: HOLE_X, cy: CY, r: HOLE_R + 11.5, fill: 'none', stroke: '#f2e6c9', 'stroke-width': 1.2 }, root);
  el('circle', { cx: HOLE_X, cy: CY, r: HOLE_R + 9, fill: 'none', stroke: '#140802', 'stroke-width': 2.2 }, root);
  el('circle', { cx: HOLE_X, cy: CY, r: HOLE_R + 5.5, fill: 'none', stroke: '#f2e6c9', 'stroke-width': 1 }, root);
  el('circle', { cx: HOLE_X, cy: CY, r: HOLE_R, fill: 'url(#hole)' }, root);
  // толщина деки видна на краю отверстия
  el('path', {
    d: `M ${HOLE_X + HOLE_R * Math.cos(0.15)},${CY + HOLE_R * Math.sin(0.15)} A ${HOLE_R} ${HOLE_R} 0 0 1 ${HOLE_X + HOLE_R * Math.cos(2.4)},${CY + HOLE_R * Math.sin(2.4)}`,
    fill: 'none', style: `stroke:${v('top-a')}`, 'stroke-width': 3, opacity: 0.8,
  }, root);
  // поперечная пружина внутри корпуса, едва заметна
  el('rect', { x: HOLE_X + 26, y: CY - 70, width: 9, height: 140, fill: '#2a170b', opacity: 0.55, 'clip-path': 'url(#holeClip)' }, root);

  // Подставка.
  const BRIDGE =
    `M 1198,${CY - 64} Q 1196,${CY - 96} 1214,${CY - 98} Q 1232,${CY - 92} 1252,${CY - 98} Q 1272,${CY - 96} 1270,${CY - 64} ` +
    `L 1270,${CY + 64} Q 1272,${CY + 96} 1252,${CY + 98} Q 1232,${CY + 92} 1214,${CY + 98} Q 1196,${CY + 96} 1198,${CY + 64} Z`;
  el('path', { d: BRIDGE, fill: 'rgba(0,0,0,.35)', transform: 'translate(3 5)', filter: 'url(#stringShadow)' }, root);
  el('path', { d: BRIDGE, style: `fill:${v('bridge')}`, stroke: 'rgba(0,0,0,.6)', 'stroke-width': 1.2 }, root);
  el('path', { d: BRIDGE, fill: 'none', stroke: 'rgba(255,255,255,.12)', 'stroke-width': 1, transform: 'translate(-.6 -.8)' }, root);
  el('rect', { x: SADDLE - 2.5, y: CY - 66, width: 5, height: 132, rx: 2.5, fill: 'url(#bone)', stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.6 }, root);

  /* --- Гриф --- */
  el('polygon', { points: neckPoly(3.5), style: `fill:${v('neck')}` }, root);
  el('polygon', { points: neckPoly(0), fill: 'url(#board)' }, root);
  el('rect', { x: NUT, y: CY - 60, width: BOARD_END - NUT, height: 120, filter: 'url(#boardGrain)', 'clip-path': 'url(#boardClip)', opacity: 0.85 }, root);
  el('polygon', { points: neckPoly(0.8), fill: 'none', style: `stroke:${v('board-binding')}`, 'stroke-width': 1.6 }, root);

  // Перламутровые метки.
  const markerX = n => (fretX(n - 1) + fretX(n)) / 2;
  const dot = (x, y) => el('circle', { cx: x, cy: y, r: 5.6, fill: 'url(#pearl)', stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.6 }, root);
  [3, 5, 7, 9, 15, 17].forEach(n => dot(markerX(n), CY));
  [-1, 1].forEach(s => dot(markerX(12), CY + s * 2 * spacing(markerX(12))));

  // Лады: металлическая проволока с тенью.
  for (let n = 1; n <= FRETS; n++) {
    const x = fretX(n), h = neckHalf(x) + 0.5;
    el('line', { x1: x + 2.2, y1: CY - h, x2: x + 2.2, y2: CY + h, stroke: 'rgba(0,0,0,.45)', 'stroke-width': 2 }, root);
    el('rect', { x: x - 1.6, y: CY - h, width: 3.2, height: h * 2, rx: 1.2, fill: 'url(#fret)' }, root);
  }

  // Номера ладов вдоль кромки грифа.
  [3, 5, 7, 9, 12, 15].forEach(n => {
    const t = el('text', { x: markerX(n), y: CY + neckHalf(markerX(n)) + 18, 'text-anchor': 'middle', class: 'fretnum' }, root);
    t.textContent = n;
  });

  // Верхний порожек.
  el('rect', { x: NUT - 4, y: CY - neckHalf(NUT) - 1, width: 8, height: neckHalf(NUT) * 2 + 2, rx: 1.5, fill: 'url(#bone)', stroke: 'rgba(0,0,0,.3)', 'stroke-width': 0.6 }, root);

  /* --- Голова грифа и колки --- */
  const POST_X = [116, 78, 40, 40, 78, 116];
  const postY = i => (i < 3 ? CY - 33 : CY + 33);
  // ручки колков выступают за край головы
  for (let i = 0; i < 6; i++) {
    const px = POST_X[i], s = i < 3 ? -1 : 1;
    const edge = CY + s * 63, by = CY + s * 86;
    el('rect', { x: px - 2.5, y: Math.min(edge, by), width: 5, height: Math.abs(by - edge), fill: 'url(#chromeBar)' }, root);
    el('path', {
      d: `M ${px - 9},${by} Q ${px - 12},${by + s * 16} ${px},${by + s * 19} Q ${px + 12},${by + s * 16} ${px + 9},${by} Z`,
      fill: 'url(#chrome)', stroke: 'rgba(0,0,0,.5)', 'stroke-width': 0.8,
    }, root);
  }
  el('path', { d: HEAD_PATH, fill: 'url(#headGrad)', stroke: 'rgba(0,0,0,.7)', 'stroke-width': 1.2 }, root);
  el('path', { d: HEAD_PATH, fill: 'none', style: `stroke:${v('binding')};opacity:var(--g-head-binding)`, 'stroke-width': 2, transform: `translate(80 ${CY}) scale(.965) translate(-80 ${-CY})` }, root);
  // блик лака
  el('ellipse', { cx: 70, cy: CY - 30, rx: 50, ry: 10, fill: 'url(#gloss)', opacity: 0.35 }, root);
  // золотая инкрустация
  el('path', { d: `M 66,${CY - 11} L 80,${CY} L 66,${CY + 11} L 52,${CY} Z`, fill: 'url(#gold)', stroke: 'rgba(60,40,0,.6)', 'stroke-width': 0.6 }, root);
  el('circle', { cx: 66, cy: CY, r: 2.4, fill: '#2a1a05' }, root);
  // крышка анкера
  el('path', { d: `M 136,${CY - 9} Q 120,${CY - 9} 116,${CY} Q 120,${CY + 9} 136,${CY + 9} Z`, fill: '#0b0b0b', stroke: '#777', 'stroke-width': 0.6 }, root);
  // втулки и стойки колков
  for (let i = 0; i < 6; i++) {
    el('circle', { cx: POST_X[i], cy: postY(i), r: 7.5, fill: 'url(#chrome)', stroke: 'rgba(0,0,0,.6)', 'stroke-width': 0.8 }, root);
    el('circle', { cx: POST_X[i], cy: postY(i), r: 4, fill: '#9a9a9a', stroke: '#3c3c3c', 'stroke-width': 0.8 }, root);
  }

  /* --- Струны --- */
  const vibLines = [];
  const fingers = [];
  const labels = [];
  const STRING_END_X = 1252;
  const strColor = i => (WOUND[i] ? v('wound') : v('plain'));

  // тени струн на деке и грифе
  const sh = el('g', { filter: 'url(#stringShadow)', opacity: 0.5 }, root);
  for (let i = 0; i < 6; i++) {
    el('line', { x1: NUT + 3, y1: stringY(i, NUT) + 3, x2: STRING_END_X + 3, y2: stringY(i, STRING_END_X) + 6, stroke: '#000', 'stroke-width': STRING_W[i] + 0.6 }, sh);
  }

  for (let i = 0; i < 6; i++) {
    const w = STRING_W[i], c = strColor(i);
    const style = `stroke:${c}`;
    el('line', { x1: POST_X[i], y1: postY(i), x2: NUT, y2: stringY(i, NUT), style, 'stroke-width': w }, root);
    el('circle', { cx: POST_X[i], cy: postY(i), r: 4 + w * 0.4, fill: 'none', style, 'stroke-width': w * 0.9 }, root);
    el('line', { x1: NUT, y1: stringY(i, NUT), x2: STRING_END_X, y2: stringY(i, STRING_END_X), style, 'stroke-width': w }, root);
    if (WOUND[i]) {
      el('line', { x1: NUT, y1: stringY(i, NUT), x2: STRING_END_X, y2: stringY(i, STRING_END_X), stroke: 'rgba(40,20,0,.45)', 'stroke-width': w, 'stroke-dasharray': '0.7 1' }, root);
    }
    el('line', { x1: NUT, y1: stringY(i, NUT) - w * 0.22, x2: STRING_END_X, y2: stringY(i, STRING_END_X) - w * 0.22,
      stroke: '#fff', 'stroke-width': Math.max(0.35, w * 0.28), opacity: 0.55 }, root);
    // штифт подставки
    el('circle', { cx: STRING_END_X, cy: stringY(i, STRING_END_X), r: 5.2, style: `fill:${v('pin')}`, stroke: 'rgba(0,0,0,.55)', 'stroke-width': 0.8 }, root);
    el('circle', { cx: STRING_END_X - 0.6, cy: stringY(i, STRING_END_X) - 0.6, r: 1.7, fill: 'url(#pearl)' }, root);

    const vib = el('line', { style, 'stroke-width': w * 1.5, opacity: 0, 'stroke-linecap': 'round' }, root);
    vibLines.push(vib);
  }

  // Блик лака на корпусе — поверх струн, как отражение лампы.
  const shine = el('g', { 'clip-path': 'url(#bodyClip)', style: 'opacity:var(--g-gloss)', 'pointer-events': 'none' }, root);
  el('ellipse', { cx: 1170, cy: 112, rx: 150, ry: 34, fill: 'url(#gloss)', transform: 'rotate(-7 1170 112)', filter: 'url(#soft)' }, shine);
  el('ellipse', { cx: 860, cy: 130, rx: 70, ry: 16, fill: 'url(#gloss)', transform: 'rotate(-12 860 130)', filter: 'url(#soft)', opacity: 0.7 }, shine);
  el('path', { d: BODY_PATH, fill: 'none', stroke: '#fff', 'stroke-width': 1.4, opacity: 0.35, transform: 'translate(0 1.6)' }, shine);

  for (let i = 0; i < 6; i++) {
    const g = el('g', { class: 'finger', opacity: 0 }, root);
    el('circle', { r: 9.5, fill: 'url(#fingertip)', stroke: '#8a4320', 'stroke-width': 1.2 }, g);
    fingers.push(g);

    const t = el('text', { x: LABEL_X, y: stringY(i, LABEL_X) + 5, 'text-anchor': 'middle', class: 'label' }, root);
    labels.push(t);
  }

  /* ---------------- Ориентация (вертикально на телефоне) ---------------- */

  const portrait = window.matchMedia('(max-width: 720px) and (orientation: portrait)');
  function layout() {
    const v = portrait.matches;
    svg.setAttribute('viewBox', v ? '0 0 480 1400' : '0 0 1400 480');
    root.setAttribute('transform', v ? 'translate(480 0) rotate(90)' : '');
    svg.classList.toggle('vertical', v);
    const rot = (t) => {
      const x = t.getAttribute('x'), y = t.getAttribute('y');
      t.setAttribute('transform', v ? `rotate(-90 ${x} ${y})` : '');
    };
    labels.forEach(rot);
    root.querySelectorAll('.fretnum').forEach(rot);
  }
  if (portrait.addEventListener) portrait.addEventListener('change', layout);
  else portrait.addListener(layout);
  layout();

  /* ---------------- Звук ---------------- */

  // Основной звук — записи настоящей акустической гитары (samples.js).
  // Пока они не загрузились, работает синтез (Карплус — Стронг).

  let ctx = null, master = null;
  const voices = [null, null, null, null, null, null];
  const bufferCache = new Map();
  const samples = {};
  const sampleKeys = [];

  function decodeSamples() {
    const data = window.GUITAR_SAMPLES;
    const OAC = window.OfflineAudioContext || window.webkitOfflineAudioContext;
    if (!data || !OAC) return;
    const dec = new OAC(1, 1, 44100);
    Object.keys(data).forEach(k => {
      const bin = atob(data[k]);
      const bytes = new Uint8Array(bin.length);
      for (let n = 0; n < bin.length; n++) bytes[n] = bin.charCodeAt(n);
      new Promise((res, rej) => {
        const r = dec.decodeAudioData(bytes.buffer, res, rej);
        if (r && r.then) r.then(res, rej);
      }).then(buf => {
        samples[k] = { buffer: buf, offset: findOnset(buf) };
        sampleKeys.push(+k);
        sampleKeys.sort((x, y) => x - y);
      }).catch(() => { /* останется синтез */ });
    });
  }

  // Точное начало звука: пропускаем тишину и задержку mp3-кодека, чтобы нота звучала сразу.
  function findOnset(buf) {
    const d = buf.getChannelData(0);
    const lim = Math.min(d.length, Math.floor(buf.sampleRate * 0.4));
    let peak = 0;
    for (let n = 0; n < lim; n++) peak = Math.max(peak, Math.abs(d[n]));
    const th = peak * 0.05;
    for (let n = 0; n < lim; n++) if (Math.abs(d[n]) > th) return Math.max(0, n - 16) / buf.sampleRate;
    return 0;
  }

  function nearestSample(midi) {
    if (!sampleKeys.length) return null;
    let best = sampleKeys[0];
    for (const k of sampleKeys) if (Math.abs(k - midi) < Math.abs(best - midi)) best = k;
    return Math.abs(best - midi) <= 12 ? { s: samples[best], rate: Math.pow(2, (midi - best) / 12) } : null;
  }

  if (window.GUITAR_SAMPLES) decodeSamples();
  else {
    const sc = document.createElement('script');
    sc.src = 'samples.js';
    sc.async = true;
    sc.onload = decodeSamples;
    document.head.appendChild(sc);
  }

  function ensureAudio() {
    if (ctx) {
      if (ctx.state !== 'running') ctx.resume();
      return;
    }
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return;
    try { ctx = new AC({ latencyHint: 'interactive' }); } catch (e) { ctx = new AC(); }

    master = ctx.createGain();
    master.gain.value = 1;

    const hp = ctx.createBiquadFilter();
    hp.type = 'highpass'; hp.frequency.value = 60;
    const comp = ctx.createDynamicsCompressor();
    comp.threshold.value = -12; comp.knee.value = 10; comp.ratio.value = 3;
    comp.attack.value = 0.003; comp.release.value = 0.2;

    master.connect(hp); hp.connect(comp);
    comp.connect(ctx.destination);

    // Лёгкая «комната», чтобы звук не был сухим.
    const conv = ctx.createConvolver();
    conv.buffer = makeImpulse(1.4);
    const wet = ctx.createGain();
    wet.gain.value = 0.1;
    hp.connect(conv); conv.connect(wet); wet.connect(comp);
  }

  function makeImpulse(seconds) {
    const len = Math.floor(ctx.sampleRate * seconds);
    const buf = ctx.createBuffer(2, len, ctx.sampleRate);
    for (let ch = 0; ch < 2; ch++) {
      const d = buf.getChannelData(ch);
      for (let n = 0; n < len; n++) d[n] = (Math.random() * 2 - 1) * Math.pow(1 - n / len, 4);
    }
    return buf;
  }

  // Запасной синтез струны: Карплус — Стронг с дробной задержкой для точного строя.
  function stringBuffer(freq) {
    const key = freq.toFixed(2);
    if (bufferCache.has(key)) return bufferCache.get(key);

    const sr = ctx.sampleRate;
    const t60 = Math.max(1.3, Math.min(4.2, 4.6 - freq / 180));
    const len = Math.floor(sr * (t60 + 0.3));
    const out = new Float32Array(len);
    const period = sr / freq;
    const D = period - 0.5;
    const P = Math.ceil(period + 1);

    let lp = 0, mean = 0, peak = 0;
    for (let n = 0; n < P; n++) { lp += 0.5 * ((Math.random() * 2 - 1) - lp); out[n] = lp; }
    const pick = Math.max(1, Math.round(P * 0.17));
    for (let n = P - 1; n >= pick; n--) out[n] -= out[n - pick];
    for (let n = 0; n < P; n++) mean += out[n];
    mean /= P;
    for (let n = 0; n < P; n++) { out[n] -= mean; peak = Math.max(peak, Math.abs(out[n])); }
    for (let n = 0; n < P; n++) out[n] /= peak || 1;

    const g = Math.pow(10, -3 / (t60 * freq));
    for (let n = P; n < len; n++) {
      const x = n - D, i0 = Math.floor(x), fr = x - i0;
      const s0 = out[i0] + (out[i0 + 1] - out[i0]) * fr;
      const s1 = out[i0 - 1] + (out[i0] - out[i0 - 1]) * fr;
      out[n] = g * 0.5 * (s0 + s1);
    }
    const buf = ctx.createBuffer(1, len, sr);
    buf.getChannelData(0).set(out);
    bufferCache.set(key, buf);
    if (bufferCache.size > 60) bufferCache.delete(bufferCache.keys().next().value);
    return buf;
  }

  // Заглушить звучащую струну (палец снят с лада, смена аккорда).
  function damp(i, when) {
    const v = voices[i];
    if (!v || !ctx) return;
    const t = Math.max(when || 0, ctx.currentTime);
    v.gain.gain.cancelScheduledValues(t);
    v.gain.gain.setTargetAtTime(0, t, 0.025);
    try { v.src.stop(t + 0.2); } catch (e) { /* уже остановлен */ }
    voices[i] = null;
  }

  function pluck(i, vel = 0.8, delay = 0) {
    ensureAudio();
    if (!ctx) { shake(i, vel, delay); return; }

    const muted = state[i] < 0;
    const midi = OPEN_MIDI[i] + Math.max(0, state[i]);
    const v = Math.max(0.1, Math.min(1, vel * (0.9 + Math.random() * 0.2))); // живая рука
    const t = ctx.currentTime + delay;

    const src = ctx.createBufferSource();
    let offset = 0, level = 0.5;
    const smp = nearestSample(midi);
    if (smp) {
      src.buffer = smp.s.buffer;
      src.playbackRate.value = smp.rate;
      offset = smp.s.offset;
    } else {
      src.buffer = stringBuffer(midiToFreq(midi));
      level = 0.3;
    }

    // Чем сильнее удар, тем ярче звук.
    const tone = ctx.createBiquadFilter();
    tone.type = 'lowpass';
    tone.frequency.value = muted ? 700 : 1600 + v * v * 14000;
    tone.Q.value = 0.5;

    const gain = ctx.createGain();
    src.connect(tone); tone.connect(gain);
    if (ctx.createStereoPanner) {
      const pan = ctx.createStereoPanner();
      pan.pan.value = (i - 2.5) * 0.07;
      gain.connect(pan); pan.connect(master);
    } else {
      gain.connect(master);
    }

    damp(i, t);
    if (muted) {
      // глухой «чик» заглушённой струны
      gain.gain.setValueAtTime(level * v * 0.5, t);
      gain.gain.exponentialRampToValueAtTime(0.0005, t + 0.06);
      src.start(t, offset);
      src.stop(t + 0.08);
    } else {
      gain.gain.setValueAtTime(level * (0.25 + 0.75 * v), t);
      src.start(t, offset);
      voices[i] = { src, gain };
      src.onended = () => { if (voices[i] && voices[i].src === src) voices[i] = null; };
    }
    shake(i, v, delay);
  }

  /* ---------------- Анимация ---------------- */

  const canAnimate = typeof Element.prototype.animate === 'function';

  function shake(i, vel, delay) {
    if (!canAnimate) return;
    const run = () => {
      const line = vibLines[i];
      const muted = state[i] < 0;
      const amp = muted ? 0.8 : 1 + vel * 2.4 + (5 - i) * 0.25;
      const steps = muted ? 4 : 18;
      const frames = [];
      for (let k = 0; k <= steps; k++) {
        const a = k === steps ? 0 : amp * Math.pow(0.84, k) * (k % 2 ? -1 : 1);
        frames.push({ transform: `translate(0px, ${a.toFixed(2)}px)`, opacity: k === steps ? 0 : 1 - (k / steps) * 0.5 });
      }
      line.animate(frames, { duration: muted ? 120 : 900, easing: 'linear' });
    };
    if (delay > 0) setTimeout(run, delay * 1000); else run();
  }

  /* ---------------- Состояние ---------------- */

  function updateString(i) {
    const f = state[i];
    const x0 = f > 0 ? fretX(f) : NUT;
    const vib = vibLines[i];
    vib.setAttribute('x1', x0); vib.setAttribute('y1', stringY(i, x0));
    vib.setAttribute('x2', SADDLE); vib.setAttribute('y2', stringY(i, SADDLE));

    const g = fingers[i];
    if (f > 0) {
      const x = fingerX(f);
      g.setAttribute('transform', `translate(${x.toFixed(1)} ${stringY(i, x).toFixed(1)})`);
      g.setAttribute('opacity', 1);
    } else {
      g.setAttribute('opacity', 0);
    }

    const lab = labels[i];
    lab.textContent = f < 0 ? '×' : noteName(OPEN_MIDI[i] + f);
    lab.classList.toggle('muted', f < 0);
  }

  const chordNameEl = document.getElementById('chordName');
  const chordNotesEl = document.getElementById('chordNotes');

  function updateReadout() {
    const idx = CHORDS.findIndex(([, s]) => s.every((v, k) => v === state[k]));
    chordButtons.forEach((b, k) => b.classList.toggle('active', k === idx));
    let name;
    if (idx >= 0) name = CHORDS[idx][0];
    else if (state.every(v => v === 0)) name = 'открытые струны';
    else name = 'свой аккорд';
    chordNameEl.textContent = name;
    chordNotesEl.textContent = state.map((f, i) => (f < 0 ? '×' : noteName(OPEN_MIDI[i] + f))).join(' ');
  }

  function refresh() {
    for (let i = 0; i < 6; i++) updateString(i);
    updateReadout();
  }

  function setChord(frets) {
    for (let i = 0; i < 6; i++) {
      if (state[i] !== frets[i]) damp(i);
      state[i] = frets[i];
    }
    refresh();
  }

  /* ---------------- Приёмы игры ---------------- */

  function strum(dir = 'down', vel = 0.85, delay = 0) {
    let order = [0, 1, 2, 3, 4, 5];
    if (dir === 'up') order = order.filter(i => i >= 2).reverse();
    const gap = dir === 'up' ? 0.009 : 0.012;
    let t = delay;
    order.forEach((i, k) => {
      const accent = dir === 'down' ? 1 - k * 0.03 : 0.85 + k * 0.04;
      pluck(i, (state[i] < 0 ? 0.5 : 1) * vel * accent, t);
      t += gap * (0.75 + Math.random() * 0.5);
    });
  }

  function bassString() {
    for (let i = 0; i < 3; i++) if (state[i] >= 0) return i;
    return 3;
  }

  function arpeggio() {
    const seq = [bassString(), 3, 4, 5, 4, 3, 2, 3];
    seq.forEach((s, k) => pluck(s, k === 0 ? 0.85 : 0.6, k * 0.16));
  }

  function strumPattern() {
    // ↓ ↓ ↑ ↑ ↓ ↑
    const beat = 0.24;
    const p = [['down', 0, 0.9], ['down', 2, 0.75], ['up', 3, 0.6], ['up', 5, 0.6], ['down', 6, 0.8], ['up', 7, 0.6]];
    p.forEach(([d, t, v]) => strum(d, v, t * beat));
  }

  /* ---------------- Указатель: лады, струны, бой ---------------- */

  function toGuitar(e) {
    const pt = svg.createSVGPoint();
    pt.x = e.clientX; pt.y = e.clientY;
    return pt.matrixTransform(root.getScreenCTM().inverse());
  }

  const nearestString = p => {
    const i = Math.round((p.y - CY) / spacing(p.x) + 2.5);
    return Math.max(0, Math.min(5, i));
  };

  const fretAt = x => {
    for (let n = 1; n <= FRETS; n++) if (x <= fretX(n)) return n;
    return FRETS;
  };

  // Играть можно по всему корпусу между грифом и подставкой.
  const inStrumZone = p => p.x >= BOARD_END - 4 && p.x <= 1270 && Math.abs(p.y - CY) <= 175;
  const onBoard = p => p.x > NUT + 3 && p.x < BOARD_END - 4 && Math.abs(p.y - CY) <= neckHalf(p.x) + 6;
  const onMuteZone = p =>
    (p.x <= NUT + 3 && p.x > NUT - 60 && Math.abs(p.y - CY) < 56) ||
    (p.x > 1270 && p.x < 1340 && Math.abs(p.y - CY) < 60);

  const tracks = new Map();   // последняя позиция каждого указателя
  const strummers = new Set(); // указатели, которые зажаты и играют
  const lastPluck = [0, 0, 0, 0, 0, 0];

  svg.addEventListener('pointerdown', e => {
    if (e.button > 0) return;
    ensureAudio();
    const p = toGuitar(e);
    const now = performance.now();

    if (inStrumZone(p)) {
      e.preventDefault();
      strummers.add(e.pointerId);
      tracks.set(e.pointerId, { x: p.x, y: p.y, t: now });
      try { svg.setPointerCapture(e.pointerId); } catch (err) { /* не критично */ }
      const i = nearestString(p);
      if (Math.abs(stringY(i, p.x) - p.y) < 7) {
        lastPluck[i] = now;
        pluck(i, 0.75);
      }
      return;
    }

    if (onBoard(p)) {
      e.preventDefault();
      const i = nearestString(p);
      const f = fretAt(p.x);
      if (state[i] === f) { state[i] = 0; damp(i); refresh(); return; }
      state[i] = f;
      refresh();
      pluck(i, 0.65);
      return;
    }

    if (onMuteZone(p)) {
      e.preventDefault();
      const i = nearestString({ x: p.x < 700 ? NUT : LABEL_X, y: p.y });
      state[i] = state[i] < 0 ? 0 : -1;
      if (state[i] < 0) damp(i);
      refresh();
      if (state[i] === 0) pluck(i, 0.6);
    }
  });

  svg.addEventListener('pointermove', e => {
    const p = toGuitar(e);
    const now = performance.now();
    const prev = tracks.get(e.pointerId);
    tracks.set(e.pointerId, { x: p.x, y: p.y, t: now });
    if (!prev) return;

    // Зажатой кнопкой (или пальцем) — по всему корпусу; мышью без нажатия — просто проводя над корпусом.
    const pressed = strummers.has(e.pointerId);
    const hover = e.pointerType === 'mouse' && e.buttons === 0 && inStrumZone(p) && inStrumZone(prev);
    if (!pressed && !hover) return;
    if (p.x < BOARD_END - 60 || p.x > 1300) return;

    const dt = Math.max(1, now - prev.t);
    const speed = Math.abs(p.y - prev.y) / dt; // единиц SVG в мс
    const vel = Math.max(0.3, Math.min(1, 0.3 + speed * 0.3)) * (hover ? 0.85 : 1);

    const hits = [];
    for (let i = 0; i < 6; i++) {
      const d0 = prev.y - stringY(i, prev.x);
      const d1 = p.y - stringY(i, p.x);
      if ((d0 < 0 && d1 >= 0) || (d0 > 0 && d1 <= 0)) hits.push({ i, frac: d0 / (d0 - d1) });
    }
    hits.sort((u, w) => u.frac - w.frac);
    // Распределяем струны во времени так, как их на самом деле задела рука.
    const span = Math.min(dt, 90) / 1000;
    hits.forEach(({ i, frac }) => {
      if (now - lastPluck[i] < 30) return;
      lastPluck[i] = now;
      pluck(i, vel, (frac - hits[0].frac) * span);
    });
  });

  const endStrum = e => { strummers.delete(e.pointerId); if (e.pointerType !== 'mouse') tracks.delete(e.pointerId); };
  svg.addEventListener('pointerup', endStrum);
  svg.addEventListener('pointercancel', endStrum);
  svg.addEventListener('pointerleave', e => { if (!strummers.has(e.pointerId)) tracks.delete(e.pointerId); });
  svg.addEventListener('contextmenu', e => e.preventDefault());

  /* ---------------- Кнопки аккордов ---------------- */

  function chordDiagram(frets) {
    const x0 = 9, dx = 8, y0 = 16, dy = 11;
    let s = `<svg viewBox="0 0 58 64" aria-hidden="true">`;
    for (let i = 0; i < 6; i++) s += `<line x1="${x0 + i * dx}" y1="${y0}" x2="${x0 + i * dx}" y2="${y0 + 4 * dy}" stroke="currentColor" stroke-opacity=".55" stroke-width="1"/>`;
    for (let r = 0; r <= 4; r++) s += `<line x1="${x0}" y1="${y0 + r * dy}" x2="${x0 + 5 * dx}" y2="${y0 + r * dy}" stroke="currentColor" stroke-opacity="${r === 0 ? 1 : 0.55}" stroke-width="${r === 0 ? 3 : 1}"/>`;
    frets.forEach((f, i) => {
      const x = x0 + i * dx;
      if (f < 0) s += `<text x="${x}" y="${y0 - 5}" font-size="9" text-anchor="middle" fill="currentColor" opacity=".6">×</text>`;
      else if (f === 0) s += `<circle cx="${x}" cy="${y0 - 7}" r="2.6" fill="none" stroke="currentColor" stroke-width="1"/>`;
      else s += `<circle cx="${x}" cy="${y0 + (f - 0.5) * dy}" r="3.4" fill="currentColor"/>`;
    });
    return s + '</svg>';
  }

  const chordsEl = document.getElementById('chords');
  const chordButtons = CHORDS.map(([name, frets], k) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'chord';
    b.title = `${name} — клавиша ${CHORD_KEYS[k]}`;
    b.innerHTML = `<span class="key">${CHORD_KEYS[k]}</span>${chordDiagram(frets)}${name}`;
    b.addEventListener('click', e => { if (e.detail) b.blur(); setChord(frets); strum('down', 0.8); });
    chordsEl.appendChild(b);
    return b;
  });

  const actions = {
    down: () => strum('down', 0.85),
    up: () => strum('up', 0.7),
    pattern: strumPattern,
    arp: arpeggio,
    clear: () => setChord([0, 0, 0, 0, 0, 0]),
  };
  document.querySelectorAll('[data-action]').forEach(b => {
    b.addEventListener('click', e => { if (e.detail) b.blur(); ensureAudio(); actions[b.dataset.action](); });
  });

  /* ---------------- Клавиатура ---------------- */

  document.addEventListener('keydown', e => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (/^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName)) return;
    if (e.repeat) return;

    const ci = CHORD_KEYS.indexOf(e.key);
    if (ci >= 0) { ensureAudio(); setChord(CHORDS[ci][1]); strum('down', 0.8); return; }

    const si = STRING_KEYS.indexOf(e.code);
    if (si >= 0) { pluck(si, 0.8); return; }

    const deskVisible = window.scrollY < document.querySelector('.desk').offsetHeight * 0.7;
    switch (deskVisible ? e.code : '') {
      case 'Space':
      case 'ArrowDown':
        if (e.target.tagName === 'BUTTON' && e.code === 'Space') return;
        e.preventDefault(); strum('down', 0.85); break;
      case 'ArrowUp': e.preventDefault(); strum('up', 0.7); break;
    }
    switch (e.code) {
      case 'KeyB': strumPattern(); break;
      case 'KeyP': arpeggio(); break;
      case 'Escape': setChord([0, 0, 0, 0, 0, 0]); break;
    }
  });

  refresh();

  const year = document.getElementById('year');
  if (year) year.textContent = new Date().getFullYear();
})();
