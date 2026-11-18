package main

import (
	"strings"
	"testing"
)

// The client exports ";;;;"-style appearances for slots it has nothing in, and
// the customisation preview (VH_CustomisationPreview_BP ReceiveTick) reads
// m_appearance.m_emblem / m_colorSet every tick: an empty group is an
// Accessed-None script call stack warning per frame. repairedShipDisplayInfo
// fills EMPTY emblem/paint/pattern/decal groups with the ship's default and
// nothing else.

const repairMesh = "335872027#335872028#335872026#335872025"

func repairGroups(t *testing.T, info string) []string {
	t.Helper()
	g := strings.Split(info, ";")
	if len(g) != 5 || (g[0] != "" && len(strings.Split(g[0], "#")) != 4) {
		t.Fatalf("%q is not a five-group display info", info)
	}
	return g
}

func TestRepairFillsEmptyGroupsOfASemicolonOnlyString(t *testing.T) {
	ship := starterShipLoadouts()[0]
	want := repairGroups(t, ship.repairDefaultDisplayInfo())
	got := repairGroups(t, repairedShipDisplayInfo(";;;;", ship.repairDefaultDisplayInfo))
	if got[0] != "" {
		t.Errorf("mesh group touched: %q", got[0])
	}
	for i := 1; i < 5; i++ {
		if got[i] != want[i] || got[i] == "" || got[i] == "-1" {
			t.Errorf("group %d = %q, want the ship default %q", i, got[i], want[i])
		}
	}
}

func TestRepairFillsAMeshOnlyStringAndKeepsTheMeshes(t *testing.T) {
	ship := starterShipLoadouts()[0]
	want := repairGroups(t, ship.repairDefaultDisplayInfo())
	ship.savedDisplayInfo = repairMesh + ";;;;"
	got := repairGroups(t, ship.displayInfo())
	if got[0] != repairMesh {
		t.Errorf("mesh group = %q, want the saved %q (the default's meshes are different)", got[0], repairMesh)
	}
	for i := 1; i < 5; i++ {
		if got[i] != want[i] {
			t.Errorf("group %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRepairLeavesACompleteStringAlone(t *testing.T) {
	ship := starterShipLoadouts()[0]
	const saved = repairMesh + ";352649226;369426573;386203669;402980885"
	ship.savedDisplayInfo = saved
	if got := ship.displayInfo(); got != saved {
		t.Errorf("displayInfo() = %q, want the player's own %q", got, saved)
	}
}

func TestRepairKeepsThePlayersChoicesInAPartialString(t *testing.T) {
	ship := starterShipLoadouts()[0]
	want := repairGroups(t, ship.repairDefaultDisplayInfo())
	// Player chose the paint (369426573) and the decal (402980885); emblem
	// and pattern are empty. -1 is also an empty slot.
	ship.savedDisplayInfo = repairMesh + ";;369426573;-1;402980885"
	got := repairGroups(t, ship.displayInfo())
	if got[0] != repairMesh || got[2] != "369426573" || got[4] != "402980885" {
		t.Errorf("player's choices changed: %q", got)
	}
	if got[1] != want[1] || got[3] != want[3] {
		t.Errorf("empty emblem/pattern = %q/%q, want %q/%q", got[1], got[3], want[1], want[3])
	}
}

func TestRepairIgnoresStringsThatAreNotFiveGroups(t *testing.T) {
	called := false
	computed := func() string { called = true; return "1#2#3#4;5;6;7;8" }
	for _, s := range []string{"keep;me;;;;", "a;b", "x"} {
		if got := repairedShipDisplayInfo(s, computed); got != s {
			t.Errorf("%q became %q", s, got)
		}
	}
	if called {
		t.Error("the default was computed for a string that is not repaired")
	}
	// The default is not computed when nothing is empty either.
	if got := repairedShipDisplayInfo("1#2#3#4;5;6;7;8", computed); got != "1#2#3#4;5;6;7;8" || called {
		t.Errorf("complete string touched or default computed: %q", got)
	}
}

// A ship with no default for a group (unknown hull line) keeps the saved,
// empty group rather than gaining "-1", which the client would try to load.
func TestRepairNeverWritesAnUnsetDefault(t *testing.T) {
	got := repairedShipDisplayInfo(repairMesh+";;;;", func() string { return noShipVanityDisplayInfo })
	if got != repairMesh+";;;;" {
		t.Errorf("got %q", got)
	}
}

// A hero cannot be customised: its own blueprint look still wins over any
// saved string, repaired or not.
func TestRepairDoesNotTouchHeroes(t *testing.T) {
	h := heroShipLoadouts[0]
	plain := mmogShipLoadoutSeed{precastLoadoutID: h.loadoutID}
	saved := plain
	saved.savedDisplayInfo = ";;;;"
	if plain.displayInfo() != saved.displayInfo() {
		t.Errorf("hero display info depends on the saved string: %q vs %q", plain.displayInfo(), saved.displayInfo())
	}
}

// End to end over the whole roster: store a ";;;;" and a meshes-only string on
// every base ship the way the client did, read them back through the real
// loaders, and check (1) every group is filled and equals the uncustomised
// ship's group, (2) the string is exactly five groups, (3) enforced ownership
// accepts it for a player who bought nothing, and (4) the battle host's line
// carries the same string.
func TestRepairedAppearanceForEveryBaseShip(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "00000000000000000000000000000002"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range baseShipLoadouts {
		if err := grantUnlockedShipLoadout(tx, pid, b.loadoutID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, saved := range []string{";;;;", repairMesh + ";;;;"} {
		if _, err := database.Exec(`UPDATE player_ship_loadouts SET display_info=? WHERE user_id=?`, saved, pid); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadPersistedShipLoadouts(database, pid)
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, b := range baseShipLoadouts {
			l, ok := loaded[b.loadoutID]
			if !ok || l.savedDisplayInfo != saved {
				continue
			}
			if _, isHero := heroByID(l.precastLoadoutID); isHero {
				continue
			}
			got := repairGroups(t, l.displayInfo())
			want := repairGroups(t, l.repairDefaultDisplayInfo())
			wantMesh := strings.Split(saved, ";")[0]
			if got[0] != wantMesh {
				t.Errorf("%s: mesh group changed to %q", b.name, got[0])
			}
			for i := 1; i < 5; i++ {
				if got[i] != want[i] || got[i] == "" || got[i] == "-1" {
					t.Errorf("%s: group %d = %q, want %q", b.name, i, got[i], want[i])
				}
			}
			if unowned := unownedAppearanceItems(map[int32]bool{}, strings.Join(append([]string{""}, got[1:]...), ";")); len(unowned) != 0 {
				// Not a default: must be the ship's own blueprint look.
				if !allowAppearance("enforce", map[int32]bool{}, pid, b.loadoutID, l.displayInfo()) {
					t.Errorf("%s: enforced ownership refuses the repaired appearance (unowned %v)", b.name, unowned)
				}
			}
			battle := formatBattleLoadout("id", pid, l)
			if !strings.Contains(battle, "display="+l.displayInfo()+"\n") {
				t.Errorf("%s: the battle host line does not carry the repaired appearance", b.name)
			}
			checked++
		}
		if checked < 20 {
			t.Errorf("only %d base ships checked for %q", checked, saved)
		}
	}
}
