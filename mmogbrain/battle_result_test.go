package main

import (
	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBattleOutcome(t *testing.T) {
	for _, c := range []struct {
		team, final int
		want        string
	}{
		{1, 1, "win"}, {2, 2, "win"}, {1, 2, "loss"}, {2, 1, "loss"},
		{1, 3, "draw"}, {1, 0, "unknown"}, {0, 1, "unknown"}, {0, 3, "draw"},
	} {
		if got := battleOutcome(c.team, c.final); got != c.want {
			t.Errorf("battleOutcome(%d,%d)=%q want %q", c.team, c.final, got, c.want)
		}
	}
}

// An empty or malformed pid must not fall back to the default dev player.
func TestBattleResultRejectsMissingPID(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	for _, pid := range []string{"", "not-a-pid"} {
		req := httptest.NewRequest(http.MethodGet, "/battle/result?match=m&final=1&team=1&pid="+pid, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		battleResultHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("pid %q: status %d, want 400", pid, rec.Code)
		}
	}
}

func TestBattleResultRefusesNonLoopback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/battle/result?match=m&pid=a", nil)
	req.RemoteAddr = "10.0.0.26:5000"
	rec := httptest.NewRecorder()
	battleResultHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 for a non-loopback caller", rec.Code)
	}
}

// A win with 3 kills pays 1500+300 credits and 1000+150 XP (the placeholder
// values) to the wallet, free XP, rank XP and the ship flown -- once: the mod
// may report twice, and the second report must grant nothing.
func TestBattleResultAwardsOnce(t *testing.T) {
	// The payout formula alone: a daily contract completed by this match
	// would add its own credits (daily_contracts_test.go covers those).
	t.Setenv("DN_DAILY_CONTRACTS", "0")
	database := useTempMmogPlayerStateDB(t)
	const pid = "0123456789abcdef0123456789abcdef"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	loadouts := ownedShipLoadoutsForPlayerData(mmogPlayerStateForPID(pid), pid)
	if len(loadouts) == 0 {
		t.Fatal("no starter loadouts")
	}
	flown := loadouts[0]
	read := func() (credits, freeXP, currentXP, shipXP int64) {
		t.Helper()
		if err := database.QueryRow(`SELECT soft_currency, free_xp, current_xp FROM player_state WHERE user_id=?`, pid).
			Scan(&credits, &freeXP, &currentXP); err != nil {
			t.Fatal(err)
		}
		_ = database.QueryRow(`SELECT xp FROM player_ship_xp WHERE user_id=? AND ship_id=?`, pid, flown.ship.id).Scan(&shipXP)
		return
	}
	c0, f0, x0, s0 := read()

	url := "/battle/result?match=M1&pid=" + pid + "&team=2&final=2&kills=3&deaths=1&ships=" + flown.entryID()
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		battleResultHandler(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "outcome=win") {
			t.Fatalf("report %d: %d %q", i, rec.Code, rec.Body.String())
		}
		wantNew := "new=true"
		if i == 1 {
			wantNew = "new=false"
		}
		if !strings.Contains(rec.Body.String(), wantNew) {
			t.Fatalf("report %d: want %s in %q", i, wantNew, rec.Body.String())
		}
	}
	c1, f1, x1, s1 := read()
	// Win + 3 kills, Recruit (match unknown), the player's first win of the
	// day, no Elite (boosters.go, CHANGED 2026-11-14):
	//   credits: (1500 + 3*100) x 1.6 = 2880 scoring, + Win 25% 720 + First
	//            Win 100% 2880 + Battle Ready Recruit 50% 1440 = 7920
	//   ship XP: (1000 + 3*50) x 1.4 = 1610, + 403 + 1610 + Recruit 100%
	//            1610 = 5233 (also the rank XP: no Elite lines)
	//   free XP: 50% of the XP base, 805, + Win 201 + First Win 805 (Battle
	//            Ready Recruit has no free XP effect) = 1811
	if c1-c0 != 7920 || f1-f0 != 1811 || x1-x0 != 5233 || s1-s0 != 5233 {
		t.Fatalf("deltas credits=%d freeXP=%d rankXP=%d shipXP=%d; want 7920/1811/5233/5233", c1-c0, f1-f0, x1-x0, s1-s0)
	}
	for counter, want := range map[string]int32{"MatchesPlayed": 1, "MatchesWon": 1, "ShipsDestroyed": 3} {
		if got := battleResultCounter(pid, counter); got != want {
			t.Errorf("%s = %d, want %d", counter, got, want)
		}
	}
}

func testBattleRewards() battleRewards {
	return battleRewards{winCredits: 1500, lossCredits: 750, killCredits: 100, winXP: 1000, lossXP: 500, killXP: 50,
		xpScale: 1, creditScale: 1, freeXPPct: 50}
}

// Each booster is base x (multiplier - 1) on top of the Scoring line, with
// the game's own multipliers (boosters.go). Scale 1 here to read the numbers.
func TestBattleRewardFormula(t *testing.T) {
	r := testBattleRewards()
	for _, c := range []struct {
		name        string
		outcome     string
		kills       int32
		fleet       int
		boosts      matchBoosts
		credits, xp int32
	}{
		// Recruit: credits +50%, ship XP +100%.
		{"recruit loss", "loss", 0, 1, matchBoosts{}, 1125, 1000},
		{"unknown match pays recruit", "loss", 0, 0, matchBoosts{}, 1125, 1000},
		// Veteran win, 2 kills: 1700 credits x (1 + .25 win + .75), 1100 XP x (1 + .25 + 1.25).
		{"veteran win 2 kills", "win", 2, 2, matchBoosts{}, 3400, 2750},
		// Legendary: credits +150%, ship XP +250%.
		{"legendary win 2 kills", "win", 2, 3, matchBoosts{}, 4675, 4125},
		// First win of the day doubles the base once more.
		{"recruit first win", "win", 0, 1, matchBoosts{firstWin: true}, 4125, 3250},
		// A first win only counts when won.
		{"first win flag on a loss", "loss", 0, 1, matchBoosts{firstWin: true}, 1125, 1000},
		// Elite: credits and ship XP +100%.
		{"recruit win elite", "win", 0, 1, matchBoosts{elite: true}, 4125, 3250},
		// Two elite allies: +10% each.
		{"recruit win 2 elite allies", "win", 0, 1, matchBoosts{eliteAllies: 2}, 2925, 2450},
	} {
		if cr, xp := r.withBoosts(c.boosts).forOutcome(c.outcome, c.kills, c.fleet); cr != c.credits || xp != c.xp {
			t.Errorf("%s: %d credits %d xp, want %d / %d", c.name, cr, xp, c.credits, c.xp)
		}
	}
}

// Free XP: its own base (freeXPPct of the XP base) and its own percentages --
// Elite +50%, allies +5%, Win +25%, First Win +100%, Veteran/Legendary +30%.
func TestFreeXPBoosters(t *testing.T) {
	r := testBattleRewards()
	// Win, Veteran, elite, 1 elite ally: base 500 x (1 + .25 + .30 + .50 + .05).
	p := r.withBoosts(matchBoosts{elite: true, eliteAllies: 1}).poolsFor("win", 0, 2)
	if got := p.freeXP.total(); got != 1050 {
		t.Errorf("free XP %d, want 1050: %v", got, p.freeXP)
	}
	// Recruit has no free XP booster.
	if p := r.poolsFor("loss", 0, 1); p.freeXP.total() != 250 {
		t.Errorf("recruit loss free XP %d, want 250", p.freeXP.total())
	}
}

// Elite Status adds nothing to rank XP: the asset has no reputation effect.
func TestEliteLinesAreNotRankXP(t *testing.T) {
	r := testBattleRewards()
	plain := r.poolsFor("win", 0, 1).shipXP
	elite := r.withBoosts(matchBoosts{elite: true, eliteAllies: 3}).poolsFor("win", 0, 1).shipXP
	if rankXP(elite) != rankXP(plain) || elite.total() <= plain.total() {
		t.Errorf("rank XP elite %d plain %d; ship XP elite %d plain %d", rankXP(elite), rankXP(plain), elite.total(), plain.total())
	}
}

func TestMatchFleetTypeFromBattleMatchID(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	if _, err := database.Exec(`INSERT INTO matches(id,game_mode,map,battle_match_id,fleet_type) VALUES('m1','TDM','x','dn-1',3)`); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"dn-1": 3, "dn-1-r2": 3, "dn-2": 0, "": 0} {
		if got := matchFleetType(database, id); got != want {
			t.Errorf("matchFleetType(%q) = %d, want %d", id, got, want)
		}
	}
}

// The end-of-match screen shows the payout in pools: XP in EYXPPoolType
// order, credits in EYCreditsPoolType order; each adds up (as the client
// adds it) to exactly what is paid, and each booster lands in its own line.
func TestRewardPoolsAddUpToThePayout(t *testing.T) {
	r := testBattleRewards().withBoosts(matchBoosts{elite: true, eliteAllies: 1, firstWin: true})
	for _, fleet := range []int{1, 2, 3} {
		for _, kills := range []int32{0, 3, 7} {
			p := r.poolsFor("win", kills, fleet)
			wantC, wantX := r.forOutcome("win", kills, fleet)
			if p.creditTotal() != wantC || p.shipXP.total() != wantX {
				t.Errorf("fleet %d kills %d: pools total %d/%d, paid %d/%d", fleet, kills, p.creditTotal(), p.shipXP.total(), wantC, wantX)
			}
			x := p.shipXP
			if x[0] != x[1]+x[2] || x[1] != 1000 || x[2] != 50*kills {
				t.Errorf("fleet %d kills %d: scoring %d = base %d + performance %d?", fleet, kills, x[0], x[1], x[2])
			}
			c := p.credits
			if c[0] != 0 || c[4] != 0 || c[1] != c[2]+c[3] || c[2] != 1500 {
				t.Errorf("fleet %d kills %d: credits not in EYCreditsPoolType order: %v", fleet, kills, c)
			}
			if x[6+fleet] == 0 || c[8+fleet] == 0 {
				t.Errorf("fleet %d: Battle Ready not in its pool: xp %v credits %v", fleet, x, c)
			}
		}
	}
	// Win + 3 kills, Recruit, elite, one elite ally, first win: credits 1800
	// scoring, Win 450, First Win 1800, Elite 1800, ally 180, Recruit 900.
	c := r.poolsFor("win", 3, 1).credits
	if c[1] != 1800 || c[5] != 450 || c[6] != 1800 || c[7] != 1800 || c[8] != 180 || c[9] != 900 {
		t.Errorf("credit pools %v", c)
	}
	// The client draws the Elite credits line itself as (RankUp + Scoring) x
	// (2.0 - 1): it must equal what we pay in it.
	if c[7] != c[0]+c[1] {
		t.Errorf("Elite credits %d, client shows %d", c[7], c[0]+c[1])
	}
}

// The boosters the mod sends as ClientSetActiveBoosterIDs.
func TestActiveBoosterIDs(t *testing.T) {
	r := testBattleRewards()
	if got := joinInt32s(r.activeBoosterIDs("loss", 1)); got != "520028169" {
		t.Errorf("recruit loss: %s", got)
	}
	got := joinInt32s(r.withBoosts(matchBoosts{elite: true, firstWin: true}).activeBoosterIDs("win", 3))
	if got != "520028165,520028166,536805377,520028167" {
		t.Errorf("legendary first win, elite: %s", got)
	}
}

// The rewards screen looks each fleet ship up by its fleet ship id; pawn ids
// made the client file ShipXpError for every ship (2026-09-28).
func TestBattleFleetShipIDsAreFleetShipIDs(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "0123456789abcdef0123456789abcdef"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	state := mmogPlayerStateForPID(pid)
	fleet := state.activeFleet()
	ids := battleFleetShipIDs(pid, int(fleet.fleetType))
	if len(ids) == 0 {
		t.Fatal("no fleet ship ids")
	}
	pawn := map[int32]bool{}
	for _, l := range fleet.shipLoadouts {
		if l.ship.id != fleetShipKey(l) {
			pawn[l.ship.id] = true
		}
	}
	for _, id := range ids {
		if pawn[id] {
			t.Errorf("id %d is a pawn id, not a fleet ship id", id)
		}
	}
}

// Every ship of the fleet earns ship XP, not only the ones flown ("all ship
// need to get xp if u take them in the fleet ... ther is like 10% of the total
// xp earned is for the other ships that were not played", operator
// 2026-09-30; the game: "every ship in that fleet will earn Ship XP"). The
// performance reward goes only to the ships played.
func TestUnplayedFleetShipsEarnAShare(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "0123456789abcdef0123456789abcdef"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	fleet := battleFleetLoadouts(pid, 0)
	if len(fleet) < 2 {
		t.Fatalf("the starter fleet has %d ships; need at least 2", len(fleet))
	}
	flown, benched := fleet[0], fleet[1]
	shipXP := func(id int32) (xp int64) {
		_ = database.QueryRow(`SELECT xp FROM player_ship_xp WHERE user_id=? AND ship_id=?`, pid, id).Scan(&xp)
		return
	}
	req := httptest.NewRequest(http.MethodGet, "/battle/result?match=M2&pid="+pid+"&team=2&final=2&kills=3&ships="+flown.entryID(), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	battleResultHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	// Flown: the whole match XP, 5233 (see TestBattleResultAwardsOnce).
	// Not flown: 10% of the XP WITHOUT the 3 kills: base 1000 x 1.4 = 1400,
	// + Win 350 + First Win 1400 + Recruit 1400 = 4550 -> 455.
	if got := shipXP(flown.ship.id); got != 5233 {
		t.Errorf("flown ship earned %d, want 5233", got)
	}
	if got := shipXP(benched.ship.id); got != 455 {
		t.Errorf("unflown fleet ship earned %d, want 455 (10%% of the XP without performance)", got)
	}
	// ...and the end-of-match screen gets the same amount for those ships.
	var line string
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(l, "unplayed_ship_xp_pools=") {
			line = strings.TrimPrefix(l, "unplayed_ship_xp_pools=")
		}
	}
	var p rewardPools
	for i, v := range strings.Split(line, ",") {
		if i < rewardPoolCount {
			n, _ := strconv.Atoi(v)
			p[i] = int32(n)
		}
	}
	if line == "" || p.total() != 455 {
		t.Errorf("unplayed_ship_xp_pools %q totals %d, want 455", line, p.total())
	}
}

// Free XP and ship XP reach the client only through YA_PlayerGet, so after a
// match they stayed stale until a relog (operator 2026-09-30). A fresh result
// queues a YA_ConvertShipXP push: result "bought", FreeXp the new total
// (assigned by the client), and each ship's gain as a NEGATIVE ShipXp (the
// client subtracts), keyed by the hull loadout id the client uses.
func TestBattleResultPushesTheNewXP(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "0123456789abcdef0123456789abcdef"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	squadHubInstance.connected(pid) // pushes go only to players online
	t.Cleanup(func() { squadHubInstance.disconnected(pid) })
	_ = squadHubInstance.drainPushes(pid)
	flown := battleFleetLoadouts(pid, 0)[0]
	req := httptest.NewRequest(http.MethodGet, "/battle/result?match=M3&pid="+pid+"&team=2&final=2&kills=3&ships="+flown.entryID(), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	battleResultHandler(httptest.NewRecorder(), req)

	var push []byte
	for _, p := range squadHubInstance.drainPushes(pid) {
		if protocol.FirstStringField(p, "RT") == "YA_ConvertShipXP" {
			push = p
		}
	}
	if push == nil {
		t.Fatal("no YA_ConvertShipXP push queued after the result")
	}
	var freeXP int32
	if err := database.QueryRow(`SELECT free_xp FROM player_state WHERE user_id=?`, pid).Scan(&freeXP); err != nil {
		t.Fatal(err)
	}
	if got := protocol.ExtractStringField(push, "result"); got != "bought" {
		t.Errorf("result %q, want bought (the handler's success value)", got)
	}
	if got := protocol.ExtractStringField(push, "FreeXp"); got != strconv.Itoa(int(freeXP)) {
		t.Errorf("FreeXp %q, want the new total %d", got, freeXP)
	}
	entry := string(protocol.AppendStringField(nil, "ShipID", strconv.Itoa(int(fleetShipKey(flown))))) +
		string(protocol.AppendStringField(nil, "ShipXp", "-5233"))
	if !strings.Contains(string(push), entry) {
		t.Errorf("no ShipXps entry {ShipID %d, ShipXp -5233} for the flown ship", fleetShipKey(flown))
	}
}

// With the battle server's original event XP the XP base is that sum plus the
// end-of-match awards; credits keep the placeholder base.
func TestEventXPReplacesTheXPBase(t *testing.T) {
	r := currentBattleRewards()
	plainCredits, _ := r.forOutcome("win", 5, 1)
	ev := r.withEventXP(1200)
	credits, xp := ev.forOutcome("win", 5, 1)
	if credits != plainCredits {
		t.Errorf("credits %d, want the formula's %d", credits, plainCredits)
	}
	// 1200 x 1.4 = 1680 scoring, + Win 25% 420 + Recruit 100% 1680.
	if want := int32(math.Round(1200*r.xpScale)) * 9 / 4; xp != want {
		t.Errorf("xp %d, want %d", xp, want)
	}
	if got := ev.unplayedShipPools("win", 1).total(); got <= 0 {
		t.Error("unflown ships earn nothing from event XP")
	}
}

// The Match Finish ribbon plus PlayTime every 30 s, by the match's mode.
func TestEndOfMatchEventXP(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	ready := time.Now().Add(-300 * time.Second).UTC().Format(time.RFC3339)
	if _, err := database.Exec(`INSERT INTO matches(id, game_mode, map, battle_match_id, server_ready_at) VALUES('m1','TDM','Gorge','b1',?), ('m2','Onslaught','Gorge','b2',?)`, ready, ready); err != nil {
		t.Fatal(err)
	}
	if xp, mode := endOfMatchEventXP(database, "b1"); mode != "TDM" || xp < 118 || xp > 122 {
		t.Errorf("TDM: %d (%s), want 100 ribbon + 2 x 10 play-time ticks = 120", xp, mode)
	}
	if xp, mode := endOfMatchEventXP(database, "b2"); mode != "IVN" || xp < 108 || xp > 111 {
		t.Errorf("Onslaught: %d (%s), want 100 + 1 x 10 = 110", xp, mode)
	}
}

// Elite, elite allies on the player's own team, and First Win of the Day.
func TestBattleBoosts(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const me, ally, eliteFoe, plainAlly = "0123456789abcdef0123456789abcde1", "0123456789abcdef0123456789abcde2",
		"0123456789abcdef0123456789abcde3", "0123456789abcdef0123456789abcde4"
	now := time.Now()
	for _, pid := range []string{me, ally, eliteFoe, plainAlly} {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`UPDATE player_state SET display_name='Ally, One' WHERE user_id=?`, ally); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []string{me, ally, eliteFoe} {
		if _, err := database.Exec(`INSERT INTO player_membership(user_id, expires_at) VALUES(?,?)`, pid, now.Unix()+3600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO matches(id,game_mode,map,battle_match_id,fleet_type) VALUES('m1','TDM','x','dn-1',1)`); err != nil {
		t.Fatal(err)
	}
	for pid, team := range map[string]int{me: 1, ally: 1, plainAlly: 1, eliteFoe: 2} {
		if _, err := database.Exec(`INSERT INTO match_slots(match_id,user_id,team) VALUES('m1',?,?)`, pid, team); err != nil {
			t.Fatal(err)
		}
	}
	b, names := battleBoosts(database, battleResult{match: "dn-1-r2", pid: me, team: 1, outcome: "win"}, now)
	if !b.elite || b.eliteAllies != 1 || !b.firstWin {
		t.Errorf("boosts %+v, want elite, 1 elite ally (not the elite enemy), first win", b)
	}
	if len(names) != 1 || names[0] != "Ally  One" {
		t.Errorf("elite share names %q", names)
	}
	// A win already today: no first win.
	if _, err := database.Exec(`INSERT INTO battle_results(match_id,user_id,team,outcome) VALUES('dn-0',?,1,'win')`, me); err != nil {
		t.Fatal(err)
	}
	if b, _ := battleBoosts(database, battleResult{match: "dn-1", pid: me, team: 1, outcome: "win"}, now); b.firstWin {
		t.Error("second win of the day counted as the first")
	}
	// An expired membership is not Elite.
	if b, _ := battleBoosts(database, battleResult{match: "dn-1", pid: plainAlly, team: 1, outcome: "loss"}, now); b.elite || b.eliteAllies != 2 {
		t.Errorf("plain ally: %+v, want no elite, 2 elite allies", b)
	}
}
