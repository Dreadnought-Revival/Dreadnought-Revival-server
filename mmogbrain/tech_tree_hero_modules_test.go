package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

// A hero's own module records (techTreeHeroModuleItems): what the client finds
// when it asks FindShipTechTreeData (0x1403F5050) for a hero's loadout id.

// techTreeDocEntry is one item object of the TechTrees document, in document
// order -- the order the client's loader files records in.
type techTreeDocEntry struct {
	id, classID int32
	proxyType   string
	xpCost      string
	hasUI       bool
	hasPrereq   bool
}

func techTreeDocEntries(t *testing.T, document []byte) []techTreeDocEntry {
	t.Helper()
	nodes, _ := decodeMmogNodes(t, document, 0)
	var out []techTreeDocEntry
	var walk func(n *mmogNode)
	walk = func(n *mmogNode) {
		idNode, classNode := n.child("Id"), n.child("ClassId")
		if idNode != nil && classNode != nil {
			id, _ := strconv.Atoi(idNode.str)
			class, _ := strconv.Atoi(classNode.str)
			if id < 0 {
				return // a tier layout row, not an item
			}
			e := techTreeDocEntry{id: int32(id), classID: int32(class), hasUI: n.child("UI") != nil, hasPrereq: n.child("Prereq") != nil}
			if c := n.child("ProxyType"); c != nil {
				e.proxyType = c.str
			}
			if c := n.child("XPCost"); c != nil {
				e.xpCost = c.str
			}
			out = append(out, e)
			return
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out
}

func heroLoadoutIDSet() map[int32]bool {
	set := map[int32]bool{}
	for _, h := range heroShipLoadouts {
		set[h.loadoutID] = true
	}
	return set
}

// Each hero's record holds exactly its locked modules: the secondary weapon and
// the four abilities, as the client's own cooked blueprint lists them -- and
// nothing else (no primary, no officer perks, no other ship's module), with no
// price, prerequisite, layout or hero flag to make it researchable, purchasable
// or drawn on a tech tree page.
func TestTechTreeHeroModuleRecords(t *testing.T) {
	cooked := map[int32][]int32{} // hero loadout id -> m_abilitiesId from the cooked blueprint
	if f, err := os.Open(filepath.Join(dreadconfig.LoadoutsDir(), "HeroLoadouts_cooked.jsonl")); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var row struct {
				Abilities  []int32 `json:"m_abilitiesId"`
				SystemData struct {
					ItemID int32 `json:"m_itemID"`
				} `json:"m_itemSystemData"`
			}
			if json.Unmarshal(scanner.Bytes(), &row) == nil {
				cooked[row.SystemData.ItemID] = row.Abilities
			}
		}
		_ = f.Close()
	}
	if len(cooked) < 40 {
		t.Fatalf("only %d hero blueprints in the cooked dump", len(cooked))
	}

	byHero := map[int32][]techTreeItem{}
	for _, item := range techTreeHeroModuleItems() {
		byHero[item.classID] = append(byHero[item.classID], item)
	}
	if len(byHero) != len(heroShipLoadouts) {
		t.Errorf("%d heroes have a module record, want all %d", len(byHero), len(heroShipLoadouts))
	}
	for _, hero := range heroShipLoadouts {
		class := eyShipClassByKey[hero.hullLine]
		want := map[int32]bool{inflatedItemID(hero.secondary, class): true}
		for _, id := range cooked[hero.loadoutID] {
			want[id] = true
		}
		got := map[int32]bool{}
		for _, item := range byHero[hero.loadoutID] {
			if got[item.id] {
				t.Errorf("%s: module %d filed twice", hero.name, item.id)
			}
			got[item.id] = true
			if !item.module || item.hero || item.shipPage != 0 || len(item.prereq) != 0 || item.xpCost != 0 || item.techItemsRequired != 0 {
				t.Errorf("%s: module %d is not a bare, priceless module entry: %+v", hero.name, item.id, item)
			}
			if c := (item.id >> 24) & 0xff; c != 4 && c != 5 {
				t.Errorf("%s: %d has category %d; only weapons/abilities belong in a module record", hero.name, item.id, c)
			}
			if item.id == inflatedItemID(hero.primary, class) {
				t.Errorf("%s: the primary weapon %d is in the record (a primary is not a module; it would draw twice)", hero.name, item.id)
			}
			if (item.id>>16)&0xff != class {
				t.Errorf("%s: %d is not the per-ship form for class %d", hero.name, item.id, class)
			}
		}
		for id := range want {
			if !got[id] {
				t.Errorf("%s: record is missing its locked module %d", hero.name, id)
			}
		}
		for id := range got {
			if !want[id] {
				t.Errorf("%s: record holds %d, which the hero does not fit", hero.name, id)
			}
		}
	}
}

// Base hulls are untouched: no base item or hero node is keyed to a hero, the
// base/hero node set is exactly what it was, and the hero records are the tail
// of the document -- after every base record, so the client's find-by-id
// (0x1403F51A0, first record wins; records are appended in document order by
// the loader) still reaches a base hull's copy of every id it researches.
func TestTechTreeHeroModuleRecordsLeaveBaseHullsAlone(t *testing.T) {
	if techTreeNoModules {
		t.Skip("modules are off")
	}
	heroes := heroLoadoutIDSet()
	for _, item := range append(techTreeBaseItems(), techTreeHeroItems()...) {
		if heroes[item.classID] {
			t.Fatalf("item %d is keyed to hero %d; heroes keep their anchor ClassId", item.id, item.classID)
		}
	}

	entries := techTreeDocEntries(t, buildMmogTechTreeDocument())
	first := -1
	var rest []int32
	for i, e := range entries {
		if heroes[e.classID] {
			if first < 0 {
				first = i
			}
			if e.proxyType != "-1" || e.hasUI || e.hasPrereq || e.xpCost != "0" {
				t.Errorf("hero module %d is not a bare, priceless module entry: %+v", e.id, e)
			}
			continue
		}
		if first >= 0 {
			t.Fatalf("entry %d (index %d) follows hero module records; they must be the tail of the document", e.id, i)
		}
		rest = append(rest, e.id)
	}
	if first < 0 {
		t.Fatal("document carries no hero module records")
	}
	var want []int32
	for _, item := range append(techTreeBaseItems(), techTreeHeroItems()...) {
		want = append(want, item.id)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if len(rest) != len(want) {
		t.Fatalf("document has %d non-hero-record entries, base+hero nodes are %d", len(rest), len(want))
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Fatalf("non-hero-record entries differ from base+hero items at %d: %d vs %d", i, rest[i], want[i])
		}
	}
	// Stripped of modules, the document has no hero record either.
	for _, e := range techTreeDocEntries(t, buildMmogTechTreeDocumentWith(true)) {
		if heroes[e.classID] {
			t.Errorf("module-less document still carries hero record entry %d", e.id)
		}
	}
}

// The client's id lookup, replayed over the document: the first RECORD (keyed
// by ClassId, in order of first appearance) whose module array holds the id
// answers. Every id a base hull researches must still be answered by a base
// record; only ids no base record holds may land on a hero.
func TestTechTreeHeroModulesNeverShadowBaseResearch(t *testing.T) {
	if techTreeNoModules {
		t.Skip("modules are off")
	}
	heroes := heroLoadoutIDSet()
	entries := techTreeDocEntries(t, buildMmogTechTreeDocument())

	var order []int32
	records := map[int32][]int32{}
	for _, e := range entries {
		if e.proxyType != "-1" {
			continue // hull nodes go to the manufacturer groups / proxy arrays
		}
		if _, ok := records[e.classID]; !ok {
			order = append(order, e.classID)
		}
		records[e.classID] = append(records[e.classID], e.id)
	}
	firstRecord := func(skipHeroes bool, id int32) (int32, bool) {
		for _, key := range order {
			if skipHeroes && heroes[key] {
				continue
			}
			for _, have := range records[key] {
				if have == id {
					return key, true
				}
			}
		}
		return 0, false
	}

	shared, heroOnly := 0, 0
	for _, e := range entries {
		if !heroes[e.classID] {
			// Every base lookup answers as it did without hero records.
			with, _ := firstRecord(false, e.id)
			without, _ := firstRecord(true, e.id)
			if with != without {
				t.Errorf("id %d is answered by record %d, was %d before the hero records", e.id, with, without)
			}
			continue
		}
		key, ok := firstRecord(false, e.id)
		if !ok {
			t.Fatalf("id %d not found at all", e.id)
		}
		if base, inBase := firstRecord(true, e.id); inBase {
			shared++
			if heroes[key] || key != base {
				t.Errorf("hero module %d is answered by hero record %d; a base copy (record %d) exists and must win", e.id, key, base)
			}
		} else {
			heroOnly++
			if key != e.classID && !heroes[key] {
				t.Errorf("hero-only module %d is answered by record %d, want a hero's", e.id, key)
			}
		}
	}
	t.Logf("hero module entries: %d share an id with a base record (base copy answers), %d are in no base record (a hero's answers)", shared, heroOnly)
	if shared == 0 || heroOnly == 0 {
		t.Errorf("expected both shared (%d) and hero-only (%d) ids; the data changed under this test", shared, heroOnly)
	}
}

// A hero's module can be neither researched nor bought through the server:
// hero-only ids are not tech tree nodes (research is refused for those,
// TestUnlockRefusesItemsOutsideTheTechTree) and no entry of a hero record is
// priced; an id a hero shares with a base hull keeps that hull's cost.
func TestTechTreeHeroModulesAreNotResearchable(t *testing.T) {
	baseIDs := map[int32]bool{}
	for _, item := range techTreeBaseItems() {
		baseIDs[item.id] = true
	}
	heroOnly := 0
	for _, item := range techTreeHeroModuleItems() {
		if item.xpCost != 0 {
			t.Errorf("hero module %d carries XP cost %d", item.id, item.xpCost)
		}
		if baseIDs[item.id] {
			continue
		}
		heroOnly++
		if cost, ok := techTreeResearchCost(item.id); ok {
			t.Errorf("hero-only module %d is a researchable tech tree node (cost %d)", item.id, cost)
		}
		if _, ok := researchHullLoadout(item.id); ok {
			t.Errorf("hero-only module %d resolves to a hull whose research list holds it", item.id)
		}
	}
	if heroOnly == 0 {
		t.Error("no hero-only modules; the data changed under this test")
	}
}
