// Tech tree: research (XP) and purchase costs per node. Each value records
// where it comes from: GUESS (the code's placeholder), Sourced (with the
// source) or Accepted (a deliberate design choice). Empty amount = the code's
// value. Players see changes at their next login; spent XP/credits stay.
(() => {
  const { $, el, api, postJSON, fmt, card, chip, act, debounce, download } = Admin;
  const PAGE = 100;
  let offset = 0; let meta = { statuses: {}, max_cost: 10000000 }; let loaded = null;

  const KIND = { hull: 'hull', hero: 'hero ship', module: 'module', briefing: 'officer briefing (same on every ship)' };

  // cell: amount + status + source for one value (xp or price) of a node.
  function cell(n, field, mark) {
    const v = n[field]; if (!v) return el('td', { class: 'muted' }, '—');
    const amount = el('input', { type: 'number', min: '0', max: String(meta.max_cost), step: '1', placeholder: fmt(v.default), value: v.overridden ? String(v.value) : '', class: 'tt-amt', 'aria-label': `${field} amount` });
    const status = el('select', { class: 'tt-st', 'aria-label': `${field} status` });
    Object.entries(meta.statuses).forEach(([k, label]) => status.append(el('option', { value: k }, label)));
    status.value = v.status;
    const source = el('input', { type: 'text', maxlength: '500', placeholder: 'source', value: v.source, class: 'tt-src', 'aria-label': `${field} source` });
    [amount, status, source].forEach((x) => { x.dataset.field = field; x.oninput = x.onchange = mark; });
    return el('td', {}, el('div', { class: 'row tt-line' }, amount, el('span', { class: 'muted' }, v.currency), v.overridden ? chip('override', 'warn') : null),
      el('div', { class: 'row tt-line' }, status, source), el('div', { class: 'muted tt-def' }, `default ${fmt(v.default)}`));
  }

  // edits: what a row's inputs differ in, or an error text.
  function edits(tr, n) {
    const out = [];
    for (const field of ['xp', 'price']) {
      const v = n[field]; if (!v) continue;
      const q = (sel) => tr.querySelector(`${sel}[data-field="${field}"]`);
      const raw = q('.tt-amt').value.trim(); const status = q('.tt-st').value; const source = q('.tt-src').value.trim();
      let amount = null;
      if (raw !== '') {
        amount = Number(raw);
        if (!Number.isInteger(amount) || amount < 0 || amount > meta.max_cost) return { error: `${n.name} ${field}: a cost is a whole number from 0 to ${fmt(meta.max_cost)}` };
      }
      if (status === 'sourced' && !source) return { error: `${n.name} ${field}: a sourced value needs its source` };
      const was = v.overridden ? v.value : null;
      if (amount === was && status === v.status && source === v.source) continue;
      out.push({ item_id: n.item_id, field, amount, status, source, was, name: n.name });
    }
    return { list: out };
  }

  async function saveRow(tr, n) {
    const e = edits(tr, n);
    if (e.error) { Admin.toast(e.error, true); return; }
    if (!e.list.length) { Admin.toast('Nothing changed.'); return; }
    const what = e.list.map((x) => `${x.name} ${x.field === 'xp' ? 'XP' : 'price'}: ${x.was === null ? 'default' : fmt(x.was)} -> ${x.amount === null ? 'default' : fmt(x.amount)} (${meta.statuses[x.status]})`);
    if (!confirm('Save?\n\n' + what.join('\n'))) return;
    const d = await act(() => postJSON('/techtree/values', { values: e.list.map(({ item_id, field, amount, status, source }) => ({ item_id, field, amount, status, source })) }), null, 'Saved.');
    if (d) load();
  }

  async function resetRow(n) {
    const fields = ['xp', 'price'].filter((f) => n[f] && (n[f].overridden || n[f].status !== 'guess' || n[f].source));
    if (!fields.length) { Admin.toast('Nothing to reset.'); return; }
    if (!confirm(`Reset ${n.name} to the code's values? Its amount, status and source are removed.`)) return;
    const d = await act(() => postJSON('/techtree/values', { values: fields.map((f) => ({ item_id: n.item_id, field: f, amount: null, status: 'guess', source: '' })) }), null, 'Reset.');
    if (d) load();
  }

  function render(d) {
    loaded = d; meta = { statuses: d.statuses, max_cost: d.max_cost };
    const s = d.summary;
    const tiers = Object.keys(s.ships_per_tier).sort().map((t) => `T${t}: ${s.ships_per_tier[t]}`).join(' · ');
    $('ttCards').replaceChildren(card('Overrides', fmt(s.overridden), false, 'amounts set on the dashboard'),
      card('Annotated only', fmt(s.annotated), false, 'status or source, code amount'),
      card('Nodes', fmt(s.nodes), false, `${fmt(s.ships)} ships and heroes`),
      card('Ships per tier', tiers || '—', true));
    const sel = $('ttLine'); const keepLine = sel.value;
    if (sel.options.length <= 1) { d.lines.forEach((l) => sel.append(el('option', { value: l }, l))); sel.value = keepLine; }
    $('ttCount').textContent = d.total ? `${fmt(offset + 1)}–${fmt(offset + d.rows.length)} of ${fmt(d.total)}` : '0 nodes';
    $('ttPrev').disabled = offset <= 0; $('ttNext').disabled = offset + d.rows.length >= d.total;
    const body = $('ttNodes'); body.replaceChildren();
    if (!d.rows.length) body.append(el('tr', {}, el('td', { class: 'muted', colspan: 5 }, 'No node matches these filters.')));
    d.rows.forEach((r) => {
      const n = r.node; const tr = el('tr', {});
      const mark = () => { const e = edits(tr, n); tr.classList.toggle('tt-dirty', !!e.error || e.list.length > 0); };
      tr.append(el('td', {}, el('div', {}, n.name), el('div', { class: 'muted tt-def' }, `${KIND[n.kind] || n.kind} · T${n.tier} · ${n.item_id}`),
        el('div', { class: 'muted tt-def' }, n.shared ? 'every ship' : `${r.ship} · ${r.line}`)),
        cell(n, 'xp', mark), cell(n, 'price', mark),
        el('td', { class: 'wrap tt-warn' }, ...n.warnings.map((w) => el('div', { class: 'err' }, w))),
        el('td', {}, el('div', { class: 'tt-act' }, el('button', { class: 'btn small primary', onclick: () => saveRow(tr, n) }, 'Save'), el('button', { class: 'btn small', onclick: () => resetRow(n) }, 'Reset'))));
      body.append(tr);
    });
  }

  async function load() {
    const q = new URLSearchParams({ limit: String(PAGE), offset: String(offset) });
    [['q', $('ttSearch').value.trim()], ['line', $('ttLine').value], ['tier', $('ttTier').value], ['state', $('ttState').value]].forEach(([k, v]) => { if (v) q.set(k, v); });
    render(await api('/techtree/costs?' + q));
  }
  const reload = () => { offset = 0; load().catch((e) => { if (e.message !== 'forbidden') Admin.toast(e.message, true); }); };

  Admin.page({
    id: 'techtree', title: 'Tech tree', group: 'Game', order: 30,
    html: `<style>
      #page-techtree .tt-amt { width: 96px; } #page-techtree .tt-st { width: 128px; } #page-techtree .tt-src { width: 128px; }
      #page-techtree .tt-line { margin: 0 0 4px; flex-wrap: nowrap; } #page-techtree .tt-def { font-size: 11px; }
      #page-techtree .tt-act { display: grid; gap: 4px; justify-items: stretch; }
      #page-techtree .tt-warn { min-width: 170px; max-width: 260px; font-size: 12px; } #page-techtree tr.tt-dirty td { background: var(--accent-soft); }
    </style>
    <div class="grid" id="ttCards"></div>
    <p class="muted">Research (XP) and purchase prices per node. Each value records its origin: GUESS (the code's placeholder), Sourced (with the source) or Accepted (a deliberate design choice). Empty amount = the code's value. Players see changes at their next login; XP and credits already spent are not changed.</p>
    <div class="row">
      <input id="ttSearch" type="search" placeholder="Search ship, item name or id">
      <select id="ttLine"><option value="">All lines</option></select>
      <select id="ttTier"><option value="">All tiers</option><option>1</option><option>2</option><option>3</option><option>4</option><option>5</option></select>
      <select id="ttState"><option value="">All values</option><option value="overridden">Overridden</option><option value="annotated">Annotated only</option><option value="guess">Still a GUESS</option></select>
      <span class="spacer"></span>
      <button class="btn" id="ttExport">Export JSON</button>
      <label class="btn">Import JSON<input id="ttImport" type="file" accept=".json,application/json" class="hidden"></label>
    </div>
    <div class="table-wrap"><table><thead><tr><th>Node</th><th>Research XP</th><th>Purchase price</th><th>Warnings</th><th></th></tr></thead><tbody id="ttNodes"></tbody></table></div>
    <div class="row" style="margin-top:8px"><button class="btn" id="ttPrev">Previous</button><button class="btn" id="ttNext">Next</button><span class="muted" id="ttCount"></span></div>
    <p id="ttMsg" class="muted"></p>`,
    init() {
      $('ttSearch').oninput = debounce(reload, 250);
      ['ttLine', 'ttTier', 'ttState'].forEach((id) => { $(id).onchange = reload; });
      $('ttPrev').onclick = () => { offset = Math.max(0, offset - PAGE); load(); };
      $('ttNext').onclick = () => { offset += PAGE; load(); };
      $('ttExport').onclick = async () => {
        const d = await act(() => api('/techtree/export'), 'ttMsg', (x) => `Exported ${x.values.length} values.`);
        if (d) download('techtree-costs.json', JSON.stringify(d, null, 2), 'application/json');
      };
      $('ttImport').onchange = async (ev) => {
        const f = ev.target.files[0]; ev.target.value = ''; if (!f) return;
        let d; try { d = JSON.parse(await f.text()); if (!Array.isArray(d.values) || !d.values.length) throw new Error('the file has no "values"'); } catch (e) { $('ttMsg').className = 'err'; $('ttMsg').textContent = 'Import failed: ' + e.message; return; }
        if (!confirm(`Import ${d.values.length} values from ${f.name}? Values not in the file stay as they are.`)) return;
        if (await act(() => postJSON('/techtree/import', { values: d.values }), 'ttMsg', `Imported ${d.values.length} values.`)) load();
      };
    },
    async load() {
      // A refresh must not wipe what the admin is typing: skip it while a row is dirty.
      if (loaded && document.querySelector('#ttNodes tr.tt-dirty')) return;
      await load();
    },
    auto: 60,
  });
})();
