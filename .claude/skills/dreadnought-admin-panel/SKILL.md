---
name: dreadnought-admin-panel
description: How the Server Admin dashboard of the Dreadnought private server works and how to extend it - where it is served, how the admin key gates it, every page with the API endpoint, tables and side effects behind each action (bans, mutes, balances, game modes and bot tuning, market, tech tree, balancing, restart, backup), the file pattern for adding a page or endpoint, and the gotchas (single-connection SQLite, read-only DB access). Use before changing or documenting anything under mmogbrain/admin_ui/ or mmogbrain/admin_*.go, before adding an admin action, and when an operator asks "can the dashboard do X".
---

# The Server Admin dashboard

A single-page web UI served by **mmogbrain**. Everything an operator does by hand
in SQL or `curl` (ban someone, grant GP, change queue sizes, restart when idle)
is a page here, and every change is written to the audit log.

Source of truth is the code; this skill is a map. Sections marked **unverified**
could not be confirmed from code alone.

## Where it lives

| Thing | Where |
|---|---|
| Front end (embedded in the binary with `go:embed`) | `mmogbrain/admin_ui/`: `index.html` (shell), `style.css`, `core.js`, `pages/*.js` |
| Serving + route mounting | `mmogbrain/admin_ui.go`, `mmogbrain/admin_dashboard.go` |
| One back-end file per page | `mmogbrain/admin_<page>.go` (list below) |
| Older back ends that predate the `admin_` naming | `mode_settings.go`, `bot_tuning.go`, `economy_settings.go`, `market_pricing.go`, `techtree_costs.go`, `tune_editor.go`, `tune_reply.go`, `battle_tune.go`, `server_messages.go`, `restart.go`, `fair_play_admin.go`, `player_rename.go` |
| Tables | `mmogbrain/db/db.go` (the `migrations` list) |
| Tests | `mmogbrain/admin_*_test.go` |

Routes (registered in `newRouter`, `mmogbrain/main.go`, by `registerAdminDashboard`):

- `GET /admin/dashboard` - the shell. **No key needed** (the sign-in form has to load). `Cache-Control: no-store`.
- `GET /admin/ui/<file>` - `core.js`, `style.css`, `pages/<page>.js` from the embedded FS. No key, no data in them.
- `/admin/api/*` - JSON API. **Every route needs the admin key.**
- `/admin/queue`, `/admin/players`, `/admin/grant` - three older key-guarded routes
  (`handlers/`), used by `admin-cli`. The dashboard does not call them.

**Port:** mmogbrain's plain HTTP listener, `ADDR` (default `:8083`). Open
`http://127.0.0.1:8083/admin/dashboard` on the box, or through an SSH tunnel.

**Exposure - check before relying on "LAN only".** The code comment says the port is not
forwarded to the internet. The gateway (`gateway/main.go`) has a host route for
mmogbrain and blocks only `/battle/` and `/online` (`machineLocalPath`); it has no
`/admin` rule. Whether `/admin/dashboard` is reachable through the gateway's public
hostname was **not tested** (unverified). The key is then the only gate.

## Authentication and authorization

Two ways in (since 2026-11-25, `mmogbrain/staff.go`):

- **Staff accounts** (table `staff`: player id + role `admin` | `moderator` | `balancer`;
  managed on the **Staff** page, global admins only). A staff member signs in on the
  dashboard with their **game** name/email and password: `POST /admin/login` asks the auth
  server (`POST /auth/`), verifies the JWT it returns with `JWT_SECRET`, and issues a 12 h
  session token (`staff_sessions` stores only its SHA-256; 10 attempts per IP per 15 min).
  The browser sends it as `X-Admin-Session`; `POST /admin/logout` ends it; removing someone
  from the staff ends their sessions at once.
- **The admin key** (`ADMIN_KEY` in `run/secrets.env`; never print its value), header
  `X-Admin-Key`, compared in constant time. It counts as a global admin (break-glass).
  `requireAdminKey` (`main.go`) makes mmogbrain refuse to start if it is empty or the
  placeholder `changeme-admin-key`. `INTERNAL_API_KEY` falls back to `ADMIN_KEY`.

**Roles.** `staffAPIMiddleware` (on the `/admin/api` subrouter) checks the credential, then
the route: `staffRoutePerms` is an allow-list of `"METHOD /template"` per role; **anything not
listed is global-admin only**, so a new route never opens to moderators or balancers by
accident. When adding a route a moderator or balancer should use, add it there (the test
`TestStaffPermissionsNameRealRoutes` catches typos). `staffPages` lists the pages each role
sees (`GET /whoami` returns them; the front end hides the rest). A bad credential is
`403 {"error":"forbidden"}` (dashboard locks); a role refusal is `403` with a readable reason.
Moderators: bans 1..168 h only and may lift only timed bans (enforced in the ban/unban
handlers and the in-game `/ban`, `/unban`). Balancers: tune, tech tree, game modes, market,
economy settings, read-only stats.

**In-game staff commands** (`staff_commands.go`): `/who`, `/warn`, `/mute`, `/unmute`,
`/kick`, `/ban`, `/unban`, `/announce`, gated by role (`staffCommandRoles`). The client sends
every "/" line as an admin command over the MMOG connection (`buildMmogChatPayload`; YMmogChat
0x2A47FC0 logs "Sending admin command"); answers are private Server lines.

- Browser: the sign-in form keeps the key (`adminKey`) or session token (`adminSession`) in
  `sessionStorage` and sends it on every `Admin.api()` call.
- The same `ADMIN_KEY` guards the auth server's `/admin/ban`, `/admin/unban`,
  `/admin/password`, `/admin/rename` (mmogbrain calls them with the key) and `admin-cli`.
- Audit: every action calls `adminAudit` and writes a row to `admin_audit`
  (`actor` = `"Name (moderator)"` for a staff session or in-game command, the caller's IP
  for the admin key, `server` for things the server did on its own, e.g. a ban expiring).

## How a page works (front end)

`core.js` exposes `window.Admin`. A page is one file in `pages/` that calls
`Admin.page({...})`:

```js
Admin.page({
  id: 'chat',            // route #/chat or #/chat/<arg>
  title: 'Chat',
  group: 'Live',         // Live | Players | Game | Economy | Insight | System
  order: 50,
  hidden: true, parent: 'players',   // detail pages: no sidebar entry
  html: `<section>...</section>`,    // static markup, written by us
  init(root) {},                     // once
  async load(arg) {},                // every refresh
  auto: 10,                          // seconds between refreshes (0 = manual)
});
```

- `admin_ui.go` lists `admin_ui/pages/*.js` itself and fills `<!--PAGES-->` in
  `index.html`, so **dropping a file in is the registration.** No build step, no npm.
- Helpers: `Admin.api(path)` (GET, `/admin/api` prefix added), `Admin.postJSON`,
  `Admin.act(fn, where, okText)` (runs an action, shows the result), `el`, `table`,
  `card`, `chip`, `chart`, `bars`, `when`, `ago`, `toCSV`/`download`.
- Server values go in as **text** (`el()` / `textContent`), never as HTML. Keep that.
- Sidebar player search (`/` focuses it) calls `GET /players?q=`.

## Pages

Group, page title, route, what it shows, actions. All endpoints are under `/admin/api`.

### Staff (System group, global admins only)

`#/staff`: add a player by captain name or id with a role, change roles, remove (ends their
sessions). `GET /staff`, `POST /staff {player, role}`, `DELETE /staff/{pid}`; audit `staff role`.

### Live

| Page | Shows / does | Endpoints | Tables / side effects |
|---|---|---|---|
| **Overview** `#/overview` | Services up (ports 8081-8085 `/health`), online, accounts, 24 h matches/kills/credits, host crashes from battle logs, **restart-when-idle** control | `GET /overview`, `/metrics?days=1`, `/restart`; `POST /restart`, `/restart/cancel` | Reads `player_state`, `queue_entries`, `matches`, `battle_results`, `client_reports`. See "Restart" below |
| **Battle servers** `#/instances` | Hosts the control plane (dn-dedicated) runs: mode, map, players, state, crash verdict, log, match. **Stop** button | `GET /instances`; `DELETE /instances/{id}` | Stop calls the control plane `DELETE /instances/<id>` with the internal key; audit `stop instance`. Kills a live match |
| **Queue** `#/queue` | Waiting players, totals per mode/fleet, forming matches. Read-only | `GET /queue` | `queue_entries`, `matches` (`forming`), `match_slots` |
| **Matches** `#/matches`, **Match** `#/match/<id>` (hidden) | History with filters/paging; one match with teams, per-player figures and stats, Havoc run, fair-play findings, battle log | `GET /matches`, `/matches/{id}` | `matches`, `match_slots`, `battle_results`, `battle_result_stats`, `havoc_runs`. Read-only. `battle_results.match_id` = `matches.battle_match_id` (a second round appends `-r<n>`) |
| **Chat** `#/chat` | Stored chat lines by channel, search, follow mode. **Mute 24 h** on a line | `GET /chat`; `POST /players/{pid}/mute` | `chat_messages`; mute: see Moderation |

### Players

| Page | Shows / does | Endpoints | Tables / side effects |
|---|---|---|---|
| **Players** `#/players` | Account search (name or id prefix; sort name/rank/last_seen/created; filters online/banned; paging, limit max 200), online list, CSV export | `GET /players?q=&sort=&online=1&banned=1&offset=&limit=`, `GET /online` | `player_state`, `admin_bans`, `battle_results`, `player_ship_loadouts`. `last_seen` is `player_state.updated_at` |
| **Player** `#/player/<pid>` (hidden) | Everything about one account in tabs: Overview, Inventory & purchases, Ships & XP, Fleets & loadouts, Progress, Social, Matches, Moderation, Reports & telemetry. Hosts all the per-player actions below | `GET /players/{pid}/profile` (one call, each section bounded), `GET /players/{pid}`, `/players/{pid}/matches`, `/players/{pid}/audit` | See actions below |
| **Moderation** `#/moderation` | Active bans, active mutes, a form to ban or mute by id, searchable audit log (action, text, target, dates, paging; limit 1-500) | `GET /bans`, `/mutes`, `/audit`; the ban/mute POSTs below | `admin_bans`, `admin_chat_mutes`, `admin_audit` |
| **Fair play** `#/fairplay[/<battle_id>]` | Checks the battle-server mod reports per result (speed vs bots, reported vs measured damage), flags, reviews | `GET /fairplay`; `POST /fairplay/review` | `battle_result_stats` (`check_*` rows), `battle_results`, `fair_play_reviews`. A review is a **note only**: status `false_positive`/`watch`/`confirmed` (empty status deletes it), note up to 500 chars; audit `fair play review`. Nothing acts on the player. The flag thresholds in `fairPlayThresholds` are a **copy** of the mod's constants: keep in step with `battle-server-mod` |
| **Bug reports** `#/reports[/<pid>]` | Client `YA_LogSpecial` reports, grouped by name, newest first, filters q/type/pid | `GET /reports` | `client_reports`. Read-only. Paging `before=<id>`, `limit` max 500 |

**Per-player actions** (all `POST /admin/api/players/{pid}/...`, `{pid}` = 32 lowercase hex, account must exist or 404; every one is audited under the player's id):

| Action | Endpoint and body | What happens |
|---|---|---|
| Kick | `/kick` `{reason}` | `adminDisconnect`: closes the social socket now, drops the gateway session, game socket closes on its next push pass (about 5 s per the code comment). They can sign in again |
| **Ban** (timed or permanent) | `/ban` `{reason, hours}` | Reason required (<=200). `hours` 0 = permanent, max 8760 (1 year). Upserts `admin_bans(user_id, reason, created_at, expires_at)` (`expires_at` `''` = permanent, else UTC `YYYY-MM-DD HH:MM:SS`); in-memory list updated; player disconnected; **forwarded to the auth server** `POST /admin/ban` so the launcher refuses too (best effort, result in the audit `auth` field). mmogbrain refuses banned players at gateway login and the Firmament handshake itself, because a launcher JWT lives 24 h |
| Unban | `/unban` `{reason}` | Deletes the row and memory entry, calls auth `/admin/unban` |
| Ban expiry | none (automatic) | `adminSweepExpired`, every minute (`admin_ban_expiry.go`): an expired ban already counts as lifted in memory; the sweeper calls auth unban **first**, and only if that works removes the row (else retries next minute); audit `ban expired`, actor `server` |
| **Mute** / Unmute | `/mute` `{reason, hours}`, `/unmute` | `admin_chat_mutes`. Muted player stays online and can play; channel and direct-message lines are refused with the error text `muted` (`chatLineRefused`, `social_handlers.go`). How the client shows that error is **unverified**. Same expiry rules and sweeper as bans |
| Reset password | `/password` `{reason}` | Auth server makes a new random password and signs the account out everywhere; the reply shows it **once**. The audit records the reset, never the password. Do not paste it into logs or notes |
| Rename | `/rename` `{name, signout}` | Auth server validates/free-checks the name; then `player_state.display_name` and `name_changed_at`; `signout` disconnects so the game picks it up |
| Balances | `/currency` `{mode:"add"\|"set", credits, premium, free_xp}` | Each amount optional; abs value <= 1,000,000,000; `add` clamps at 0, `set` needs >= 0. Writes `player_state`. If online, pushes `YA_RewardCurrencies` (credits/GP) and, for free XP, a `YA_ConvertShipXP` sync. `premium` = GP |
| Grant hero ship | `/ships` `{ship_id}` | Hero loadouts only (`heroShipLoadouts`); 409 if owned. Inserts `player_purchases` (currency `admin`, price 0) + loadout rows in one transaction; pushes the ship grant if online (`pushShipGrants`) |
| Ship XP | `/shipxp` `{ship_id, amount, mode}` | `ship_id` may be a hull loadout id (category 1/3) or a pawn id (10); stored in `player_ship_xp` by **pawn** id (the client reads by hull loadout id, see `ship_xp_keys.go`). Capped to int32. Online: pushed as a `YA_ConvertShipXP` delta (the client SUBTRACTS the value it is sent, so gains go out negative); the code says **not verified on a live client** |
| Reset daily contracts | `/contracts/reset` | `resetDailyContracts` and a contract-refresh push |
| Notes | `GET/POST /notes`, `DELETE /notes/{id}` | `admin_player_notes`, <=2000 chars, never shown to the player; audit `player note` |
| Tune test mode | `/tune-test` `{mode: ""\|"check"\|"apply"}` | `server_flags` `tune_test:<pid>`; applies at the player's next login |

### Game

| Page | Shows / does | Endpoints | Tables / side effects |
|---|---|---|---|
| **Game modes** `#/modes` | One card per queue the client offers (team size, teams, max in a match, matches in 7 days, **bots**), plus switches. Edits are staged; one Save with a confirm | `GET /modes`; `POST /modes` | Detail below |
| **Balancing** `#/tune` | Edit the game's tune tables, keep a draft, publish a numbered version, restore an old one, switch what battle servers apply | `GET /tune`, `/tune/table/{table}`, `/tune/battle`, `/tune/versions/{id}`; `POST /tune/draft`, `/tune/publish`, `/tune/discard`, `/tune/restore`, `/tune/battle` | `tune_draft`, `tune_versions`, `server_flags` `tune_battle_servers`. Detail below |
| **Tech tree** `#/techtree` | Research XP and purchase price per node, each with a status (`guess`/`sourced`/`accepted`) and source text; export/import JSON | `GET /techtree/ships`, `/techtree/ships/{id}`, `/techtree/costs`, `/techtree/export`; `POST /techtree/values`, `/techtree/import` | `techtree_cost_overrides(item_id, field xp\|price, amount NULL, status, source)`. `amount` NULL = keep the code's value, only annotate. Max 10,000,000. Players see it at next login; XP and credits already spent are untouched. Import **merges** |
| **Messages** `#/messages` | Send a line to everyone online; set the message of the day | `GET /messages`; `POST /messages/send`, `/messages/motd` | Send: a Server line in Global chat to all online players (<=300 chars, one line), audit `server message`. MOTD: `server_flags` `motd`, sent about 5 s after a player's chat connects (the delay is a GUESS in code); empty text turns it off. No scheduling or expiry |
| **Leaderboard** `#/leaderboard` | The ranking `YA_GetLeaderboardData` sends (same query, read-only). Name format and score rule are GUESSes noted in `leaderboard.go` | `GET /leaderboard?board=` | `battle_result_stats`; players with a row in `admin_bans` are excluded |
| **Havoc** `#/havoc` | Runs, weekly top scores, milestone rewards paid, waves reached | `GET /havoc?weeks=1..26` | `havoc_runs`, `havoc_reward_claims`. Read-only |

**Game modes in detail** (`mode_settings.go`, `bot_tuning.go`). Settings live in `server_flags`
and apply to the matchmaker at once; the environment gives the defaults and a "use default" /
`reset` entry deletes the override:

| Setting | Flag | Range | Effect |
|---|---|---|---|
| Escalation queue | `mode_escalation_queue` | `""` or a queueable mode | which queue runs Escalation |
| Havoc tile / Custom Game tile | `mode_havoc`, `mode_custom_game` | listed/hidden | shows at each player's **next login** (mode list is sent then) |
| Players per match / max wait | `match_max_players`, `match_max_wait_s` | 1-120 / 0-600 s | matchmaker sizing |
| Team size per queue | `team_size:<mode>` | 1..`matchmaker.MaxTeamSize`; `null` = default | max humans per team |
| **Bots per hosted mode** | `bot_tune:<mode>` = `"aim,reaction,abilities"` | aim 0-300, reaction 10-300, abilities 10-500 (percent; 100/100/100 = original game, which deletes the flag) | scales the AI controller numbers on the battle server |

- Bot tuning is keyed by the mode the host runs (`RunsAs`), so it is edited on that mode's
  card; a queue hosted as another mode shows "tuned on the X card". Not offered for
  Custom Game or PvE episodes. The limits come back in the `bot_limits` field.
- The battle-server mod asks once per match: `GET /battle/bots?mode=<hosted mode>`
  (loopback only; replies `"aim reaction abilities"`). **Bot changes apply to matches started
  after the save**; running matches keep theirs. The values are an operator setting, not
  original data; what reaction and ability % do is partly GUESS (see the header of `bot_tuning.go`).
- `backfill_window_s` / `backfill_modes` are shown read-only (env `DN_MATCH_BACKFILL_*`).
- Audit `game modes` with `from`/`to`.

**Balancing in detail** (`tune_editor.go`, `tune_reply.go`, `battle_tune.go`): only
`WeaponsTune`, `ProjectilesTune`, `OfficersTune` are editable, and a value keeps its type.
Edit goes to `tune_draft`; **publish** copies the draft into `tune_versions` as version
`1.0.<id>`, which reaches (a) accounts in tune test mode `apply` and (b) battle servers from
the next match, unless "battle servers" is switched off (`tune_battle_servers = 0`).
Restore puts an old version back into the draft (publish again to make it live). Audits:
`tune draft edit`, `tune publish`, `tune draft discarded`, `tune draft restored`,
`tune battle servers`.

### Economy

| Page | Shows / does | Endpoints | Tables / side effects |
|---|---|---|---|
| **Economy** `#/economy` | Currency in circulation, top holders, purchases by currency, what the server paid out, active sales; the **contract GP reward** setting | `GET /economy?days=`; `GET/POST /settings` | `player_state`, `player_purchases`, `battle_results`, `player_daily_contracts`, `market_sales`. Setting: `server_flags` `contract_gp_reward`, 0-100000 (default 200), applies to the next claim; audit `setting` |
| **Market** `#/market` | Price overrides (inline edit, reset) and sales (running/upcoming/ended) | `GET /market`; `POST /market/prices`, `/market/sales`, `/market/sales/{id}/end` | `market_price_overrides(key, amount)` (<=10,000,000; `null` resets to the code default), `market_sales(name <=60, percent 1-90, scope all\|ship\|coatings\|captain\|halloween, start_at, end_at)`. Audits `market prices`, `market sale`. **No push**: players see new prices at next login but are **charged** the new price immediately |

### Insight and System

| Page | Shows / does | Endpoints | Tables / side effects |
|---|---|---|---|
| **Metrics** `#/metrics` | Activity, modes, maps, ship classes, balance, retention (days 1-90) | `GET /metrics?days=` | A sampler (`startAdminMetricsSampler`) writes `admin_metrics_samples` every 5 min (first one 1 min after start; kept 90 days): the only history of who was online |
| **Telemetry** `#/telemetry[/<pid>]` | Client analytics events; reward events show the client's breakdown beside what the server paid | `GET /telemetry?rt=&pid=&q=` | `client_telemetry`, `battle_results`. Read-only, paged like reports |
| **System** `#/system` | Host, process, database, backups, certificates, TLS fallback memory, log sizes, runtime config | `GET /system`; `POST /system/backup` | Backup runs `bash scripts/backup.sh` from the repo root (5 min timeout, one at a time, else 409); audit `backup run`. Config values: secret-looking env names are never read, addresses show only "set" |
| **Logs** `#/logs[/<battle log>]` | Tail of an allow-listed log, grep, follow; battle logs with a crash verdict | `GET /logs?src=&name=&grep=&lines=`, `GET /battle-logs` | Read-only. `adminLogPath` is the allow-list: `mmogbrain`, `firmament` (repo-root `mmogbrain.log`), `dn-dedicated`, `gateway`, `auth-server`, `legacy-api`, `master-server` (`run/<name>.log`), `mod`, `crashprobe` (next to the game exe), `battle` (a plain `battle-*.log` name in `run/battle-logs`). `lines` 1-2000, grep <=200 chars, last 2 MB read |

### Restart (Overview page)

`POST /restart {hold_matches, announce}` (`restart.go`): checks every 10 s until the control
plane lists **no battle server** (gives up after 6 h), optionally holds new matches meanwhile
(`SetHold`) and posts `announce` as a Server chat line. When idle it launches a detached
helper that waits for mmogbrain to exit, sends mmogbrain SIGTERM, and the helper runs
`scripts/start-services.sh` (output `run/restart.log`). **Every online player is
disconnected.** `POST /restart/cancel` aborts while waiting. Audits: `restart requested`,
`restart`, `restart cancelled`, `restart gave up`, `restart failed`.

## Adding a page or endpoint

Each page is one front-end file plus one Go file, and neither needs central wiring beyond
`init()`:

1. **Back end**, `mmogbrain/admin_<page>.go`:
   ```go
   func init() { adminRegisterAPI(registerAdminThing) }

   func registerAdminThing(api *mux.Router) {
       api.HandleFunc("/thing", adminAPIThing).Methods(http.MethodGet)
       api.HandleFunc("/players/{pid}/thing", adminAPIThingSet).Methods(http.MethodPost)
   }
   ```
   `registerAdminDashboard` mounts everything in `adminAPIExtra` behind the key. No auth
   code in the handler. (Older pages are called by name from `registerAdminDashboard`
   instead; new ones should use `init()`.)
2. **Handler rules**: reply with `writeAdminJSON(w, v)` / errors with `writeAdminError`;
   cap bodies with `http.MaxBytesReader`; for a player route start with
   `pid, database, ok := adminPlayerPID(w, r)` (validates 32-hex, 404 for an unknown
   account, never creates one); **write `adminAudit(database, r, "<action>", target, details)` for
   every change** (details = a small map; never secrets); for an online player, push the
   same in-session messages the game flow uses so no relog is needed; for lists, bound
   `limit` (`adminPageLimit` caps at 500) and page with `before=<id>`.
3. **New table**: append (never insert in the middle; migrations are keyed by index) a
   `CREATE TABLE IF NOT EXISTS ...` to `migrations` in `mmogbrain/db/db.go`.
   `ALTER TABLE ... ADD COLUMN` is not re-run safely, so it goes at the end only once.
4. **Front end**, `mmogbrain/admin_ui/pages/<page>.js`: `Admin.page({...})` as above.
   Use `Admin.api`/`postJSON`/`act`; confirm destructive actions with `confirm()`.
   Rebuild mmogbrain to embed it (the files are in the binary, not read from disk).
5. **Tests**, in `mmogbrain/`: `adminTestRouter()` builds the router with key
   `test-admin-key`; `adminGet`, `adminPost`, `useTempMmogPlayerStateDB(t)` and
   `seedMmogPlayerState` give a temp DB. Two tests walk the router and cover a new route
   for free:
   - `TestAdminEveryAPIRouteNeedsTheKey` - 403 with no or a wrong key. It only fills
     the path variables `{pid}`, `{id}`, `{table}`, `{name}`; **use only those names** or the test fails.
   - `TestAdminEveryGetRouteAnswers` - every GET answers within 3 s (a deadlock check).
   Add a test of your own for the behaviour and the audit row.
6. **Look at it**: `ADMIN_UI_SERVE=127.0.0.1:18090 go test -run TestAdminUIServe -count=1 -timeout 30m .`
   (from `mmogbrain/`) serves the dashboard on a temp DB of invented players, key
   `test-admin-key`; `GET /shot?page=<id>&theme=dark` signs in and opens a page. It
   never touches the live stack.

Run `go test ./...` and `go vet ./...` inside `mmogbrain/` (per-module; see `dreadnought-stack`).

## Gotchas

- **mmogbrain's SQLite has ONE connection** (`SetMaxOpenConns(1)`). A DB call inside an
  open `rows.Next()` loop **deadlocks the whole server** (the 2026-09-29 outage). Read
  the rows into a slice, `Close()`, then query again; or JOIN. Helpers like
  `adminPlayerName` and `adminProfileRows` follow this. A name lookup per row is the usual
  way to break it.
- Read the live DB only with `sqlite3 -readonly run/mmog.db` (schema with `.schema`),
  never write by hand while mmogbrain runs.
- Do not call state-changing admin endpoints on the live server to "test" them. Several pay out,
  disconnect players or restart the service. Use `TestAdminUIServe` or unit tests.
- Player ids are 32 lowercase hex in the API. The gateway sees them dashed;
  `normalizedPlayerStatePID` handles both inside mmogbrain.
- Do not use `mmogPlayerStateForPID` to look a player up for display: it **seeds an
  account** for an unknown id (this created accounts from audit rows until 2026-11-12). Use
  `adminPlayerName` (read-only).
- Hot-path checks (ban, mute) read **in-memory** lists loaded lazily from the tables
  (`adminLoadBans`, `adminLoadMutes`). Never query the DB per chat line or handshake. Tests
  that touch them must reset the lists (`resetAdminModeration`).
- Changes by hand in SQL to `admin_bans` or `admin_chat_mutes` are not seen until
  mmogbrain restarts (memory list). Use the dashboard.
- The catalog, tech tree and market are built on every client fetch and the client fetches
  them at login: price, tech-tree and tile changes are seen at **next login**; there is no
  in-session re-fetch (`dreadnought-in-session-refresh` in memory notes).
- Front-end files are embedded: editing `admin_ui/` without rebuilding mmogbrain changes
  nothing. Static assets are `no-store`, so a rebuild shows on the next page load.
- With the admin key the audit `actor` is the caller's IP (proxied requests show
  `127.0.0.1`); give people staff accounts instead of the key so actions carry their names.
- Deploy and restart follow `dreadnought-stack`. The dashboard Restart control only waits for battle servers to finish; it never kills a match.
