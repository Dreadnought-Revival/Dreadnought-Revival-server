package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The game's loadout record is positional (0x34E550 copies 2 weapon, 4 ability
// and 4 perk slots verbatim), so empty slots must stay in place as 0.
func TestBattleLoadoutKeepsEmptySlotsInPlace(t *testing.T) {
	loadout := mmogShipLoadoutSeed{
		precastLoadoutID:  33489267,
		loadoutName:       "Dover",
		weaponPrimaryID:   84017210,
		weaponSecondaryID: 84017966,
		abilityIDs:        [4]int32{67240123, 0, 83820699, 83820692},
	}
	out := formatBattleLoadout("Default__VH_SniperLight_T2_PrecastLoadout_BP_C", "p", loadout)
	for _, want := range []string{
		"id=Default__VH_SniperLight_T2_PrecastLoadout_BP_C\n",
		"precast=33489267\n",
		"name=Dover\n",
		"abilities=67240123,0,83820699,83820692\n",
		"perks=0,0,0,0\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestBattleLoadoutRefusesNonLoopback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/battle/loadout?pid=a&id=b", nil)
	req.RemoteAddr = "10.0.0.26:5000"
	rec := httptest.NewRecorder()
	battleLoadoutHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 for a non-loopback caller", rec.Code)
	}
}

func TestBattlePlayerNameLookup(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "0123456789abcdef0123456789abcdef"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE player_state SET display_name='Tester' WHERE user_id=?`, pid); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/battle/player?pid="+pid, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	battlePlayerHandler(rec, req)
	// captain=: the host copies it into the player state's
	// m_captainDisplayInformation (otherwise every captain draws as default).
	if want := "name=Tester\ncaptain=" + defaultCaptainDisplayInfo + "\n"; rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/battle/player?pid="+pid, nil)
	req.RemoteAddr = "10.0.0.5:5000"
	rec = httptest.NewRecorder()
	battlePlayerHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback: %d", rec.Code)
	}
}

// A player who picked no ship before the drop gets the flagship of the
// match's fleet, as the game's own fallback (0x370970) would with real fleet
// data: /battle/loadout?flagship=1&match=<host -MatchID>.
func TestBattleLoadoutFlagship(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "0123456789abcdef0123456789abcdef"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO matches(id,game_mode,map,battle_match_id,fleet_type,status) VALUES('m1','TDM','x','bm-recruit',1,'active'),('m2','TDM','x','bm-legend',3,'active')`); err != nil {
		t.Fatal(err)
	}
	var fleet mmogFleetSeed
	for _, f := range mmogPlayerStateForPID(pid).fleets {
		if f.fleetType == 1 {
			fleet = f
		}
	}
	if len(fleet.shipLoadouts) == 0 {
		t.Fatal("the seeded player has no Recruit fleet")
	}
	want := fleet.shipLoadouts[fleet.flagshipIndex()]

	get := func(query string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/battle/loadout?"+query, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		battleLoadoutHandler(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := get("pid=" + pid + "&flagship=1&match=bm-recruit")
	if code != http.StatusOK || !strings.Contains(body, "id="+want.entryID()+"\n") ||
		!strings.Contains(body, "precast="+strconv.Itoa(int(want.precastLoadoutID))+"\n") {
		t.Fatalf("flagship: %d %q, want id=%s", code, body, want.entryID())
	}
	// The id it names is one the normal lookup resolves too.
	if code, _ := get("pid=" + pid + "&id=" + want.entryID()); code != http.StatusOK {
		t.Errorf("the flagship id does not resolve by id: %d", code)
	}
	if code, _ := get("pid=" + pid + "&flagship=1&match=nope"); code != http.StatusNotFound {
		t.Errorf("unknown match: %d, want 404", code)
	}
	if code, _ := get("pid=" + pid + "&flagship=1&match=bm-legend"); code != http.StatusNotFound {
		t.Errorf("no Legendary ships: %d, want 404", code)
	}
}
