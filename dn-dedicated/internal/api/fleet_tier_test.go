package api

import (
	"strings"
	"testing"
)

// fleet_tier reaches the battle server as ?FleetTier=<n>, and nothing else a
// caller sends can: only the two values the GameState decodes (0x3A5831) pass.
func TestFleetTierURLOptions(t *testing.T) {
	for tier, want := range map[int]string{4: "FleetTier=4", 5: "FleetTier=5"} {
		got, err := fleetTierURLOptions(tier)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("fleet_tier %d -> %v, %v; want [%s]", tier, got, err, want)
		}
	}
	if got, err := fleetTierURLOptions(0); err != nil || len(got) != 0 {
		t.Errorf("fleet_tier 0 (Recruit) -> %v, %v; want no option", got, err)
	}
	for _, bad := range []int{1, 2, 3, 6, -1} {
		if _, err := fleetTierURLOptions(bad); err == nil {
			t.Errorf("fleet_tier %d was accepted", bad)
		}
	}
}

// team_size reaches the battle server as ?DNTeamSize=<n> with the player
// limit as ?MaxPlayers=<n>; without a team size neither is added (the host
// keeps its own), and out-of-range values are refused.
func TestHostURLOptionsTeamSize(t *testing.T) {
	got, err := hostURLOptions(4, 60, 120)
	if err != nil || strings.Join(got, ",") != "FleetTier=4,DNTeamSize=60,MaxPlayers=120" {
		t.Errorf("fleet 4, team 60, max 120 -> %v, %v", got, err)
	}
	if got, err := hostURLOptions(0, 0, 10); err != nil || len(got) != 0 {
		t.Errorf("no team size -> %v, %v; want no options", got, err)
	}
	for _, c := range [][2]int{{61, 10}, {-1, 10}, {5, 121}, {5, -1}} {
		if _, err := hostURLOptions(0, c[0], c[1]); err == nil {
			t.Errorf("team %d max %d was accepted", c[0], c[1])
		}
	}
}
