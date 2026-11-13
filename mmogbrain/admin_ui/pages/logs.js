// Logs: the tail of the log files the server allows (server side allow-list,
// admin_logs.go) and the battle-server logs with their crash verdict.
// Route: #/logs or #/logs/<battle log name> (opens that battle log).
(() => {
  const { $, el, api, fmt, when, bytes, chip, table, download, debounce } = Admin;
  let list = null; let lastArg = null; let tick = 0; let shown = []; let battle = '';
  const ERR = /\b(error|fatal|exception|crash(ed)?|panic|unhandled|EXCEPTION_\w+)\b|failed\b/i;
  const WARN = /\bwarn(ing)?\b/i;
  const level = (line) => (ERR.test(line) ? 'err' : WARN.test(line) ? 'warn' : '');
  const SOURCES = [['mmogbrain', 'mmogbrain (server)'], ['firmament', 'mmogbrain (social/traffic)'], ['dn-dedicated', 'dn-dedicated (battle server control)'],
    ['mod', 'battle-server mod'], ['crashprobe', 'crash handler / stack probe'], ['gateway', 'gateway'], ['auth-server', 'auth-server'],
    ['legacy-api', 'legacy-api'], ['master-server', 'master-server'], ['battle', 'A battle server log…']];

  const wanted = () => ({ src: $('lgSrc').value, lines: $('lgLines').value, grep: $('lgGrep').value.trim(), noise: $('lgNoise').checked ? '1' : '0' });

  function renderLines() {
    const lv = $('lgLevel').value;
    const keep = shown.filter((l) => (lv === 'err' ? level(l) === 'err' : lv === 'warn' ? level(l) !== '' : true));
    const out = $('lgOut');
    const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 40;
    out.replaceChildren();
    if (!keep.length) out.append('(no matching lines)');
    keep.forEach((l, i) => {
      const lvl = level(l);
      out.append(el('span', { class: lvl ? 'lg-' + lvl : null }, l), i < keep.length - 1 ? '\n' : '');
    });
    if (atBottom || !$('lgFollow').checked) out.scrollTop = out.scrollHeight;
    $('lgDownload').disabled = !keep.length;
    return keep;
  }

  function renderList() {
    if (!list) return;
    const rows = list.map((l) => {
      const crash = l.crash ? chip(l.crash, 'bad') : chip('clean', 'good');
      const pick = el('span', { class: 'link' }, l.name);
      return el('tr', { class: l.name === battle ? 'click lg-on' : 'click', onclick: () => { battle = l.name; Admin.refresh(); } },
        el('td', {}, when(l.modified)), el('td', {}, pick), el('td', {}, l.mode || ''), el('td', { class: 'num' }, bytes(l.size)), el('td', {}, crash));
    });
    const t = table([{ label: 'Modified' }, 'Log', 'Mode', { label: 'Size', num: true }, 'Outcome'], [], 'No battle logs yet.');
    if (rows.length) { const body = t.querySelector('tbody'); body.replaceChildren(...rows); }
    $('lgList').replaceChildren(el('h3', {}, 'Battle server logs (newest 60)'), t);
  }

  async function loadList() { list = (await api('/battle-logs')).logs; renderList(); }

  async function load(arg) {
    if (arg !== lastArg) { lastArg = arg; if (arg) { $('lgSrc').value = 'battle'; battle = arg; } }
    const w = wanted(); const isBattle = w.src === 'battle';
    $('lgListBox').classList.toggle('hidden', !isBattle);
    if (isBattle && (!list || tick++ % 12 === 0)) await loadList();
    if (isBattle) { if (!battle && list && list.length) battle = list[0].name; renderList(); w.name = battle; }
    if (isBattle && !battle) { shown = []; renderLines(); $('lgMeta').textContent = 'No battle logs yet.'; return; }
    const p = new URLSearchParams(w);
    let d;
    try { d = await api('/logs?' + p); } catch (e) { if (e.message === 'forbidden') throw e; shown = []; $('lgOut').textContent = e.message; $('lgMeta').textContent = ''; return; }
    shown = d.lines;
    const keep = renderLines();
    $('lgMeta').textContent = d.exists ? `${d.file} · ${bytes(d.size)} · modified ${d.modified} UTC · ${fmt(d.matched)} matching lines`
      + (d.matched > shown.length ? `, last ${fmt(shown.length)} shown` : '') + (keep.length !== shown.length ? `, ${fmt(keep.length)} after the level filter` : '')
      + (d.truncated ? ' · only the last 2 MB of the file is read' : '') : `${d.file}: the file does not exist (yet).`;
  }

  Admin.page({
    id: 'logs', title: 'Logs', group: 'System', order: 20,
    auto: () => ($('lgFollow') && $('lgFollow').checked ? 5 : 0),
    html: `
      <style>#page-logs .lg-err { color: var(--bad); } #page-logs .lg-warn { color: var(--warn); }
      #page-logs tr.lg-on td { background: var(--accent-soft); } #page-logs pre.log { min-height: 160px; } #page-logs #lgMeta { margin: 8px 0 6px; }</style>
      <div class="row">
        <select id="lgSrc"></select>
        <input id="lgGrep" type="search" maxlength="200" placeholder="Filter lines (text)">
        <select id="lgLevel"><option value="">All levels</option><option value="warn">Warnings and errors</option><option value="err">Errors only</option></select>
        <select id="lgLines"><option>100</option><option selected>300</option><option>1000</option><option>2000</option></select>
        <label class="check muted"><input type="checkbox" id="lgNoise"> show noise</label>
        <label class="check"><input type="checkbox" id="lgFollow"> follow (5 s)</label>
        <button class="btn" id="lgLoad">Reload</button>
        <button class="btn" id="lgDownload">Download shown lines</button>
      </div>
      <div class="muted" id="lgMeta"></div>
      <pre class="log" id="lgOut"></pre>
      <section class="hidden" id="lgListBox"><div class="panel" id="lgList"></div></section>`,
    init() {
      SOURCES.forEach(([v, n]) => $('lgSrc').append(el('option', { value: v }, n)));
      const reload = () => Admin.refresh();
      $('lgSrc').onchange = reload; $('lgLines').onchange = reload; $('lgNoise').onchange = reload;
      $('lgGrep').oninput = debounce(reload, 300);
      $('lgLevel').onchange = () => renderLines();
      $('lgLoad').onclick = () => { list = null; tick = 0; Admin.refresh(); };
      $('lgFollow').onchange = () => Admin.schedule();
      $('lgDownload').onclick = () => {
        const name = $('lgSrc').value === 'battle' ? battle : $('lgSrc').value + '.log';
        const lv = $('lgLevel').value;
        download(name.replace(/\.log$/, '') + '-shown.log', shown.filter((l) => (lv === 'err' ? level(l) === 'err' : lv === 'warn' ? level(l) !== '' : true)).join('\n') + '\n', 'text/plain');
      };
    },
    load,
  });
})();
