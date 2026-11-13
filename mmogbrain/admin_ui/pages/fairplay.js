// Fair play: the checks the battle-server mod reports with every result
// (speed against the bots, reported vs measured damage), the flags they
// raised, and the admins' reviews. A review is a note; it never acts on the
// player. Route: #/fairplay or #/fairplay/<battle_match_id> (one match).
(() => {
  const { $, el, api, postJSON, fmt, when, card, chip, table, bars, link, playerLink, modeName, act, debounce } = Admin;
  const SHIP_CLASSES = { 1: 'Dreadnought (Light)', 2: 'Corvette (Light)', 3: 'Artillery Cruiser (Light)', 4: 'Tactical Cruiser (Light)', 5: 'Destroyer (Light)',
    6: 'Dreadnought (Medium)', 7: 'Dreadnought (Heavy)', 8: 'Corvette (Medium)', 9: 'Corvette (Heavy)', 10: 'Artillery Cruiser (Medium)', 11: 'Artillery Cruiser (Heavy)',
    12: 'Tactical Cruiser (Medium)', 13: 'Tactical Cruiser (Heavy)', 14: 'Destroyer (Medium)', 15: 'Destroyer (Heavy)' };
  const REVIEW_NAMES = { false_positive: 'False positive', watch: 'Watch', confirmed: 'Confirmed' };
  const reviewKind = (s) => (s === 'confirmed' ? 'bad' : s === 'watch' ? 'warn' : 'good');
  let selected = null; let scopePid = ''; let lastArg = null;

  const ratioText = (a, b) => (b ? (a / b).toFixed(2) + 'x' : '—');

  function filters() {
    const p = new URLSearchParams();
    if ($('fpFlagged').checked) p.set('flagged', '1');
    for (const [k, id] of [['verdict', 'fpVerdict'], ['q', 'fpPlayer'], ['mode', 'fpMode'], ['class', 'fpClass'], ['days', 'fpDays']]) {
      const v = $(id).value.trim(); if (v) p.set(k, v);
    }
    if (scopePid) p.set('pid', scopePid);
    return p;
  }

  function openReview(x) {
    selected = x;
    $('fpReview').classList.remove('hidden');
    $('fprTitle').replaceChildren(playerLink(x.pid, x.name), ` · ${modeName(x.mode)} on ${x.map || '?'}`);
    $('fprInfo').replaceChildren(when(x.at), ` · match `, link(x.match_id.slice(0, 8) + '…', 'match', x.match_id),
      ` · flags: ${x.flags.join(', ') || 'none'}` + (x.review ? ` · reviewed ${x.review.at} UTC by ${x.review.actor}` : ''));
    $('fprStatus').value = x.review ? x.review.status : 'false_positive';
    $('fprNote').value = x.review ? x.review.note : '';
    $('fprClear').disabled = !x.review;
    $('fprMsg').textContent = '';
    $('fpReview').scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  async function saveReview(status) {
    const x = selected; if (!x) return;
    const d = await act(() => postJSON('/fairplay/review', { match_id: x.match_id, pid: x.pid, status, note: status ? $('fprNote').value : '' }),
      $('fprMsg'), status ? 'Review saved.' : 'Review removed.');
    if (d) { $('fprClear').disabled = !status; Admin.refresh(); }
  }

  function speedCell(x) {
    const box = el('div', { class: 'results' });
    box.append(el('div', {}, `${fmt(x.jumps)} jump${x.jumps === 1 ? '' : 's'}`));
    x.speed.forEach((s) => box.append(el('div', {}, `${s.class_name}: ${fmt(s.sustained)} / ${s.bots ? fmt(s.bots) : '—'}`
      + (s.ratio ? ` (${s.ratio.toFixed(2)}x)` : s.bots ? ` (${fmt(s.bot_samples)} bot s, too few)` : ' (no bots)') + ` · fastest ${fmt(s.fastest)}`)));
    return box;
  }

  function render(d, arg) {
    const t = d.thresholds; const s = d.summary || {};
    $('fpRules').textContent = `The battle-server mod flags a player whose sustained speed (slowest of three seconds in a row) is over ${t.speed_bot_factor}x the bots' of the same class `
      + `(when the bots flew it for ${t.speed_min_bot_samples}+ seconds) or over ${fmt(t.speed_flag_ups)} u/s, and damage dealt when the reported and measured figures differ `
      + `beyond ${t.dealt_ratio_min}-${t.dealt_ratio_max}x past ${fmt(t.dealt_min)}. A one-second move over ${fmt(t.speed_jump_ups)} u is a jump (respawn, teleport), not speed. `
      + 'A flag is a measurement, not proof: bot-heavy modes flag normal players (issue #10).';
    const modeSel = $('fpMode');
    if (modeSel.options.length === 1) (d.modes || []).forEach((m) => modeSel.append(el('option', { value: m }, modeName(m))));

    // Scope chips: one match (route arg) or one player.
    const scope = [];
    if (arg) scope.push(el('span', {}, 'Match ', link(arg.slice(0, 8) + '…', 'match', arg), ` · every checked player (${fmt((d.results || []).length)})`), el('button', { class: 'btn small', onclick: () => Admin.go('fairplay') }, 'Show everyone'));
    if (scopePid) {
      const p = d.player || {};
      scope.push(el('span', {}, playerLink(scopePid, p.name), `: ${fmt(p.checked)} checked results, ${fmt(p.flagged)} flagged, ${fmt(p.reviewed)} reviewed`),
        el('button', { class: 'btn small', onclick: () => { scopePid = ''; Admin.refresh(); } }, 'Show everyone'));
    }
    $('fpScope').replaceChildren(...scope);
    $('fpScope').classList.toggle('hidden', !scope.length);

    $('fpCards').replaceChildren(
      card('Listed', fmt(s.listed)), card('Flagged', fmt(s.flagged), false, 'in this list'),
      card('Open', fmt(s.open), false, 'flagged, not reviewed'), card('Reviewed', fmt(s.reviewed), false, 'in this list'));
    const flagRows = Object.entries(s.by_flag || {}).sort((a, b) => b[1] - a[1]).map(([k, n]) => ({ label: k, value: n }));
    const verdictRows = Object.entries(s.by_verdict || {}).sort((a, b) => b[1] - a[1]).map(([k, n]) => ({ label: REVIEW_NAMES[k] || k, value: n }));
    $('fpBars').replaceChildren(
      el('div', { class: 'panel' }, el('h3', {}, 'Flags by check'), bars(flagRows, { empty: 'No flags in this list.' })),
      el('div', { class: 'panel' }, el('h3', {}, 'Verdicts'), bars(verdictRows, { empty: 'Nothing reviewed in this list.', color: 'var(--s3)' })));

    const rows = d.results.map((x) => {
      const review = x.review ? el('span', { title: x.review.note || '' }, chip(REVIEW_NAMES[x.review.status] || x.review.status, reviewKind(x.review.status)))
        : (x.flags.length ? el('span', { class: 'muted' }, 'not reviewed') : '');
      return [
        when(x.at),
        el('span', {}, playerLink(x.pid, x.name), ' ', el('a', { class: 'link', title: 'Only this player\'s checks', onclick: () => { scopePid = x.pid; $('fpFlagged').checked = false; $('fpDays').value = ''; Admin.refresh(); } }, '⌕')),
        el('span', {}, link(`${modeName(x.mode)} · ${x.map || '?'}`, 'match', x.match_id), el('div', { class: 'results' }, `${x.fleet} · ${x.outcome} · `,
          el('a', { class: 'link', onclick: () => Admin.go('fairplay', x.match_id) }, 'all players')), el('div', {}, ...x.flags.map((f) => chip(f, 'bad')))),
        speedCell(x),
        el('span', {}, `${fmt(x.damage_reported)} / ${fmt(x.damage_measured)}`, el('div', { class: 'results' }, ratioText(x.damage_reported, x.damage_measured) + ` · ${fmt(x.damage_hits)} hits`)),
        el('span', { class: 'results', style: 'display:block;min-width:140px;white-space:normal' }, `${fmt(x.received)} / ${fmt(x.received_measured)} (${ratioText(x.received, x.received_measured)})`,
          el('div', {}, `raw ${fmt(x.received_raw)} · shield ${fmt(x.received_shield)} · ${fmt(x.received_hits)} hits`)),
        el('div', { class: 'fp-rev' }, review, el('button', { class: 'btn small', onclick: () => openReview(x) }, 'Review')),
      ];
    });
    $('fpTable').replaceChildren(table(['When', 'Player', 'Match and flags', 'Speed u/s: sustained / bots', { label: 'Damage: rep. / meas.', num: true }, 'Received: rep. / meas.', 'Review'],
      rows, $('fpFlagged').checked ? 'No flagged results with these filters.' : 'No checked results with these filters.'));
  }

  Admin.page({
    id: 'fairplay', title: 'Fair play', group: 'Players', order: 30, auto: 60,
    html: `
      <style>#page-fairplay .rules { font-size: 12px; } #page-fairplay .bars2 { display: grid; gap: 12px; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); }
      #page-fairplay td .results div { white-space: normal; min-width: 150px; }
      #page-fairplay .fp-rev { display: grid; gap: 4px; justify-items: start; }</style>
      <details class="note"><summary>What is checked</summary><div class="rules" id="fpRules"></div></details>
      <div class="row">
        <label class="check"><input type="checkbox" id="fpFlagged" checked> Only flagged</label>
        <select id="fpVerdict"><option value="">Any verdict</option><option value="none">Not reviewed</option><option value="false_positive">False positive</option><option value="watch">Watch</option><option value="confirmed">Confirmed</option></select>
        <input id="fpPlayer" type="search" placeholder="Player name or id">
        <select id="fpMode"><option value="">All modes</option></select>
        <select id="fpClass"><option value="">All ship classes</option></select>
        <select id="fpDays"><option value="1">Last 24 h</option><option value="7" selected>Last 7 days</option><option value="30">Last 30 days</option><option value="">All time</option></select>
      </div>
      <div class="row banner hidden" id="fpScope" style="color:var(--ink);border-color:var(--line)"></div>
      <div class="grid" id="fpCards"></div>
      <div class="bars2" id="fpBars"></div>
      <div id="fpReview" class="manage hidden">
        <div class="row" style="justify-content:space-between;margin:0">
          <div><h3 id="fprTitle"></h3><div class="muted" id="fprInfo"></div></div>
          <button class="btn" id="fprClose">Close</button>
        </div>
        <div class="fields">
          <label>Review<select id="fprStatus"><option value="false_positive">False positive</option><option value="watch">Watch</option><option value="confirmed">Confirmed</option></select></label>
          <label style="grid-column: span 2">Note<input id="fprNote" type="text" maxlength="500" placeholder="What you checked"></label>
        </div>
        <div class="row"><button class="btn primary" id="fprSave">Save review</button><button class="btn danger" id="fprClear">Remove review</button>
          <span class="muted">A review is a note for admins and goes in the player's audit log. It does nothing to the player.</span></div>
        <p id="fprMsg"></p>
      </div>
      <div id="fpTable"></div>`,
    init() {
      Object.entries(SHIP_CLASSES).forEach(([id, n]) => $('fpClass').append(el('option', { value: id }, n)));
      const reload = () => Admin.refresh();
      ['fpFlagged', 'fpVerdict', 'fpMode', 'fpClass', 'fpDays'].forEach((id) => { $(id).onchange = reload; });
      $('fpPlayer').oninput = debounce(reload, 300);
      $('fprSave').onclick = () => saveReview($('fprStatus').value);
      $('fprClear').onclick = () => { if (confirm('Remove this review?')) saveReview(''); };
      $('fprClose').onclick = () => { $('fpReview').classList.add('hidden'); selected = null; };
    },
    async load(arg) {
      // "p:<pid>" in the route (the player page's link) scopes to one player;
      // anything else is a match. Both ids are 32 hex, hence the prefix.
      if (arg && arg.startsWith('p:')) {
        if (arg !== lastArg) { scopePid = arg.slice(2); $('fpFlagged').checked = false; $('fpDays').value = ''; lastArg = arg; }
        arg = '';
        render(await api('/fairplay?' + filters()), arg);
        return;
      }
      // A match in the route shows every player of it, whatever else is set.
      if (arg !== lastArg) {
        const was = lastArg; lastArg = arg;
        if (arg) { $('fpFlagged').checked = false; $('fpDays').value = ''; }
        else if (was) { $('fpFlagged').checked = true; $('fpDays').value = '7'; }
      }
      const p = filters(); if (arg) p.set('match', arg);
      render(await api('/fairplay?' + p), arg);
    },
  });
})();
