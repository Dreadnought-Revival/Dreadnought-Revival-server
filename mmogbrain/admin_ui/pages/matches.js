// Matches: the history with filters, newest first; every row opens the match.
(() => {
  const { $, el, api, chip, when, duration, parseTime, fmt, modeName, fleetName, go, debounce, MODE_NAMES } = Admin;
  const PAGE = 50;
  let rows = []; let more = false; let cursor = ''; let extended = false; let seq = 0;

  const query = (extra) => {
    const p = new URLSearchParams({ limit: String(PAGE) });
    [['mode', 'mxMode'], ['status', 'mxStatus'], ['q', 'mxQ']].forEach(([k, id]) => { const v = $(id).value.trim(); if (v) p.set(k, v); });
    Object.entries(extra || {}).forEach(([k, v]) => p.set(k, v));
    return '/matches?' + p.toString();
  };

  const statusChip = (s) => chip(s, s === 'active' ? 'good' : s === 'forming' ? 'warn' : '');
  // What the match came to: PvP names the winning side; one-sided (co-op) matches say won/lost.
  function outcome(m) {
    if (!m.players || !m.outcome) return el('span', { class: 'muted' }, m.status === 'ended' ? 'no results reported' : '—');
    const teams = new Set((m.results || []).map((r) => r.team));
    if (m.outcome === 'draw') return chip('Draw');
    if (m.outcome === 'win') return chip(teams.size > 1 ? `Team ${m.winner_team} won` : 'Won', 'good');
    return el('span', { class: 'muted' }, 'unknown');
  }
  function draw() {
    const body = $('mxBody'); body.replaceChildren();
    if (!rows.length) body.append(el('tr', {}, el('td', { class: 'muted', colspan: 9 }, 'No match fits these filters.')));
    rows.forEach((m) => {
      const s = parseTime(m.started || m.created); const e = parseTime(m.ended);
      body.append(el('tr', { class: 'click', onclick: () => go('match', m.id) },
        el('td', {}, when(m.started || m.created)), el('td', {}, modeName(m.mode)), el('td', {}, fleetName(m.fleet_type)), el('td', {}, m.map || '—'),
        el('td', {}, statusChip(m.status)), el('td', { class: 'num' }, !isNaN(s) && !isNaN(e) ? duration((e - s) / 1000) : '—'),
        el('td', { class: 'num' }, m.players ? fmt(m.players) : '—'), el('td', {}, outcome(m)), el('td', { class: 'num' }, m.players ? fmt(m.kills) : '—')));
    });
    $('mxMore').classList.toggle('hidden', !more);
    $('mxCount').textContent = `${fmt(rows.length)} shown${more ? ', more available' : ''}`;
  }
  async function fetchFirst() {
    const mine = ++seq; const d = await api(query());
    if (mine !== seq) return;
    rows = d.matches || []; more = !!d.more; cursor = d.next_before || ''; extended = false; draw();
  }

  Admin.page({
    id: 'matches', title: 'Matches', group: 'Live', order: 40, auto: 30,
    html: `
      <div class="row">
        <select id="mxMode" aria-label="Mode"></select>
        <select id="mxStatus" aria-label="Status"><option value="">Any status</option><option value="forming">Forming</option><option value="active">Running</option><option value="ended">Ended</option></select>
        <input id="mxQ" type="search" placeholder="Name, player id or match id" maxlength="64" aria-label="Search">
        <button class="btn" id="mxReset" type="button">Reset</button>
        <span class="spacer"></span><span class="muted" id="mxCount"></span>
      </div>
      <div class="table-wrap"><table><thead><tr><th>Started</th><th>Mode</th><th>Fleet</th><th>Map</th><th>Status</th><th class="num">Length</th><th class="num">Players</th><th>Outcome</th><th class="num">Kills</th></tr></thead><tbody id="mxBody"></tbody></table></div>
      <div class="row" style="margin-top:10px"><button class="btn" id="mxMore" type="button">Load more</button></div>`,
    init() {
      const mode = $('mxMode');
      mode.append(el('option', { value: '' }, 'Any mode'));
      [...new Set(Object.keys(MODE_NAMES).concat(['Havoc']))].forEach((k) => mode.append(el('option', { value: k }, modeName(k) + (modeName(k) === k ? '' : ` (${k})`))));
      const change = () => fetchFirst().catch((e) => Admin.toast(e.message, true));
      mode.onchange = change; $('mxStatus').onchange = change; $('mxQ').oninput = debounce(change, 300);
      $('mxReset').onclick = () => { mode.value = ''; $('mxStatus').value = ''; $('mxQ').value = ''; change(); };
      $('mxMore').onclick = async () => {
        const d = await Admin.act(() => api(query({ before: cursor })), null, '');
        if (!d) return;
        rows = rows.concat(d.matches || []); more = !!d.more; cursor = d.next_before || ''; extended = true; draw();
      };
    },
    // An automatic refresh must not throw away pages the admin loaded.
    async load() { if (!extended) await fetchFirst(); },
  });
})();
