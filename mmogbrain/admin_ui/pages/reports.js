// Bug reports the game clients send (YA_LogSpecial): the most frequent
// report names grouped, and the newest reports with their details text.
// Route: #/reports or #/reports/<pid> (one player).
(() => {
  const { $, el, api, fmt, when, ago, card, chip, table, bars, playerLink, debounce, toCSV, download } = Admin;
  const PAGE = 150;
  let pid = ''; let lastArg = null; let rows = []; let more = false; let wanted = PAGE;

  const query = (extra) => {
    const p = new URLSearchParams(extra || {});
    if ($('rpQ').value.trim()) p.set('q', $('rpQ').value.trim());
    if ($('rpType').value) p.set('type', $('rpType').value);
    if (pid) p.set('pid', pid);
    return p;
  };

  function renderGroups(d) {
    const top = d.groups.slice(0, 12);
    const rowsOut = top.map((g) => ({
      label: el('a', { class: 'link', title: 'Show only this report', onclick: () => { $('rpQ').value = g.name; $('rpType').value = g.type; wanted = PAGE; Admin.refresh(); } },
        g.name || '(no name)'),
      value: g.count, text: `${fmt(g.count)} · ${ago(g.newest)} ago`,
    }));
    const parts = [el('h3', {}, 'Most frequent reports'), d.groups.length ? bars(rowsOut) : el('div', { class: 'muted' }, 'No reports match.')];
    if (d.groups.length > 12) parts.push(el('div', { class: 'muted' }, `${d.groups.length - 12} less frequent kinds not shown.`));
    $('rpGroups').replaceChildren(...parts);
    const types = {};
    d.groups.forEach((g) => { types[g.type] = (types[g.type] || 0) + g.count; });
    $('rpTypes').replaceChildren(el('h3', {}, 'By type'), bars(Object.entries(types).sort((a, b) => b[1] - a[1]).map(([t, n]) => ({ label: t || '(none)', value: n })),
      { color: 'var(--s2)', empty: 'No reports match.' }));
  }

  function renderList() {
    const body = rows.map((r) => {
      const first = (r.details || '').split('\n')[0];
      const detail = r.details ? el('details', {}, el('summary', {}, first.length > 90 ? first.slice(0, 90) + '…' : first || '(empty)'), el('pre', { class: 'log' }, r.details)) : '—';
      return [when(r.when), r.pid ? playerLink(r.pid, r.player) : (r.player || '?'), chip(r.type || '?'), r.name, detail];
    });
    $('rpTable').replaceChildren(table(['When', 'Player', 'Type', 'Report', 'Details'], body, 'No reports with these filters.'));
    $('rpMore').classList.toggle('hidden', !more);
  }

  async function load(arg) {
    if (arg !== lastArg) { lastArg = arg; pid = arg || ''; wanted = PAGE; }
    const d = await api('/reports?' + query({ limit: String(Math.min(500, wanted)) }));
    const sel = $('rpType'); const keep = sel.value;
    if (sel.options.length - 1 !== d.types.length) { sel.replaceChildren(el('option', { value: '' }, 'All types'), ...d.types.map((t) => el('option', { value: t }, t))); sel.value = keep; }
    rows = d.reports; more = d.more;
    $('rpScope').replaceChildren(...(pid ? [el('span', {}, 'Player ', playerLink(pid, rows[0] && rows[0].player), ` · ${fmt(d.total)} reports`),
      el('button', { class: 'btn small', onclick: () => { if (Admin.arg()) Admin.go('reports'); else { pid = ''; Admin.refresh(); } } }, 'Show everyone')] : []));
    $('rpScope').classList.toggle('hidden', !pid);
    $('rpCards').replaceChildren(card('Reports', fmt(d.total), false, 'with these filters'), card('Kinds', fmt(d.groups.length), false, 'distinct type + name'),
      card('Newest', d.reports[0] ? ago(d.reports[0].when) + ' ago' : '—', true));
    renderGroups(d); renderList();
  }

  Admin.page({
    id: 'reports', title: 'Bug reports', group: 'Players', order: 40, auto: 60,
    html: `
      <style>#page-reports .two { display: grid; gap: 12px; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); }
      #page-reports details summary { cursor: pointer; color: var(--muted); white-space: normal; }
      #page-reports details pre.log { margin-top: 6px; max-height: 260px; }
      #page-reports td:nth-child(4), #page-reports td:nth-child(5) { max-width: 420px; min-width: 140px; white-space: normal; overflow-wrap: anywhere; }</style>
      <div class="row"><span class="muted">Reports the game clients send (YA_LogSpecial), newest first.</span></div>
      <div class="row">
        <input id="rpQ" type="search" placeholder="Text in name or details">
        <select id="rpType"><option value="">All types</option></select>
        <button class="btn" id="rpClear">Clear filters</button>
        <span class="spacer"></span>
        <button class="btn" id="rpCsv">Export CSV</button>
      </div>
      <div class="row banner hidden" id="rpScope" style="color:var(--ink);border-color:var(--line)"></div>
      <div class="grid" id="rpCards"></div>
      <div class="two"><div class="panel" id="rpGroups"></div><div class="panel" id="rpTypes"></div></div>
      <div id="rpTable"></div>
      <div class="row hidden" id="rpMore"><button class="btn" id="rpOlder">Load older reports</button></div>`,
    init() {
      const reload = () => { wanted = PAGE; Admin.refresh(); };
      $('rpQ').oninput = debounce(reload, 300);
      $('rpType').onchange = reload;
      $('rpClear').onclick = () => { $('rpQ').value = ''; $('rpType').value = ''; reload(); };
      $('rpOlder').onclick = async () => {
        if (!rows.length) return;
        const d = await api('/reports?' + query({ limit: String(PAGE), before: String(rows[rows.length - 1].id) }));
        rows = rows.concat(d.reports); more = d.more; wanted = Math.min(500, rows.length); renderList();
      };
      $('rpCsv').onclick = () => download('bug-reports.csv', toCSV([['id', 'when', 'player', 'pid', 'type', 'name', 'details'],
        ...rows.map((r) => [r.id, r.when, r.player, r.pid, r.type, r.name, r.details])]), 'text/csv');
    },
    load,
  });
})();
