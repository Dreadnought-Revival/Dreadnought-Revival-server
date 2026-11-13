// One match: its facts, the two teams with every player's figures, per-player
// statistics, the Havoc run, fair-play findings and the battle log.
(() => {
  const { $, el, api, card, chip, kv, table, link, playerLink, when, duration, parseTime, fmt, modeName, fleetName } = Admin;

  const OUTCOME = { win: ['Won', 'good'], loss: ['Lost', 'bad'], draw: ['Draw', ''], unknown: ['Unknown', 'warn'] };
  const outcomeChip = (o) => { const [t, k] = OUTCOME[o] || [o || '?', '']; return chip(t, k); };
  const STAT_NAMES = { score: 'score', healing: 'healing', double_kills: 'double kills', first_strikes: 'first strikes' };
  const statName = (s) => STAT_NAMES[s] || (s.startsWith('time_') ? s.slice(5) + ' time' : s.replace(/_/g, ' '));
  const statValue = (s, v) => (s.startsWith('time_') ? duration(v) : fmt(v));

  function teamTable(players) {
    return table(['Player', 'Result', { label: 'K / D / A', num: true }, { label: 'Damage', num: true }, { label: 'Score', num: true }, { label: 'Credits', num: true }, { label: 'XP', num: true }, 'Fair play'],
      players.map((p) => [
        el('span', {}, playerLink(p.pid, p.name), p.round ? el('span', { class: 'muted' }, ' · ' + p.round.slice(p.round.lastIndexOf('-r') + 1)) : null),
        outcomeChip(p.outcome), `${fmt(p.kills)} / ${fmt(p.deaths)} / ${fmt(p.assists)}`, fmt(p.damage),
        p.stats && p.stats.score != null ? fmt(p.stats.score) : '—', fmt(p.credits), fmt(p.xp),
        p.flags.length ? Object.assign(chip('flagged', 'bad'), { title: p.flags.join(', ') }) : p.review ? chip(p.review, 'warn') : p.checked ? chip('checked', 'good') : '—']),
      'No results.');
  }

  async function load(arg) {
    $('mdMsg').classList.add('hidden');
    let d;
    try { d = await api('/matches/' + encodeURIComponent(arg)); } catch (e) {
      if (e.message === 'forbidden') throw e;
      $('mdBody').classList.add('hidden'); $('mdMsg').classList.remove('hidden');
      $('mdMsg').replaceChildren(`No match found for "${arg}". ${e.message}. `, link('Back to the matches', 'matches'));
      return;
    }
    $('mdBody').classList.remove('hidden');
    const m = d.match; const players = d.results || [];
    const s = parseTime(m.started || m.created); const e = parseTime(m.ended);
    Admin.setTitle('Match ' + (m.mode ? modeName(m.mode) + ' · ' : '') + (m.map || (m.id || m.battle_id).slice(0, 8)));

    const teams = [...new Set(players.map((p) => p.team))].sort((a, b) => a - b);
    const result = m.outcome === 'win' ? (teams.length > 1 ? `Team ${m.winner_team} won` : 'Won') : m.outcome === 'draw' ? 'Draw' : players.length ? 'Unknown' : '—';
    $('mdCards').replaceChildren(
      card('Result', result, true, players.length ? `${fmt(players.length)} result${players.length === 1 ? '' : 's'} reported` : m.status === 'ended' ? 'no results reported' : 'no results yet'),
      card('Status', m.status || '?', true, m.fleet_type ? fleetName(m.fleet_type) + ' fleets' : ''),
      card('Length', !isNaN(s) && !isNaN(e) ? duration((e - s) / 1000) : '—', true, m.port ? 'host port ' + m.port : ''),
      card('Kills', fmt(m.kills || 0), true, d.flagged ? `${fmt(d.flagged)} flagged by fair-play checks` : ''));

    $('mdFacts').replaceChildren(kv([
      ['Mode', m.mode ? `${modeName(m.mode)} (${m.mode})` : ''], ['Map', m.map],
      ['Created', m.created ? when(m.created) : ''], ['Started', m.started ? when(m.started) : ''], ['Ended', m.ended ? when(m.ended) : ''],
      ['Match id', m.id ? el('span', { class: 'mono' }, m.id) : ''], ['Battle server id', m.battle_id ? el('span', { class: 'mono' }, m.battle_id) : ''],
      ['Instance', d.instance_id ? el('span', { class: 'mono' }, d.instance_id) : ''],
      ['Battle log', d.log ? link(d.log, 'logs', d.log) : (m.port ? 'not found (older than the kept logs, or the host was started elsewhere)' : '')],
      ['Fair play', (players.some((p) => p.checked) || d.flagged) ? link('Open the checks for this match', 'fairplay', m.battle_id) : ''],
    ]));
    if (!m.id) $('mdFacts').append(el('div', { class: 'note', style: 'margin-top:8px' }, 'No match record carries this battle id (a host started by hand, or the record was removed); only the reported results are shown.'));

    const host = $('mdTeams'); host.replaceChildren();
    if (players.length) {
      teams.forEach((t) => host.append(el('section', {}, el('h2', {}, teams.length > 1 ? `Team ${t}` : 'Players'), teamTable(players.filter((p) => p.team === t)))));
    } else if ((d.slots || []).length) {
      host.append(el('section', {}, el('h2', {}, 'Slots'), table(['Player', 'Team', 'Joined'], d.slots.map((x) => [playerLink(x.pid, x.name), x.team, when(x.joined)]), '')));
    } else host.append(el('div', { class: 'muted' }, 'No results and no slots: the host has not reported yet.'));

    const withStats = players.filter((p) => Object.keys(p.stats || {}).some((k) => k !== 'score'));
    $('mdStats').replaceChildren(...(withStats.length ? [el('h2', {}, 'Statistics'),
      table(['Player', { label: 'Reported figures', wrap: true }], withStats.map((p) => [playerLink(p.pid, p.name),
        Object.entries(p.stats).filter(([k]) => k !== 'score').sort().map(([k, v]) => `${statName(k)} ${statValue(k, v)}`).join(' · ')]), '')] : []));

    $('mdHavoc').replaceChildren(...((d.havoc || []).length ? [el('h2', {}, 'Havoc run'),
      table(['Player', { label: 'Score', num: true }, { label: 'Waves', num: true }], d.havoc.map((h) => [playerLink(h.pid, h.name), fmt(h.score), fmt(h.waves)]), '')] : []));

    const fp = players.filter((p) => p.flags.length || p.review);
    $('mdFair').replaceChildren(...(fp.length ? [el('h2', {}, 'Fair play'),
      table(['Player', 'Flags', 'Review', { label: 'Note', wrap: true }], fp.map((p) => [playerLink(p.pid, p.name), p.flags.join(', ') || '—', p.review || 'not reviewed', p.review_note || '']), ''),
      el('div', { style: 'margin-top:8px' }, link('Review them on the Fair play page', 'fairplay', m.battle_id))] : []));
    $('mdNote').textContent = 'Ribbons are kept as running totals per player, not per match, so they are not listed here.';
  }

  Admin.page({
    id: 'match', title: (arg) => 'Match ' + (arg || '').slice(0, 8), hidden: true, parent: 'matches', auto: 20,
    html: `
      <div class="row"><a class="link" href="#/matches">&larr; All matches</a></div>
      <div class="banner hidden" id="mdMsg"></div>
      <div id="mdBody">
        <div class="grid" id="mdCards"></div>
        <section class="panel"><h2>Match</h2><div id="mdFacts"></div></section>
        <div id="mdTeams"></div>
        <section id="mdStats"></section>
        <section id="mdHavoc"></section>
        <section id="mdFair"></section>
        <div class="note" id="mdNote"></div>
      </div>`,
    load,
  });
})();
