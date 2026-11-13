// Leaderboard: the ranking the game's Leaderboards screen gets.
Admin.page({
  id: 'leaderboard', title: 'Leaderboard', group: 'Game', order: 50, auto: 300,
  html: `
<section>
  <div class="note" style="margin-bottom:10px">This is the ranking the game sends to the Leaderboards screen (same query). Some details are GUESSES, not confirmed from the original game: the score is the sum of a player's reported match scores, the name shown is Name#1234, and the weekly board resets Monday 00:00 UTC. Banned players are left out.</div>
  <div class="row">
    <select id="lbBoard"></select>
    <select id="lbSpan"><option value="weekly">This week</option><option value="lastweek">Last week</option><option value="lifetime">Lifetime</option></select>
    <span class="muted" id="lbInfo"></span>
  </div>
  <div id="lbTable"></div>
</section>`,
  init() {
    const { $, refresh } = Admin;
    this.pick = { board: '', span: 'weekly' };
    $('lbBoard').onchange = (e) => { this.pick.board = e.target.value; refresh(); };
    $('lbSpan').onchange = (e) => { this.pick.span = e.target.value; refresh(); };
  },
  async load() {
    const { $, el, api, table, playerLink, fmt, modeName, fleetName, when } = Admin;
    const p = this.pick;
    const d = await api('/leaderboard');
    const opts = d.boards.map((b) => ({ id: `${b.mode}|${b.fleet}`, label: `${modeName(b.mode)}, ${fleetName(b.fleet)} (${fmt(b.players)} players)` }));
    $('lbBoard').replaceChildren(...opts.map((o) => el('option', { value: o.id }, o.label)));
    if (!opts.length) { $('lbTable').replaceChildren(el('div', { class: 'empty' }, 'No match has reported scores yet.')); $('lbInfo').textContent = ''; return; }
    if (!opts.some((o) => o.id === p.board)) p.board = opts[0].id;
    $('lbBoard').value = p.board; $('lbSpan').value = p.span;
    const r = await api('/leaderboard?board=' + encodeURIComponent(`${p.board}|${p.span}`));
    $('lbInfo').replaceChildren(r.from ? el('span', {}, 'Window ', when(r.from), ' to ', when(r.to)) : 'All time');
    $('lbTable').replaceChildren(table(['#', 'Player', 'In-game tag', { label: 'Score', num: true }],
      (r.entries || []).map((e) => [String(e.rank), playerLink(e.pid, e.name), e.sid || '—', fmt(e.score)]), 'Nobody has a score on this board.'));
  },
});
