package main

import (
	"fmt"
	"testing"
)

// Fleet ship ids: heroes are YShipLoadoutHero ids (category 3, the client's
// test 0x547970), base ships precast loadout ids (category 1).
const (
	testHeroA int32 = 67043393 // 0x03FF0041
	testHeroB int32 = 67043394
	testBase  int32 = 33489262 // 0x01FF016E, the Agosta
)

func testMatchPools() rewardPools {
	var p rewardPools
	p[xpPoolLayout.scoring], p[xpPoolLayout.base], p[xpPoolLayout.performance] = 1000, 800, 200
	p[xpPoolLayout.win] = 250
	p[xpPoolLayout.battleReady[0]] = 1000
	return p // total 2250: Scoring 1000 + boosters 1250
}

func TestIsHeroFleetKey(t *testing.T) {
	if !isHeroFleetKey(testHeroA) || isHeroFleetKey(testBase) {
		t.Fatal("hero test wrong")
	}
}

// An idle hero earns the fleet bonus only: 10% of Scoring in pool 10, 10% of
// the booster lines in pool 11; a base ship earns nothing extra.
func TestIdleHeroEarnsTheFleetBonus(t *testing.T) {
	match := testMatchPools()
	h := heroRewards(match, match, []int32{testBase, testHeroA}, []int32{testBase})
	if len(h.ship) != 0 {
		t.Errorf("an idle hero got a ship XP list: %v", h.ship)
	}
	free, ok := h.free[testHeroA]
	if !ok || len(h.free) != 1 {
		t.Fatalf("free lists %v", h.free)
	}
	if free[poolFreeXPFleetBonus] != 100 || free[poolFreeXPFleetBoosted] != 125 || free.total() != 0 {
		t.Errorf("idle hero free list %v, want pool 10 = 100, pool 11 = 125, nothing else", free)
	}
	if h.freeXP != 225 { // 10% of the match's 2250
		t.Errorf("free XP %d, want 225", h.freeXP)
	}
}

// A flown hero: its share splits 70% ship XP / 30% free XP, exactly, plus the
// fleet bonus.
func TestFlownHeroSplitsItsXP(t *testing.T) {
	match := testMatchPools()
	per := perFlownShipPools(match, 2) // flown with one other ship
	h := heroRewards(match, per, []int32{testHeroA, testBase}, []int32{testHeroA, testBase})
	ship, free := h.ship[testHeroA], h.free[testHeroA]
	if ship.total()+free.total() != per.total() {
		t.Errorf("70/30 split %d + %d != its share %d", ship.total(), free.total(), per.total())
	}
	if want := int32(788); ship.total() != want { // round(1125 x 0.7)
		t.Errorf("ship XP %d, want %d", ship.total(), want)
	}
	if free[poolFreeXPFleetBonus] != 100 || free[poolFreeXPFleetBoosted] != 125 {
		t.Errorf("flown hero lost its fleet bonus: %v", free)
	}
	if h.freeXP != free.total()+225 {
		t.Errorf("free XP %d, want %d", h.freeXP, free.total()+225)
	}
	if _, ok := h.ship[testBase]; ok {
		t.Error("a base ship got a hero list")
	}
}

// At most five heroes earn the fleet bonus (a 50% cap); a flown hero past
// the cap still gets its 70/30 split.
func TestHeroFleetBonusIsCapped(t *testing.T) {
	match := testMatchPools()
	var fleet []int32
	for i := int32(0); i < 7; i++ {
		fleet = append(fleet, testHeroA+i)
	}
	flown := fleet[6]
	h := heroRewards(match, match, fleet, []int32{flown})
	earning := 0
	for _, p := range h.free {
		if p[poolFreeXPFleetBonus] > 0 {
			earning++
		}
	}
	if earning != heroFleetBonusMaxHeroes {
		t.Errorf("%d heroes earned the bonus, want %d", earning, heroFleetBonusMaxHeroes)
	}
	if p, ok := h.free[flown]; !ok || p.total() == 0 || p[poolFreeXPFleetBonus] != 0 {
		t.Errorf("flown hero past the cap: %v (want its 30%%, no bonus)", p)
	}
	if bonus := h.freeXP - h.free[flown].total(); bonus != 5*225 {
		t.Errorf("fleet bonus total %d, want 5 x 225 (50%% of the match XP)", bonus)
	}
}

func TestHeroListsCSV(t *testing.T) {
	match := testMatchPools()
	h := heroRewards(match, match, []int32{testHeroB, testHeroA}, nil)
	got := h.listsCSV(h.free)
	want := fmt.Sprintf("%d:0,0,0,0,0,0,0,0,0,0,100,125,0;%d:0,0,0,0,0,0,0,0,0,0,100,125,0", testHeroB, testHeroA)
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if h.listsCSV(h.ship) != "" {
		t.Errorf("ship lists for idle heroes: %q", h.listsCSV(h.ship))
	}
}

// A fleet with no hero pays exactly what it did, and the reply carries empty
// hero lists (the mod then writes zeros, as before).
func TestNoHeroNoChange(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "0123456789abcdef0123456789abcdef"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	fleet := battleFleetLoadouts(pid, 0)
	for _, l := range fleet {
		if isHeroFleetKey(fleetShipKey(l)) {
			t.Skip("the starter fleet holds a hero")
		}
	}
	h := matchHeroRewards(pid, []string{fleet[0].entryID()}, 0, testMatchPools())
	if h.freeXP != 0 || len(h.order) != 0 || h.listsCSV(h.free) != "" || h.listsCSV(h.ship) != "" {
		t.Errorf("hero rewards for a hero-less fleet: %+v", h)
	}
}
