// Player: one account in full. Header and key figures, the admin actions, and
// sub tabs for everything the server knows about the player. Every value is
// put on the page as text (Admin.el), never as markup.
(() => {
  const { $, el, api, apiBlob, postJSON, fmt, ago, when, duration, bytes, chip, card, table, kv, tabs, act, auditText, bars, link, modeName, setTitle, download } = Admin;
  let P = null; let openedTab = ''; let pid = ''; let tabCtl = null; let invFilter = '';

  // The contracts' in-game names (MPQuestCollection m_name, English).
  const CONTRACT_NAMES = {
    YMPQ_CompleteMatches: 'You Know The Drill! (complete battles)', YMPQ_CompleteMatchesONS: 'Frontier Warfare (complete Onslaught battles)',
    YMPQ_CompleteMatchesTURBO: 'Complete Turbo battles', YMPQ_WinMatches: 'Path To Glory! (win battles)',
    YMPQ_WinMatchesONS: "Don't Leave Me Hanging! (win Onslaught)", YMPQ_WinMatchesTER: 'Territorial Conquest (win Conquest)',
    YMPG_MatchScoreONS: 'Onslaught score', YMPQ_Kills: 'Know The Ropes (destroy enemies)',
    YMPQ_DestroyArtilleryCruiser: 'Counter-Battery (destroy artillery cruisers)', YMPQ_DestroyCorvette: 'Seek And Destroy (destroy corvettes)',
    YMPQ_DestroyDestroyer: 'Down The Boat! (destroy destroyers)', YMPQ_DestroyDreadnought: 'The More Danger, The More Fame! (destroy dreadnoughts)',
    YMPQ_DestroyTacticalCruiser: 'Cut The Lifeline! (destroy tactical cruisers)', YMPQ_KillsArtilleryCruiser: 'Target Practice (kills in an artillery cruiser)',
    YMPQ_KillsCorvette: 'On The Hunt (kills in a corvette)', YMPQ_KillsDestroyer: 'Vicious Tin Can (kills in a destroyer)',
    YMPQ_KillsDreadnought: 'Bring Out The Big Guns (kills in a dreadnought)', YMPQ_KillsTacticalCruiser: "I Ain't No Pacifist (kills in a tactical cruiser)",
  };
  const TABS = [{ id: 'overview', label: 'Overview' }, { id: 'inventory', label: 'Inventory & purchases' }, { id: 'ships', label: 'Ships & XP' },
    { id: 'fleets', label: 'Fleets & loadouts' }, { id: 'progress', label: 'Progress' }, { id: 'social', label: 'Social' }, { id: 'matches', label: 'Matches' },
    { id: 'moderation', label: 'Moderation' }, { id: 'reports', label: 'Reports, telemetry & logs' }];

  const name = () => (P && P.identity.name) || 'this player';
  const section = (title, ...kids) => el('section', {}, el('h2', {}, title), ...kids);
  const count = (n, total) => (total > n ? `showing ${fmt(n)} of ${fmt(total)}` : `${fmt(n)}`);
  const untilText = (b) => (b.permanent ? 'permanently' : 'until ' + b.until + ' UTC');
  const pct = (a, b) => (b ? Math.round(100 * a / b) + ' %' : '—');
  const copyable = (text) => el('span', { class: 'mono', style: 'user-select:all' }, text);
  const msg = () => $('plMsg');

  // --- actions ---------------------------------------------------------------
  // run(label, fn): act() into the message line, then reload the profile.
  async function run(fn, ok) {
    const r = await act(fn, msg(), ok);
    if (r !== undefined) await Admin.refresh();
    return r;
  }
  const reason = () => $('plReason').value.trim();
  const needReason = () => { if (!reason()) { msg().className = 'err'; msg().textContent = 'Enter a reason first.'; $('plReason').focus(); return false; } return true; };
  const hoursLabel = (h) => ({ 1: '1 hour', 24: '24 hours', 168: '7 days', 720: '30 days', 0: 'permanently' }[h] || h + ' h');
  const base = () => '/players/' + pid;

  function amounts() {
    const out = {};
    [['credits', 'plCredits'], ['premium', 'plPremium'], ['free_xp', 'plFreeXP']].forEach(([k, id]) => { const v = $(id).value.trim(); if (v !== '') out[k] = Math.trunc(Number(v)); });
    return out;
  }
  function changeBalances(mode) {
    const a = amounts();
    if (!Object.keys(a).length || Object.values(a).some((v) => !Number.isFinite(v))) { msg().className = 'err'; msg().textContent = 'Enter at least one amount (numbers only).'; return; }
    const what = Object.entries(a).map(([k, v]) => `${k}: ${mode === 'add' && v > 0 ? '+' : ''}${v}`).join(', ');
    if (!confirm(`${mode === 'add' ? 'Add' : 'Set'} for ${name()}: ${what}?`)) return;
    run(() => postJSON(base() + '/currency', { mode, ...a }), 'Balances updated.').then((r) => { if (r) ['plCredits', 'plPremium', 'plFreeXP'].forEach((id) => { $(id).value = ''; }); });
  }

  function wireActions() {
    $('plKick').onclick = () => { if (confirm(`Kick ${name()}? Their game disconnects; they can sign in again.`)) run(() => postJSON(base() + '/kick', { reason: reason() }), (d) => (d && d.was_online === false ? 'The player was not connected.' : 'Kicked.')); };
    $('plBan').onclick = () => {
      if (!needReason()) return;
      const hours = Number($('plBanFor').value);
      if (confirm(`Ban ${name()} ${hoursLabel(hours)}?\n\nReason: ${reason()}\n\nThey are disconnected now and cannot sign in until the ban ends.`)) run(() => postJSON(base() + '/ban', { reason: reason(), hours }), 'Banned.').then((r) => { if (r) $('plReason').value = ''; });
    };
    $('plUnban').onclick = () => { if (confirm(`Unban ${name()}?`)) run(() => postJSON(base() + '/unban', { reason: reason() }), 'Unbanned.'); };
    $('plMute').onclick = () => {
      if (!needReason()) return;
      const hours = Number($('plMuteFor').value);
      if (confirm(`Mute ${name()} in chat ${hoursLabel(hours)}?\n\nReason: ${reason()}`)) run(() => postJSON(base() + '/mute', { reason: reason(), hours }), 'Muted.').then((r) => { if (r) $('plReason').value = ''; });
    };
    $('plUnmute').onclick = () => { if (confirm(`Unmute ${name()}?`)) run(() => postJSON(base() + '/unmute', { reason: reason() }), 'Unmuted.'); };
    $('plResetPw').onclick = async () => {
      if (!confirm(`Reset the password of ${name()}?\n\nTheir old password stops working and they are signed out everywhere.`)) return;
      const r = await act(() => postJSON(base() + '/password', { reason: reason() }), msg(), 'Password reset. Copy it now: it is not shown again.');
      if (r) { $('plPw').textContent = r.password; $('plPwBox').classList.remove('hidden'); Admin.refresh(); }
    };
    $('plPwCopy').onclick = async () => {
      try { await navigator.clipboard.writeText($('plPw').textContent); $('plPwCopy').textContent = 'Copied'; setTimeout(() => { $('plPwCopy').textContent = 'Copy'; }, 1500); }
      catch (e) { const r = document.createRange(); r.selectNodeContents($('plPw')); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
    };
    $('plRename').onclick = () => {
      const n = $('plNewName').value.trim(); const out = $('plSignOut').checked;
      if (!n) { msg().className = 'err'; msg().textContent = 'Enter the new name.'; return; }
      if (!confirm(`Rename ${name()} to ${n}?` + (out ? '\n\nThey are signed out of the game now.' : ''))) return;
      run(() => postJSON(base() + '/rename', { name: n, signout: out }), `Renamed to ${n}.`).then((r) => { if (r) { $('plNewName').value = ''; $('plSignOut').checked = false; } });
    };
    $('plAdd').onclick = () => changeBalances('add');
    $('plSet').onclick = () => changeBalances('set');
    $('plGrant').onclick = () => {
      const sel = $('plHero'); if (!sel.value) return;
      if (confirm(`Grant ${sel.options[sel.selectedIndex].text} to ${name()}?`)) run(() => postJSON(base() + '/ships', { ship_id: Number(sel.value) }), 'Ship granted.');
    };
    $('plResetContracts').onclick = () => {
      if (confirm(`Reset the daily contracts of ${name()}? Their current contracts and progress are dropped and a fresh set is given now.`)) run(() => postJSON(base() + '/contracts/reset', {}), 'Contracts reset.');
    };
    $('plTuneSave').onclick = () => {
      const mode = $('plTune').value;
      if (mode === 'apply' && !confirm('Send the full tune reply to this account at its next login? Abilities may break for it until it is set back.')) return;
      run(() => postJSON(base() + '/tune-test', { mode }), mode ? `Tune test "${mode}" set; it applies at the next login.` : 'Tune test off.');
    };
  }

  // renderHead: name, chips, KPI cards, and the controls that depend on state.
  function renderHead() {
    const i = P.identity; const b = P.balances; const r = P.results;
    setTitle(i.name || 'Player');
    $('plName').textContent = i.name || '(unnamed)';
    $('plPid').textContent = i.pid;
    const chips = [chip(i.online ? 'online' : 'offline', i.online ? 'good' : '')];
    if (P.ban && P.ban.active) chips.push(chip('banned ' + untilText(P.ban), 'bad'));
    if (P.mute && P.mute.active) chips.push(chip('muted ' + untilText(P.mute), 'warn'));
    if (P.membership.active) chips.push(chip('elite member', 'good'));
    $('plChips').replaceChildren(...chips);
    $('plKpis').replaceChildren(card('Credits', fmt(b.credits)), card('GP', fmt(b.premium)), card('Free XP', fmt(b.free_xp)), card('Rank', fmt(i.rank), false, `${fmt(i.xp)} XP`),
      card('Matches', fmt(r.matches), false, r.matches ? `${pct(r.wins, r.matches)} won` : 'none played'),
      card('K / D', r.deaths ? (r.kills / r.deaths).toFixed(2) : fmt(r.kills), false, `${fmt(r.kills)} kills, ${fmt(r.deaths)} deaths`),
      card('Ships', fmt(P.ships.length), false, `${fmt(P.owned_total)} owned items`),
      card('Last active', i.last_active ? ago(i.last_active) + ' ago' : '—', true, i.last_active ? i.last_active + ' UTC' : ''),
      card('Created', (i.created || '').slice(0, 10), true));
    const banned = !!(P.ban && P.ban.active); const muted = !!(P.mute && P.mute.active);
    $('plBan').classList.toggle('hidden', banned); $('plBanFor').classList.toggle('hidden', banned); $('plUnban').classList.toggle('hidden', !banned);
    $('plMute').classList.toggle('hidden', muted); $('plMuteFor').classList.toggle('hidden', muted); $('plUnmute').classList.toggle('hidden', !muted);
    $('plKick').disabled = !i.online;
    $('plKick').title = i.online ? '' : 'The player is not connected.';
    $('plTune').value = i.tune_test || '';
    const sel = $('plHero'); const keep = sel.value; sel.replaceChildren();
    const free = P.heroes.filter((h) => !h.owned);
    free.forEach((h) => sel.append(el('option', { value: h.id }, `T${h.tier} ${h.name}`)));
    if (!free.length) sel.append(el('option', { value: '' }, 'owns every hero ship'));
    if (keep && free.some((h) => String(h.id) === keep)) sel.value = keep;
    $('plGrant').disabled = !free.length;
  }

  // --- tabs ---------------------------------------------------------------------
  const where = (m) => [modeName(m.mode), m.map].filter(Boolean).join(' / ') || '?';
  const outcome = (o) => chip(o, o === 'win' ? 'good' : o === 'loss' ? 'bad' : '');
  const matchRows = (list) => list.map((m) => [link(when(m.at).textContent, 'match', m.match_id), where(m), m.fleet, outcome(m.outcome),
    `${m.kills} / ${m.deaths} / ${m.assists}`, fmt(m.damage), fmt(m.credits), fmt(m.xp)]);
  const MATCH_COLS = ['When', 'Mode / map', 'Fleet', 'Result', { label: 'K / D / A', num: true }, { label: 'Damage', num: true }, { label: 'Credits', num: true }, { label: 'XP', num: true }];
  const restriction = (label, b, empty) => {
    if (!b) return el('div', { class: 'muted' }, empty);
    return el('div', {}, chip(b.active ? 'active' : 'expired', b.active ? (label === 'Ban' ? 'bad' : 'warn') : ''), ' ', b.reason || '(no reason)',
      el('div', { class: 'muted' }, `since ${b.since} UTC, ${b.permanent ? 'permanent' : (b.active ? 'until ' : 'ended ') + b.until + ' UTC'}`));
  };

  function tabOverview() {
    const i = P.identity; const r = P.results;
    const sinceLogin = i.last_login_date ? `${i.last_login_date} (streak ${i.login_streak})` : '—';
    return [
      el('div', { class: 'split' },
        el('section', { class: 'panel' }, el('h2', {}, 'Account'), kv([
          ['Name', i.name || '(unnamed)'], ['Player id', copyable(i.pid)], ['Created', when(i.created)], ['Last active', when(i.last_active)],
          ['Last login day', sinceLogin], ['Rank', `${fmt(i.rank)} (${fmt(i.xp)} XP)`],
          ['Membership', P.membership.expires_at ? (P.membership.active ? 'active until ' : 'ended ') + new Date(P.membership.expires_at * 1000).toISOString().slice(0, 10) : 'never purchased'],
          ['Tune test', i.tune_test || 'off'],
          ['Factions', P.factions.map((f) => `${f.name || 'faction ' + f.id} ${fmt(f.reputation)}`).join(', ')]])),
        el('section', { class: 'panel' }, el('h2', {}, 'Standing'), kv([
          ['Ban', restriction('Ban', P.ban, 'not banned')], ['Chat mute', restriction('Mute', P.mute, 'not muted')],
          ['Played', `${fmt(r.matches)} matches, ${pct(r.wins, r.matches)} won`], ['Totals', `${fmt(r.kills)} kills, ${fmt(r.deaths)} deaths, ${fmt(r.assists)} assists`],
          ['Damage', fmt(r.damage)], ['Last match', r.last_match ? when(r.last_match) : 'never'],
          ['Reports', `${fmt(P.reports.total)} (${fmt(P.reports.last_7d)} in 7 days)`], ['Telemetry', `${fmt(P.telemetry.total)} events`],
          ['Ignored by', `${fmt(P.ignored_by)} players`]]))),
      section('Latest notes', P.notes.length ? table(['When', 'By', { label: 'Note', wrap: true }], P.notes.slice(0, 3).map((n) => [when(n.created_at), n.actor, n.note])) : el('div', { class: 'muted' }, 'No notes on this account.')),
      section('Recent matches', table(MATCH_COLS, matchRows(P.matches.slice(0, 5)), 'No matches played.')),
    ];
  }

  function tabInventory() {
    const f = invFilter.toLowerCase();
    const list = el('div');
    const draw = () => {
      const q = $('plInvQ') ? $('plInvQ').value.trim().toLowerCase() : f;
      invFilter = q;
      const rows = P.owned.filter((o) => !q || o.name.toLowerCase().includes(q) || String(o.id).includes(q) || o.kind.includes(q))
        .map((o) => [o.name, o.kind, o.source, el('span', { class: 'mono' }, String(o.id))]);
      list.replaceChildren(table(['Item', 'Kind', 'Source', 'Id'], rows, 'Nothing matches.'));
    };
    const box = el('input', { type: 'search', id: 'plInvQ', placeholder: 'Filter items', value: invFilter, oninput: draw });
    queueMicrotask(draw);
    return [
      section(`Owned items (${count(P.owned.length, P.owned_total)})`, el('div', { class: 'row' }, box,
        el('span', { class: 'muted' }, 'Starter items and purchases; cosmetics owned through ships are not listed here.')), list),
      section(`Purchases, newest first (${count(P.purchases.length, P.purchases_total)})`,
        table(['When', 'Item', 'Kind', 'Type', { label: 'Price', num: true }, 'Id'], P.purchases.map((p) => [when(p.at), p.name, p.kind, p.type,
          `${fmt(p.price)} ${p.currency}`, el('span', { class: 'mono' }, String(p.id))]), 'No purchases.')),
    ];
  }

  function tabShips() {
    const opts = []; const seen = new Set();
    P.ships.forEach((s) => { if (!seen.has(s.precast_id)) { seen.add(s.precast_id); opts.push(el('option', { value: s.precast_id }, `T${s.tier} ${s.name}`)); } });
    const ship = el('select', { id: 'plXpShip', 'aria-label': 'Ship' }, opts.length ? opts : el('option', { value: '' }, 'no ships'));
    const amount = el('input', { type: 'number', step: '1', id: 'plXpAmt', placeholder: 'XP amount' });
    const mode = el('select', { id: 'plXpMode', 'aria-label': 'Mode' }, el('option', { value: 'add' }, 'Add (negative subtracts)'), el('option', { value: 'set' }, 'Set exactly'));
    const apply = el('button', { class: 'btn', type: 'button', onclick: () => {
      const n = Math.trunc(Number(amount.value));
      if (!ship.value || amount.value.trim() === '' || !Number.isFinite(n)) { msg().className = 'err'; msg().textContent = 'Pick a ship and enter an XP amount.'; return; }
      if (!confirm(`${mode.value === 'add' ? 'Add' : 'Set'} ${fmt(n)} ship XP on ${ship.options[ship.selectedIndex].text} for ${name()}?`)) return;
      run(() => postJSON(base() + '/shipxp', { ship_id: Number(ship.value), amount: n, mode: mode.value }), (d) => `Ship XP ${fmt(d.before)} → ${fmt(d.after)}: ${d.note}.`);
    } }, 'Apply');
    return [
      section('Ship XP', el('div', { class: 'panel' }, el('div', { class: 'row' }, ship, mode, amount, apply),
        el('div', { class: 'muted' }, 'Stored per ship (pawn) id, like battle rewards; the game shows it under each hull of that ship. Free XP is changed in the action bar.')),
        table(['Ship', { label: 'XP', num: true }, 'Hull ids (client keys)', 'Updated'], P.ship_xp.map((x) => [x.name, fmt(x.xp),
          el('span', { class: 'mono' }, (x.hull_ids || []).join(', ') || '—'), when(x.updated)]), 'No ship has XP yet.')),
      section(`Ships (${fmt(P.ships.length)} loadouts)`, table(['Ship', { label: 'Tier', num: true }, 'Type', 'Loadout', 'Active', 'Id'],
        P.ships.map((s) => [s.name, s.tier || '—', s.hero ? chip('hero', 'warn') : 'base', `#${s.index + 1}`, s.active ? 'yes' : '—', el('span', { class: 'mono' }, String(s.precast_id))]), 'No ships.')),
    ];
  }

  function tabFleets() {
    const out = [];
    P.fleets.forEach((f) => out.push(section(`${f.name || 'Fleet ' + f.id}`, el('div', { class: 'row' }, chip(f.type), f.active ? chip('active', 'good') : null,
      el('span', { class: 'muted' }, f.flagship_name ? 'flagship: ' + f.flagship_name : 'no flagship set')),
      table([{ label: 'Place', num: true }, 'Ship', 'Loadout id'], f.ships.map((s) => [s.position + 1, s.name || '?', el('span', { class: 'mono' }, String(s.loadout_id))]), 'Empty fleet.'))));
    if (!P.fleets.length) out.push(el('div', { class: 'empty' }, 'No fleets.'));
    const rows = P.ships.map((s) => [s.name, `#${s.index + 1}`, el('span', { class: 'results' }, s.equipment.map((e) => `${e.slot}: ${e.name}`).join(' · ') || 'stock fit')]);
    out.push(section('Loadouts', table(['Ship', 'Slot', { label: 'Equipment', wrap: true }], rows, 'No loadouts.')));
    return out;
  }

  function tabProgress() {
    const cname = (q) => CONTRACT_NAMES[q] || q;
    const cs = P.contract_state;
    return [
      el('div', { class: 'split' },
        section('Career goals claimed', table(['Goal', { label: 'Stages', num: true }, 'Updated'], P.career_claims.map((c) => [c.goal, c.stages, when(c.updated)]), 'Nothing claimed.')),
        section('Ribbons', el('div', { class: 'panel' }, bars(P.ribbons.map((x) => ({ label: x.name, value: x.value })), { empty: 'No ribbons yet.' })))),
      el('div', { class: 'split' },
        section('Statistics counters', table(['Counter', { label: 'Value', num: true }], P.counters.map((c) => [c.name, fmt(c.value)]), 'No counters.')),
        section('Faction reputation', table(['Faction', { label: 'Reputation', num: true }], P.factions.map((f) => [f.name || 'faction ' + f.id, fmt(f.reputation)]), 'None.'))),
      section(`Daily contracts, newest first (${fmt(P.contracts.length)})`,
        table(['Slot', 'Contract', { label: 'Progress', num: true }, { label: 'Credits', num: true }, { label: 'GP', num: true }, 'State', 'Assigned', 'Completed'],
          P.contracts.map((c) => [c.elite ? 'Elite' : String(c.slot + 1), cname(c.quest), `${fmt(c.progress)} / ${fmt(c.target)}`, fmt(c.reward), c.gp_paid ? fmt(c.gp_paid) : '—',
            chip(c.state + (c.paid ? ', paid' : ''), c.state === 'acknowledged' ? 'good' : ''), when(c.assigned_at), c.completed_at ? when(c.completed_at) : '—']), 'No contracts yet.'),
        el('div', { class: 'muted', style: 'margin-top:6px' }, `Contract set ${fmt(cs.state_id)}, last assignment ${cs.last_assignment ? new Date(cs.last_assignment * 1000).toISOString().replace('T', ' ').slice(0, 16) + ' UTC' : 'never'}.`)),
      P.contracts_legacy.length ? section('Older contract rows', table(['Contract', 'State', { label: 'Progress', num: true }, 'Updated'], P.contracts_legacy.map((c) => [c.id, c.state, c.progress + ' %', when(c.updated)]))) : null,
    ];
  }

  function tabSocial() {
    const person = (p) => Admin.playerLink(p.pid, p.name || p.pid.slice(0, 8) + '…');
    return [
      el('div', { class: 'split' },
        section(`Friends (${fmt(P.friends.length)})`, table(['Player', 'State', 'Asked by', 'Since'], P.friends.map((p) => [person(p), chip(p.state, p.state === 'accepted' ? 'good' : 'warn'),
          p.state === 'accepted' ? '—' : (p.requested_by_player ? name() : 'them'), when(p.created)]), 'No friends.')),
        section(`Ignores (${fmt(P.ignores.length)})`, table(['Player', 'Since'], P.ignores.map((p) => [person(p), when(p.created)]), 'Ignores nobody.'),
          el('div', { class: 'muted' }, `${fmt(P.ignored_by)} players ignore ${name()}.`))),
      section(`Chat, newest ${fmt(P.chat.length)}`, table(['When', 'Channel', { label: 'Line', wrap: true }], P.chat.map((l) => [when(l.at), l.channel, l.text]), 'No chat lines.')),
    ];
  }

  function tabMatches() {
    return [section(`Recent matches (${fmt(P.matches.length)})`, table(MATCH_COLS, matchRows(P.matches), 'No matches played.'),
      el('div', { class: 'muted' }, 'The date opens the match.'))];
  }

  function tabModeration() {
    const text = el('textarea', { id: 'plNoteText', maxlength: '2000', placeholder: 'A note about this account (never shown to the player)' });
    const add = el('button', { class: 'btn primary', type: 'button', onclick: async () => {
      const note = text.value.trim(); if (!note) return;
      const r = await act(() => postJSON(base() + '/notes', { note }), msg(), 'Note added.');
      if (r) { text.value = ''; Admin.refresh(); }
    } }, 'Add note');
    const del = (n) => el('button', { class: 'btn small danger', type: 'button', onclick: async () => {
      if (!confirm('Delete this note?')) return;
      if (await act(() => api(base() + '/notes/' + n.id, { method: 'DELETE' }), msg(), 'Note deleted.')) Admin.refresh();
    } }, 'Delete');
    return [
      el('div', { class: 'split' },
        section('Ban', restriction('Ban', P.ban, 'Not banned.')), section('Chat mute', restriction('Mute', P.mute, 'Not muted.'))),
      section('Notes', el('div', { class: 'panel' }, text, el('div', { class: 'row', style: 'margin:8px 0 0' }, add)),
        table(['When', 'By', { label: 'Note', wrap: true }, ''], P.notes.map((n) => [when(n.created_at), n.actor, n.note, del(n)]), 'No notes on this account.')),
      section(`Admin actions on this player (${fmt(P.audit.length)})`, table(['When', 'Action', { label: 'Details', wrap: true }, 'From'],
        P.audit.map((a) => [when(a.at), a.action, auditText(a.details), a.actor]), 'No admin actions on this player.')),
    ];
  }

  // The whole log as text, or (.gz) as it is stored: about a tenth of the size.
  const logButton = (g) => el('span', { class: 'row', style: 'margin:0' },
    el('button', { class: 'btn small', type: 'button', onclick: async () => {
      const r = await act(() => apiBlob('/client-logs/' + g.id), msg(), 'Downloaded.');
      if (r) download(r.name, r.blob);
    } }, 'Download'),
    el('button', { class: 'btn small', type: 'button', title: 'The stored file, gzip-compressed', onclick: async () => {
      const r = await act(() => apiBlob('/client-logs/' + g.id + '?gzip=1'), msg(), 'Downloaded.');
      if (r) download(r.name, r.blob);
    } }, '.gz'));

  function tabReports() {
    const r = P.reports; const t = P.telemetry;
    return [
      el('div', { class: 'grid' }, card('Bug reports', fmt(r.total), false, `${fmt(r.last_7d)} in 7 days`), card('Last report', r.last ? ago(r.last) + ' ago' : 'never', true),
        card('Telemetry events', fmt(t.total), false, `${fmt(t.last_7d)} in 7 days`), card('Last event', t.last ? ago(t.last) + ' ago' : 'never', true)),
      el('div', { class: 'row' }, link('Open this player\'s reports', 'reports', pid), link('Open this player\'s telemetry', 'telemetry', pid),
        link('Fair-play checks', 'fairplay', 'p:' + pid)),
      section(`Game logs from the launcher (${fmt((P.client_logs || []).length)})`,
        table(['Uploaded', 'Session', 'Result', 'Size', 'Launcher', ''], (P.client_logs || []).map((g) => [when(g.uploaded_at),
          g.seconds ? duration(g.seconds) : '—',
          g.crashed ? chip('crashed (exit ' + g.exit_code + ')', 'bad') : g.late ? chip('sent at next start', 'warn')
            : g.exit_code !== 0 ? chip('closed, crashed on exit (' + g.exit_code + ')', 'warn') : chip('closed', 'good'),
          bytes(g.bytes) + (g.stored_bytes ? ' (' + bytes(g.stored_bytes) + ' stored)' : ''), g.launcher_version || '—', logButton(g)]), 'No game logs uploaded yet.'),
        el('div', { class: 'muted' }, 'The launcher uploads the whole DreadGame.log when the game closes (since launcher v1.0.11; older ones sent only the first 256 KiB and the last 1.75 MiB, which is all the game kept), without the sign-in token and the Windows computer and user names. "Crashed on exit": the game shut down completely, then its process ended with an error code (0xC0000005 = 3221225477 on every quit, harmless).')),
    ];
  }

  const RENDER = { overview: tabOverview, inventory: tabInventory, ships: tabShips, fleets: tabFleets, progress: tabProgress, social: tabSocial, matches: tabMatches, moderation: tabModeration, reports: tabReports };
  function renderBody(id) { if (P) $('plBody').replaceChildren(...RENDER[id]().filter(Boolean)); }

  Admin.page({
    id: 'player', title: () => 'Player', hidden: true, parent: 'players', auto: 30,
    html: `
      <style>
        #page-player .head { display: flex; gap: 12px; align-items: baseline; flex-wrap: wrap; }
        #page-player .head h2 { margin: 0; font-size: 20px; text-transform: none; letter-spacing: 0; color: var(--ink); }
        #page-player .acts { display: grid; gap: 10px; }
        #page-player .acts .row { margin: 0; }
        #page-player .acts h3 { font-size: 12px; color: var(--muted); text-transform: uppercase; letter-spacing: .06em; margin: 0; width: 100px; flex: none; }
        #page-player .acts input[type=number] { width: 120px; }
        #page-player .acts #plReason { flex: 1; min-width: 200px; }
        #page-player .split > section { margin-top: 0; }
        #page-player .pw { display: flex; gap: 8px; align-items: center; }
        #page-player .pw code { font-size: 14px; user-select: all; }
        #page-player textarea { min-height: 80px; }
        @media (max-width: 640px) { #page-player .acts h3 { width: 100%; } #page-player .acts input[type=number] { width: 100%; } }
      </style>
      <div class="row"><a class="link" href="#/players">&larr; All players</a></div>
      <div class="head"><h2 id="plName"></h2><span id="plChips"></span><span class="spacer"></span><span class="mono muted" id="plPid"></span></div>
      <div class="grid" id="plKpis"></div>
      <section class="panel acts" id="plActs">
        <div class="row"><h3>Moderation</h3>
          <input id="plReason" type="text" maxlength="200" placeholder="Reason (required for ban and mute)" aria-label="Reason">
          <button class="btn" id="plKick" type="button">Kick</button>
          <select id="plBanFor" aria-label="Ban length"><option value="1">Ban: 1 hour</option><option value="24">Ban: 24 hours</option><option value="168">Ban: 7 days</option><option value="720">Ban: 30 days</option><option value="0">Ban: permanent</option></select>
          <button class="btn danger" id="plBan" type="button">Ban</button><button class="btn" id="plUnban" type="button">Unban</button>
          <select id="plMuteFor" aria-label="Mute length"><option value="1">Mute: 1 hour</option><option value="24">Mute: 24 hours</option><option value="168">Mute: 7 days</option><option value="720">Mute: 30 days</option><option value="0">Mute: permanent</option></select>
          <button class="btn" id="plMute" type="button">Mute chat</button><button class="btn" id="plUnmute" type="button">Unmute</button>
        </div>
        <div class="row"><h3>Account</h3>
          <input id="plNewName" type="text" maxlength="20" placeholder="New name" aria-label="New name">
          <label class="check"><input type="checkbox" id="plSignOut"> Sign out now</label>
          <button class="btn" id="plRename" type="button">Rename</button>
          <button class="btn danger" id="plResetPw" type="button">Reset password</button>
          <button class="btn" id="plResetContracts" type="button">Reset contracts</button>
          <select id="plTune" aria-label="Tune channel test"><option value="">Tune test: off</option><option value="check">Tune test: check</option><option value="apply">Tune test: apply</option></select>
          <button class="btn" id="plTuneSave" type="button">Set</button>
        </div>
        <div class="row hidden pw" id="plPwBox"><span class="muted">New password:</span><code id="plPw"></code><button class="btn small" id="plPwCopy" type="button">Copy</button></div>
        <div class="row"><h3>Balances</h3>
          <input id="plCredits" type="number" step="1" placeholder="Credits" aria-label="Credits"><input id="plPremium" type="number" step="1" placeholder="GP" aria-label="GP">
          <input id="plFreeXP" type="number" step="1" placeholder="Free XP" aria-label="Free XP">
          <button class="btn" id="plAdd" type="button">Add</button><button class="btn" id="plSet" type="button">Set exactly</button>
          <span class="muted">Empty fields stay unchanged; a negative add subtracts.</span>
        </div>
        <div class="row"><h3>Hero ship</h3><select id="plHero" aria-label="Hero ship"></select><button class="btn" id="plGrant" type="button">Grant ship</button></div>
        <div id="plMsg" role="status"></div>
      </section>
      <div id="plTabs"></div>
      <div id="plBody"></div>`,
    init() {
      wireActions();
      tabCtl = tabs($('plTabs'), TABS, renderBody);
      tabCtl.pick('overview');
    },
    async load(address) {
      // player/<pid> or player/<pid>/<tab> (a link straight to a sub tab).
      const [arg, wantTab] = (address || '').split('/');
      if (!arg) { Admin.go('players'); return; }
      if (wantTab && wantTab !== openedTab) { openedTab = wantTab; tabCtl.pick(wantTab); }
      if (arg !== pid) { pid = arg; P = null; invFilter = ''; msg().textContent = ''; $('plPwBox').classList.add('hidden'); $('plPw').textContent = ''; $('plBody').replaceChildren(); }
      try { P = await api('/players/' + encodeURIComponent(arg) + '/profile'); }
      catch (e) { if (e.message === 'forbidden') throw e; P = null; $('plName').textContent = 'Player not found'; $('plKpis').replaceChildren(); $('plChips').replaceChildren(); $('plBody').replaceChildren(el('div', { class: 'banner' }, e.message)); return; }
      renderHead();
      // An auto refresh must not wipe what the admin is typing in a tab.
      const a = document.activeElement;
      if (!(a && $('plBody').contains(a) && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName))) renderBody(tabCtl.current());
    },
  });
})();
