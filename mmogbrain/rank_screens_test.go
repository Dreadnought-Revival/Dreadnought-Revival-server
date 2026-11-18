package main

import (
	"strconv"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

// clientRankPosition must be the client's own rank-for-reputation function
// (FUN_1403FB620): the same number the player's hangar shows for itself, so
// the squad, friend list and lobby show the same rank for the player as the
// player sees.
func TestClientRankPositionMirrorsTheClientLadder(t *testing.T) {
	ladder := playerRankLadder()
	if len(ladder) != playerRankCount {
		t.Fatalf("ladder has %d entries, want %d", len(ladder), playerRankCount)
	}
	if ladder[0] != 0 {
		t.Fatalf("entry 0 must start at 0, got %d", ladder[0])
	}
	for i := 1; i < len(ladder); i++ {
		if ladder[i] <= ladder[i-1] {
			t.Fatalf("ladder not increasing at %d: %d <= %d", i, ladder[i], ladder[i-1])
		}
	}
	// Reference: the decompiled loop, written the way the binary does it.
	ref := func(rep int32) int32 {
		n := int32(len(ladder))
		if ladder[n-1] <= rep {
			return n - 1
		}
		if rep != 0 {
			for i := int32(1); i < n; i++ {
				if rep < ladder[i] {
					return i - 1
				}
			}
		}
		return 0
	}
	for _, rep := range []int32{-5, 0, 1, 99, 100, 999, 1000, 1001, 3999, 4000} {
		if got, want := clientRankPosition(rep), ref(rep); got != want {
			t.Errorf("rep %d: position %d, want %d", rep, got, want)
		}
	}
	for i, rp := range ladder {
		for _, rep := range []int32{rp - 1, rp, rp + 1} {
			if rep < 0 {
				continue
			}
			if got, want := clientRankPosition(rep), ref(rep); got != want {
				t.Errorf("entry %d rep %d: position %d, want %d", i, rep, got, want)
			}
		}
		if rp > 0 && clientRankPosition(rp) != int32(i) {
			t.Errorf("rep exactly at entry %d must be position %d, got %d", i, i, clientRankPosition(rp))
		}
	}
	if got := clientRankPosition(1 << 30); got != playerRankCount-1 {
		t.Errorf("huge rep: position %d, want the last entry %d", got, playerRankCount-1)
	}
}

// The "Rank" of YA_GetPlayersInformation is used by the client unchanged as the
// icon number (UI_captain_rank_<n>, 0..50 exist) and ladder index. It was the
// 1-based current_rank: one too high for everybody, and 52+ (25 of 276 live
// accounts, up to 233) named an icon and a rank that do not exist.
func TestPlayersInformationRankIsAnExistingClientRankPosition(t *testing.T) {
	ladder := playerRankLadder()
	cases := []mmogPlayerState{
		{currentRank: 1, currentXP: 0},
		{currentRank: 1, currentXP: 100},
		{currentRank: 2, currentXP: ladder[1]},
		{currentRank: 20, currentXP: ladder[19] + 5},
		{currentRank: 50, currentXP: ladder[49]},
		{currentRank: 51, currentXP: ladder[50]},
		{currentRank: 233, currentXP: ladder[50] + 3000000},
	}
	for _, st := range cases {
		b, _ := appendMmogPlayerDisplayInfoEntry(nil, nil, "a1000000000000000000000000000001", st)
		got, err := strconv.Atoi(protocol.ExtractStringField(b, "Rank"))
		if err != nil {
			t.Fatalf("rank %d: Rank field missing or not numeric", st.currentRank)
		}
		if got < 0 || got >= playerRankCount {
			t.Errorf("current_rank %d: Rank %d has no icon/name (valid 0..%d)", st.currentRank, got, playerRankCount-1)
		}
		if want := int(clientRankPosition(st.currentXP)); got != want {
			t.Errorf("current_rank %d: Rank %d, want position %d", st.currentRank, got, want)
		}
		// Server ranks up to 51 map to position rank-1 when the XP is
		// consistent with the rank, which is how the server ranks players.
		if st.currentRank <= playerRankCount && got != int(st.currentRank)-1 {
			t.Errorf("current_rank %d: Rank %d, want %d", st.currentRank, got, st.currentRank-1)
		}
	}
}

// The host writes the reputation it is given into the player's reputation
// manager. A value equal to the class default (0) is not replicated, so the
// clients would keep their own reputation for that player: 0 is sent as 1,
// which is the same client rank (position 0) -- ladder entry 1 starts at 1000.
func TestBattleHostReputation(t *testing.T) {
	if battleHostReputation(0) != 1 || battleHostReputation(-7) != 1 {
		t.Fatal("a reputation below 1 must be sent as 1 (0 is never replicated)")
	}
	if got := battleHostReputation(123456); got != 123456 {
		t.Fatalf("got %d, want the reputation unchanged", got)
	}
	if clientRankPosition(1) != clientRankPosition(0) {
		t.Fatal("reputation 1 must be the same client rank as 0")
	}
	for _, xp := range []int32{0, 1, 999, 1000, 4000, 1 << 29} {
		if clientRankPosition(battleHostReputation(xp)) != clientRankPosition(xp) {
			t.Fatalf("xp %d: the replicated value changes the client's rank", xp)
		}
	}
}
