// System: host, process, database, backups, certificates and the runtime
// settings worth seeing. Every part of /system is optional ("error" text).
(() => {
  const { $, el, api, postJSON, fmt, when, ago, duration, bytes, card, chip, table, kv, meter, link, act } = Admin;

  const group = (title, ...kids) => el('section', { class: 'panel' }, el('h2', {}, title), ...kids);
  const errLine = (p) => (p && p.error ? el('div', { class: 'err' }, p.error) : null);
  const usage = (label, used, total, what) => el('div', { class: 'sysmeter' },
    el('div', { class: 'row' }, el('b', {}, label), el('span', { class: 'spacer' }), el('span', { class: 'muted' }, `${bytes(used)} used of ${bytes(total)}${what ? ' ' + what : ''}`)),
    meter(total ? used / total : 0));

  function host(h) {
    const kids = [];
    const load = h.load && h.load.length ? h.load.map((x) => x.toFixed(2)).join(' / ') : null;
    kids.push(kv([['Load (1 / 5 / 15 min)', load && `${load} on ${h.cpus} CPUs`], ['Host up', h.uptime_s != null ? duration(h.uptime_s) : null], ['System', h.os && !load ? h.os + ' (no details on this system)' : null]]));
    if (h.mem_total) kids.push(usage('Memory', h.mem_total - h.mem_available, h.mem_total, ''));
    (h.disks || []).forEach((d) => kids.push(d.error ? el('div', { class: 'muted' }, d.label + ': ' + d.error) : usage(d.label, d.total - d.free, d.total, `(${bytes(d.free)} free)`)));
    return group('Host', ...kids);
  }

  function proc(p) {
    const b = p.build || {};
    const rev = b.revision ? b.revision.slice(0, 10) + (b.modified === 'true' ? ' (local changes)' : '') : null;
    return group('mmogbrain', kv([['Up', duration(p.uptime_s)], ['Go', p.go_version], ['Build', rev], ['Built from', b.time ? when(b.time) : null],
      ['Goroutines', fmt(p.goroutines)], ['Heap in use', bytes(p.heap_inuse)], ['GC runs', fmt(p.gc_count)], ['Open files', p.open_fds != null ? fmt(p.open_fds) : null]]));
  }

  function database(d) {
    const s = d.sizes || {};
    const kids = [errLine(d), kv([['File', d.file], ['Database', s.db != null ? bytes(s.db) : null], ['Write-ahead log', s.wal != null ? bytes(s.wal) : null], ['Shared memory', s.shm != null ? bytes(s.shm) : null], ['Schema version', d.schema_version != null ? fmt(d.schema_version) : null]])];
    kids.push(el('h3', {}, 'Rows per table'), table(['Table', { label: 'Rows', num: true }],
      (d.tables || []).map((t) => [el('code', {}, t.table), t.error ? chip(t.error, 'warn') : fmt(t.rows)]), 'No tables read.'));
    return group('Database', ...kids.filter(Boolean));
  }

  function backups(b, onBackup) {
    const btn = el('button', { class: 'btn primary', type: 'button', onclick: onBackup }, 'Back up now');
    if (!b.script) { btn.disabled = true; btn.title = 'scripts/backup.sh is not beside mmogbrain'; }
    const kids = [el('div', { class: 'row' }, btn, b.script ? null : chip('scripts/backup.sh not found beside mmogbrain', 'warn'), el('span', { id: 'sysBackupMsg' })),
      el('div', { class: 'muted' }, `${fmt(b.count || 0)} archive${b.count === 1 ? '' : 's'} on disk` + (b.error ? ' · ' + b.error : ''))];
    kids.push(table(['Archive', { label: 'Size', num: true }, 'Taken'], (b.files || []).map((f) => [el('code', {}, f.name), bytes(f.size), el('span', {}, when(f.modified), ' ', el('span', { class: 'muted' }, '(' + ago(f.modified) + ')'))]), 'No backups yet.'));
    if (b.log_tail) kids.push(el('h3', {}, 'backup.log (end)'), el('pre', { class: 'log' }, b.log_tail));
    kids.push(el('pre', { class: 'log hidden', id: 'sysBackupOut' }));
    return group('Backups', ...kids);
  }

  function certs(list) {
    return group('Certificates', table(['Certificate', 'Subject', 'Expires', 'Left'], list.map((c) => c.error ? [c.label, '', '', chip(c.error, 'bad')] : [
      c.label, c.cn || '—', when(c.not_after), chip(c.days_left < 0 ? 'expired' : c.days_left + ' days', c.days_left < 0 ? 'bad' : c.days_left < 30 ? 'warn' : 'good')]), 'No certificates.'));
  }

  function config(list, tls, logs) {
    const rows = list.map((c) => [el('code', {}, c.name), c.value ? c.value : (c.set ? chip('set', 'good') : el('span', { class: 'muted' }, 'not set'))]);
    return group('Configuration',
      el('div', { class: 'muted' }, 'Only non-secret settings. Addresses, domains and anything key-like show as set / not set.'),
      table(['Setting', 'Value'], rows, 'Nothing to show.'),
      el('h3', {}, 'TLS and logs'),
      kv([['SHA-384 fallback clients (24 h)', fmt(tls.clients) + (tls.mode ? ' (mode ' + tls.mode + ')' : '')],
        ['run/*.log', `${fmt(logs.run_files)} files, ${bytes(logs.run_bytes)}`], ['Battle server logs', `${fmt(logs.battle_files)} files, ${bytes(logs.battle_bytes)}`], ['Read them', link('Logs page', 'logs')]]));
  }

  async function load() {
    const d = await api('/system');
    const warn = [];
    (d.certs || []).forEach((c) => { if (!c.error && c.days_left < 30) warn.push(`${c.label} expires in ${Math.max(c.days_left, 0)} days`); });
    const dsk = ((d.host || {}).disks || []).find((x) => x.total && x.free / x.total < 0.1);
    if (dsk) warn.push(dsk.label + ' is over 90% full');
    $('sysWarn').replaceChildren(...warn.map((w) => el('div', { class: 'alert warn' }, el('span', { class: 'dot warn' }), el('b', {}, 'Attention'), el('span', {}, w))));
    $('sysCols').replaceChildren(
      el('div', { class: 'syscol' }, host(d.host || {}), proc(d.process || {}), certs(d.certs || []), database(d.database || {})),
      el('div', { class: 'syscol' }, backups(d.backups || {}, doBackup), config(d.config || [], d.tls || {}, d.logs || {})));
  }

  async function doBackup() {
    if (!confirm('Run scripts/backup.sh now?\n\nIt copies the databases, certificates and secrets into one archive. This can take a minute.')) return;
    const r = await act(async () => postJSON('/system/backup', {}), $('sysBackupMsg'), (x) => (x.ok ? 'Backup finished.' : 'Backup failed: ' + (x.error || '')));
    if (r) { const o = $('sysBackupOut'); o.textContent = r.output || ''; o.classList.toggle('hidden', !r.output); await load().catch(() => {}); const o2 = $('sysBackupOut'); o2.textContent = r.output || ''; o2.classList.toggle('hidden', !r.output); }
  }

  Admin.page({
    id: 'system', title: 'System', group: 'System', order: 10, auto: 30,
    html: `<style>
      #page-system .syscols, #sysCols { display: grid; grid-template-columns: repeat(auto-fit, minmax(380px, 1fr)); gap: 16px; align-items: start; }
      #page-system .syscol { display: grid; gap: 16px; min-width: 0; }
      #page-system .syscol section { margin: 0; }
      #page-system .sysmeter { margin: 10px 0; }
      #page-system h3 { font-size: 13px; margin: 14px 0 6px; }
      @media (max-width: 640px) { #sysCols { grid-template-columns: minmax(0, 1fr); } }
    </style>
    <div class="alerts" id="sysWarn"></div>
    <div id="sysCols"></div>`,
    load,
  });
})();
