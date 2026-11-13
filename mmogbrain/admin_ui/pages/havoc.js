// Havoc: runs, weekly top scores, milestone rewards paid, waves reached.
Admin.page({
  id: 'havoc', title: 'Havoc', group: 'Game', order: 60, auto: 60,
  html: `
<section>
  <div class="row"><label>Weeks shown <select id="hvWeeks"><option>1</option><option selected>4</option><option>8</option><option>26</option></select></label><span class="muted" id="hvInfo"></span></div>
  <div class="grid" id="hvCards"></div>
</section>
<section><h2>Runs by wave reached</h2><div class="panel" id="hvStages"></div></section>
<section><h2>Weekly top scores</h2><div id="hvTop"></div></section>
<section><h2>Recent runs</h2><div id="hvRuns"></div></section>
<section><h2>Milestone rewards paid</h2><div id="hvClaims"></div></section>`,
  init() { Admin.$('hvWeeks').onchange = () => Admin.refresh(); },
  async load() {
    const { $, el, api, card, table, fmt, when, duration, link, playerLink, bars } = Admin;
    const d = await api('/havoc?weeks=' + $('hvWeeks').value);
    const t = d.totals || {};
    $('hvInfo').replaceChildren('Current week started ', when(d.week_start), ', resets ', when(d.reset), '.');
    $('hvCards').replaceChildren(
      card('Runs this week', fmt(t.week_runs || 0), false, `${fmt(t.week_players || 0)} players`),
      card('Runs in window', fmt(t.runs || 0), false, `${fmt(t.players || 0)} players`),
      card('Best score', fmt(t.best || 0)),
      card('Average waves', t.avg_waves ? t.avg_waves.toFixed(1) : '—'),
      card('Rewards paid', fmt(t.claims || 0)),
      card('Runs all time', fmt(t.runs_all_time || 0)));
    const st = Object.entries(d.stages || {}).map(([k, v]) => [Number(k), v]).sort((a, b) => a[0] - b[0]);
    $('hvStages').replaceChildren(bars(st.map(([w, n]) => ({ label: 'Wave ' + w, value: n })), { empty: 'No runs in this window.' }));
    const weekTitle = (w) => el('span', {}, 'week of ', when(w));
    $('hvTop').replaceChildren(table(['Week', '#', 'Player', { label: 'Best score', num: true }, { label: 'Waves', num: true }, { label: 'Runs in window', num: true }],
      (d.weekly_top || []).map((x) => [weekTitle(x.week), String(x.rank), playerLink(x.pid, x.name), fmt(x.score), String(x.waves), String(x.runs)]), 'No runs in this window.'));
    $('hvRuns').replaceChildren(table(['Time', 'Player', { label: 'Score', num: true }, { label: 'Waves', num: true }, { label: 'Length', num: true }, 'Match'],
      (d.runs || []).map((x) => [when(x.created), playerLink(x.pid, x.name), fmt(x.score), String(x.waves), x.seconds ? duration(x.seconds) : '—', link(x.match.slice(0, 8) + '…', 'match', x.match_ref)]), 'No runs in this window.'));
    $('hvClaims').replaceChildren(table(['Time', 'Player', 'Reward', 'Milestone score', 'Type', { label: 'Amount', num: true }, 'Match'],
      (d.claims || []).map((c) => [when(c.created), playerLink(c.pid, c.name), c.reward, c.threshold ? fmt(c.threshold) : '—', c.type, c.value ? fmt(c.value) : '—', c.match ? link(c.match.slice(0, 8) + '…', 'match', c.match) : '—']), 'No rewards paid in this window.'));
  },
});
