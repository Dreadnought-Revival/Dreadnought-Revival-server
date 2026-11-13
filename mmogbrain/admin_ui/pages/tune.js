// Balancing: the game's tune tables (weapons, projectiles, officers), a draft
// kept apart from the live version, publish / discard / restore, and what the
// battle servers apply. Values keep their type: a number stays a number.
(() => {
  const { $, el, api, postJSON, fmt, when, card, chip, fill, table, act, debounce, toCSV, download } = Admin;
  let status = null; let tbl = ''; let openRow = ''; let rowFields = []; let tablesBuilt = false; let lastNames = [];

  // text: a value as the page prints it; null/undefined = the game's own value.
  const text = (v) => (v === null || v === undefined ? 'game value' : typeof v === 'boolean' ? (v ? 'true' : 'false') : typeof v === 'object' ? JSON.stringify(v) : String(v));
  const same = (a, b) => text(a) === text(b);
  const eff = (x) => x.draft ?? x.published ?? x.original;

  // --- status, battle servers ---------------------------------------------
  function renderStatus(d) {
    status = d;
    const apply = d.test_accounts.filter((a) => a.mode === 'apply').map((a) => a.name || a.pid.slice(0, 8));
    const check = d.test_accounts.filter((a) => a.mode === 'check').length;
    $('tnCards').replaceChildren(
      card('Live version', d.live_id ? d.live : 'none', true, d.live_id ? 'sent to Apply accounts' : "the game's own values"),
      card('Draft', d.pending ? `${fmt(d.pending)} unpublished` : 'same as live', true, `${fmt(d.diff.length)} field${d.diff.length === 1 ? '' : 's'} differ from the game`),
      card('Battle servers', d.battle_servers ? 'applying live' : 'game values', true, d.battle_servers ? 'from the next match on' : 'switched off'),
      card('Sent to', d.everyone ? 'everyone' : fmt(apply.length) + ' account' + (apply.length === 1 ? '' : 's'), true, d.everyone ? 'DN_TUNE_SEND=1' : (apply.length ? apply.join(', ') : 'nobody receives it') + (check ? ` · ${check} check only` : '')));
    $('tnBattleToggle').textContent = d.battle_servers ? 'Turn off on battle servers' : 'Turn on on battle servers';
    $('tnBattleToggle').className = 'btn ' + (d.battle_servers ? '' : 'primary');
    if (!tablesBuilt) {
      const sel = $('tnTable'); sel.replaceChildren();
      d.tables.filter((t) => t.editable).forEach((t) => sel.append(el('option', { value: t.name }, `${t.name} (${fmt(t.rows)} rows)`)));
      tablesBuilt = true; tbl = sel.value;
    }
    $('tnPublish').disabled = !d.pending; $('tnDiscard').disabled = !d.pending;
    $('tnDraftHead').textContent = 'Draft' + (d.pending ? ` (${d.pending} not live yet)` : '');
    renderDraft(d); renderVersions(d);
  }

  async function loadBattle() {
    let b; try { b = await api('/tune/battle'); } catch (e) { if (e.message !== 'forbidden') $('tnHosts').replaceChildren(el('div', { class: 'err' }, e.message)); return; }
    const rows = b.hosts.map((h) => [String(h.port), Admin.modeName(h.mode), h.map || '—', when(h.started_at),
      chip(h.version, h.version === 'older than live' ? 'warn' : h.version === 'unknown' ? '' : 'good'), el('span', { class: 'muted' }, h.basis)]);
    $('tnHosts').replaceChildren(
      el('div', { class: 'note' }, 'A battle server reads the live version once, when its map loads, and the server keeps no record of what each one was given. "Older than live" is worked out from the start time: it started before the live version was published' + (b.published_at ? ' (' + b.published_at + ' UTC)' : '') + '. Tables applied: ' + b.tables.join(', ') + '.'),
      b.error ? el('div', { class: 'banner' }, b.error) : null,
      table(['Port', 'Mode', 'Map', 'Started', 'Applies', 'Basis'], rows, b.error ? 'Running servers cannot be listed.' : 'No battle server is running.'));
  }

  async function call(path, body, ok) {
    const d = await act(() => postJSON('/tune' + path, body), 'tnMsg', ok);
    if (d) { renderStatus(d); if (openRow) loadRow(openRow); if (path === '/battle') loadBattle(); }
    return d;
  }

  // --- tables tab ---------------------------------------------------------
  async function loadNames() {
    const q = $('tnSearch').value.trim();
    const d = await api(`/tune/table/${encodeURIComponent(tbl)}?names=1&limit=500&q=${encodeURIComponent(q)}`);
    // rows with draft or live changes first, so the edited ones are not buried in 226 names
    d.names.sort((a, b) => (b.draft ? 2 : b.changed ? 1 : 0) - (a.draft ? 2 : a.changed ? 1 : 0));
    lastNames = d.names;
    $('tnFound').textContent = d.total > d.names.length ? `${fmt(d.total)} rows match; showing ${d.names.length}` : `${fmt(d.total)} row${d.total === 1 ? '' : 's'}`;
    const list = $('tnRows'); list.replaceChildren();
    if (!d.names.length) list.append(el('div', { class: 'muted' }, 'No row matches.'));
    if (!openRow && d.names.length) loadRow((d.names.find((n) => n.draft) || d.names[0]).name);
    d.names.forEach((n) => list.append(el('button', { type: 'button', class: 'tn-row' + (n.name === openRow ? ' on' : ''), onclick: () => loadRow(n.name), title: `${n.fields} fields` },
      el('span', {}, n.name), n.draft ? chip(String(n.draft), 'warn') : n.changed ? chip(String(n.changed), 'good') : null)));
  }

  async function loadRow(name) {
    openRow = name;
    document.querySelectorAll('#tnRows .tn-row').forEach((b) => b.classList.toggle('on', b.firstChild.textContent === name));
    let d; try { d = await api(`/tune/table/${encodeURIComponent(tbl)}?exact=${encodeURIComponent(name)}`); } catch (e) { return; }
    const row = d.rows[0]; if (!row) { $('tnRowBox').classList.add('hidden'); return; }
    rowFields = row.fields;
    $('tnRowBox').classList.remove('hidden'); $('tnRowName').textContent = name;
    renderFields();
  }

  function renderFields() {
    const f = $('tnFieldFilter').value.trim().toLowerCase();
    const only = $('tnOnlyChanged').checked;
    const shown = rowFields.filter((x) => (!f || x.field.toLowerCase().includes(f)) && (!only || x.draft !== null && x.draft !== undefined || x.published !== null && x.published !== undefined));
    fill('tnFields', shown.map((x) => {
      const cur = eff(x); let input;
      if (x.kind === 'bool') { input = el('select', {}); ['true', 'false'].forEach((o) => input.append(el('option', { value: o }, o))); input.value = String(cur); }
      else if (x.kind === 'number') input = el('input', { type: 'number', step: 'any', value: String(cur) });
      else if (x.kind === 'string') input = el('input', { type: 'text', maxlength: '200', value: String(cur) });
      else input = el('span', { class: 'muted', title: 'Only plain numbers, booleans and strings can be edited here.' }, 'not editable');
      if (x.kind !== 'other') { input.dataset.field = x.field; input.dataset.kind = x.kind; input.dataset.cur = String(cur); input.dataset.orig = String(x.original); input.classList.add('tn-in'); }
      const inDraft = x.draft !== null && x.draft !== undefined;
      return el('tr', { class: inDraft && !same(x.draft, x.published) ? 'tn-changed' : '' }, el('td', {}, x.field, el('div', { class: 'muted tn-kind' }, x.kind)),
        el('td', { class: 'num' }, text(x.original)), el('td', { class: 'num' }, x.published === null || x.published === undefined ? el('span', { class: 'muted' }, 'same') : text(x.published)),
        el('td', {}, input));
    }), 'No field matches.', 4);
  }

  function saveRow() {
    const changes = []; let bad = '';
    document.querySelectorAll('#tnFields .tn-in').forEach((inp) => {
      const raw = inp.value.trim(); if (raw === inp.dataset.cur) return;
      let value = raw;
      if (inp.dataset.kind === 'number') {
        const n = Number(raw);
        if (raw === '' || !isFinite(n)) { bad = `${inp.dataset.field}: not a number`; return; }
        if (/^-?\d+$/.test(inp.dataset.orig) && !Number.isInteger(n)) { bad = `${inp.dataset.field} is a whole number in the game`; return; }
        value = n;
      } else if (inp.dataset.kind === 'bool') value = raw === 'true';
      else if (/[\t\n\r"\\]/.test(raw)) { bad = `${inp.dataset.field}: no line breaks, quotes or backslashes`; return; }
      if (String(value) === inp.dataset.orig) value = null; // back to the game's value
      changes.push({ table: tbl, row: openRow, field: inp.dataset.field, value });
    });
    if (bad) { $('tnMsg').className = 'err'; $('tnMsg').textContent = bad; return; }
    if (!changes.length) { $('tnMsg').className = 'muted'; $('tnMsg').textContent = 'Nothing changed.'; return; }
    call('/draft', { changes }, `${changes.length} field${changes.length > 1 ? 's' : ''} saved to the draft.`);
  }

  async function exportCSV() {
    const q = $('tnSearch').value.trim();
    const d = await act(() => api(`/tune/table/${encodeURIComponent(tbl)}?limit=500&q=${encodeURIComponent(q)}`), 'tnMsg', null);
    if (!d) return;
    const rows = [['table', 'row', 'field', 'type', 'game', 'live', 'draft']];
    d.rows.forEach((r) => r.fields.forEach((f) => rows.push([tbl, r.name, f.field, f.kind, f.original, f.published ?? '', f.draft ?? ''])));
    download(`${tbl}.csv`, toCSV(rows), 'text/csv');
    $('tnMsg').className = 'ok'; $('tnMsg').textContent = `Exported ${d.rows.length} of ${d.total} rows (${rows.length - 1} fields).`;
  }

  // --- draft tab ------------------------------------------------------------
  function renderDraft(d) {
    const pend = d.diff.filter((x) => !same(x.published, x.draft));
    const rows = d.diff.map((x) => {
      const isPend = !same(x.published, x.draft);
      const live = x.published ?? x.original; const draft = x.draft ?? x.original;
      return el('tr', { class: isPend ? 'tn-changed' : '' }, el('td', {}, x.table), el('td', { class: 'mono' }, x.row), el('td', {}, x.field),
        el('td', { class: 'num' }, text(x.original)),
        el('td', { class: 'num' }, isPend ? `${text(live)} → ${text(draft)}` : text(live)),
        el('td', {}, isPend ? chip(x.draft === null || x.draft === undefined ? 'removed' : 'edited', 'warn') : chip('live', 'good')),
        el('td', {}, isPend ? el('a', { class: 'link', onclick: () => call('/draft', { changes: [{ table: x.table, row: x.row, field: x.field, value: x.published ?? null }] }, 'Undone.') }, 'undo') : ''));
    });
    $('tnDiff').replaceChildren(table(['Table', 'Row', 'Field', 'Game', 'Live → draft', '', ''], rows, "No changes: the game's own values are live."));
    $('tnPendText').textContent = pend.length ? `${fmt(pend.length)} change${pend.length === 1 ? '' : 's'} are not live yet.` : 'The draft is the same as the live version.';
  }

  function publish() {
    const pend = status.diff.filter((x) => !same(x.published, x.draft));
    const lines = pend.slice(0, 20).map((x) => `${x.row}.${x.field}: ${text(x.published ?? x.original)} → ${text(x.draft ?? x.original)}`);
    if (!confirm(`Publish ${pend.length} change${pend.length === 1 ? '' : 's'} as the new live balancing version?\nAccounts set to Apply get it at their next login; battle servers from the next match on.\n\n` + lines.join('\n') + (pend.length > 20 ? `\n… and ${pend.length - 20} more` : ''))) return;
    call('/publish', { note: $('tnNote').value.trim() }, 'Published.').then((d) => { if (d) { $('tnNote').value = ''; loadBattle(); } });
  }

  // --- versions tab -----------------------------------------------------------
  function renderVersions(d) {
    fill('tnVersions', d.versions.map((v) => el('tr', {}, el('td', {}, v.version, v.live ? [' ', chip('live', 'good')] : null), el('td', {}, when(v.at)),
      el('td', { class: 'num' }, fmt(v.changes)), el('td', { class: 'wrap' }, v.note), el('td', { class: 'muted' }, v.actor),
      el('td', {}, el('button', { class: 'btn small', onclick: () => preview(v.id) }, 'Show changes'), ' ',
        el('button', { class: 'btn small', onclick: () => restore(v.id, 'Version ' + v.version) }, 'Restore into draft')))), 'Nothing published yet.', 6);
  }
  async function preview(id) {
    const d = await act(() => api('/tune/versions/' + id), 'tnMsg', null); if (!d) return;
    $('tnPreview').replaceChildren(el('h3', {}, `${d.version} — ${d.changes.length} change${d.changes.length === 1 ? '' : 's'} against the game`), d.note ? el('div', { class: 'muted' }, d.note) : null,
      table(['Table', 'Row', 'Field', 'Game → this version'], d.changes.map((x) => [x.table, el('span', { class: 'mono' }, x.row), x.field, `${text(x.original)} → ${text(x.published)}`]), 'This version changes nothing.'));
  }
  function restore(id, label) {
    if (!confirm(`Put ${label} into the draft? The current draft is replaced. Publish it to make it live.`)) return;
    call('/restore', { id }, `${label} is in the draft; publish to make it live.`);
  }

  Admin.page({
    id: 'tune', title: 'Balancing', group: 'Game', order: 20, auto: 30,
    html: `<style>
      #page-tune .tn-row { display: flex; justify-content: space-between; text-align: left; word-break: break-all; gap: 6px; align-items: center; margin: 0 0 4px; padding: 4px 9px; border: 1px solid var(--line); border-radius: 6px; background: var(--panel); color: var(--ink); cursor: pointer; font: inherit; font-size: 12px; }
      #page-tune .tn-row.on { border-color: var(--accent); background: var(--accent-soft); }
      #page-tune #tnRows { max-height: 360px; overflow: auto; margin-top: 8px; display: flex; flex-direction: column; align-items: stretch; }
      #page-tune .tn-split { display: grid; gap: 12px; grid-template-columns: minmax(220px, 1fr) minmax(0, 2fr); align-items: start; }
      @media (max-width: 900px) { #page-tune .tn-split { grid-template-columns: minmax(0, 1fr); } }
      #page-tune .tn-fieldwrap { max-height: 420px; overflow: auto; }
      #page-tune tr.tn-changed td { background: var(--accent-soft); }
      #page-tune .tn-kind { font-size: 11px; }
      #page-tune .tn-in { width: 120px; max-width: 100%; }
      #page-tune #tnPreview { margin-top: 12px; }
    </style>
    <div class="grid" id="tnCards"></div>
    <p class="muted">Changes the game's own weapon, projectile and officer tables. Edits go into a draft first; nothing reaches players until it is published. Accounts set to Apply (player page, Tune channel test) receive the live version at their next login.</p>
    <section><h2>Battle servers</h2>
      <div class="row"><button class="btn" id="tnBattleToggle"></button><span class="muted">Battle servers apply the live version from the next match on, for every player.</span></div>
      <div id="tnHosts"></div>
    </section>
    <p id="tnMsg" class="muted"></p>
    <section id="tnPaneTables"><h2>Tables</h2>
      <div class="row"><select id="tnTable"></select><input id="tnSearch" type="search" placeholder="Search a row, e.g. WP_Dreadnought"><button class="btn" id="tnCsv">Export table as CSV</button><span class="muted" id="tnFound"></span></div>
      <div class="tn-split">
        <div class="panel"><h3>Rows</h3><div class="muted">Yellow: fields in the draft, green: fields changed in the live version.</div><div id="tnRows"></div></div>
        <div class="panel hidden" id="tnRowBox"><h3 id="tnRowName" class="mono"></h3>
          <div class="row"><input id="tnFieldFilter" type="search" placeholder="Filter fields"><label class="check"><input type="checkbox" id="tnOnlyChanged"> only changed</label></div>
          <div class="table-wrap tn-fieldwrap"><table><thead><tr><th>Field</th><th class="num">Game</th><th class="num">Live</th><th>New value</th></tr></thead><tbody id="tnFields"></tbody></table></div>
          <div class="row" style="margin-top:8px"><button class="btn primary" id="tnSaveRow">Save to draft</button><span class="muted">Setting a field back to the game value removes it from the draft.</span></div>
        </div>
      </div>
    </section>
    <section id="tnPaneDraft"><h2 id="tnDraftHead">Draft</h2>
      <div class="row"><span id="tnPendText" class="muted"></span></div>
      <div id="tnDiff"></div>
      <div class="row" style="margin-top:8px"><input id="tnNote" type="text" maxlength="300" placeholder="Note for this version, e.g. +10% Dreadnought primary damage" style="flex:1;min-width:240px">
        <button class="btn danger" id="tnPublish">Publish</button><button class="btn" id="tnDiscard">Discard draft</button></div>
    </section>
    <section id="tnPaneVersions"><h2>Versions</h2>
      <div id="tnVersionsHost"><div class="table-wrap"><table><thead><tr><th>Version</th><th>When</th><th class="num">Changes</th><th>Note</th><th>From</th><th></th></tr></thead><tbody id="tnVersions"></tbody></table></div></div>
      <div class="row" style="margin-top:8px"><button class="btn" id="tnDefaults">Put the game's own values in the draft</button></div>
      <div id="tnPreview"></div>
    </section>`,
    init() {
      $('tnTable').onchange = () => { tbl = $('tnTable').value; openRow = ''; $('tnRowBox').classList.add('hidden'); loadNames(); };
      $('tnSearch').oninput = debounce(loadNames, 250);
      $('tnFieldFilter').oninput = renderFields; $('tnOnlyChanged').onchange = renderFields;
      $('tnSaveRow').onclick = saveRow; $('tnCsv').onclick = exportCSV;
      $('tnPublish').onclick = publish;
      $('tnDiscard').onclick = () => { if (confirm('Throw the draft away and go back to the live version?')) call('/discard', {}, 'Draft discarded.'); };
      $('tnDefaults').onclick = () => restore(0, "the game's own values");
      $('tnBattleToggle').onclick = () => {
        const on = !status.battle_servers;
        if (confirm(on ? 'Battle servers apply the live balancing version from the next match on, for every player. Continue?' : "Battle servers go back to the game's own values from the next match on. Continue?")) call('/battle', { on }, on ? 'Battle servers apply the live version from the next match.' : "Battle servers use the game's values from the next match.");
      };
    },
    async load() {
      renderStatus(await api('/tune'));
      await Promise.all([loadBattle(), loadNames()]);
    },
  });
})();
