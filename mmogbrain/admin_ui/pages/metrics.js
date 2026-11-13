// Insight / Metrics: activity, modes, maps, ship classes, balance, retention.
(() => {
  const { $, el, api, fmt, card, chart, bars, table, chip, playerLink, modeName, duration, toCSV, download } = Admin;
  let days = 7; let last = null;
  try { days = Number(localStorage.getItem('adminMetricsDays')) || 7; } catch (e) {}
  // core.js chart() appends its missing legend (null) as the text "null" for a
  // single series; drop stray text nodes until that is fixed there.
  const dropNull = (box) => { [...box.childNodes].filter((n) => n.nodeType === 3).forEach((n) => n.remove()); };
  const pct = (a, b) => (b ? Math.round(100 * a / b) + ' %' : '—');
  const words = (s) => s.replace(/([a-z])([A-Z])/g, '$1 $2');
  const SHIP = { Dreadnought: 'Dreadnought', Corvette: 'Corvette', ArtilleryCruiser: 'Artillery Cruiser', TacticalCruiser: 'Tactical Cruiser', Destroyer: 'Destroyer' };

  function render(m) {
    last = m;
    const t = m.totals; const r = m.retention || {};
    const crashRate = t.host_logs ? Math.round(1000 * t.crashes / t.host_logs) / 10 + ' %' : '—';
    $('mxCards').replaceChildren(card('Matches', fmt(t.matches)), card('Active players', fmt(t.players)),
      card('Returning players', fmt(m.returning_players), false, 'also played before the range'), card('New accounts', fmt(t.new_accounts)),
      card('Peak online', m.samples.length ? fmt(t.peak_online) : '—'), card('Results reported', fmt(t.results)),
      card('Credits paid', fmt(t.credits)), card('XP paid', fmt(t.xp)), card('Kills', fmt(t.kills)),
      card('Host crashes', `${fmt(t.crashes)} / ${fmt(t.host_logs)}`, true, `${crashRate} of battle server logs`));
    $('mxNote').textContent = m.samples_hourly ? `online history: hourly peaks of ${m.sample_minutes}-minute samples` : `online history: a sample every ${m.sample_minutes} minutes`;

    if (m.samples.length) {
      chart('mxOnline', 'Online, in a match, queued, battle servers', m.samples.map((s) => s.ts.slice(5, 16)), [
        { name: 'online', values: m.samples.map((s) => s.online) },
        { name: 'in a match', values: m.samples.map((s) => s.in_match) },
        { name: 'queued', values: m.samples.map((s) => s.queued) },
        { name: 'battle servers', values: m.samples.map((s) => s.instances) },
      ], { maxLabels: 8, height: 210, short: (l) => (m.samples_hourly ? l.slice(0, 8) + 'h' : l.slice(6)) });
    } else {
      $('mxOnline').replaceChildren(el('h3', {}, 'Online'), el('div', { class: 'muted' }, 'No samples yet. The server records one every few minutes from now on.'));
    }
    const dl = m.daily.map((d) => d.day); const short = (d) => d.slice(5);
    const box = $('mxDaily'); box.replaceChildren();
    const daily = [['Matches', 'matches', 1], ['Active players', 'players', 2], ['New accounts', 'new_accounts', 3], ['Peak online', 'peak_online', 4], ['Credits paid', 'credits', 5], ['Host crashes', 'crashes', 6]];
    // All boxes first: chart() draws at its box's width, and an auto-fit grid
    // holding one box gives it the whole row (the first charts came out tiny).
    const divs = daily.map(() => el('div')); box.append(...divs);
    daily.forEach(([title, f, c], i) => {
      chart(divs[i], title + ' per day', dl, [{ name: title, color: `var(--s${c})`, values: m.daily.map((d) => d[f]) }], { bars: true, short, maxLabels: 12, height: 170 });
      dropNull(divs[i]);
    });
    chart('mxHours', 'Matches started by hour of day (UTC)', m.hours_utc.map((_, h) => String(h).padStart(2, '0')), [{ name: 'matches', color: 'var(--s1)', values: m.hours_utc }], { bars: true, maxLabels: 24, height: 150 });
    dropNull($('mxHours'));

    const totalModes = m.modes.reduce((a, x) => a + x.matches, 0) || 1;
    $('mxModes').replaceChildren(table(['Mode', { label: 'Matches', num: true }, { label: 'Share', num: true }, { label: 'Avg length', num: true }, { label: 'Avg players reporting', num: true }],
      m.modes.map((x) => [el('span', {}, modeName(x.mode), el('span', { class: 'muted' }, ` (${x.mode})`)), fmt(x.matches), pct(x.matches, totalModes), x.avg_minutes ? x.avg_minutes + ' min' : '—', String(x.avg_players)]), 'No matches in this range.'));

    $('mxMaps').replaceChildren(el('h3', {}, 'Map popularity (matches)'), bars(m.maps.map((x) => ({ label: x.map, value: x.matches })), { empty: 'No matches in this range.' }));
    const cls = m.ship_classes || []; const secs = cls.reduce((a, c) => a + c.seconds, 0) || 1;
    $('mxShips').replaceChildren(el('h3', {}, 'Ship class usage (time alive)'),
      bars(cls.map((c) => ({ label: SHIP[c.class] || words(c.class), value: c.seconds, text: `${duration(c.seconds)} · ${pct(c.seconds, secs)}` })), { empty: 'No class times reported in this range.', color: 'var(--s2)' }),
      el('div', { class: 'muted', style: 'margin-top:8px' }, 'Seconds alive per ship type, as the battle server reports them. Matches from an older server mod split the time evenly over the ships flown (an estimate).'));
    const fl = ['Recruit', 'Veteran', 'Legendary'].map((f) => ({ label: f, value: m.fleets[f] || 0 }));
    $('mxFleets').replaceChildren(el('h3', {}, 'Matches by fleet'), bars(fl, { empty: 'No matches.', color: 'var(--s3)' }));
    $('mxOutcomes').replaceChildren(el('h3', {}, 'Results by outcome'),
      bars(Object.entries(m.outcomes).sort((a, b) => b[1] - a[1]).map(([o, n]) => ({ label: o, value: n })), { empty: 'No results.', color: 'var(--s4)' }));

    $('mxBalance').replaceChildren(table(['Mode', { label: 'Decided matches', num: true }, { label: 'Team 0 won', num: true }, { label: 'Team 1 won', num: true }, { label: 'Team 0 share', num: true }, 'Balance'],
      (m.team_balance || []).map((b) => {
        const n = b.team0_wins + b.team1_wins; const share = n ? b.team0_wins / n : 0;
        const verdict = n < 10 ? chip('too few matches') : Math.abs(share - 0.5) > 0.15 ? chip('lopsided', 'warn') : chip('even', 'good');
        return [el('span', {}, modeName(b.mode), el('span', { class: 'muted' }, ` (${b.mode})`)), fmt(n), fmt(b.team0_wins), fmt(b.team1_wins), pct(b.team0_wins, n), verdict];
      }), 'No decided team matches in this range.'));

    $('mxRetention').replaceChildren(
      card('New accounts in range', fmt(r.cohort || 0)),
      card('Day 1 return', r.day1_eligible ? Math.round(100 * r.day1_share) + ' %' : '—', false, `${fmt(r.day1_returned || 0)} of ${fmt(r.day1_eligible || 0)} played the day after signing up`),
      card('Day 7 return', r.day7_eligible ? Math.round(100 * r.day7_share) + ' %' : '—', false, `${fmt(r.day7_returned || 0)} of ${fmt(r.day7_eligible || 0)} played 7 days after signing up`));

    $('mxTop').replaceChildren(table(['Name', { label: 'Matches', num: true }, { label: 'Wins', num: true }, { label: 'Win rate', num: true }, { label: 'Kills', num: true }],
      m.top_players.map((p) => [playerLink(p.pid, p.name), fmt(p.matches), fmt(p.wins), pct(p.wins, p.matches), fmt(p.kills)]), 'Nobody played in this range.'));
    $('mxDayTable').replaceChildren(table(['Day (UTC)', { label: 'Matches', num: true }, { label: 'Active players', num: true }, { label: 'New accounts', num: true }, { label: 'Peak online', num: true }, { label: 'Results', num: true }, { label: 'Credits', num: true }, { label: 'XP', num: true }, { label: 'Kills', num: true }, { label: 'Host crashes / logs', num: true }],
      m.daily.map((d) => [d.day, fmt(d.matches), fmt(d.players), fmt(d.new_accounts), fmt(d.peak_online), fmt(d.results), fmt(d.credits), fmt(d.xp), fmt(d.kills), `${d.crashes} / ${d.host_logs}`]).reverse()));
  }

  Admin.page({
    id: 'metrics', title: 'Metrics', group: 'Insight', order: 10, auto: 120,
    html: `
      <div class="row" id="mxRange"><span class="muted">Range:</span>
        <button class="btn" data-days="1">24 h</button><button class="btn" data-days="7">7 days</button>
        <button class="btn" data-days="30">30 days</button><button class="btn" data-days="90">90 days</button>
        <span class="muted" id="mxNote"></span><span class="spacer"></span><button class="btn" id="mxCSV" type="button">Download daily table (CSV)</button></div>
      <div class="grid" id="mxCards"></div>
      <div class="charts wide"><div class="chart" id="mxOnline"></div></div>
      <section><h2>Per day (UTC)</h2><div class="charts" id="mxDaily"></div></section>
      <div class="charts wide"><div class="chart" id="mxHours"></div></div>
      <section><h2>Game modes</h2><div id="mxModes"></div></section>
      <section><h2>Maps, ships and fleets</h2><div class="charts"><div class="chart" id="mxMaps"></div><div class="chart" id="mxShips"></div><div class="chart" id="mxFleets"></div><div class="chart" id="mxOutcomes"></div></div></section>
      <section><h2>Team balance</h2><div id="mxBalance"></div>
        <div class="muted" style="margin-top:6px">A match counts for a team when that team has a winning result. Modes without two sides (Bootcamp, Onslaught) show only team 0.</div></section>
      <section><h2>Retention</h2><div class="grid" id="mxRetention"></div>
        <div class="muted" style="margin-top:6px">Accounts created in the range that played a match on the calendar day (UTC) after, or seven days after, the day they were created. Only accounts whose day has fully passed are counted; played = a reported match result, the only per-account activity history the server keeps.</div></section>
      <section><h2>Most active players</h2><div id="mxTop"></div></section>
      <section><h2>Per day, exact numbers</h2><div id="mxDayTable"></div></section>`,
    init() {
      const mark = () => document.querySelectorAll('#mxRange button[data-days]').forEach((b) => b.classList.toggle('on', Number(b.dataset.days) === days));
      mark();
      document.querySelectorAll('#mxRange button[data-days]').forEach((b) => {
        b.onclick = () => { days = Number(b.dataset.days); try { localStorage.setItem('adminMetricsDays', days); } catch (e) {} mark(); Admin.refresh(); };
      });
      $('mxCSV').onclick = () => {
        if (!last) return;
        download(`metrics-${last.days}d.csv`, toCSV([['day', 'matches', 'players', 'new_accounts', 'peak_online', 'results', 'credits', 'xp', 'kills', 'crashes', 'host_logs'],
          ...last.daily.map((d) => [d.day, d.matches, d.players, d.new_accounts, d.peak_online, d.results, d.credits, d.xp, d.kills, d.crashes, d.host_logs])]), 'text/csv');
      };
    },
    async load() { render(await api('/metrics?days=' + days)); },
  });
})();
