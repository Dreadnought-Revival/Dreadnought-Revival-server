// Chat: every stored chat line, by channel, with search, player filter and a
// follow mode. Mute is the moderation action on a line.
Admin.page({
  id: 'chat', title: 'Chat', group: 'Live', order: 50, auto: 10,
  html: `
<style>
#page-chat .cols { display: grid; grid-template-columns: 220px 1fr; gap: 12px; align-items: start; }
#page-chat .chans { display: flex; flex-direction: column; gap: 2px; }
#page-chat .chans button { text-align: left; display: flex; gap: 6px; align-items: baseline; }
#page-chat .chans button small { opacity: .7; margin-left: auto; }
#page-chat .ln .t { white-space: nowrap; }
#page-chat .chanSel { display: none; }
#page-chat .lines { border: 1px solid var(--line); border-radius: 8px; background: var(--panel); max-height: 70vh; overflow-y: auto; }
#page-chat .ln { display: grid; grid-template-columns: 124px 150px 1fr auto; gap: 8px; padding: 5px 10px; border-bottom: 1px solid var(--line); align-items: baseline; }
#page-chat .ln:last-child { border-bottom: 0; }
#page-chat .ln .t { color: var(--muted); font-size: 12px; }
#page-chat .ln .x { overflow-wrap: anywhere; }
#page-chat .ln .act { visibility: hidden; }
#page-chat .ln:hover .act, #page-chat .ln:focus-within .act { visibility: visible; }
#page-chat .ln .c { color: var(--muted); font-size: 12px; }
@media (max-width: 760px) {
  #page-chat .cols { grid-template-columns: 1fr; }
  #page-chat .chans { display: none; } #page-chat .chanSel { display: block; }
  #page-chat .ln { grid-template-columns: 1fr auto; }
  #page-chat .ln .t, #page-chat .ln .n { grid-column: 1 / -1; }
  #page-chat .ln .act { visibility: visible; }
}
</style>
<section>
  <div class="row">
    <input type="search" id="chQ" placeholder="Search text">
    <input type="search" id="chPid" placeholder="Player id">
    <select id="chSel" class="chanSel"></select>
    <label class="check"><input type="checkbox" id="chFollow" checked> Follow newest</label>
    <span class="muted" id="chCount"></span>
  </div>
  <div class="cols">
    <div class="chans" id="chChans"></div>
    <div>
      <div class="lines" id="chLines"></div>
      <div class="row" style="margin-top:10px"><button class="btn" id="chMore" type="button">Older messages</button><span id="chMsg"></span></div>
    </div>
  </div>
</section>`,
  init(root) {
    const { $, debounce, refresh } = Admin;
    this.s = { channel: '', before: 0, extra: [], more: false };
    const reset = () => { this.s.before = 0; this.s.extra = []; refresh(); };
    $('chQ').oninput = debounce(reset, 300);
    $('chPid').oninput = debounce(reset, 300);
    $('chSel').onchange = (e) => { this.s.channel = e.target.value; reset(); };
    $('chMore').onclick = () => { this.s.before = this.s.lastSeq; this.s.loadMore = true; refresh(); };
  },
  async load() {
    const { $, el, api, postJSON, act, when, playerLink, link, fmt, toast } = Admin;
    const s = this.s;
    const params = new URLSearchParams({ limit: '100' });
    if (s.channel) params.set('channel', s.channel);
    if ($('chQ').value.trim()) params.set('q', $('chQ').value.trim());
    if ($('chPid').value.trim()) params.set('pid', $('chPid').value.trim());
    const paging = s.loadMore && s.before; s.loadMore = false;
    if (paging) params.set('before', String(s.before));
    const d = await api('/chat?' + params);
    // The auto refresh re-reads the newest page; older pages stay below it.
    if (paging) { s.extra = s.extra.concat(d.messages); s.more = d.more; } else { s.newest = d.messages; if (!s.extra.length) s.more = d.more; }
    const all = (s.newest || []).concat(s.extra);
    s.lastSeq = all.length ? all[all.length - 1].seq : 0;

    // channels
    const chans = [{ channel: '', label: 'All channels', count: d.channels.reduce((a, c) => a + c.count, 0), last: '' }, ...d.channels];
    $('chChans').replaceChildren(...chans.map((c) => el('button', { class: 'btn small' + (c.channel === s.channel ? ' on' : ''), type: 'button', title: c.channel || null,
      onclick: () => { s.channel = c.channel; s.before = 0; s.extra = []; $('chSel').value = c.channel; Admin.refresh(); } },
      c.label, el('small', {}, fmt(c.count)))));
    $('chSel').replaceChildren(...chans.map((c) => el('option', { value: c.channel }, `${c.label} (${fmt(c.count)})`)));
    $('chSel').value = s.channel;

    // lines, oldest at the top so the newest is at the bottom
    const box = $('chLines'); const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
    const label = Object.fromEntries(d.channels.map((c) => [c.channel, c]));
    const rows = all.slice().reverse().map((m) => {
      const ci = label[m.channel];
      const chan = ci && ci.match ? link(ci.label, 'match', ci.match) : el('span', {}, ci ? ci.label : m.channel);
      const mute = el('button', { class: 'btn small danger act', type: 'button', onclick: async () => {
        const reason = prompt('Mute this player for 24 h. Reason:', 'chat abuse');
        if (reason === null) return;
        if (!reason.trim()) { toast('A reason is required.', true); return; }
        if (!confirm('Mute ' + (m.name || m.pid) + ' for 24 hours?')) return;
        await act(() => postJSON('/players/' + m.pid + '/mute', { reason: reason.trim(), hours: 24 }), null, 'Muted for 24 h.');
        Admin.refresh();
      } }, 'mute 24 h');
      return el('div', { class: 'ln' }, el('span', { class: 't' }, when(m.sent)),
        el('span', { class: 'n' }, playerLink(m.pid, m.name), m.muted ? ' ' : null, m.muted ? Admin.chip('muted', 'warn') : null, s.channel ? null : el('div', { class: 'c' }, chan)),
        el('span', { class: 'x' }, m.text), mute);
    });
    box.replaceChildren(...(rows.length ? rows : [el('div', { class: 'empty', style: 'padding:12px' }, 'No messages match.')]));
    if ($('chFollow').checked && !paging && !s.extra.length) box.scrollTop = box.scrollHeight;
    else if (atBottom && !paging) box.scrollTop = box.scrollHeight;
    $('chMore').classList.toggle('hidden', !s.more);
    $('chCount').textContent = `${fmt(all.length)} shown`;
  },
});
