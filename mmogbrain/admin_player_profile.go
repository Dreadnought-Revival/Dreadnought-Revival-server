package main

// The player page's one-call profile: GET /admin/api/players/{pid}/profile.
// Everything the admin wants on one account in a single reply, each section
// bounded. The store has ONE connection, so every query below is scanned
// completely and closed (adminProfileRows) before the next one runs, and no
// name lookup happens inside a scan: names come from JOINs or are resolved
// from in-memory tables after the rows are closed.

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminPlayerProfile) }

func registerAdminPlayerProfile(api *mux.Router) {
	api.HandleFunc("/players/{pid}/profile", adminAPIPlayerProfile).Methods(http.MethodGet)
	api.HandleFunc("/players/{pid}/shipxp", adminAPIPlayerShipXP).Methods(http.MethodPost)
}

// adminProfileRows runs one query and hands every row to each, then closes the
// result set. each must not touch the database.
func adminProfileRows(database *sql.DB, query string, args []any, each func(rs *sql.Rows)) {
	rows, err := database.Query(query, args...)
	if err != nil {
		return
	}
	for rows.Next() {
		each(rows)
	}
	_ = rows.Close()
}

// adminItemName is the best name for any item or ship id the code can
// resolve: hero ships, base hulls, the client's conversion table, the
// per-ship research rows and the market metadata, else "item <id>".
func adminItemName(id int32) string {
	if name := adminShipName(id); name != "" {
		return name
	}
	if name, ok := authoritativeItemName(id); ok && name != "" {
		return name
	}
	return techTreeItemName(id)
}

// adminItemKind names the family an item id belongs to (its top byte is the
// ItemIDTable category, see the client data invariants).
func adminItemKind(id int32) string {
	switch c := (id >> 24) & 0xff; {
	case c == mmogItemCategoryShipLoadoutPrecast:
		return "ship hull"
	case c == mmogItemCategoryShipLoadoutHero:
		return "hero ship"
	case c == mmogItemCategoryShipPawn:
		return "ship pawn"
	case isVanityItemID(id):
		return "cosmetic"
	default:
		return "module / other"
	}
}

type adminProfileBan struct {
	Active    bool   `json:"active"`
	Reason    string `json:"reason"`
	Since     string `json:"since"`
	Until     string `json:"until"` // "" = permanent
	Permanent bool   `json:"permanent"`
}

// adminProfileRestriction reads one ban/mute row (table admin_bans or
// admin_chat_mutes; fixed names, never request text). Active = no expiry or
// one still ahead; an expired row is returned with active false.
func adminProfileRestriction(database *sql.DB, table, pid string) *adminProfileBan {
	var b adminProfileBan
	if database.QueryRow(`SELECT reason, created_at, expires_at FROM `+table+` WHERE user_id=?`, pid).Scan(&b.Reason, &b.Since, &b.Until) != nil {
		return nil
	}
	b.Permanent = b.Until == ""
	b.Active = b.Permanent || b.Until > time.Now().UTC().Format("2006-01-02 15:04:05")
	return &b
}

type adminProfileItem struct {
	ID     int32  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"` // starter | purchase
}

type adminProfileEquip struct {
	Slot string `json:"slot"`
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

func adminAPIPlayerProfile(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	out := map[string]any{}
	// Online: the hub's peer table, no database (onlinePlayers looks names up).
	socialHubInstance.mu.RLock()
	online := socialHubInstance.peers[pid] != nil
	socialHubInstance.mu.RUnlock()

	// --- identity, balances ---------------------------------------------
	var name, created, updated, loginDate string
	var rank, streak int
	var credits, premium, freeXP, xp, rankXP int64
	if err := database.QueryRow(`SELECT COALESCE(display_name,''), created_at, updated_at, current_rank, current_xp, rank_xp,
		soft_currency, premium_currency, free_xp, login_streak, last_login_date FROM player_state WHERE user_id=?`, pid).
		Scan(&name, &created, &updated, &rank, &xp, &rankXP, &credits, &premium, &freeXP, &streak, &loginDate); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if name == "Local" {
		name = ""
	}
	out["identity"] = map[string]any{"pid": pid, "name": name, "created": created, "last_active": updated,
		"last_login_date": loginDate, "login_streak": streak, "online": online, "rank": rank, "xp": xp, "rank_xp": rankXP,
		"tune_test": tuneTestMode(pid)}
	out["balances"] = map[string]any{"credits": credits, "premium": premium, "free_xp": freeXP}
	out["ban"] = adminProfileRestriction(database, "admin_bans", pid)
	out["mute"] = adminProfileRestriction(database, "admin_chat_mutes", pid)

	// --- membership, faction reputation (read-only) -----------------------
	var expires int64
	_ = database.QueryRow(`SELECT expires_at FROM player_membership WHERE user_id=?`, pid).Scan(&expires)
	out["membership"] = map[string]any{"expires_at": expires, "active": expires > time.Now().Unix()}
	type faction struct {
		ID   int32  `json:"id"`
		Name string `json:"name"`
		Rep  int64  `json:"reputation"`
	}
	factions := []faction{}
	adminProfileRows(database, `SELECT faction_id, reputation FROM player_faction_reputation WHERE user_id=? ORDER BY faction_id LIMIT 50`, []any{pid}, func(rs *sql.Rows) {
		var f faction
		if rs.Scan(&f.ID, &f.Rep) == nil {
			f.Name = knownFactionNames[f.ID]
			factions = append(factions, f)
		}
	})
	out["factions"] = factions

	// --- purchases (newest 200) --------------------------------------------
	type purchase struct {
		adminProfileItem
		Type     string `json:"type"`
		Price    int64  `json:"price"`
		Currency string `json:"currency"`
		At       string `json:"at"`
	}
	purchases := []purchase{}
	adminProfileRows(database, `SELECT item_id, item_type, price_paid, currency, purchased_at FROM player_purchases
		WHERE user_id=? ORDER BY purchased_at DESC, rowid DESC LIMIT 200`, []any{pid}, func(rs *sql.Rows) {
		var p purchase
		if rs.Scan(&p.ID, &p.Type, &p.Price, &p.Currency, &p.At) == nil {
			purchases = append(purchases, p)
		}
	})
	for i := range purchases {
		purchases[i].Name, purchases[i].Kind = adminItemName(purchases[i].ID), adminItemKind(purchases[i].ID)
	}
	var purchaseTotal int
	_ = database.QueryRow(`SELECT COUNT(*) FROM player_purchases WHERE user_id=?`, pid).Scan(&purchaseTotal)
	out["purchases"] = purchases
	out["purchases_total"] = purchaseTotal

	// --- ships (loadout rows), ship XP -------------------------------------
	type ship struct {
		LoadoutID int32               `json:"loadout_id"`
		Precast   int32               `json:"precast_id"`
		ShipID    int32               `json:"ship_id"`
		Name      string              `json:"name"`
		Tier      int32               `json:"tier"`
		Hero      bool                `json:"hero"`
		Active    bool                `json:"active"`
		Index     int                 `json:"index"`
		Equip     []adminProfileEquip `json:"equipment"`
	}
	ships := []ship{}
	slotNames := []string{"weapon 1", "weapon 2", "ability 1", "ability 2", "ability 3", "ability 4", "perk 1", "perk 2", "perk 3", "perk 4"}
	adminProfileRows(database, `SELECT loadout_id, precast_loadout_id, ship_id, loadout_name, active, loadout_index,
		weapon_primary_id, weapon_secondary_id, ability_primary_id, ability_secondary_id, ability_perimeter_id, ability_internal_id,
		perk_com_id, perk_weapon_id, perk_navigation_id, perk_engineer_id
		FROM player_ship_loadouts WHERE user_id=? ORDER BY precast_loadout_id, loadout_index LIMIT 300`, []any{pid}, func(rs *sql.Rows) {
		var s ship
		var eq [10]int32
		var active int
		if rs.Scan(&s.LoadoutID, &s.Precast, &s.ShipID, &s.Name, &active, &s.Index, &eq[0], &eq[1], &eq[2], &eq[3], &eq[4], &eq[5], &eq[6], &eq[7], &eq[8], &eq[9]) != nil {
			return
		}
		s.Active = active != 0
		s.Hero = (s.Precast>>24)&0xff == mmogItemCategoryShipLoadoutHero
		s.Equip = []adminProfileEquip{}
		for i, id := range eq {
			if id > 0 {
				s.Equip = append(s.Equip, adminProfileEquip{Slot: slotNames[i], ID: id})
			}
		}
		ships = append(ships, s)
	})
	pawnName := map[int32]string{}
	for i := range ships {
		ships[i].Tier, _ = shipTierForIDChecked(ships[i].Precast)
		if _, seen := pawnName[ships[i].ShipID]; !seen {
			pawnName[ships[i].ShipID] = ships[i].Name
		}
		for j := range ships[i].Equip {
			ships[i].Equip[j].Name = adminItemName(ships[i].Equip[j].ID)
		}
	}
	out["ships"] = ships
	// Hero ships the player does not own yet: what "grant ship" offers.
	type hero struct {
		ID    int32  `json:"id"`
		Name  string `json:"name"`
		Tier  int32  `json:"tier"`
		Owned bool   `json:"owned"`
	}
	ownedPrecast := map[int32]bool{}
	for _, s := range ships {
		ownedPrecast[s.Precast] = true
	}
	heroes := []hero{}
	for _, h := range heroShipLoadouts {
		heroes = append(heroes, hero{ID: h.loadoutID, Name: h.name, Tier: h.tier, Owned: ownedPrecast[h.loadoutID]})
	}
	sort.Slice(heroes, func(i, j int) bool {
		if heroes[i].Tier != heroes[j].Tier {
			return heroes[i].Tier < heroes[j].Tier
		}
		return heroes[i].Name < heroes[j].Name
	})
	out["heroes"] = heroes

	type shipXP struct {
		ShipID int32   `json:"ship_id"`
		Name   string  `json:"name"`
		XP     int64   `json:"xp"`
		Hulls  []int32 `json:"hull_ids"` // the client's keys for this stored pawn id
		At     string  `json:"updated"`
	}
	shipXPs := []shipXP{}
	adminProfileRows(database, `SELECT ship_id, xp, updated_at FROM player_ship_xp WHERE user_id=? ORDER BY xp DESC, ship_id LIMIT 300`, []any{pid}, func(rs *sql.Rows) {
		var x shipXP
		if rs.Scan(&x.ShipID, &x.XP, &x.At) == nil {
			shipXPs = append(shipXPs, x)
		}
	})
	for i := range shipXPs {
		x := &shipXPs[i]
		x.Hulls = hullLoadoutsForPawn(x.ShipID)
		if x.Name = pawnName[x.ShipID]; x.Name == "" {
			if n, ok := authoritativeShipName(x.ShipID); ok {
				x.Name = n
			} else {
				x.Name = "ship " + strconv.Itoa(int(x.ShipID))
			}
		}
	}
	out["ship_xp"] = shipXPs

	// --- fleets ------------------------------------------------------------
	type fleetShip struct {
		Position int    `json:"position"`
		Loadout  int32  `json:"loadout_id"`
		Name     string `json:"name"`
	}
	type fleet struct {
		ID        int         `json:"id"`
		Name      string      `json:"name"`
		Type      string      `json:"type"`
		Active    bool        `json:"active"`
		Flagship  int32       `json:"flagship_loadout_id"`
		FlagName  string      `json:"flagship_name"`
		Ships     []fleetShip `json:"ships"`
		UpdatedAt string      `json:"updated"`
	}
	fleets := []fleet{}
	fleetAt := map[int]int{}
	adminProfileRows(database, `SELECT fleet_id, display_name, fleet_type, active, flagship_loadout_id, updated_at
		FROM player_fleets WHERE user_id=? ORDER BY fleet_id LIMIT 50`, []any{pid}, func(rs *sql.Rows) {
		var f fleet
		var typ, active int32
		if rs.Scan(&f.ID, &f.Name, &typ, &active, &f.Flagship, &f.UpdatedAt) == nil {
			f.Type, f.Active, f.Ships = fleetTypeName(typ), active != 0, []fleetShip{}
			fleetAt[f.ID] = len(fleets)
			fleets = append(fleets, f)
		}
	})
	adminProfileRows(database, `SELECT fl.fleet_id, fl.position, fl.loadout_id, COALESCE(l.loadout_name,'')
		FROM player_fleet_loadouts fl LEFT JOIN player_ship_loadouts l ON l.user_id=fl.user_id AND l.loadout_id=fl.loadout_id
		WHERE fl.user_id=? ORDER BY fl.fleet_id, fl.position LIMIT 500`, []any{pid}, func(rs *sql.Rows) {
		var id int
		var s fleetShip
		if rs.Scan(&id, &s.Position, &s.Loadout, &s.Name) == nil {
			if at, ok := fleetAt[id]; ok {
				fleets[at].Ships = append(fleets[at].Ships, s)
			}
		}
	})
	for i := range fleets {
		for _, s := range fleets[i].Ships {
			if s.Loadout == fleets[i].Flagship {
				fleets[i].FlagName = s.Name
			}
		}
	}
	out["fleets"] = fleets

	// --- career claims, ribbons, stats counters ---------------------------------
	type claim struct {
		Goal   string `json:"goal"`
		Stages int    `json:"stages"`
		At     string `json:"updated"`
	}
	claims := []claim{}
	adminProfileRows(database, `SELECT goal_id, claimed_stages, updated_at FROM player_career_claims WHERE user_id=? ORDER BY goal_id LIMIT 200`, []any{pid}, func(rs *sql.Rows) {
		var c claim
		if rs.Scan(&c.Goal, &c.Stages, &c.At) == nil {
			claims = append(claims, c)
		}
	})
	out["career_claims"] = claims
	type counter struct {
		Name  string `json:"name"`
		Value int64  `json:"value"`
	}
	ribbons := []counter{}
	adminProfileRows(database, `SELECT ribbon_type, count FROM player_ribbons WHERE user_id=? ORDER BY count DESC, ribbon_type LIMIT 100`, []any{pid}, func(rs *sql.Rows) {
		var c counter
		if rs.Scan(&c.Name, &c.Value) == nil {
			ribbons = append(ribbons, c)
		}
	})
	out["ribbons"] = ribbons
	counters := []counter{}
	adminProfileRows(database, `SELECT counter_id, counter_sub_id, value FROM player_stats_counters WHERE user_id=? ORDER BY counter_id, counter_sub_id LIMIT 200`, []any{pid}, func(rs *sql.Rows) {
		var c counter
		var sub string
		if rs.Scan(&c.Name, &sub, &c.Value) == nil {
			if sub != "" {
				c.Name += " / " + sub
			}
			counters = append(counters, c)
		}
	})
	out["counters"] = counters
	var results struct {
		Matches, Wins, Kills, Deaths, Assists int
		Damage                                int64
		Last                                  string
	}
	_ = database.QueryRow(`SELECT COUNT(*), COALESCE(SUM(outcome='win'),0), COALESCE(SUM(kills),0), COALESCE(SUM(deaths),0),
		COALESCE(SUM(assists),0), COALESCE(SUM(damage),0), COALESCE(MAX(created_at),'') FROM battle_results WHERE user_id=?`, pid).
		Scan(&results.Matches, &results.Wins, &results.Kills, &results.Deaths, &results.Assists, &results.Damage, &results.Last)
	out["results"] = map[string]any{"matches": results.Matches, "wins": results.Wins, "kills": results.Kills, "deaths": results.Deaths,
		"assists": results.Assists, "damage": results.Damage, "last_match": results.Last}

	// --- contracts ---------------------------------------------------------
	type contract struct {
		Entry    int64  `json:"entry"`
		Slot     int    `json:"slot"`
		Elite    bool   `json:"elite"`
		Quest    string `json:"quest"`
		Target   int64  `json:"target"`
		Progress int64  `json:"progress"`
		Reward   int64  `json:"reward"`
		GP       int64  `json:"gp_paid"`
		State    string `json:"state"`
		Paid     bool   `json:"paid"`
		Assigned int64  `json:"assigned_at"` // Unix seconds
		Done     int64  `json:"completed_at"`
	}
	contracts := []contract{}
	adminProfileRows(database, `SELECT entry_id, slot, quest_id, target, progress, reward, gp_paid, state, paid, assigned_at, completed_at
		FROM player_daily_contracts WHERE user_id=? ORDER BY entry_id DESC LIMIT 100`, []any{pid}, func(rs *sql.Rows) {
		var c contract
		var paid int
		if rs.Scan(&c.Entry, &c.Slot, &c.Quest, &c.Target, &c.Progress, &c.Reward, &c.GP, &c.State, &paid, &c.Assigned, &c.Done) == nil {
			c.Paid, c.Elite = paid != 0, c.Slot >= mpQuestNumBaseContractSlots
			contracts = append(contracts, c)
		}
	})
	out["contracts"] = contracts
	var stateID, lastAssign, lastReplace int64
	_ = database.QueryRow(`SELECT state_id, last_assignment, last_replace FROM player_contract_state WHERE user_id=?`, pid).Scan(&stateID, &lastAssign, &lastReplace)
	out["contract_state"] = map[string]any{"state_id": stateID, "last_assignment": lastAssign, "last_replace": lastReplace}
	type legacy struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Progress int    `json:"progress"`
		At       string `json:"updated"`
	}
	legacyRows := []legacy{}
	adminProfileRows(database, `SELECT contract_id, state, progress, updated_at FROM player_contracts WHERE user_id=? ORDER BY updated_at DESC LIMIT 50`, []any{pid}, func(rs *sql.Rows) {
		var c legacy
		if rs.Scan(&c.ID, &c.State, &c.Progress, &c.At) == nil {
			legacyRows = append(legacyRows, c)
		}
	})
	out["contracts_legacy"] = legacyRows

	// --- recent matches (battle_results.match_id = matches.battle_match_id) ---
	type match struct {
		MatchID string `json:"match_id"`
		At      string `json:"at"`
		Mode    string `json:"mode"`
		Map     string `json:"map"`
		Fleet   string `json:"fleet"`
		Outcome string `json:"outcome"`
		Kills   int    `json:"kills"`
		Deaths  int    `json:"deaths"`
		Assists int    `json:"assists"`
		Damage  int64  `json:"damage"`
		Credits int64  `json:"credits"`
		XP      int64  `json:"xp"`
	}
	matches := []match{}
	adminProfileRows(database, `SELECT b.match_id, b.created_at, COALESCE(m.game_mode,''), COALESCE(m.map,''), b.fleet_type, b.outcome,
		b.kills, b.deaths, b.assists, b.damage, b.credits, b.xp
		FROM battle_results b LEFT JOIN matches m ON m.battle_match_id=b.match_id AND b.match_id<>''
		WHERE b.user_id=? ORDER BY b.created_at DESC LIMIT 50`, []any{pid}, func(rs *sql.Rows) {
		var x match
		var fleet int32
		if rs.Scan(&x.MatchID, &x.At, &x.Mode, &x.Map, &fleet, &x.Outcome, &x.Kills, &x.Deaths, &x.Assists, &x.Damage, &x.Credits, &x.XP) == nil {
			x.Map, x.Fleet = adminMapName(x.Map), fleetTypeName(fleet)
			matches = append(matches, x)
		}
	})
	out["matches"] = matches

	// --- friends, ignores (names joined) -----------------------------------
	type person struct {
		PID     string `json:"pid"`
		Name    string `json:"name"`
		State   string `json:"state,omitempty"`
		Sent    bool   `json:"requested_by_player,omitempty"`
		Created string `json:"created"`
	}
	friends := []person{}
	adminProfileRows(database, `SELECT f.other, COALESCE(ps.display_name,''), f.state, f.requester_id=?, f.created_at FROM
		(SELECT CASE WHEN pid_a=? THEN pid_b ELSE pid_a END AS other, state, requester_id, created_at FROM player_friends WHERE pid_a=? OR pid_b=?) f
		LEFT JOIN player_state ps ON ps.user_id=f.other ORDER BY f.state, f.created_at DESC LIMIT 200`, []any{pid, pid, pid, pid}, func(rs *sql.Rows) {
		var p person
		if rs.Scan(&p.PID, &p.Name, &p.State, &p.Sent, &p.Created) == nil {
			friends = append(friends, p)
		}
	})
	out["friends"] = friends
	ignores := []person{}
	adminProfileRows(database, `SELECT i.ignored_id, COALESCE(ps.display_name,''), i.created_at FROM player_ignores i
		LEFT JOIN player_state ps ON ps.user_id=i.ignored_id WHERE i.pid=? ORDER BY i.created_at DESC LIMIT 200`, []any{pid}, func(rs *sql.Rows) {
		var p person
		if rs.Scan(&p.PID, &p.Name, &p.Created) == nil {
			ignores = append(ignores, p)
		}
	})
	out["ignores"] = ignores
	var ignoredBy int
	_ = database.QueryRow(`SELECT COUNT(*) FROM player_ignores WHERE ignored_id=?`, pid).Scan(&ignoredBy)
	out["ignored_by"] = ignoredBy

	// --- chat (newest 100, long lines cut) ------------------------------------
	type line struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
		At      string `json:"at"`
	}
	chat := []line{}
	adminProfileRows(database, `SELECT channel, substr(content,1,500), sent_at FROM chat_messages WHERE sender_id=? ORDER BY sent_at DESC LIMIT 100`, []any{pid}, func(rs *sql.Rows) {
		var l line
		if rs.Scan(&l.Channel, &l.Text, &l.At) == nil {
			chat = append(chat, l)
		}
	})
	out["chat"] = chat

	// --- reports, telemetry counts -------------------------------------------
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	var reports, reports7, telemetry, telemetry7 int
	var lastReport, lastTelemetry string
	_ = database.QueryRow(`SELECT COUNT(*), COALESCE(SUM(created_at>?),0), COALESCE(MAX(created_at),'') FROM client_reports WHERE user_id=?`, since, pid).Scan(&reports, &reports7, &lastReport)
	_ = database.QueryRow(`SELECT COUNT(*), COALESCE(SUM(created_at>?),0), COALESCE(MAX(created_at),'') FROM client_telemetry WHERE user_id=?`, since, pid).Scan(&telemetry, &telemetry7, &lastTelemetry)
	out["reports"] = map[string]any{"total": reports, "last_7d": reports7, "last": lastReport}
	out["telemetry"] = map[string]any{"total": telemetry, "last_7d": telemetry7, "last": lastTelemetry}

	// --- notes ---------------------------------------------------------------
	out["notes"] = adminPlayerNotes(database, pid)

	// --- owned items (starter + purchases) and the audit trail ---------------
	// Both look more up in the store (and the audit rows resolve names), so
	// they come last, when no result set is open.
	owned := []adminProfileItem{}
	seen := map[int32]bool{}
	for _, item := range starterOwnedInventorySeeds() {
		if item.itemID != 0 && !seen[item.itemID] {
			seen[item.itemID] = true
			owned = append(owned, adminProfileItem{ID: item.itemID, Source: "starter"})
		}
	}
	for _, id := range purchasedInventoryItemIDs(pid) {
		if !seen[id] {
			seen[id] = true
			owned = append(owned, adminProfileItem{ID: id, Source: "purchase"})
		}
	}
	ownedTotal := len(owned)
	if len(owned) > 600 {
		owned = owned[:600]
	}
	for i := range owned {
		owned[i].Name, owned[i].Kind = adminItemName(owned[i].ID), adminItemKind(owned[i].ID)
	}
	sort.SliceStable(owned, func(i, j int) bool { return owned[i].Kind < owned[j].Kind })
	out["owned"] = owned
	out["owned_total"] = ownedTotal
	audit, err := adminAuditRows(database, `WHERE target=?`, pid)
	if err != nil {
		audit = []adminAuditRow{}
	}
	out["audit"] = audit
	writeAdminJSON(w, out)
}
