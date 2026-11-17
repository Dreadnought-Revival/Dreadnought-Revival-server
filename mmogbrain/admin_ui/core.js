// Server Admin core: sign-in, navigation, refresh, and the helpers every
// page uses. A page is one file in pages/ that calls Admin.page({...}); the
// server lists that folder, so dropping a file in adds the page.
//
//   Admin.page({
//     id: 'chat',              // also the address: #/chat or #/chat/<arg>
//     title: 'Chat',           // or title: (arg) => 'Player ' + arg
//     group: 'Live',           // sidebar group (GROUPS below); omit with hidden
//     order: 40,               // position inside the group
//     hidden: true,            // no sidebar entry (detail pages)
//     parent: 'players',       // sidebar entry to light up for a hidden page
//     html: `<section>…</section>`,   // static markup, written by us
//     init(root) {},           // once, after the markup is in the document
//     async load(arg) {},      // every refresh; fetch and render
//     auto: 10,                // seconds between refreshes, or () => seconds; 0/absent = manual
//   });
//
// Every value from the server goes into the page as TEXT (el(), textContent),
// never as HTML. Only a page's own static `html` template is parsed.
window.Admin = (() => {
  'use strict';
  const GROUPS = ['Live', 'Players', 'Game', 'Economy', 'Insight', 'System'];
  const $ = (id) => document.getElementById(id);
  const store = {
    get(k) { try { return sessionStorage.getItem(k); } catch (e) { return null; } },
    set(k, v) { try { sessionStorage.setItem(k, v); } catch (e) {} },
    del(k) { try { sessionStorage.removeItem(k); } catch (e) {} },
  };

  let key = store.get('adminKey');
  const pages = new Map();
  let current = null; let currentArg = ''; let timer = null; let paused = false; let busy = false; let started = false;

  // --- server calls ---------------------------------------------------------
  // api returns the parsed JSON reply. A refused key locks the dashboard and
  // throws Error('forbidden'); any other failure throws the server's message.
  async function api(path, opts = {}) {
    const res = await fetch('/admin/api' + path, { ...opts, headers: { 'X-Admin-Key': key || '', ...(opts.headers || {}) } });
    if (res.status === 403) { lock('That key was not accepted.'); throw new Error('forbidden'); }
    const text = await res.text();
    if (!res.ok) { let m = text; try { m = JSON.parse(text).error || text; } catch (e) {} throw new Error(m.trim() || res.statusText); }
    return text ? JSON.parse(text) : {};
  }
  // apiText: like api, for an endpoint that answers plain text (a file).
  async function apiText(path) {
    const res = await fetch('/admin/api' + path, { headers: { 'X-Admin-Key': key || '' } });
    if (res.status === 403) { lock('That key was not accepted.'); throw new Error('forbidden'); }
    const text = await res.text();
    if (!res.ok) { let m = text; try { m = JSON.parse(text).error || text; } catch (e) {} throw new Error(m.trim() || res.statusText); }
    return { text, name: ((res.headers.get('Content-Disposition') || '').match(/filename="([^"]+)"/) || [])[1] || 'download.txt' };
  }
  const postJSON = (path, body) => api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });

  // --- building the page ----------------------------------------------------
  function el(tag, attrs = {}, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === 'class') e.className = v;
      else if (k.startsWith('on') && typeof v === 'function') e[k] = v;
      else e.setAttribute(k, v === true ? '' : v);
    }
    for (const k of kids.flat()) if (k != null && k !== false) e.append(k instanceof Node ? k : String(k));
    return e;
  }
  const NS = 'http://www.w3.org/2000/svg';
  function svg(tag, attrs = {}, ...kids) {
    const e = document.createElementNS(NS, tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    for (const k of kids) if (k != null) e.append(k instanceof Node ? k : document.createTextNode(String(k)));
    return e;
  }
  const fmt = (n) => (n ?? 0).toLocaleString();
  const compact = (n) => { const a = Math.abs(n); return a >= 1e9 ? (n / 1e9).toFixed(1) + 'B' : a >= 1e6 ? (n / 1e6).toFixed(a >= 1e7 ? 0 : 1) + 'M' : a >= 1e4 ? Math.round(n / 1e3) + 'k' : String(n); };
  // The server writes times as RFC 3339 or as SQLite's "YYYY-MM-DD HH:MM:SS"
  // (UTC); a number is Unix seconds.
  const parseTime = (v) => {
    if (v == null || v === '' || v === 0) return NaN;
    if (typeof v === 'number') return v * 1000;
    return Date.parse(v.includes('T') ? v : v.replace(' ', 'T') + 'Z');
  };
  const ago = (iso) => {
    if (!iso) return '';
    const t = parseTime(iso);
    if (isNaN(t)) return String(iso);
    const s = Math.max(0, (Date.now() - t) / 1000);
    if (s < 90) return Math.round(s) + ' s';
    if (s < 5400) return Math.round(s / 60) + ' min';
    if (s < 172800) return Math.round(s / 3600) + ' h';
    return Math.round(s / 86400) + ' d';
  };
  // when: the admin's local date and time, with the exact value on hover.
  const when = (iso) => {
    const t = parseTime(iso);
    if (isNaN(t)) return el('span', { class: 'muted' }, iso ? String(iso) : '—');
    const d = new Date(t);
    const text = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
    return el('span', { title: d.toISOString().replace('T', ' ').slice(0, 19) + ' UTC' }, text);
  };
  const duration = (seconds) => {
    const s = Math.max(0, Math.round(seconds));
    if (s < 90) return s + ' s';
    if (s < 5400) return Math.round(s / 60) + ' min';
    const mins = Math.round(s / 60); const h = Math.floor(mins / 60); const m = mins % 60;
    return h < 48 ? `${h} h ${m} min` : `${Math.round(s / 86400)} d`;
  };
  const bytes = (n) => { const u = ['B', 'kB', 'MB', 'GB', 'TB']; let i = 0; let v = n || 0; while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; } return (i && v < 100 ? v.toFixed(1) : Math.round(v)) + ' ' + u[i]; };

  // card: a stat tile. sub is an optional line under the value.
  const card = (k, v, small, sub) => el('div', { class: 'card' }, el('div', { class: 'k' }, k), el('div', { class: 'v' + (small ? ' small' : '') }, v), sub ? el('div', { class: 'sub' }, sub) : null);
  // chip(text, 'good' | 'warn' | 'bad' | '')
  const chip = (text, kind) => el('span', { class: 'chip' + (kind ? ' ' + kind : '') }, text);
  // fill: put rows into the <tbody id>, or one "empty" line spanning cols.
  const fill = (id, rows, empty, cols) => {
    const t = typeof id === 'string' ? $(id) : id; t.replaceChildren();
    if (!rows.length) t.append(el('tr', {}, el('td', { class: 'muted', colspan: cols }, empty)));
    else rows.forEach((r) => t.append(r));
  };
  // table: a whole table from data. cols: ['Name', { label: 'Kills', num: true }, …];
  // rows: arrays of cells (text or nodes). Returns the .table-wrap element.
  function table(cols, rows, empty) {
    const defs = cols.map((c) => (typeof c === 'string' ? { label: c } : c));
    const body = el('tbody');
    if (!rows.length) body.append(el('tr', {}, el('td', { class: 'muted', colspan: defs.length }, empty || 'Nothing to show.')));
    rows.forEach((r) => body.append(r instanceof Node ? r : el('tr', {}, r.map((c, i) => el('td', { class: [defs[i] && defs[i].num ? 'num' : '', defs[i] && defs[i].wrap ? 'wrap' : ''].join(' ').trim() || null }, c == null || c === '' ? '—' : c)))));
    return el('div', { class: 'table-wrap' }, el('table', {}, el('thead', {}, el('tr', {}, defs.map((c) => el('th', { class: c.num ? 'num' : null }, c.label)))), body));
  }
  // kv: a definition list from [label, value] pairs (empty values are skipped).
  const kv = (pairs) => el('dl', { class: 'kv' }, pairs.filter((p) => p && p[1] != null && p[1] !== '').flatMap(([k, v]) => [el('dt', {}, k), el('dd', {}, v)]));
  // tabs: sub tabs inside a page. defs: [{ id, label }]; onPick(id) renders.
  // Returns { pick(id), current() }.
  function tabs(host, defs, onPick) {
    let cur = null;
    const bar = el('div', { class: 'subtabs' }, defs.map((d) => el('button', { type: 'button', 'data-id': d.id, onclick: () => pick(d.id) }, d.label)));
    function pick(id) {
      cur = defs.some((d) => d.id === id) ? id : defs[0].id;
      bar.querySelectorAll('button').forEach((b) => b.classList.toggle('on', b.dataset.id === cur));
      onPick(cur);
    }
    host.replaceChildren(bar);
    return { pick, current: () => cur };
  }
  function toast(text, bad) {
    const t = el('div', { class: 'toast' + (bad ? ' err' : '') }, text);
    $('toasts').append(t); setTimeout(() => t.remove(), bad ? 7000 : 3500);
  }
  // act: run one admin action and report it. where is an element (or id) for
  // the message, or null for a toast. Returns the reply, or undefined on failure.
  async function act(fn, where, okText) {
    const m = typeof where === 'string' ? $(where) : where;
    const say = (text, good) => { if (m) { m.className = good ? 'ok' : 'err'; m.textContent = text; } else if (text) toast(text, !good); };
    try { const d = await fn(); say(typeof okText === 'function' ? okText(d) : (okText || 'Done.'), true); return d; }
    catch (e) { if (e.message !== 'forbidden') say(String(e.message || e), false); return undefined; }
  }
  const debounce = (fn, ms = 250) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; };
  const toCSV = (rows) => rows.map((r) => r.map((c) => { const s = c == null ? '' : String(c); return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s; }).join(',')).join('\n');
  function download(name, text, mime) {
    const a = el('a', { href: URL.createObjectURL(new Blob([text], { type: mime || 'text/plain' })), download: name });
    document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }

  // --- names the pages share -------------------------------------------------
  const MODE_NAMES = { TDM: 'Team Deathmatch', TurboTDM: 'Turbo TDM', PodTDM: 'Ceres Awakens', TE: 'Team Elimination', TER: 'Conquest', Territory: 'Conquest', BC: 'Bootcamp', TM: 'Training Match', Onslaught: 'Onslaught' };
  const modeName = (m) => MODE_NAMES[m] || m || '?';
  const FLEET_NAMES = ['?', 'Recruit', 'Veteran', 'Legendary'];
  const fleetName = (n) => FLEET_NAMES[n] || '?';
  // Audit details are JSON; nested values (price and tech tree changes, game
  // mode switches) are spelled out instead of printing "[object Object]".
  const auditValue = (v) => {
    if (v === null || v === undefined) return '';
    if (typeof v !== 'object') return String(v);
    if (Array.isArray(v)) return v.map(auditValue).filter((x) => x !== '').join('; ');
    if ('from' in v && 'to' in v) {
      const what = ['name', 'field'].filter((k) => v[k] !== undefined && v[k] !== '').map((k) => auditValue(v[k])).join(' ');
      const extra = Object.entries(v).filter(([k, x]) => !['from', 'to', 'name', 'field', 'item'].includes(k) && x !== null && x !== '')
        .map(([k, x]) => `${k} ${auditValue(x)}`).join(', ');
      return `${what ? what + ': ' : ''}${auditValue(v.from) || '—'} → ${auditValue(v.to) || '—'}${extra ? ` (${extra})` : ''}`;
    }
    return Object.entries(v).filter(([, x]) => x !== null && x !== '').map(([k, x]) => `${k} ${auditValue(x)}`).join(', ');
  };
  const auditText = (raw) => {
    try { return Object.entries(JSON.parse(raw)).map(([k, v]) => [k, auditValue(v)]).filter(([, v]) => v !== '').map(([k, v]) => `${k}: ${v}`).join(' · '); } catch (e) { return raw; }
  };

  // --- charts ---------------------------------------------------------------
  const SERIES = [1, 2, 3, 4, 5, 6, 7, 8].map((i) => `var(--s${i})`);
  // niceMax: the axis top for four grid steps, each step a round number
  // (1, 2, 2.5 or 5 x 10^k; 2.5 only from 25 up, so counts never get
  // fractional ticks).
  const niceMax = (v) => {
    const raw = v / 4; if (raw <= 1) return 4;
    const p = Math.pow(10, Math.floor(Math.log10(raw)));
    return 4 * [1, 2, 2.5, 5, 10].map((m) => m * p).find((st) => st >= raw && Number.isInteger(st));
  };
  // chart draws series over shared x labels: bars (opts.bars, one series) or
  // lines. series: [{ name, values, color?, unit? }]; colours default to the
  // palette in series order. Hovering shows the values at that x.
  // opts: bars, height, maxLabels, short(label) for the axis text.
  function chart(target, title, labels, series, opts = {}) {
    const box = typeof target === 'string' ? $(target) : target;
    // Drawn at the box's own width, so text and strokes are never scaled
    // (640 while the page is still hidden and has no width).
    const W = Math.max(300, Math.round(box.clientWidth - 30) || 640); const H = opts.height || 190; const L = 44; const R = 12; const T = 10; const B = 24;
    series = series.map((s, i) => ({ ...s, values: s.values || [], color: s.color || SERIES[i % SERIES.length] }));
    const max = niceMax(Math.max(1, ...series.flatMap((s) => s.values)));
    const pw = W - L - R; const ph = H - T - B; const n = Math.max(1, labels.length);
    const x = (i) => L + (opts.bars ? (i + 0.5) * pw / n : (n === 1 ? pw / 2 : i * pw / (n - 1)));
    const y = (v) => T + ph - (v / max) * ph;
    const g = svg('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': title });
    for (let k = 0; k <= 4; k++) {
      const v = max * k / 4;
      g.append(svg('line', { x1: L, x2: W - R, y1: y(v), y2: y(v), class: 'grid-line' }), svg('text', { x: L - 6, y: y(v) + 3, 'text-anchor': 'end', class: 'axis' }, compact(Math.round(v * 10) / 10)));
    }
    const step = Math.max(1, Math.ceil(n / Math.min(opts.maxLabels || 10, Math.floor(pw / 56))));
    // A line chart's last point sits on the right edge: its label ends there.
    labels.forEach((lb, i) => { if (i % step === 0) g.append(svg('text', { x: x(i), y: H - 6, 'text-anchor': !opts.bars && n > 1 && i === n - 1 ? 'end' : 'middle', class: 'axis' }, opts.short ? opts.short(lb) : lb)); });
    const band = svg('rect', { class: 'band', y: T, height: ph, x: 0, width: 0, visibility: 'hidden' });
    const cross = svg('line', { class: 'cross', y1: T, y2: T + ph, x1: 0, x2: 0, visibility: 'hidden' });
    g.append(band, cross);
    if (opts.bars) {
      const s = series[0]; const bw = Math.min(24, Math.max(2, pw / n - 2));
      s.values.forEach((v, i) => {
        const h = Math.max(0, T + ph - y(v)); const r = Math.min(4, bw / 2, h);
        // Rounded at the data end, square on the baseline.
        g.append(svg('path', { d: `M${x(i) - bw / 2},${T + ph} v${-(h - r)} q0,${-r} ${r},${-r} h${bw - 2 * r} q${r},0 ${r},${r} v${h - r} z`, style: `fill:${s.color}` }));
      });
    } else {
      series.forEach((s) => {
        if (!s.values.length) return;
        g.append(svg('polyline', { points: s.values.map((v, i) => `${x(i)},${y(v)}`).join(' '), style: `fill:none;stroke:${s.color};stroke-width:2;stroke-linejoin:round;stroke-linecap:round` }));
      });
    }
    const dots = series.map((s) => { const c = svg('circle', { r: 4, class: 'ring', style: `fill:${s.color}`, visibility: 'hidden' }); if (!opts.bars) g.append(c); return c; });
    const tip = el('div', { class: 'tip hidden' });
    const hit = svg('rect', { x: L, y: T, width: pw, height: ph, fill: 'transparent' });
    const hide = () => { tip.classList.add('hidden'); [band, cross, ...dots].forEach((e) => e.setAttribute('visibility', 'hidden')); };
    hit.addEventListener('pointermove', (ev) => {
      if (!labels.length) return;
      const rect = g.getBoundingClientRect(); const px = (ev.clientX - rect.left) / rect.width * W;
      const i = Math.max(0, Math.min(n - 1, opts.bars ? Math.floor((px - L) / (pw / n)) : Math.round((px - L) / (n === 1 ? pw : pw / (n - 1)))));
      if (opts.bars) { band.setAttribute('x', L + i * pw / n); band.setAttribute('width', pw / n); band.setAttribute('visibility', 'visible'); }
      else {
        cross.setAttribute('x1', x(i)); cross.setAttribute('x2', x(i)); cross.setAttribute('visibility', 'visible');
        series.forEach((s, k) => { if (s.values[i] == null) return; dots[k].setAttribute('cx', x(i)); dots[k].setAttribute('cy', y(s.values[i])); dots[k].setAttribute('visibility', 'visible'); });
      }
      tip.replaceChildren(el('b', {}, labels[i]), ...series.filter((s) => s.values[i] != null).map((s) => { const sw = el('i'); sw.style.background = s.color; return el('span', {}, sw, `${series.length > 1 || s.name ? (s.name || '') + ' ' : ''}${fmt(s.values[i])}${s.unit || ''}`); }));
      tip.classList.remove('hidden');
      const left = x(i) / W * rect.width; const flip = left > rect.width * 0.6;
      tip.style.left = flip ? '' : `${left + 24}px`; tip.style.right = flip ? `${rect.width - left + 12}px` : '';
      tip.style.top = '34px';
    });
    hit.addEventListener('pointerleave', hide);
    g.append(hit);
    const legend = series.length > 1 ? el('div', { class: 'legend' }, ...series.map((s) => { const i = el('i'); i.style.background = s.color; return el('span', {}, i, s.name); })) : null;
    box.classList.add('chart');
    box.replaceChildren(...[el('h3', {}, title), g, legend, tip].filter(Boolean));
  }
  // bars: a ranked list as horizontal bars. rows: [{ label, value, text? }]
  // (label may be a node). Returns the element; put it in a .chart or .panel.
  function bars(rows, opts = {}) {
    const max = Math.max(1, ...rows.map((r) => r.value));
    const box = el('div', { class: 'bars' });
    if (!rows.length) return el('div', { class: 'muted' }, opts.empty || 'Nothing to show.');
    rows.forEach((r) => {
      const i = el('i'); i.style.width = `${Math.max(0, r.value) / max * 100}%`; if (!(r.value > 0)) i.style.minWidth = '0'; if (opts.color) i.style.background = opts.color;
      box.append(el('span', {}, r.label), el('span', { class: 'track' }, i), el('span', { class: 'n' }, r.text != null ? r.text : fmt(r.value)));
    });
    return box;
  }
  // meter: a usage bar (0..1); turns amber above 0.8 and red above 0.92.
  function meter(share) {
    const i = el('i'); i.style.width = `${Math.max(0, Math.min(1, share)) * 100}%`;
    return el('div', { class: 'meter' + (share > 0.92 ? ' bad' : share > 0.8 ? ' warn' : '') }, i);
  }

  // --- navigation -----------------------------------------------------------
  function page(def) { pages.set(def.id, def); }
  const href = (id, arg) => '#/' + id + (arg != null && arg !== '' ? '/' + encodeURIComponent(arg) : '');
  function go(id, arg) { if (location.hash === href(id, arg)) show(); else location.hash = href(id, arg); }
  // link: an in-dashboard link to a page.
  const link = (text, id, arg) => el('a', { class: 'link', href: href(id, arg) }, text);
  const playerLink = (pid, name) => (pid ? link(name || pid.slice(0, 8) + '…', 'player', pid) : el('span', {}, name || '?'));
  function route() {
    const m = location.hash.match(/^#\/([\w-]+)(?:\/(.*))?$/);
    const id = m && pages.has(m[1]) ? m[1] : 'overview';
    let arg = '';
    if (m && m[2]) { try { arg = decodeURIComponent(m[2]); } catch (e) { arg = m[2]; } }
    return { id, arg };
  }
  function buildNav() {
    const nav = $('nav'); nav.replaceChildren();
    GROUPS.forEach((g) => {
      const items = [...pages.values()].filter((p) => !p.hidden && p.group === g).sort((a, b) => (a.order || 0) - (b.order || 0));
      if (!items.length) return;
      nav.append(el('h4', {}, g));
      items.forEach((p) => nav.append(el('a', { href: href(p.id), 'data-page': p.id }, el('span', {}, typeof p.title === 'function' ? p.title('') : p.title), el('span', { class: 'badge hidden', id: 'badge-' + p.id }))));
    });
  }
  // setBadge: a count beside a sidebar entry; 0 or '' removes it.
  function setBadge(id, n, quiet) {
    const b = $('badge-' + id); if (!b) return;
    b.textContent = n ? String(n) : ''; b.classList.toggle('hidden', !n); b.classList.toggle('quiet', !!quiet);
  }
  function mount(def) {
    if (def.root) return;
    def.root = el('div', { class: 'page hidden', id: 'page-' + def.id });
    def.root.innerHTML = def.html || '';
    $('main').append(def.root);
    if (def.init) def.init(def.root);
  }
  function show() {
    if (!key || !started) return;
    const { id, arg } = route();
    const def = pages.get(id) || pages.values().next().value;
    if (!def) return;
    mount(def);
    current = def; currentArg = arg;
    pages.forEach((p) => p.root && p.root.classList.toggle('hidden', p !== def));
    const lit = def.hidden ? def.parent : def.id;
    document.querySelectorAll('#nav a').forEach((a) => a.classList.toggle('on', a.dataset.page === lit));
    setTitle(typeof def.title === 'function' ? def.title(arg) : def.title);
    $('app').classList.remove('menu');
    window.scrollTo(0, 0);
    refresh(); schedule();
  }
  function setTitle(text) { $('pageTitle').textContent = text; document.title = text + ' · Server Admin'; }
  async function refresh() {
    if (!current || busy) return;
    const def = current; busy = true;
    try {
      if (def.load) await def.load(currentArg);
      $('updated').textContent = 'updated ' + new Date().toLocaleTimeString();
    } catch (e) {
      if (e.message !== 'forbidden') { $('updated').textContent = 'update failed: ' + e.message; console.error(e); }
    } finally { busy = false; }
  }
  // schedule: (re)start the automatic refresh; a page calls it when something
  // its `auto` depends on changed (a search box, a "follow" checkbox).
  function schedule() {
    clearInterval(timer); timer = null;
    if (!current || paused) return;
    const s = typeof current.auto === 'function' ? current.auto(currentArg) : current.auto;
    if (s > 0) timer = setInterval(() => { if (!document.hidden) refresh(); }, s * 1000);
  }

  // --- sign-in --------------------------------------------------------------
  function lock(msg) {
    key = null; store.del('adminKey'); clearInterval(timer);
    $('app').classList.add('hidden'); $('login').classList.remove('hidden');
    $('loginErr').textContent = msg || ''; $('keyInput').value = ''; $('keyInput').focus();
  }
  function unlock() { $('login').classList.add('hidden'); $('app').classList.remove('hidden'); show(); }

  // --- find a player (sidebar) ---------------------------------------------
  function initJump() {
    const box = $('jump'); const list = $('jumpList'); let seq = 0;
    const close = () => { list.classList.add('hidden'); list.replaceChildren(); };
    const pick = (pid) => { close(); box.value = ''; go('player', pid); };
    const search = debounce(async () => {
      const q = box.value.trim(); const mine = ++seq;
      if (q.length < 2) { close(); return; }
      let d; try { d = await api('/players?q=' + encodeURIComponent(q)); } catch (e) { return; }
      if (mine !== seq) return;
      const rows = (d.players || []).slice(0, 8);
      list.replaceChildren(...(rows.length ? rows.map((p, i) => el('a', { class: i ? '' : 'on', 'data-pid': p.pid, onclick: () => pick(p.pid) }, el('span', { class: 'dot ' + (p.banned ? 'bad' : p.online ? 'good' : '') }), el('span', {}, p.name || '?'), el('span', { class: 'spacer' }), el('span', { class: 'muted' }, 'rank ' + fmt(p.rank))))
        : [el('div', { class: 'none' }, 'No account matches.')]));
      list.classList.remove('hidden');
    }, 220);
    box.oninput = search;
    box.onkeydown = (e) => {
      const items = [...list.querySelectorAll('a')]; const at = items.findIndex((a) => a.classList.contains('on'));
      if (e.key === 'Escape') { close(); box.blur(); }
      else if (e.key === 'Enter') { if (items[at]) pick(items[at].dataset.pid); else if (box.value.trim()) { const q = box.value.trim(); close(); box.value = ''; go('players', q); } }
      else if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && items.length) {
        e.preventDefault(); const next = (at + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length;
        items.forEach((a, i) => a.classList.toggle('on', i === next));
      }
    };
    box.onblur = () => setTimeout(close, 180);
    document.addEventListener('keydown', (e) => { if (e.key === '/' && !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)) { e.preventDefault(); box.focus(); } });
  }

  function start() {
    started = true;
    buildNav(); initJump();
    $('loginForm').onsubmit = (e) => { e.preventDefault(); key = $('keyInput').value; store.set('adminKey', key); unlock(); };
    $('logoutBtn').onclick = () => lock('');
    $('refreshBtn').onclick = () => refresh();
    $('pauseBtn').onclick = () => { paused = !paused; $('pauseBtn').textContent = paused ? 'Paused' : 'Live'; $('pauseBtn').classList.toggle('on', !paused); schedule(); };
    $('pauseBtn').classList.add('on');
    $('menuBtn').onclick = () => $('app').classList.toggle('menu');
    $('themeBtn').onclick = () => {
      const cur = document.documentElement.dataset.theme || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
      const next = cur === 'dark' ? 'light' : 'dark';
      document.documentElement.dataset.theme = next;
      try { localStorage.setItem('adminTheme', next); } catch (e) {}
    };
    window.addEventListener('hashchange', show);
    document.addEventListener('visibilitychange', () => { if (!document.hidden && timer) refresh(); });
    if (key) unlock(); else lock('');
  }

  return {
    $, el, svg, api, apiText, postJSON, fmt, compact, ago, when, duration, bytes, parseTime, card, chip, fill, table, kv, tabs, toast, act, debounce, toCSV, download,
    MODE_NAMES, modeName, FLEET_NAMES, fleetName, auditText, SERIES, chart, bars, meter,
    page, go, link, playerLink, href, setBadge, setTitle, refresh, schedule, start,
    arg: () => currentArg,
  };
})();
