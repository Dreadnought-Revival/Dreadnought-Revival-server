// Overview: what needs attention, the numbers of the last 24 hours, and the
// restart-when-idle control.
(() => {
  const { $, el, api, postJSON, fmt, ago, duration, card, chart, bars, link, modeName, setBadge, act } = Admin;
  let ticks = 0;

  function renderRestart(d) {
    $('rsIdle').classList.toggle('hidden', !!d.pending);
    $('rsWaiting').classList.toggle('hidden', !d.pending);
    $('rsStart').disabled = !d.available;
    if (!d.available) { $('rsMsg').className = 'err'; $('rsMsg').textContent = 'scripts/start-services.sh is not beside mmogbrain: restart it by hand.'; }
    else if (d.error && !d.pending) { $('rsMsg').className = 'err'; $('rsMsg').textContent = d.error; }
    if (d.pending) {
      const running = d.running < 0 ? 'checking the battle servers' : `${fmt(d.running)} battle server${d.running === 1 ? '' : 's'} still running`;
      $('rsStatus').textContent = d.stage === 'restarting' ? 'Restarting now…'
        : `Waiting since ${d.since} UTC (${ago(d.since)}): ${running}${d.hold_matches ? ', new matches held' : ''}${d.error ? ' · ' + d.error : ''}`;
    }
    return d;
  }

  // alert(kind, title, text, action?): one line in the "needs attention" list.
  const alertRow = (kind, title, text, action) => el('div', { class: 'alert ' + kind }, el('span', { class: 'dot ' + (kind === 'good' ? 'good' : kind === 'bad' ? 'bad' : 'warn') }), el('b', {}, title), el('span', {}, text), el('span', { class: 'spacer' }), action);

  async function load() {
    const [o, rs] = await Promise.all([api('/overview'), api('/restart').catch(() => null)]);
    if (rs) renderRestart(rs);

    const alerts = [];
    Object.entries(o.services || {}).sort().forEach(([n, ok]) => { if (!ok) alerts.push(alertRow('bad', 'Service down', `${n} is not answering its health check.`, link('Logs', 'logs'))); });
    if (o.control_plane_error) alerts.push(alertRow('bad', 'Control plane', o.control_plane_error, link('Battle servers', 'instances')));
    if (o.instances_crashed) alerts.push(alertRow('bad', 'Crashed battle server', `${fmt(o.instances_crashed)} running host${o.instances_crashed === 1 ? ' has' : 's have'} crashed.`, link('Battle servers', 'instances')));
    if (rs && rs.pending) alerts.push(alertRow('warn', 'Restart pending', rs.stage === 'restarting' ? 'mmogbrain is restarting now.' : `Waiting for the battle servers to finish${rs.hold_matches ? '; new matches are held' : ''}.`));
    $('ovAlerts').replaceChildren(...(alerts.length ? alerts : [alertRow('good', 'All clear', 'Every service answers and no running battle server has crashed.')]));

    $('ovCards').replaceChildren(
      card('Online now', fmt(o.online)),
      card('Battle servers', fmt(o.instances), false, o.instances_crashed ? `${fmt(o.instances_crashed)} crashed` : 'running'),
      card('Queued', fmt(o.queued)),
      card('Matches, 24 h', fmt(o.matches_24h), false, `${fmt(o.matches_total)} in total`),
      card('Kills, 24 h', fmt(o.kills_24h), false, `${fmt(o.results_24h)} results reported`),
      card('Credits paid, 24 h', fmt(o.credits_paid_24h)),
      card('Accounts', fmt(o.accounts), false, `${fmt(o.new_accounts_24h)} new in 24 h`),
      card('Bug reports, 24 h', fmt(o.reports_24h)),
    );
    setBadge('instances', o.instances, !o.instances_crashed);
    setBadge('queue', o.queued, true);
    $('sideFoot').textContent = 'mmogbrain up ' + duration(o.uptime_seconds);

    $('ovServices').replaceChildren(...Object.entries(o.services || {}).sort().map(([n, ok]) =>
      el('div', { class: 'card' }, el('div', { class: 'v small' }, el('span', { class: 'dot ' + (ok ? 'good' : 'bad') }), ' ', n), el('div', { class: 'sub' }, ok ? 'healthy' : 'not answering'))));

    const modes = Object.entries(o.modes_24h || {}).sort((a, b) => b[1] - a[1]);
    $('ovModes').replaceChildren(el('h3', {}, 'Matches in the last 24 h by mode'),
      bars(modes.map(([m, n]) => ({ label: modeName(m), value: n })), { empty: 'No matches in the last 24 hours.' }));

    const c = o.host_crashes_recent || {};
    const total = Object.values(c).reduce((a, n) => a + n, 0);
    const order = ['clean', 'stack overflow', 'access violation'];
    const names = { clean: 'Ended cleanly', 'stack overflow': 'Stack overflow', 'access violation': 'Access violation' };
    const keys = [...order.filter((k) => k in c || k === 'clean'), ...Object.keys(c).filter((k) => !order.includes(k))];
    $('ovCrashes').replaceChildren(el('h3', {}, `Battle server outcomes (newest ${fmt(total)} logs)`),
      bars(keys.map((k) => ({ label: names[k] || k, value: c[k] || 0 })), { empty: 'No battle server logs yet.' }));

    // The 24 hour history costs a dozen queries; once a minute is enough.
    if (ticks++ % 6 === 0) {
      api('/metrics?days=1').then((m) => {
        if (!m.samples || !m.samples.length) { $('ovOnline').replaceChildren(el('h3', {}, 'Today'), el('div', { class: 'muted' }, 'No samples yet. The server records who is online every few minutes.')); return; }
        chart('ovOnline', `Today (UTC), a sample every ${m.sample_minutes} minutes`, m.samples.map((s) => s.ts.slice(11, 16)), [
          { name: 'online', values: m.samples.map((s) => s.online) },
          { name: 'in a match', values: m.samples.map((s) => s.in_match) },
          { name: 'queued', values: m.samples.map((s) => s.queued) },
          { name: 'battle servers', values: m.samples.map((s) => s.instances) },
        ], { maxLabels: 8, height: 210 });
      }).catch(() => {});
    }
  }

  Admin.page({
    id: 'overview', title: 'Overview', group: 'Live', order: 10, auto: 10,
    html: `
      <div class="alerts" id="ovAlerts"></div>
      <div class="grid" id="ovCards"></div>
      <div class="charts wide"><div class="chart" id="ovOnline"></div></div>
      <div class="charts"><div class="chart" id="ovModes"></div><div class="chart" id="ovCrashes"></div></div>
      <section><h2>Services</h2><div class="grid" id="ovServices"></div></section>
      <section class="manage"><h2>Restart mmogbrain</h2>
        <div class="muted" style="margin-bottom:8px">Waits until no battle server is running, then restarts mmogbrain with scripts/start-services.sh (which reads run/secrets.env again). Everyone online is disconnected, as with a restart by hand; the game server is back in a few seconds.</div>
        <div id="rsIdle">
          <div class="row"><label class="check"><input type="checkbox" id="rsHold" checked> Hold new matches while waiting (players can still queue)</label></div>
          <input id="rsAnnounce" type="text" maxlength="300" style="width:100%" value="The server restarts when the current matches have ended. It will be back in a minute.">
          <div class="row" style="margin-top:8px"><button class="btn danger" id="rsStart" type="button">Restart when idle</button><span class="muted">The text above goes to Global chat when the wait starts; leave it empty to send nothing.</span></div>
        </div>
        <div id="rsWaiting" class="hidden">
          <div class="row"><span class="chip warn" id="rsStatus"></span><button class="btn" id="rsCancel" type="button">Cancel</button></div>
        </div>
        <p id="rsMsg"></p>
      </section>`,
    init() {
      $('rsStart').onclick = async () => {
        const hold = $('rsHold').checked; const announce = $('rsAnnounce').value.trim();
        if (!confirm('Restart mmogbrain as soon as no battle server is running?\n\nEveryone online will be disconnected.' + (hold ? '\nNew matches are held until then.' : ''))) return;
        await act(async () => renderRestart(await postJSON('/restart', { hold_matches: hold, announce })), 'rsMsg', 'Waiting for the battle servers to finish.');
      };
      $('rsCancel').onclick = async () => {
        if (!confirm('Cancel the restart? Held matches start again.')) return;
        await act(async () => renderRestart(await postJSON('/restart/cancel', {})), 'rsMsg', 'Cancelled.');
      };
    },
    load,
  });
})();
