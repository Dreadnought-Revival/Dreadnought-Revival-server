// Game modes: one card per queue the game offers (team size, teams, matches
// of the last 7 days) and the matchmaking switches. Every edit is staged; one
// Save sends them together after a confirm (the route audits "game modes").
(() => {
  const { $, el, api, postJSON, fmt, chip, modeName, act } = Admin;
  let data = null; let ctl = [];

  // The server holds no per-mode on/off, map pool or fleet-tier setting; the
  // cards show only what /modes reports.
  const show = (key, v) => key === 'escalation_queue' ? (v ? modeName(v) + ' (' + v + ')' : 'no queue') : typeof v === 'boolean' ? (v ? 'listed' : 'hidden') : String(v);

  function dirtyList() { return ctl.filter((c) => c.changed()); }
  function refreshBar() {
    const d = dirtyList();
    const dirtyBoxes = new Set(d.map((c) => c.box));
    ctl.forEach((c) => c.box && c.box.classList.toggle('md-dirty', dirtyBoxes.has(c.box))); // several controls share a card
    $('md-bar').classList.toggle('hidden', !d.length);
    $('md-count').textContent = d.length ? `${d.length} unsaved change${d.length === 1 ? '' : 's'}` : '';
    $('md-list').textContent = d.map((c) => c.describe()).join(' · ');
  }

  // control: one editable setting. kind 'team' (queue team size) or 'flag'.
  function control(o) {
    const c = { ...o, reset: false };
    c.read = () => o.input.value;
    c.changed = () => (c.reset && o.dashboard) || ((o.input.value.trim() !== '' || o.input.tagName === 'SELECT') && o.input.value !== o.cur);
    c.value = () => o.fromInput(o.input.value);
    c.describe = () => (c.reset && o.dashboard) ? `${o.label}: back to the default` : `${o.label}: ${o.showCur} → ${o.showNew(o.input.value)}`;
    o.input.addEventListener('input', () => { c.reset = false; refreshBar(); });
    o.input.addEventListener('change', () => { c.reset = false; refreshBar(); });
    c.defaultLink = o.dashboard ? el('a', { class: 'link', onclick: () => { c.reset = true; o.input.value = o.envValue; refreshBar(); } }, 'use default') : null;
    return c;
  }

  function render(d) {
    data = d; ctl = [];
    const counts = d.matches_7d || {};
    const cards = d.queues.map((q) => {
      const ts = q.team_size;
      const box = el('div', { class: 'panel md-card' });
      let input = null; let teamCtl = null;
      if (ts) {
        input = el('input', { type: 'number', min: '1', max: String(d.max_team_size), step: '1', value: String(ts.value), 'aria-label': 'Max players per team, ' + q.queue });
        const c = control({ box, input, cur: String(ts.value), dashboard: ts.dashboard, envValue: String(ts.env), kind: 'team', mode: q.mode,
          label: `${modeName(q.queue)} team size`, showCur: String(ts.value), showNew: (v) => v, fromInput: (v) => Math.trunc(Number(v)) });
        ctl.push(c); box.dataset.i = ctl.length - 1; teamCtl = c;
      }
      // Bots: percent of the game's own values, per hosted mode (bot_tuning.go).
      let botRow = null;
      if (q.bots && q.runs === q.mode) {
        const lim = d.bot_limits || {};
        const fields = [['aim', 'Aim error'], ['reaction', 'Reaction time'], ['abilities', 'Ability use']];
        const inputs = {};
        const row = el('div', { class: 'md-bots' });
        fields.forEach(([key, text]) => {
          const [lo, hi] = lim[key] || [0, 500];
          const inp = el('input', { type: 'number', min: String(lo), max: String(hi), step: '5', value: String(q.bots[key]), 'aria-label': text + ' percent, ' + q.queue });
          inputs[key] = inp;
          ctl.push(control({ box, input: inp, cur: String(q.bots[key]), dashboard: false, kind: 'bot', mode: q.runs, field: key, inputs, lo, hi,
            label: `${modeName(q.queue)} bots, ${text.toLowerCase()}`, showCur: q.bots[key] + '%', showNew: (v) => v + '%', fromInput: (v) => Math.trunc(Number(v)) }));
          row.append(el('label', {}, text, inp, '%'));
        });
        const original = fields.every(([key]) => q.bots[key] === 100);
        botRow = el('div', { class: 'md-team' }, el('div', { class: 'muted' }, 'Bots (100% = the original game)'), row,
          el('span', { class: 'muted' }, original ? 'original' : ['set here ', el('a', { class: 'link', onclick: () => { fields.forEach(([key]) => { inputs[key].value = '100'; }); refreshBar(); } }, 'use original')]));
      } else if (q.bots) {
        botRow = el('div', { class: 'muted' }, 'Bots: tuned on the ' + modeName(q.runs) + ' card.');
      }
      const n = counts[q.mode] || 0;
      const isEsc = d.escalation_queue && d.escalation_queue.value === q.queue;
      box.append(
        el('div', { class: 'md-head' }, el('h3', {}, modeName(q.queue)), el('span', { class: 'muted mono' }, q.queue), el('span', { class: 'spacer' }),
          isEsc ? chip('Escalation runs here', 'warn') : null, q.runs !== q.queue ? chip('hosted as ' + modeName(q.runs)) : null),
        ts ? el('div', { class: 'md-team' }, el('label', {}, 'Max players per team', input),
          el('span', { class: 'muted' }, ts.dashboard ? ['set here (default ' + ts.env + ') ', teamCtl.defaultLink] : 'default')) : el('div', { class: 'muted' }, 'Lobby (Custom Game): not a queue, no team size.'),
        botRow,
        el('dl', { class: 'kv md-kv' }, el('dt', {}, 'Teams'), el('dd', {}, q.teams == null ? '—' : String(q.teams)),
          el('dt', {}, 'Max in a match'), el('dd', {}, q.max_players == null ? '—' : String(q.max_players)),
          el('dt', {}, 'Matches, 7 days'), el('dd', {}, fmt(n))));
      return box;
    });
    $('md-cards').replaceChildren(...(cards.length ? cards : [el('div', { class: 'empty' }, 'No queues.')]));

    const sw = $('md-switches'); sw.replaceChildren();
    const sel = (opts, value) => { const s = el('select', {}, opts.map(([v, t]) => el('option', { value: v }, t))); s.value = value; return s; };
    const flag = (key, label, input, fromInput, showNew) => {
      const s = d[key]; if (!s) return;
      const cur = typeof s.value === 'boolean' ? (s.value ? '1' : '0') : String(s.value);
      input.value = cur;
      const box = el('label', {}, label, input);
      const c = control({ box, input, cur, dashboard: s.dashboard, kind: 'flag', key, label, envValue: typeof s.env === 'boolean' ? (s.env ? '1' : '0') : String(s.env == null ? '' : s.env),
        showCur: show(key, s.value), showNew: (v) => show(key, fromInput(v)), fromInput });
      ctl.push(c);
      sw.append(el('div', { class: 'md-field' }, box, el('span', { class: 'muted' }, s.dashboard ? ['set here (default ' + show(key, s.env) + ') ', c.defaultLink] : 'default (environment)')));
    };
    flag('escalation_queue', 'Escalation runs on', sel([['', 'No queue (off)'], ...d.queueable.map((m) => [m, `${modeName(m)} (${m})`])], d.escalation_queue.value), (v) => v);
    flag('havoc', 'Havoc tile', sel([['1', 'Listed'], ['0', 'Hidden']], '1'), (v) => v === '1');
    flag('custom_game', 'Custom Game tile', sel([['1', 'Listed'], ['0', 'Hidden']], '1'), (v) => v === '1');
    flag('max_players', d.autoscale ? 'Max players per match' : 'Players per match (fixed)', el('input', { type: 'number', min: '1', max: String(d.max_players_max), step: '1' }), (v) => Math.trunc(Number(v)));
    flag('max_wait_s', 'Max wait for idle players (s)', el('input', { type: 'number', min: '0', max: String(d.max_wait_s_max), step: '1' }), (v) => Math.trunc(Number(v)));
    if (d.backfill_modes) {
      sw.append(el('div', { class: 'muted' }, d.backfill_window_s > 0 && d.backfill_modes.length
        ? 'Joining a running match: on for ' + d.backfill_modes.join(', ') + ', in the first ' + d.backfill_window_s + ' s of a match (DN_MATCH_BACKFILL_WINDOW / DN_MATCH_BACKFILL_MODES).'
        : 'Joining a running match: off (DN_MATCH_BACKFILL_WINDOW / DN_MATCH_BACKFILL_MODES).'));
    }
    if (!sw.children.length) sw.append(el('div', { class: 'muted' }, 'The matchmaker is not running: its switches are not available.'));
    refreshBar();
  }

  async function save() {
    const body = {}; const team_sizes = {}; const reset = []; const bot_tuning = {};
    const bad = [];
    dirtyList().forEach((c) => {
      const resetting = c.reset && c.dashboard;
      if (c.kind === 'bot') { // a mode's three values go together
        const v = {}; Object.keys(c.inputs).forEach((k) => { v[k] = Math.trunc(Number(c.inputs[k].value)); });
        bot_tuning[c.mode] = v;
        if (!(c.value() >= c.lo && c.value() <= c.hi)) bad.push(c.label + ': ' + c.lo + ' to ' + c.hi);
        return;
      }
      if (c.kind === 'team') { team_sizes[c.mode] = resetting ? null : c.value(); if (!resetting && !(c.value() >= 1 && c.value() <= data.max_team_size)) bad.push(c.label + ': 1 to ' + data.max_team_size); }
      else if (resetting) reset.push(c.key);
      else {
        body[c.key] = c.value();
        if (c.key === 'max_players' && !(body[c.key] >= 1 && body[c.key] <= data.max_players_max)) bad.push('players per match: 1 to ' + data.max_players_max);
        if (c.key === 'max_wait_s' && !(body[c.key] >= 0 && body[c.key] <= data.max_wait_s_max)) bad.push('max wait: 0 to ' + data.max_wait_s_max);
      }
    });
    if (bad.length) { $('md-msg').className = 'err'; $('md-msg').textContent = 'Out of range: ' + bad.join('; '); return; }
    if (Object.keys(team_sizes).length) body.team_sizes = team_sizes;
    if (Object.keys(bot_tuning).length) body.bot_tuning = bot_tuning;
    if (reset.length) body.reset = reset;
    if (!dirtyList().length) return;
    if (!confirm('Change these settings? They apply at once to matchmaking; bot settings apply to matches started from now on.\n\n' + dirtyList().map((c) => c.describe()).join('\n'))) return;
    const d = await act(() => postJSON('/modes', body), 'md-msg', 'Saved. A tile shows or hides at each player\'s next login.');
    if (d) render(d);
  }

  Admin.page({
    id: 'modes', title: 'Game modes', group: 'Game', order: 10,
    html: `<style>
      #page-modes .md-cards { display: grid; gap: 12px; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); }
      #page-modes .md-card { margin: 0; }
      #page-modes .md-card.md-dirty, #page-modes .md-field label.md-dirty { border-color: var(--warn); box-shadow: 0 0 0 1px var(--warn); }
      #page-modes .md-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
      #page-modes .md-team { display: grid; gap: 4px; margin-bottom: 8px; }
      #page-modes .md-team label { display: flex; gap: 8px; align-items: center; color: var(--muted); font-size: 12px; }
      #page-modes .md-team input { width: 80px; }
      #page-modes .md-bots { display: flex; gap: 10px; flex-wrap: wrap; }
      #page-modes .md-bots input { width: 64px; }
      #page-modes .md-kv { font-size: 13px; }
      #page-modes .md-field { display: grid; gap: 4px; }
      #page-modes .md-field label { padding: 2px; border: 1px solid transparent; border-radius: 7px; }
      #page-modes .md-bar { position: sticky; bottom: 0; z-index: 2; display: flex; gap: 10px; align-items: center; flex-wrap: wrap; padding: 10px 14px; background: var(--panel); border: 1px solid var(--warn); border-radius: 10px; box-shadow: 0 -2px 8px rgba(0,0,0,.12); }
    </style>
    <div class="note">The queues the game offers, the mode their matches are hosted in, and how many players a team takes. Co-op modes put every player on one team (bots fill the rest); PvP modes have two. A match never goes over "Players per match" below. In PvP modes the team size (default 5, the original 5 against 5) also goes to the battle server, which fills each side with bots up to it, so a side always has that many ships. In co-op modes only a size set here goes to the battle server; on the default it keeps its own count (8 ships a side). Large teams are untested: every bot is a full ship the battle server has to run. Bots: three percentages per hosted mode scale the game's own bot values on the battle server (needs the battle-server DLL from 2026-11-16). Lower aim error = more accurate, lower reaction time = faster, higher ability use = abilities fired more readily; 100% everywhere is the original game.</div>
    <section><h2>Queues</h2><div class="md-cards" id="md-cards"></div></section>
    <section class="manage"><h2>Matchmaking switches</h2>
      <div class="fields" id="md-switches"></div>
      <div class="muted">Applies at once to matchmaking. A tile shows or hides at each player's next login. Matches counted by the mode they were hosted in.</div>
    </section>
    <div class="md-bar hidden" id="md-bar"><b id="md-count"></b><span class="muted" id="md-list"></span><span class="spacer"></span>
      <button class="btn" id="md-discard">Discard</button><button class="btn primary" id="md-save">Save changes</button></div>
    <p id="md-msg"></p>`,
    init() { $('md-save').onclick = save; $('md-discard').onclick = () => { render(data); $('md-msg').textContent = ''; }; },
    async load() {
      const d = await api('/modes');
      if (data && dirtyList().length) return; // keep staged edits across refresh
      render(d);
    },
  });
})();
