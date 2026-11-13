// Players: the account search (name or player id, filters, sort, paging) and
// who is online right now. A row opens the player page.
(() => {
  const { $, el, api, fmt, when, chip, table, playerLink, go, debounce, toCSV, download } = Admin;
  const PAGE = 50;
  let offset = 0; let appliedArg = null; let last = null;

  const params = () => {
    const p = new URLSearchParams({ q: $('plQ').value.trim(), sort: $('plSort').value, limit: String(PAGE), offset: String(offset) });
    if ($('plOnline').checked) p.set('online', '1');
    if ($('plBanned').checked) p.set('banned', '1');
    return p;
  };

  function status(p) {
    const s = [];
    if (p.online) s.push(chip('online', 'good'));
    if (p.banned) s.push(chip('banned', 'bad'));
    if (p.muted) s.push(chip('muted', 'warn'));
    return el('span', { class: 'plStatus' }, ...s);
  }

  function render(d) {
    last = d;
    const rows = d.players.map((p) => {
      const tr = el('tr', { class: 'click', onclick: () => go('player', p.pid) },
        el('td', {}, playerLink(p.pid, p.name || '?')),
        el('td', { class: 'num' }, fmt(p.rank)), el('td', { class: 'num' }, fmt(p.credits)), el('td', { class: 'num' }, fmt(p.premium)),
        el('td', {}, status(p)),
        el('td', {}, p.last_seen ? when(p.last_seen) : '—'), el('td', {}, p.created ? when(p.created) : '—'));
      return tr;
    });
    $('plTable').replaceChildren(table(['Name', { label: 'Rank', num: true }, { label: 'Credits', num: true }, { label: 'GP', num: true }, 'Status', 'Last active', 'Created'], rows,
      $('plQ').value.trim() ? 'No account matches.' : 'No accounts.'));
    const from = d.total ? d.offset + 1 : 0; const to = d.offset + d.players.length;
    $('plCount').textContent = `${fmt(from)}–${fmt(to)} of ${fmt(d.total)} accounts`;
    $('plPrev').disabled = d.offset <= 0; $('plNext').disabled = to >= d.total;
  }

  async function loadOnline() {
    const d = await api('/online');
    const rows = (d.players || []).map((p) => [playerLink(p.pid, p.name || '?'), p.status, p.match || '—', p.queued || '—',
      p.fleet ? (p.queued ? chip(p.fleet, 'good') : p.fleet) : '—', p.squad ? 'yes' : '—']);
    $('plOnlineList').replaceChildren(table(['Name', 'Status', 'In match', 'Queued', 'Fleet', 'Squad'], rows, 'Nobody is online.'));
    $('plOnlineHead').textContent = `Online now (${fmt(rows.length)})`;
  }

  async function search() {
    try { render(await api('/players?' + params())); } catch (e) { if (e.message !== 'forbidden') $('plCount').textContent = 'search failed: ' + e.message; }
  }

  Admin.page({
    id: 'players', title: 'Players', group: 'Players', order: 10, auto: 30,
    html: `
      <section><h2 id="plOnlineHead">Online now</h2><div id="plOnlineList"></div></section>
      <section><h2>Accounts</h2>
        <div class="row">
          <input id="plQ" type="search" placeholder="Name or player id" aria-label="Search name or player id">
          <label class="check"><input id="plOnline" type="checkbox"> Online only</label>
          <label class="check"><input id="plBanned" type="checkbox"> Banned only</label>
          <select id="plSort" aria-label="Sort">
            <option value="last_seen">Last active</option><option value="name">Name</option><option value="rank">Rank</option><option value="created">Newest account</option>
          </select>
          <span class="spacer"></span>
          <span class="muted" id="plCount"></span>
          <button class="btn small" id="plPrev">Previous</button><button class="btn small" id="plNext">Next</button>
          <button class="btn small" id="plCSV">Export page</button>
        </div>
        <div id="plTable"></div>
      </section>`,
    init() {
      const again = () => { offset = 0; search(); };
      $('plQ').oninput = debounce(again, 250);
      ['plOnline', 'plBanned', 'plSort'].forEach((id) => { $(id).onchange = again; });
      $('plPrev').onclick = () => { offset = Math.max(0, offset - PAGE); search(); };
      $('plNext').onclick = () => { offset += PAGE; search(); };
      $('plCSV').onclick = () => {
        if (!last) return;
        download('players.csv', toCSV([['pid', 'name', 'rank', 'credits', 'gp', 'online', 'banned', 'muted', 'last_active', 'created'],
          ...last.players.map((p) => [p.pid, p.name, p.rank, p.credits, p.premium, p.online, p.banned, p.muted, p.last_seen, p.created])]), 'text/csv');
      };
    },
    async load(arg) {
      // players/<text> from the sidebar search fills the box once per address.
      if (arg !== appliedArg) { appliedArg = arg; $('plQ').value = arg || ''; offset = 0; }
      await Promise.all([loadOnline(), search()]);
    },
  });
})();
