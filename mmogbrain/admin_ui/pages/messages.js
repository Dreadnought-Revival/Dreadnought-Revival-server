// Server messages: a line to everyone online now, and the message of the day.
// The server supports no scheduling or expiry (server_messages.go): a message
// goes out when sent, the MOTD stays until changed or turned off.
(() => {
  const { $, el, api, postJSON, fmt, when, act } = Admin;
  let motdShown = false; let max = 300;

  // The server squeezes whitespace to single spaces before sending
  // (cleanServerMessage); the preview shows the same text.
  const clean = (t) => t.split(/\s+/).filter(Boolean).join(' ');
  function preview(inputId, outId, countId) {
    const t = clean($(inputId).value);
    const out = $(outId); out.replaceChildren();
    if (!t) { out.append(el('span', { class: 'muted' }, 'Nothing to preview.')); }
    else out.append(el('span', { class: 'mg-name' }, 'Server'), ': ', t);
    const n = [...t].length;
    $(countId).textContent = `${n} / ${max}`;
    $(countId).className = n > max ? 'err' : 'muted';
  }

  async function load() {
    const d = await api('/messages');
    max = d.max || 300;
    $('mg-online').textContent = `${fmt(d.online)} online now`;
    $('mg-motdNow').textContent = d.motd ? 'A message of the day is set.' : 'No message of the day.';
    if (!motdShown) { $('mg-motdText').value = d.motd; motdShown = true; preview('mg-motdText', 'mg-motdPrev', 'mg-motdCount'); }
    const rows = d.recent.map((x) => {
      let det = {}; try { det = JSON.parse(x.details); } catch (e) { /* details are JSON written by us */ }
      const sent = x.action === 'server message';
      return el('tr', {}, el('td', {}, when(x.at)), el('td', {}, sent ? 'Sent' : (det.text ? 'MOTD set' : 'MOTD off')),
        el('td', { class: 'wrap' }, det.text || el('span', { class: 'muted' }, '—')), el('td', { class: 'num' }, sent ? fmt(det.recipients) : ''), el('td', { class: 'muted' }, x.actor));
    });
    const f = $('mg-filter').value.trim().toLowerCase();
    const shown = f ? rows.filter((r) => r.textContent.toLowerCase().includes(f)) : rows;
    Admin.fill('mg-rows', shown, f ? 'No message matches.' : 'Nothing sent yet.', 5);
  }

  async function post(path, body, msgId, ok) {
    const r = await act(() => postJSON(path, body), msgId, ok);
    if (r) load();
    return !!r;
  }

  Admin.page({
    id: 'messages', title: 'Messages', group: 'Game', order: 40,
    html: `<style>
      #page-messages .mg-line { font-family: var(--mono); font-size: 13px; padding: 8px 10px; border-radius: 8px; background: var(--panel-2); border: 1px solid var(--line); margin-top: 8px; white-space: pre-wrap; overflow-wrap: anywhere; }
      #page-messages .split > section { margin-top: 0; }
      #page-messages .mg-name { color: var(--warn); font-weight: 600; }
    </style>
    <div class="note">Messages appear in everyone's Global chat from "Server", like the online-count lines. A chat line cannot be edited or deleted once sent. The server has no scheduling or expiry: a message goes out when you send it, and the message of the day stays until changed or turned off.</div>
    <div class="split">
      <section class="manage"><h2>Send to everyone online now</h2>
        <textarea id="mg-text" maxlength="300" placeholder="e.g. The server restarts in 10 minutes for an update."></textarea>
        <div class="muted" style="margin-top:8px">Preview in Global chat</div>
        <div class="mg-line" id="mg-prev"></div>
        <div class="row" style="margin-top:8px"><button class="btn primary" id="mg-send">Send</button><span id="mg-count" class="muted"></span><span class="muted" id="mg-online"></span></div>
        <p id="mg-msg"></p>
      </section>
      <section class="manage"><h2>Message of the day</h2>
        <div class="muted" style="margin-bottom:8px">Sent to each player a few seconds after they come online. Empty = none.</div>
        <textarea id="mg-motdText" maxlength="300" placeholder="e.g. Welcome back, Captain! Halloween sale: 50% off until 24 November."></textarea>
        <div class="muted" style="margin-top:8px">Preview in Global chat</div>
        <div class="mg-line" id="mg-motdPrev"></div>
        <div class="row" style="margin-top:8px"><button class="btn primary" id="mg-motdSave">Save</button><button class="btn danger" id="mg-motdOff">Turn off</button><span id="mg-motdCount" class="muted"></span><span class="muted" id="mg-motdNow"></span></div>
        <p id="mg-motdMsg"></p>
      </section>
    </div>
    <section><h2>Sent lately</h2>
      <div class="row"><input type="search" id="mg-filter" placeholder="Filter the history"><span class="muted">The last 50 messages and MOTD changes, from the audit log.</span></div>
      <div class="table-wrap"><table><thead><tr><th>When</th><th>What</th><th>Text</th><th class="num">Players</th><th>From</th></tr></thead><tbody id="mg-rows"></tbody></table></div>
    </section>`,
    init() {
      $('mg-text').oninput = () => preview('mg-text', 'mg-prev', 'mg-count');
      $('mg-motdText').oninput = () => preview('mg-motdText', 'mg-motdPrev', 'mg-motdCount');
      preview('mg-text', 'mg-prev', 'mg-count');
      $('mg-filter').oninput = Admin.debounce(() => Admin.refresh(), 200);
      $('mg-send').onclick = async () => {
        const text = clean($('mg-text').value);
        if (!text) { $('mg-msg').className = 'err'; $('mg-msg').textContent = 'Write the message first.'; return; }
        if ([...text].length > max) { $('mg-msg').className = 'err'; $('mg-msg').textContent = `Too long (${max} characters at most).`; return; }
        if (!confirm(`Send to everyone online now?\n\n${text}`)) return;
        if (await post('/messages/send', { text }, 'mg-msg', (r) => `Sent to ${fmt(r.recipients)} player${r.recipients === 1 ? '' : 's'}.`)) { $('mg-text').value = ''; preview('mg-text', 'mg-prev', 'mg-count'); }
      };
      $('mg-motdSave').onclick = () => {
        const text = clean($('mg-motdText').value);
        if (!text) { $('mg-motdOff').click(); return; }
        if ([...text].length > max) { $('mg-motdMsg').className = 'err'; $('mg-motdMsg').textContent = `Too long (${max} characters at most).`; return; }
        post('/messages/motd', { text }, 'mg-motdMsg', 'Saved. Players get it the next time they come online.');
      };
      $('mg-motdOff').onclick = async () => {
        if (!confirm('Turn the message of the day off?')) return;
        if (await post('/messages/motd', { text: '' }, 'mg-motdMsg', 'Turned off.')) { $('mg-motdText').value = ''; preview('mg-motdText', 'mg-motdPrev', 'mg-motdCount'); }
      };
    },
    load,
  });
})();
