package main

import (
	"testing"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

// The client names a rank by KEY, not by value: the PR ladder it parses from
// YA_GetProgressionData is a list of {index, RP, CR} entries (the index is the
// parser's own loop counter, 0x2A738D0), and the rank name shown for a player is
// DN_Ranks_Player row FString::FromInt(entry index) -- m_rankName of row "0",
// "1", ... "50". So the ladder needs exactly one entry per table row, or a rank
// without an entry has no name and one without a row has no name either.
//
// This pins that the server's ladder length is the table's row count and that
// the rows are the contiguous keys 0..N-1 the lookup assumes. (The rank ICON
// does not depend on the ladder: UI_captain_rank_<index> is built from the
// rank index alone, 0x14041DB90; UI_captain_rank_0..50 all exist.)
func TestRankLadderLengthMatchesTheClientsRankTable(t *testing.T) {
	ranks := dreadconfig.AllRanks()
	if len(ranks) == 0 {
		t.Fatal("DN_Ranks_Player.json loaded no rows (DataDir broken?)")
	}
	if len(ranks) != playerRankCount {
		t.Fatalf("DN_Ranks_Player has %d rows, playerRankCount = %d", len(ranks), playerRankCount)
	}
	for i, r := range ranks {
		if int(r.RankID) != i {
			t.Fatalf("row %d has key %d; the client looks rows up by FromInt(index), keys must be 0..%d", i, r.RankID, len(ranks)-1)
		}
	}
}
