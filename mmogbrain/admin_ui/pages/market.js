// Market: price overrides (searchable table, inline edit, reset) and sales
// (active / scheduled / ended). Prices are shared by the shelf and the till;
// players see a change at their next login. Every route audits itself.
(() => {
  const { $, el, api, postJSON, fmt, chip, when, duration, act } = Admin;
  let data = null; const pending = new Map(); // price key -> typed text

  const parse = (txt) => (/^\d+$/.test(txt.trim()) ? Number(txt.trim()) : NaN);
  const valid = (txt) => { const n = parse(txt); return n >= 1 && n <= data.max_price ? '' : `Whole number 1 to ${fmt(data.max_price)}`; };
  const changedKeys = () => [...pending].filter(([k, t]) => { const p = data.prices.find((x) => x.key === k); return p && t.trim() !== String(p.value); }).map(([k]) => k);

  function bar() {
    const n = changedKeys().length; const bad = changedKeys().filter((k) => valid(pending.get(k)));
    $('mk-count').textContent = n ? `${n} unsaved price change${n === 1 ? '' : 's'}` : '';
    $('mk-save').disabled = !n || bad.length > 0; $('mk-discard').disabled = !n;
    $('mk-resetAll').disabled = !data.prices.some((p) => p.overridden);
  }

  function renderPrices() {
    const q = $('mk-q').value.trim().toLowerCase(); const grp = $('mk-group').value; const only = $('mk-only').checked;
    const rows = data.prices.filter((p) => (!q || (p.label + ' ' + p.key + ' ' + p.group).toLowerCase().includes(q)) && (!grp || p.group === grp) && (!only || p.overridden || pending.has(p.key))).map((p) => {
      const typed = pending.get(p.key);
      const input = el('input', { type: 'text', inputmode: 'numeric', value: typed != null ? typed : String(p.value), 'aria-label': 'Price of ' + p.label, style: 'width:120px' });
      const err = el('span', { class: 'err' });
      const tr = el('tr', {});
      const upd = () => {
        const t = input.value;
        if (t.trim() === String(p.value)) pending.delete(p.key); else pending.set(p.key, t);
        const e = t.trim() === String(p.value) ? '' : valid(t);
        err.textContent = e; input.setAttribute('aria-invalid', e ? 'true' : 'false');
        tr.classList.toggle('mk-dirty', pending.has(p.key)); bar();
      };
      input.oninput = upd;
      tr.append(el('td', {}, p.label, el('div', { class: 'muted mono' }, p.key)), el('td', {}, p.group), el('td', {}, p.currency),
        el('td', { class: 'num' }, fmt(p.default)), el('td', {}, input, err),
        el('td', {}, p.overridden ? [chip('overridden', 'warn'), ' ', el('a', { class: 'link', onclick: () => resetKeys([p.key], `${p.label}: back to ${fmt(p.default)}.`) }, 'use default')] : el('span', { class: 'muted' }, 'default')));
      if (pending.has(p.key)) { tr.classList.add('mk-dirty'); const e = valid(typed); err.textContent = e; }
      return tr;
    });
    const host = $('mk-prices');
    host.replaceChildren(Admin.table(['Item', 'Category', 'Currency', { label: 'Default', num: true }, 'Price', ''], rows, 'No price matches.'));
    $('mk-shown').textContent = `${rows.length} of ${data.prices.length} prices`;
    bar();
  }

  function sale(x) {
    const now = Date.now(); const s = Date.parse(x.start); const e = Date.parse(x.end);
    const state = x.state === 'running' ? chip('active', 'good') : x.state === 'upcoming' ? chip('scheduled', 'warn') : chip('ended');
    const remain = x.state === 'running' ? `ends in ${duration((e - now) / 1000)}` : x.state === 'upcoming' ? `starts in ${duration((s - now) / 1000)}` : `ended ${duration((now - e) / 1000)} ago`;
    return el('tr', { class: x.state === 'ended' ? 'muted' : '' }, el('td', {}, x.name, x.built_in ? el('span', { class: 'muted' }, ' (built in)') : null),
      el('td', {}, state), el('td', { class: 'num' }, x.percent + ' %'), el('td', {}, `${x.scope_label} (${fmt(x.items)})`),
      el('td', {}, when(x.start)), el('td', {}, when(x.end)), el('td', {}, remain),
      el('td', {}, x.state === 'ended' ? '' : el('button', { class: 'btn small danger', onclick: () => endSale(x) }, 'End now')));
  }
  function renderSales() {
    const all = data.sales; const showEnded = $('mk-ended').checked;
    const list = all.filter((x) => showEnded || x.state !== 'ended').sort((a, b) => ({ running: 0, upcoming: 1, ended: 2 }[a.state] - { running: 0, upcoming: 1, ended: 2 }[b.state]) || Date.parse(b.end) - Date.parse(a.end));
    $('mk-sales').replaceChildren(Admin.table(['Sale', 'Status', { label: 'Off', num: true }, 'Covers', 'Start', 'End', 'Time', ''], list.map(sale), 'No sales.'));
    const nEnded = all.filter((x) => x.state === 'ended').length;
    $('mk-endedN').textContent = nEnded ? `(${nEnded})` : '';
  }

  function render(d) {
    data = d;
    for (const k of [...pending.keys()]) if (!d.prices.some((p) => p.key === k)) pending.delete(k);
    const g = $('mk-group'); const cur = g.value;
    g.replaceChildren(el('option', { value: '' }, 'All categories'), ...[...new Set(d.prices.map((p) => p.group))].map((x) => el('option', { value: x }, x)));
    g.value = cur;
    const sc = $('mk-scope');
    if (!sc.options.length) d.scopes.forEach((x) => sc.append(el('option', { value: x.scope }, `${x.label} (${fmt(x.items)} items)`)));
    renderPrices(); renderSales();
  }

  async function savePrices(body, ok) {
    const d = await act(() => postJSON('/market/prices', body), 'mk-msg', ok);
    if (d) { pending.clear(); render(d); }
  }
  function resetKeys(keys, ok) {
    if (!confirm(`Put ${keys.length === 1 ? 'this price' : keys.length + ' prices'} back to the default?`)) return;
    const body = {}; keys.forEach((k) => { body[k] = null; });
    savePrices(body, ok);
  }
  async function endSale(x) {
    if (!confirm(`End "${x.name}" now? Prices go back to normal at each player's next login.`)) return;
    const d = await act(() => postJSON(`/market/sales/${encodeURIComponent(x.id)}/end`, {}), 'mk-saleMsg', 'Sale ended.');
    if (d) render(d);
  }

  Admin.page({
    id: 'market', title: 'Market', group: 'Economy', order: 20,
    html: `<style>
      #page-market tr.mk-dirty td { background: color-mix(in srgb, var(--warn) 14%, transparent); }
      #page-market td input[aria-invalid=true] { border-color: var(--bad); }
      #page-market td .err { display: block; font-size: 12px; }
    </style>
    <div class="note">The store and the purchase use the same price. Players see a change in the Market at their next login (the game loads the catalog then); anyone who buys before that pays the new price.</div>
    <section><h2>Prices</h2>
      <div class="row"><input type="search" id="mk-q" placeholder="Search items"><select id="mk-group"></select>
        <label class="check"><input type="checkbox" id="mk-only"> Overridden only</label><span class="muted" id="mk-shown"></span></div>
      <div id="mk-prices"></div>
      <div class="row" style="margin-top:8px"><button class="btn primary" id="mk-save">Save changed prices</button><button class="btn" id="mk-discard">Discard edits</button>
        <button class="btn danger" id="mk-resetAll">Reset all overrides</button><b id="mk-count"></b>
        <span class="muted">Single ships keep this curve until #15 adds per-ship prices.</span></div>
      <p id="mk-msg"></p>
    </section>
    <section><h2>Sales</h2>
      <div class="row"><span class="muted">Sales lower cosmetic prices; the game shows them in the ship customizer and on the captain screen. Hero ships and bundles cannot be put on sale yet.</span>
        <span class="spacer"></span><label class="check"><input type="checkbox" id="mk-ended"> Show ended <span id="mk-endedN"></span></label></div>
      <div id="mk-sales"></div><p id="mk-saleMsg"></p>
    </section>
    <section class="manage"><h2>New sale</h2>
      <div class="fields">
        <label>Name<input id="mk-slName" type="text" maxlength="60" placeholder="e.g. Winter Sale"></label>
        <label>Discount (%)<input id="mk-slPercent" type="number" min="1" max="90" step="1" value="25"></label>
        <label>Covers<select id="mk-scope"></select></label>
        <label>Start (your time; empty = now)<input id="mk-slStart" type="datetime-local"></label>
        <label>End (your time)<input id="mk-slEnd" type="datetime-local"></label>
      </div>
      <div class="row"><button class="btn primary" id="mk-slCreate">Create sale</button></div>
      <p id="mk-slMsg"></p>
    </section>`,
    init() {
      const redo = Admin.debounce(() => data && renderPrices(), 120);
      $('mk-q').oninput = redo; $('mk-group').onchange = redo; $('mk-only').onchange = redo;
      $('mk-ended').onchange = () => data && renderSales();
      $('mk-discard').onclick = () => { pending.clear(); renderPrices(); $('mk-msg').textContent = ''; };
      $('mk-resetAll').onclick = () => { const keys = data.prices.filter((p) => p.overridden).map((p) => p.key); if (keys.length) resetKeys(keys, 'All overrides removed.'); };
      $('mk-save').onclick = () => {
        const keys = changedKeys(); if (!keys.length) return;
        const body = {}; const what = [];
        for (const k of keys) { const p = data.prices.find((x) => x.key === k); const v = parse(pending.get(k)); body[k] = v; what.push(`${p.label}: ${fmt(p.value)} -> ${fmt(v)} ${p.currency}`); }
        if (!confirm('Change these prices?\n\n' + what.join('\n'))) return;
        savePrices(body, 'Prices saved.');
      };
      $('mk-slCreate').onclick = async () => {
        const msg = $('mk-slMsg'); const bad = (t) => { msg.className = 'err'; msg.textContent = t; };
        const name = $('mk-slName').value.trim().replace(/\s+/g, ' '); const pct = Number($('mk-slPercent').value);
        const sv = $('mk-slStart').value; const ev = $('mk-slEnd').value;
        if (!name) return bad('Give the sale a name.');
        if (!Number.isInteger(pct) || pct < 1 || pct > 90) return bad('The discount must be a whole number from 1 to 90.');
        if (!ev) return bad('Pick the end.');
        const start = sv ? new Date(sv) : new Date(); const end = new Date(ev);
        if (!(end > start) || !(end > new Date())) return bad('The end must be in the future and after the start.');
        const sel = $('mk-scope');
        if (!confirm(`Create "${name}": ${pct} % off ${sel.options[sel.selectedIndex].text}, ${sv ? 'from ' + sv : 'from now'} until ${ev}?`)) return;
        const d = await act(() => postJSON('/market/sales', { name, percent: pct, scope: sel.value, start: sv ? start.toISOString() : '', end: end.toISOString() }), msg, 'Sale created. Players see it at their next login.');
        if (d) { $('mk-slName').value = ''; render(d); }
      };
    },
    async load() { const d = await api('/market'); if (data && pending.size) { data = d; renderSales(); return; } render(d); },
    auto: 60,
  });
})();
