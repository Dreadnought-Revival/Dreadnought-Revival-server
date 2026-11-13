// Moderation: active bans and chat mutes (timed or permanent), a form to ban or
// mute a player, and the searchable audit log of every admin action.
(() => {
  const { $, el, api, postJSON, fmt, when, parseTime, chip, table, playerLink, act, debounce, toCSV, download, auditText } = Admin;
  const DURATIONS = [[1, '1 hour'], [24, '24 hours'], [168, '7 days'], [720, '30 days'], [0, 'Permanent']];
  const PID = /^[0-9a-f]{32}$/;
  let picked = null;     // { pid, name } chosen in the form
  let auditRows = [];    // rows shown (first page + any "older" pages)
  let auditNext = 0; let auditMore = false; let actionsSeen = '';

  // remaining: floors (core's duration() can print "44 h 60 min").
  const remaining = (s) => (s >= 172800 ? Math.floor(s / 86400) + ' d' : s >= 3600 ? Math.floor(s / 3600) + ' h ' + Math.floor(s % 3600 / 60) + ' min' : Math.max(1, Math.floor(s / 60)) + ' min');

  // endCell: when a sanction ends, and how long is left.
  function endCell(expires) {
    if (!expires) return chip('permanent', 'bad');
    const t = parseTime(expires);
    const left = (t - Date.now()) / 1000;
    return el('span', {}, when(expires), el('br'), el('span', { class: 'muted' }, left > 0 ? remaining(left) + ' left' : 'ending'));
  }

  async function lift(kind, x) {
    const verb = kind === 'ban' ? 'Unban' : 'Unmute';
    if (!confirm(`${verb} ${x.name}?`)) return;
    if (await act(async () => postJSON(`/players/${x.pid}/${kind === 'ban' ? 'unban' : 'unmute'}`, { reason: 'lifted from the moderation page' }), null, (kind === 'ban' ? 'Unbanned.' : 'Unmuted.'))) loadLists();
  }

  async function loadLists() {
    const [b, m] = await Promise.all([api('/bans'), api('/mutes')]);
    const rowsFor = (list, kind) => list.map((x) => [playerLink(x.pid, x.name), x.reason, when(x.at), endCell(x.expires_at),
      el('button', { class: 'btn small', type: 'button', onclick: () => lift(kind, x) }, kind === 'ban' ? 'Unban' : 'Unmute')]);
    const cols = ['Player', { label: 'Reason', wrap: true }, 'Since', 'Until', ''];
    $('mdBans').replaceChildren(table(cols, rowsFor(b.bans || [], 'ban'), 'Nobody is banned.'));
    $('mdMutes').replaceChildren(table(cols, rowsFor(m.mutes || [], 'mute'), 'Nobody is muted.'));
    $('mdBanN').textContent = fmt((b.bans || []).length);
    $('mdMuteN').textContent = fmt((m.mutes || []).length);
  }

  function auditQuery(before) {
    const p = new URLSearchParams();
    const add = (k, id) => { const v = $(id).value.trim(); if (v) p.set(k, v); };
    add('action', 'mdAction'); add('q', 'mdText'); add('target', 'mdTarget'); add('since', 'mdSince');
    const until = $('mdUntil').value.trim();
    if (until) { // the last day is included: the server's "until" is exclusive
      const d = new Date(until + 'T00:00:00Z'); if (!isNaN(d)) { d.setUTCDate(d.getUTCDate() + 1); p.set('until', d.toISOString().slice(0, 10)); }
    }
    p.set('limit', '100');
    if (before) p.set('before', before);
    return p.toString();
  }

  function renderAudit() {
    const rows = auditRows.map((x) => [when(x.at), x.action,
      x.target ? (PID.test(x.target) ? playerLink(x.target, x.name) : x.target) : '—',
      auditText(x.details), el('span', { class: 'muted' }, x.actor)]);
    $('mdAudit').replaceChildren(table(['When', 'Action', 'Target', { label: 'Details', wrap: true }, 'From'], rows, 'No admin actions match.'));
    $('mdMore').classList.toggle('hidden', !auditMore);
    $('mdAuditN').textContent = fmt(auditRows.length) + (auditMore ? '+' : '') + ' shown';
  }

  async function loadAudit(more) {
    let d;
    try { d = await api('/audit?' + auditQuery(more ? auditNext : 0)); } catch (e) { if (e.message !== 'forbidden') $('mdAuditN').textContent = e.message; return; }
    auditRows = more ? auditRows.concat(d.audit) : d.audit;
    auditMore = d.more; auditNext = d.next;
    const seen = (d.actions || []).join('|');
    if (seen !== actionsSeen) { // refill the action list, keeping the choice
      actionsSeen = seen; const cur = $('mdAction').value;
      $('mdAction').replaceChildren(el('option', { value: '' }, 'Any action'), ...(d.actions || []).map((a) => el('option', { value: a }, a)));
      $('mdAction').value = cur;
    }
    renderAudit();
  }

  async function searchPlayer() {
    const q = $('mdWho').value.trim(); const box = $('mdPick');
    picked = null; $('mdPicked').textContent = '';
    const bare = q.toLowerCase().replace(/-/g, '');
    if (PID.test(bare)) { picked = { pid: bare, name: '' }; $('mdPicked').textContent = 'Player id ' + bare; box.replaceChildren(); return; }
    if (q.length < 2) { box.replaceChildren(); return; }
    let d; try { d = await api('/players?q=' + encodeURIComponent(q)); } catch (e) { return; }
    const list = (d.players || []).slice(0, 8);
    box.replaceChildren(...(list.length ? list.map((p) => el('button', { class: 'btn small', type: 'button', onclick: () => {
      picked = { pid: p.pid, name: p.name }; $('mdPicked').textContent = 'Selected: ' + (p.name || '?') + ' (rank ' + fmt(p.rank) + ')'; box.replaceChildren();
    } }, p.name || '?')) : [el('span', { class: 'muted' }, 'No account matches.')]));
  }

  async function sanction(kind) {
    if (!picked) { $('mdMsg').className = 'err'; $('mdMsg').textContent = 'Pick a player first (search by name or paste the player id).'; return; }
    const hours = parseInt($('mdHours').value, 10); const reason = $('mdReason').value.trim();
    if (!reason) { $('mdMsg').className = 'err'; $('mdMsg').textContent = 'A reason is required.'; return; }
    const label = (DURATIONS.find((d) => d[0] === hours) || [0, ''])[1].toLowerCase();
    const who = picked.name || picked.pid;
    if (!confirm(`${kind === 'ban' ? 'Ban' : 'Mute'} ${who} (${label})?\n\n${kind === 'ban' ? 'They are signed out of the game now.' : 'Their chat lines are dropped; they can keep playing.'}`)) return;
    const d = await act(async () => postJSON(`/players/${picked.pid}/${kind}`, { reason, hours }), 'mdMsg', `${who} is ${kind === 'ban' ? 'banned' : 'muted'} (${label}).`);
    if (d) { $('mdReason').value = ''; loadLists(); loadAudit(false); }
  }

  function exportCSV() {
    const rows = [['id', 'at (UTC)', 'actor', 'action', 'target', 'name', 'details']].concat(
      auditRows.map((x) => [x.id, x.at, x.actor, x.action, x.target, x.name || '', auditText(x.details)]));
    download('admin-audit-' + new Date().toISOString().slice(0, 10) + '.csv', toCSV(rows), 'text/csv');
  }

  Admin.page({
    id: 'moderation', title: 'Moderation', group: 'Players', order: 20, auto: 30,
    html: `
      <style>
        #page-moderation .mdform { display: grid; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); gap: 12px; }
        #page-moderation .split > section { margin-top: 0; }
        #page-moderation #mdAudit td.wrap { min-width: 11em; }
        #page-moderation td { white-space: nowrap; }
        #page-moderation td.wrap { white-space: normal; }
        #page-moderation .mdform { align-items: start; }
        #page-moderation .mdpick { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
        #page-moderation .mdpick:empty { display: none; }
        #page-moderation .mdfilters input, #page-moderation .mdfilters select { min-width: 0; }
        @media (max-width: 760px) { #page-moderation .mdform { grid-template-columns: 1fr; } }
      </style>
      <div class="grid">
        <div class="card"><div class="k">Active bans</div><div class="v" id="mdBanN">0</div></div>
        <div class="card"><div class="k">Active chat mutes</div><div class="v" id="mdMuteN">0</div></div>
      </div>
      <section class="panel"><h2>Ban or mute a player</h2>
        <div class="muted" style="margin-bottom:8px">A ban signs the player out of the game now, refuses them at the gateway and the social socket, and is passed to the auth server so the launcher refuses them too. A mute only drops their chat lines (channels and direct messages); they stay online. Timed sanctions lift themselves within a minute of ending.</div>
        <div class="mdform">
          <div>
            <input id="mdWho" type="text" maxlength="64" placeholder="Player name, or paste the player id" style="width:100%" autocomplete="off">
            <div class="mdpick" id="mdPick"></div>
            <div class="muted" id="mdPicked"></div>
          </div>
          <select id="mdHours" aria-label="Duration"></select>
        </div>
        <input id="mdReason" type="text" maxlength="200" placeholder="Reason (required, kept in the audit log)" style="width:100%; margin-top:8px">
        <div class="row" style="margin-top:8px">
          <button class="btn danger" id="mdBan" type="button">Ban</button>
          <button class="btn" id="mdMute" type="button">Mute chat</button>
          <span id="mdMsg"></span>
        </div>
      </section>
      <div class="split">
        <section><h2>Banned players</h2><div id="mdBans"></div></section>
        <section><h2>Muted players</h2><div id="mdMutes"></div></section>
      </div>
      <section><h2>Admin actions</h2>
        <div class="row mdfilters">
          <select id="mdAction" aria-label="Action"><option value="">Any action</option></select>
          <input id="mdText" type="text" placeholder="Search actor, action, details" maxlength="80">
          <input id="mdTarget" type="text" placeholder="Player id" maxlength="36" class="mono">
          <label class="muted">from <input id="mdSince" type="date"></label>
          <label class="muted">to <input id="mdUntil" type="date"></label>
          <button class="btn" id="mdClear" type="button">Clear</button>
          <span class="spacer"></span>
          <span class="muted" id="mdAuditN"></span>
          <button class="btn" id="mdCSV" type="button">Export CSV</button>
        </div>
        <div id="mdAudit"></div>
        <div class="row"><button class="btn hidden" id="mdMore" type="button">Older entries</button></div>
      </section>`,
    init() {
      $('mdHours').replaceChildren(...DURATIONS.map(([h, l]) => el('option', { value: String(h) }, l)));
      $('mdHours').value = '24';
      $('mdWho').oninput = debounce(searchPlayer, 220);
      $('mdBan').onclick = () => sanction('ban');
      $('mdMute').onclick = () => sanction('mute');
      const reload = debounce(() => loadAudit(false), 300);
      ['mdText', 'mdTarget'].forEach((id) => { $(id).oninput = reload; });
      ['mdAction', 'mdSince', 'mdUntil'].forEach((id) => { $(id).onchange = reload; });
      $('mdClear').onclick = () => { ['mdAction', 'mdText', 'mdTarget', 'mdSince', 'mdUntil'].forEach((id) => { $(id).value = ''; }); loadAudit(false); };
      $('mdMore').onclick = () => loadAudit(true);
      $('mdCSV').onclick = exportCSV;
    },
    // The auto refresh reloads the lists and the FIRST page of the log; it does
    // not touch the form, and keeps "older" pages only until the next refresh.
    async load() { await Promise.all([loadLists(), loadAudit(false)]); },
  });
})();
