// Queue: who is waiting for a match right now (queue_entries), the totals per
// mode and fleet type, and matches that are still forming.
(() => {
  const { $, el, api, card, chip, table, link, playerLink, ago, when, fmt, modeName, fleetName, setBadge } = Admin;

  async function load() {
    const [d, rs] = await Promise.all([api('/queue'), api('/restart').catch(() => null)]);
    const waiting = d.waiting || 0;
    const held = !!(rs && rs.pending && rs.hold_matches);
    $('quHold').classList.toggle('hidden', !held);
    $('quHold').textContent = held ? 'New matches are held while a restart waits: players stay queued until it is done.' : '';
    const oldest = (d.entries || []).filter((e) => e.status === 'waiting').map((e) => Admin.parseTime(e.since)).filter((t) => !isNaN(t)).sort((a, b) => a - b)[0];
    $('quCards').replaceChildren(
      card('Waiting', fmt(waiting), false, waiting ? 'in the queue now' : 'nobody is queued'),
      card('Longest wait', oldest ? Admin.duration((Date.now() - oldest) / 1000) : '—'),
      card('Queues in use', fmt((d.totals || []).length)),
      card('Matches forming', fmt((d.forming || []).length)));
    setBadge('queue', waiting, true);

    $('quTotals').replaceChildren(table(['Mode', 'Fleet', { label: 'Waiting', num: true }, 'Longest wait'],
      (d.totals || []).map((b) => [modeName(b.mode), fleetName(b.fleet_type), fmt(b.waiting), ago(b.oldest)]), 'No queues are filling.'));

    const entries = d.entries || [];
    $('quEntries').replaceChildren(table(['Player', 'Mode', 'Fleet', 'Squad', 'Status', 'Waiting'],
      entries.map((e) => [playerLink(e.pid, e.name), modeName(e.mode), fleetName(e.fleet_type),
        e.party ? chip(`squad of ${e.party_size}`) : 'solo',
        e.status === 'waiting' ? chip('waiting') : chip('being placed', 'warn'),
        el('span', { title: e.since }, ago(e.since))]), 'Nobody is in the queue.'));

    $('quForming').replaceChildren(...((d.forming || []).length ? d.forming.map((f) => el('div', { class: 'panel' },
      el('h3', {}, modeName(f.mode), ' on ', f.map || '?', ' ', chip(fleetName(f.fleet_type))),
      el('div', { class: 'muted' }, 'created ', when(f.created), ' · ', link('open match', 'match', f.id)),
      el('div', {}, ...(f.players.length ? f.players.flatMap((p, k) => (k ? [', ', playerLink(p.pid, p.name)] : [playerLink(p.pid, p.name)])) : ['no slots yet'])))) : [el('div', { class: 'muted' }, 'No match is forming.')]));
  }

  Admin.page({
    id: 'queue', title: 'Queue', group: 'Live', order: 30, auto: 5,
    html: `
      <div class="grid" id="quCards"></div>
      <div class="banner hidden" id="quHold"></div>
      <div class="split">
        <section><h2>By mode and fleet</h2><div id="quTotals"></div></section>
        <section><h2>Forming matches</h2><div id="quForming"></div></section>
      </div>
      <section><h2>Everyone queued</h2>
        <div class="muted" style="margin-bottom:8px">The matchmaker places players of one mode and fleet type together, a squad whole. "Being placed" is a player whose match is being started right now.</div>
        <div id="quEntries"></div></section>
      <style>#page-queue .split > section { margin-top: 0; }</style>`,
    load,
  });
})();
