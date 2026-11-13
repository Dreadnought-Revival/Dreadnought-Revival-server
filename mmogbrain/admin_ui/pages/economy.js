// Economy: currency in circulation, who holds it, what is bought, what the
// server pays out, active sales, and the contract GP setting.
(() => {
  const { $, el, api, postJSON, fmt, card, chart, bars, table, chip, playerLink, link, when, act, toCSV, download } = Admin;
  let days = 30; let last = null; let settingsShown = false;
  try { days = Number(localStorage.getItem('adminEconomyDays')) || 30; } catch (e) {}

  // core.js chart() appends its missing legend (null) as the text "null" for a
  // single series; drop stray text nodes until that is fixed there.
  const dropNull = (box) => { [...box.childNodes].filter((n) => n.nodeType === 3).forEach((n) => n.remove()); };
  const short = (d) => d.slice(5);
  const CUR = { credits: ['Credits (CR)', 'var(--s1)'], gp: ['GP (premium)', 'var(--s2)'], free_xp: ['Free XP', 'var(--s3)'] };
  // The client's own currency names (see dreadnought-purchase-currency).
  const CURNAME = { CR: 'Credits (CR)', SP_regular: 'GP (SP_regular)', freexp: 'Free XP (research)', gp: 'GP (legacy)', bundle: 'Bundle', admin: 'Provisioned / admin', refunded: 'Refunded' };
  const curName = (c) => CURNAME[c] || c;

  function perDay(e, currency, field) {
    const m = {}; e.purchases_daily.filter((p) => p.currency === currency).forEach((p) => { m[p.day] = p[field]; });
    return e.day_list.map((d) => m[d] || 0);
  }
  function chartBox(host, title, labels, name, values, color, unit) {
    const box = el('div'); host.append(box);
    chart(box, title, labels, [{ name, values, color, unit }], { short, maxLabels: 10, height: 170 });
    dropNull(box);
  }

  function holders(key, e) {
    const s = e.supply[key];
    const box = el('div', { class: 'chart' }, el('h3', {}, 'Richest: ' + CUR[key][0]),
      bars(s.richest.map((h) => ({ label: playerLink(h.pid, h.name || '?'), value: h.value })), { empty: 'Nobody holds any.', color: CUR[key][1] }));
    return box;
  }

  function render(e) {
    last = e;
    const labels = e.day_list;
    const S = e.supply;
    const sum = (a, f) => a.reduce((n, x) => n + x[f], 0);
    const buys = e.purchases_daily.filter((p) => p.currency !== 'admin' && p.currency !== 'refunded');
    $('ecCards').replaceChildren(
      card('Credits in circulation', fmt(S.credits.total), false, `median ${fmt(S.credits.median)} · ${fmt(S.credits.holders)} holders`),
      card('GP in circulation', fmt(S.gp.total), false, `median ${fmt(S.gp.median)} · ${fmt(S.gp.holders)} holders`),
      card('Free XP held', fmt(S.free_xp.total), false, `median ${fmt(S.free_xp.median)} · ${fmt(S.free_xp.holders)} holders`),
      card(`Purchases, ${e.days} d`, fmt(buys.reduce((n, p) => n + p.count, 0)), false, 'store, research and bundles'),
      card(`Credits paid by matches, ${e.days} d`, fmt(sum(e.match_pay_daily, 'credits')), false, `${fmt(sum(e.match_pay_daily, 'xp'))} XP`),
      card(`Contracts claimed, ${e.days} d`, fmt(sum(e.contract_daily, 'claims')), false, `${fmt(sum(e.contract_daily, 'credits'))} CR + ${fmt(sum(e.contract_daily, 'gp'))} GP · ${fmt(e.contracts_unclaimed)} waiting to be claimed`),
    );
    $('ecRich').replaceChildren(holders('credits', e), holders('gp', e), holders('free_xp', e));

    const sp = $('ecSpend'); sp.replaceChildren();
    chartBox(sp, 'Credits spent per day (CR purchases)', labels, 'credits spent', perDay(e, 'CR', 'spent'), CUR.credits[1]);
    chartBox(sp, 'GP spent per day (SP_regular purchases)', labels, 'GP spent', perDay(e, 'SP_regular', 'spent'), CUR.gp[1]);
    chartBox(sp, 'Free XP spent per day (research unlocks)', labels, 'free XP spent', perDay(e, 'freexp', 'spent'), CUR.free_xp[1]);
    const total = labels.map((d, i) => buys.filter((p) => p.day === d).reduce((n, p) => n + p.count, 0));
    chartBox(sp, 'Purchases per day (all currencies)', labels, 'purchases', total, 'var(--s4)');

    const pay = $('ecPay'); pay.replaceChildren();
    chartBox(pay, 'Credits paid by matches per day', labels, 'credits', e.match_pay_daily.map((d) => d.credits), CUR.credits[1]);
    chartBox(pay, 'XP paid by matches per day', labels, 'XP', e.match_pay_daily.map((d) => d.xp), 'var(--s5)');
    chartBox(pay, 'Contracts claimed per day', labels, 'claims', e.contract_daily.map((d) => d.claims), 'var(--s6)');
    chartBox(pay, 'GP paid by contracts per day', labels, 'GP', e.contract_daily.map((d) => d.gp), CUR.gp[1]);

    $('ecItems').replaceChildren(el('h3', {}, `Top purchased items, ${e.days} d`),
      bars(e.top_items.map((it) => ({ label: it.name, value: it.count, text: fmt(it.count) })), { empty: 'No purchases in this range.' }));
    $('ecCur').replaceChildren(table(['Currency', { label: 'Purchases', num: true }, { label: 'Paid in total', num: true }],
      e.purchase_currencies.map((p) => [curName(p.currency), fmt(p.count), p.spent ? fmt(p.spent) : '—']), 'No purchases in this range.'));

    $('ecItemsTable').replaceChildren(table(['Item', 'Type', 'Currency', { label: 'Bought', num: true }, { label: 'Paid in total', num: true }],
      e.top_items.map((it) => [it.name + ` (${it.item_id})`, it.type || '—', curName(it.currency), fmt(it.count), it.spent ? fmt(it.spent) : '—']), 'No purchases in this range.'));

    $('ecSales').replaceChildren(table(['Sale', { label: 'Off', num: true }, 'Scope', 'From', 'To', 'State'],
      e.sales.map((s) => [s.name, s.percent + ' %', s.scope, when(s.start_at), when(s.end_at), chip(s.status, s.status === 'active' ? 'good' : s.status === 'upcoming' ? 'warn' : '')]),
      'No sale is running or was running in the last 30 days.'));

    $('ecDaily').replaceChildren(table(['Day (UTC)', { label: 'Purchases', num: true }, { label: 'Match credits', num: true }, { label: 'Match XP', num: true }, { label: 'Contract claims', num: true }, { label: 'Contract GP', num: true }],
      labels.map((d, i) => [d, fmt(total[i]), fmt(e.match_pay_daily[i].credits), fmt(e.match_pay_daily[i].xp), fmt(e.contract_daily[i].claims), fmt(e.contract_daily[i].gp)]).reverse()));
  }

  function renderSettings(d) {
    $('ecGP').max = d.contract_gp_reward_max;
    $('ecInfo').textContent = `Now ${fmt(d.contract_gp_reward)} GP (default ${fmt(d.contract_gp_reward_default)}).`;
  }

  async function load() {
    const e = await api('/economy?days=' + days);
    render(e);
    renderSettings(e.settings);
    if (!settingsShown) { $('ecGP').value = e.settings.contract_gp_reward; settingsShown = true; }
  }

  Admin.page({
    id: 'economy', title: 'Economy', group: 'Economy', order: 10, auto: 60,
    html: `
      <div class="row" id="ecRange"><span class="muted">Range:</span>
        <button class="btn" data-days="7">7 days</button><button class="btn" data-days="30">30 days</button><button class="btn" data-days="90">90 days</button>
        <span class="spacer"></span><button class="btn" id="ecCSV" type="button">Download daily table (CSV)</button></div>
      <div class="grid" id="ecCards"></div>
      <section><h2>Who holds the currency</h2><div class="charts" id="ecRich"></div></section>
      <section><h2>What players spend (by purchase day, UTC)</h2><div class="charts" id="ecSpend"></div></section>
      <section><h2>What the server pays out (UTC)</h2><div class="charts" id="ecPay"></div>
        <div class="muted" style="margin-top:6px">Contract days are the day the contract was completed: the claim time itself is not stored.</div></section>
      <section><h2>Purchases</h2><div class="split"><div class="chart" id="ecItems"></div><div><h3 style="margin:0 0 6px;font-size:15px">By currency</h3><div id="ecCur"></div></div></div>
        <div id="ecItemsTable" style="margin-top:12px"></div></section>
      <section><h2>Sales</h2><div id="ecSales"></div></section>
      <section><h2>Per day</h2><div id="ecDaily"></div></section>
      <section class="manage"><h2>Daily contracts</h2>
        <div class="fields"><label>GP per claimed contract<input id="ecGP" type="number" min="0" step="1"></label></div>
        <div class="row"><button class="btn primary" id="ecSave" type="button">Save</button><span class="muted" id="ecInfo"></span></div>
        <div class="muted">Paid on top of the contract's credits when a player claims a finished contract, from the next claim on. 0 turns it off. The contract card in the game only shows the credits; the GP shows up in the player's balance. Every change is written to the audit log.</div>
        <p id="ecMsg"></p>
      </section>`,
    init() {
      const mark = () => document.querySelectorAll('#ecRange button[data-days]').forEach((b) => b.classList.toggle('on', Number(b.dataset.days) === days));
      mark();
      document.querySelectorAll('#ecRange button[data-days]').forEach((b) => {
        b.onclick = () => {
          days = Number(b.dataset.days); try { localStorage.setItem('adminEconomyDays', days); } catch (e) {}
          mark(); Admin.refresh();
        };
      });
      $('ecCSV').onclick = () => {
        if (!last) return;
        const e = last;
        const rows = [['day', 'purchases_cr', 'cr_spent', 'purchases_sp_regular', 'gp_spent', 'purchases_freexp', 'freexp_spent', 'match_results', 'match_credits', 'match_xp', 'contract_claims', 'contract_credits', 'contract_gp']];
        e.day_list.forEach((d, i) => {
          const f = (c, k) => perDay(e, c, k)[i];
          rows.push([d, f('CR', 'count'), f('CR', 'spent'), f('SP_regular', 'count'), f('SP_regular', 'spent'), f('freexp', 'count'), f('freexp', 'spent'),
            e.match_pay_daily[i].results, e.match_pay_daily[i].credits, e.match_pay_daily[i].xp, e.contract_daily[i].claims, e.contract_daily[i].credits, e.contract_daily[i].gp]);
        });
        download(`economy-${e.days}d.csv`, toCSV(rows), 'text/csv');
      };
      $('ecSave').onclick = async () => {
        const v = $('ecGP').value.trim();
        if (v === '' || !Number.isInteger(Number(v))) { $('ecMsg').className = 'err'; $('ecMsg').textContent = 'Enter a whole number.'; return; }
        if (!confirm(`Pay ${fmt(Number(v))} GP per claimed daily contract?`)) return;
        const d = await act(() => postJSON('/settings', { contract_gp_reward: Number(v) }), 'ecMsg', 'Saved.');
        if (d) { renderSettings(d); $('ecGP').value = d.contract_gp_reward; }
      };
    },
    load,
  });
})();
