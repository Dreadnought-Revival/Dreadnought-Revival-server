// Telemetry: what the game clients report about themselves (YA_Analytics*,
// YA_GameModeEvent). Reward events show what the server paid beside what the
// client showed. Route: #/telemetry or #/telemetry/<pid> (one player).
(() => {
  const { $, el, api, fmt, when, card, chip, table, bars, link, playerLink, debounce } = Admin;
  const PAGE = 300;
  const short = (t) => t.replace(/^YA_(Analytics)?/, '');
  let pid = ''; let lastArg = null; let events = []; let more = false; let wanted = PAGE;

  const fieldText = (raw) => { try { return JSON.parse(raw).map((f) => `${f.n}=${f.v}`).join(' · '); } catch (e) { return raw; } };

  const query = (extra) => {
    const p = new URLSearchParams(extra || {});
    if ($('tlType').value) p.set('rt', $('tlType').value);
    if (pid) p.set('pid', pid); else if ($('tlPlayer').value.trim()) p.set('q', $('tlPlayer').value.trim());
    return p;
  };

  function renderList() {
    const body = events.map((e) => {
      const fields = fieldText(e.fields);
      const paid = e.server_paid ? chip(e.server_paid, e.server_paid.startsWith('no result') ? 'warn' : '') : '';
      return [when(e.when), e.pid ? playerLink(e.pid, e.player) : (e.player || '?'), short(e.rt),
        el('span', {}, e.summary),
        paid,
        e.battle_id ? link(e.battle_id.slice(0, 8) + '…', 'match', e.battle_id) : '',
        fields ? el('details', {}, el('summary', {}, fields.length > 60 ? fields.slice(0, 60) + '…' : fields), el('div', { class: 'results' }, fields)) : ''];
    });
    $('tlTable').replaceChildren(table(['When', 'Player', 'Type', 'Summary', 'Server paid', 'Battle', 'Fields'], body, 'No telemetry with these filters.'));
    $('tlMore').classList.toggle('hidden', !more);
  }

  async function load(arg) {
    if (arg !== lastArg) { lastArg = arg; pid = arg || ''; wanted = PAGE; }
    const d = await api('/telemetry?' + query({ limit: String(Math.min(500, wanted)) }));
    const sel = $('tlType'); const keep = sel.value;
    sel.replaceChildren(el('option', { value: '' }, 'All types'), ...d.types.map((t) => el('option', { value: t }, `${short(t)} (${fmt(d.counts[t])})`)));
    sel.value = keep;
    events = d.events; more = d.more;
    $('tlScope').replaceChildren(...(pid ? [el('span', {}, 'Player ', playerLink(pid, events[0] && events[0].player)),
      el('button', { class: 'btn small', onclick: () => { if (Admin.arg()) Admin.go('telemetry'); else { pid = ''; Admin.refresh(); } } }, 'Show everyone')] : []));
    $('tlScope').classList.toggle('hidden', !pid);
    const total = Object.values(d.counts).reduce((a, n) => a + n, 0);
    $('tlCards').replaceChildren(card('Events', fmt(total), false, pid ? 'for this player' : 'stored'), card('Types', fmt(d.types.length)),
      card('Listed', fmt(events.length), false, more ? 'older ones on request' : 'all of them'));
    $('tlCounts').replaceChildren(el('h3', {}, 'Events per type'),
      bars(d.types.slice().sort((a, b) => d.counts[b] - d.counts[a]).map((t) => ({
        label: el('a', { class: 'link', onclick: () => { $('tlType').value = t; wanted = PAGE; Admin.refresh(); } }, short(t)), value: d.counts[t] })), { empty: 'No telemetry yet.' }));
    renderList();
  }

  Admin.page({
    id: 'telemetry', title: 'Telemetry', group: 'Insight', order: 20, auto: 60,
    html: `
      <style>#page-telemetry details summary { cursor: pointer; color: var(--muted); white-space: normal; }
      #page-telemetry td:nth-child(4), #page-telemetry td:nth-child(7) { max-width: 340px; min-width: 140px; white-space: normal; overflow-wrap: anywhere; }</style>
      <div class="row"><span class="muted">What the game clients report about themselves (YA_Analytics*, YA_GameModeEvent), newest first. Reward events show what the server paid beside what the client showed.</span></div>
      <div class="row">
        <select id="tlType"><option value="">All types</option></select>
        <input id="tlPlayer" type="search" placeholder="Player name or id">
      </div>
      <div class="row banner hidden" id="tlScope" style="color:var(--ink);border-color:var(--line)"></div>
      <div class="grid" id="tlCards"></div>
      <div class="panel" id="tlCounts"></div>
      <div id="tlTable"></div>
      <div class="row hidden" id="tlMore"><button class="btn" id="tlOlder">Load older events</button></div>`,
    init() {
      const reload = () => { wanted = PAGE; Admin.refresh(); };
      $('tlType').onchange = reload;
      $('tlPlayer').oninput = debounce(reload, 300);
      $('tlOlder').onclick = async () => {
        if (!events.length) return;
        const d = await api('/telemetry?' + query({ limit: String(PAGE), before: String(events[events.length - 1].id) }));
        events = events.concat(d.events); more = d.more; wanted = Math.min(500, events.length); renderList();
      };
    },
    load,
  });
})();
