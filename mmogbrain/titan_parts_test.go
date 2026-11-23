package main

import (
	"strconv"
	"strings"
	"testing"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

// The Titan hero parts (352256197-9) are built on the Dreadnought Medium
// skeleton (VH_DreadM_SKEL): they fit that hull line only, not every ship.
func TestTitanPartsFitTheDreadnoughtMediumOnly(t *testing.T) {
	for _, id := range []int32{352256197, 352256198, 352256199} {
		v, ok := dreadconfig.VanityItemByID(id)
		if !ok || !strings.Contains(v.File, "/Heroships/Titan/") {
			t.Fatalf("%d is not a Titan part (%+v)", id, v)
		}
		if got := shipVanityClasses(v); len(got) != 1 || got[0] != eyShipClassByKey["DreadnoughtMedium"] {
			t.Errorf("%s fits %v, want only Dreadnought Medium (%d)", v.Name, got, eyShipClassByKey["DreadnoughtMedium"])
		}
	}
}

// meshOf is the mesh group of a hull line's default appearance, per-ship form.
func meshOf(t *testing.T, hullLine string) []string {
	t.Helper()
	def := dreadconfig.DefaultShipDisplayInfo(hullLine, baseShipManufacturerByClassSize[hullLine])
	mesh := strings.Split(strings.Split(def, ";")[0], "#")
	if len(mesh) != 4 {
		t.Fatalf("%s default mesh %q", hullLine, mesh)
	}
	return mesh
}

func perShip(t *testing.T, field string, class int32) string {
	t.Helper()
	v, err := strconv.ParseInt(field, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(int(shipVanityClientID(int32(v), class)))
}

// A Titan part saved on a Heavy Dreadnought (class 7; 9 ships of six classes
// wore 19 Titan parts on 2026-11-23) is replaced by that ship's own default part for
// the slot it left empty; the player's other parts stay.
func TestRepairReplacesATitanPartOnAnotherHull(t *testing.T) {
	const dreadH = 7
	def := meshOf(t, "DreadnoughtHeavy")
	slots := map[string]int{}
	for i, f := range def {
		v, _ := strconv.ParseInt(f, 10, 32)
		slots[shipMeshSlot(int32(v))] = i
	}
	hull, ok := slots["hull"]
	if !ok {
		t.Fatalf("no hull part in the Heavy Dreadnought default %v", def)
	}
	saved := make([]string, 4)
	for i, f := range def {
		saved[i] = perShip(t, f, dreadH)
	}
	titanHull := strconv.Itoa(int(shipVanityClientID(352256198, dreadH)))
	// The client keeps no fixed order: put the Titan hull last.
	saved[hull], saved[3] = saved[3], titanHull
	got := strings.Split(repairedShipMesh(strings.Join(saved, "#")+";1;2;3;4", dreadH, func() string {
		return strings.Join(def, "#") + ";5;6;7;8"
	}), ";")
	mesh := strings.Split(got[0], "#")
	if strings.Contains(got[0], titanHull) {
		t.Fatalf("Titan hull still fitted: %q", got[0])
	}
	if mesh[3] != def[hull] {
		t.Errorf("slot of the Titan hull = %s, want the default hull %s", mesh[3], def[hull])
	}
	for i := 0; i < 3; i++ {
		if mesh[i] != saved[i] {
			t.Errorf("player's part %d changed: %s -> %s", i, saved[i], mesh[i])
		}
	}
	if strings.Join(got[1:], ";") != "1;2;3;4" {
		t.Errorf("other groups changed: %q", got)
	}
}

// On a Dreadnought Medium the Titan parts are the player's choice and stay.
func TestRepairKeepsTitanPartsOnTheDreadnoughtMedium(t *testing.T) {
	const dreadM = 6
	def := meshOf(t, "DreadnoughtMedium")
	saved := []string{
		strconv.Itoa(int(shipVanityClientID(352256197, dreadM))),
		perShip(t, def[1], dreadM),
		strconv.Itoa(int(shipVanityClientID(352256198, dreadM))),
		strconv.Itoa(int(shipVanityClientID(352256199, dreadM))),
	}
	s := strings.Join(saved, "#") + ";1;2;3;4"
	called := false
	if got := repairedShipMesh(s, dreadM, func() string { called = true; return "" }); got != s || called {
		t.Errorf("Dreadnought Medium with Titan parts changed: %q -> %q", s, got)
	}
}

// Every Titan part on every other class is caught (in any form: the saved
// id's own class byte is not trusted), and every hull line's
// default names its four slots (so the slot match never falls back blindly).
func TestTitanPartsAreWrongOnEveryOtherClass(t *testing.T) {
	for hull, class := range eyShipClassByKey {
		for _, id := range []int32{352256197, 352256198, 352256199} {
			wrong := shipMeshPartWrongClass(strconv.Itoa(int(shipVanityClientID(id, class))), class)
			if wrong != (hull != "DreadnoughtMedium") {
				t.Errorf("Titan part %d on %s: wrong=%v", id, hull, wrong)
			}
			if shipMeshPartWrongClass(strconv.Itoa(int(id)), class) != (hull != "DreadnoughtMedium") {
				t.Errorf("Titan part %d (shared form) on %s misjudged", id, hull)
			}
		}
		def := dreadconfig.DefaultShipDisplayInfo(hull, baseShipManufacturerByClassSize[hull])
		seen := map[string]bool{}
		for _, f := range strings.Split(strings.Split(def, ";")[0], "#") {
			v, err := strconv.ParseInt(f, 10, 32)
			if err != nil || v <= 0 {
				continue // a hull line with no default parts in this build
			}
			slot := shipMeshSlot(int32(v))
			if slot == "" || seen[slot] {
				t.Errorf("%s default part %d: slot %q (seen %v)", hull, v, slot, seen)
			}
			seen[slot] = true
		}
	}
}

// End to end through the loader: a saved Heavy Dreadnought look with the
// Titan bridge comes back with the ship's own default part in its place.
func TestSavedTitanPartIsReplacedOnAHeavyDreadnought(t *testing.T) {
	var monarch mmogShipLoadoutSeed
	for _, b := range baseShipLoadouts {
		if b.name == "Monarch" {
			monarch = mmogShipLoadoutSeed{precastLoadoutID: b.loadoutID}
		}
	}
	if monarch.precastLoadoutID == 0 {
		t.Fatal("no Monarch in the roster")
	}
	if hull, ok := monarch.hullLine(); !ok || hull != "DreadnoughtHeavy" {
		t.Fatalf("hull line %q %v", hull, ok)
	}
	def := strings.Split(monarch.repairDefaultDisplayInfo(), ";")
	mesh := strings.Split(def[0], "#")
	bridge := -1
	for i, f := range mesh {
		v, _ := strconv.ParseInt(f, 10, 32)
		if shipMeshSlot(int32(v)) == "bridge" {
			bridge = i
		}
	}
	if bridge < 0 {
		t.Fatalf("no bridge slot in %q", def[0])
	}
	saved := append([]string(nil), mesh...)
	saved[bridge] = strconv.Itoa(int(shipVanityClientID(352256197, 7)))
	monarch.savedDisplayInfo = strings.Join(saved, "#") + ";" + strings.Join(def[1:], ";")
	got := strings.Split(monarch.displayInfo(), ";")
	if got[0] != def[0] {
		t.Errorf("mesh = %q, want the Monarch's own %q", got[0], def[0])
	}
}
