// Battle servers: every host the control plane (dn-dedicated) is running,
// with its mode, map, players, state, battle log and match, and the stop action.
(() => {
  const { $, el, api, card, chip, table, link, playerLink, ago, duration, parseTime, fmt, modeName, act, setBadge } = Admin;

  const stateChip = (i) => i.crashed ? chip('crashed: ' + i.crashed, 'bad') : i.ready ? chip('running', 'good') : chip(i.running ? 'starting' : 'stopped', 'warn');

  async function load() {
    const d = await api('/instances');
    const list = (d.instances || []).slice().sort((a, b) => a.port - b.port);
    const crashed = list.filter((i) => i.crashed).length;
    $('inCards').replaceChildren(
      card('Battle servers', d.error ? '—' : fmt(list.length), false, d.error ? 'control plane not reachable' : 'listed by the control plane'),
      card('Ready', d.error ? '—' : fmt(list.filter((i) => i.ready && !i.crashed).length)),
      card('Crashed', d.error ? '—' : fmt(crashed), false, crashed ? 'still listed until stopped' : d.error ? '' : 'none'),
      card('Players inside', d.error ? '—' : fmt(list.reduce((n, i) => n + (i.players || []).length, 0))));
    $('inError').classList.toggle('hidden', !d.error);
    $('inError').textContent = d.error ? d.error + '. The list below is empty because the control plane did not answer, not because nothing is running.' : '';
    if (!d.error) setBadge('instances', list.length, !crashed);

    const rows = list.map((i) => {
      const players = (i.players || []).map((pid, k) => playerLink(pid, (i.player_names || [])[k]));
      const who = el('span', {}, ...players.flatMap((p, k) => (k ? [', ', p] : [p])));
      const t = parseTime(i.started_at);
      const stop = el('button', { class: 'btn danger small', type: 'button', onclick: async (ev) => {
        if (!confirm(`Stop the battle server on port ${i.port}?\nPlayers in it are sent back to the hangar.`)) return;
        ev.target.disabled = true;
        await act(() => api('/instances/' + encodeURIComponent(i.id), { method: 'DELETE' }), null, `Stopped the server on port ${i.port}.`);
        Admin.refresh();
      } }, 'Stop');
      return [el('span', { class: 'mono' }, String(i.port)), modeName(i.game_mode), i.map || '—', players.length ? who : '—', stateChip(i),
        isNaN(t) ? '—' : el('span', { title: i.started_at }, duration((Date.now() - t) / 1000)),
        i.match_ref ? link('Match', 'match', i.match_ref) : (i.match_id ? el('span', { class: 'muted', title: 'No match row carries this id' }, 'no match row') : '—'),
        i.log ? link('Log', 'logs', i.log) : '—', stop];
    });
    $('inTable').replaceChildren(table(['Port', 'Mode', 'Map', { label: 'Players', wrap: true }, 'State', 'Up for', 'Match', 'Battle log', ''], rows,
      d.error ? 'Nothing to show.' : 'No battle servers running.'));
  }

  Admin.page({
    id: 'instances', title: 'Battle servers', group: 'Live', order: 20, auto: 10,
    html: `
      <div class="grid" id="inCards"></div>
      <div class="banner hidden" id="inError"></div>
      <section><h2>Running hosts</h2>
        <div class="muted" style="margin-bottom:8px">Every battle server the control plane is running. A crashed host can stay listed until it is stopped (Wine's debugger keeps it alive); the state comes from its battle log.</div>
        <div id="inTable"></div>
      </section>`,
    load,
  });
})();
