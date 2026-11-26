// Staff: who may sign in to this dashboard with their game account, and in
// which role (global admin, moderator, balancer). Global admins only
// (staff.go). Removing someone signs them out at once.
(() => {
  const { $, el, api, postJSON, when, chip, table, playerLink, act } = Admin;
  const ROLE_TEXT = {
    admin: 'Global admin: everything, including this page.',
    moderator: 'Moderator: players, chat, moderation (bans up to 7 days), fair play, reports, messages; /warn /mute /kick /ban /announce in game.',
    balancer: 'Balancer: balancing tables, tech tree, game modes and bots, market and economy; read-only match stats.',
  };
  const ROLE_NAME = { admin: 'Global admin', moderator: 'Moderator', balancer: 'Balancer' };

  async function remove(x) {
    if (!confirm(`Remove ${x.name || x.pid} from the staff? They are signed out at once.`)) return;
    if (await act(() => api('/staff/' + x.pid, { method: 'DELETE' }), null, 'Removed.')) load();
  }
  async function resetTOTP(x) {
    if (!confirm(`Reset two-factor sign-in for ${x.name || x.pid}? They are signed out and set it up again at their next sign-in (lost phone).`)) return;
    if (await act(() => postJSON('/staff/' + x.pid + '/totp-reset', {}), null, '2FA reset.')) load();
  }
  async function setRole(x, role) {
    if (role === x.role) return;
    if (await act(() => postJSON('/staff', { player: x.pid, role }), null, 'Role changed.')) load();
  }

  async function load() {
    const d = await api('/staff');
    const rows = (d.staff || []).map((x) => {
      const pick = el('select', { 'aria-label': 'Role' }, (d.roles || []).map((r) => el('option', { value: r, selected: r === x.role }, ROLE_NAME[r] || r)));
      pick.onchange = () => setRole(x, pick.value);
      return [playerLink(x.pid, x.name), pick, x.two_fa ? chip('on', 'good') : chip('at next sign-in', 'warn'),
        x.sessions ? chip(x.sessions + ' signed in', 'good') : el('span', { class: 'muted' }, '—'),
        el('span', { class: 'muted' }, x.added_by || '—'), when(x.created_at),
        el('span', { class: 'row', style: 'margin:0' }, x.two_fa ? el('button', { class: 'btn small', type: 'button', onclick: () => resetTOTP(x) }, 'Reset 2FA') : null,
          el('button', { class: 'btn small', type: 'button', onclick: () => remove(x) }, 'Remove'))];
    });
    $('stList').replaceChildren(table(['Player', 'Role', '2FA', 'Dashboard', 'Added by', 'Since', ''], rows, 'No staff yet. The admin key still works.'));
  }

  Admin.page({
    id: 'staff', title: 'Staff', group: 'System', order: 5,
    html: `
      <section>
        <h2>Add staff</h2>
        <div class="muted">A staff member signs in here with their own game account (captain name or email and password) plus a code from an authenticator app (set up at their first sign-in), and uses the staff commands in game chat (/help lists them). A forgotten password is reset by email from the launcher, like any player's.</div>
        <div class="row">
          <input type="text" id="stWho" placeholder="Captain name or player id" spellcheck="false">
          <select id="stRole" aria-label="Role">
            <option value="moderator">Moderator</option>
            <option value="balancer">Balancer</option>
            <option value="admin">Global admin</option>
          </select>
          <button class="btn primary" id="stAdd" type="button">Add</button>
        </div>
        <div class="muted" id="stRoleText"></div>
      </section>
      <section>
        <h2>Staff</h2>
        <div id="stList"></div>
      </section>`,
    init() {
      const explain = () => { $('stRoleText').textContent = ROLE_TEXT[$('stRole').value] || ''; };
      $('stRole').onchange = explain; explain();
      $('stAdd').onclick = async () => {
        const player = $('stWho').value.trim();
        if (!player) return;
        if (await act(() => postJSON('/staff', { player, role: $('stRole').value }), null, (d) => `${d.name || player} is now ${ROLE_NAME[d.role] || d.role}.`)) { $('stWho').value = ''; load(); }
      };
    },
    load,
  });
})();
