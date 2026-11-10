package main

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

// realIncrementRequest is a verbatim capture of what the client sends. The ids
// are STRINGS -- the server used to answer YA_GetPlayerStatsCounterData with a
// single row whose counterId was the int32 0.
func realIncrementRequest(counterID, subID string, increment int32) []byte {
	var b []byte
	b = protocol.AppendStringField(b, "RT", "YA_IncrementPlayerStatsCounter")
	b = protocol.AppendStringField(b, "counterId", counterID)
	b = protocol.AppendStringField(b, "counterSubId", subID)
	b = protocol.AppendInt32Field(b, "increment", increment)
	return b
}

func TestStatsCountersPersistAndAccumulate(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	const playerPID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	database := currentMmogPlayerStateDB()

	for _, step := range []struct {
		counterID, subID string
		increment        int32
	}{
		{"Customize", "Captain", 1},
		{"Customize", "Ship", 1},
		{"Customize", "Ship", 2},
	} {
		if err := persistMmogPlayerMutation(playerPID, "YA_IncrementPlayerStatsCounter",
			realIncrementRequest(step.counterID, step.subID, step.increment)); err != nil {
			t.Fatalf("persist %s/%s: %v", step.counterID, step.subID, err)
		}
	}
	_ = database

	if got := playerStatsCounterValue(playerPID, "Customize", "Ship"); got != 3 {
		t.Errorf("Customize/Ship = %d, want 3 (1+2)", got)
	}
	if got := playerStatsCounterValue(playerPID, "Customize", ""); got != 4 {
		t.Errorf("Customize (all sub-ids) = %d, want 4", got)
	}
	if got := playerStatsCounterValue(playerPID, "NeverReported", ""); got != 0 {
		t.Errorf("an unreported counter = %d, want 0", got)
	}
}

func TestStatsCounterResponseUsesStringIDs(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	const playerPID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := persistMmogPlayerMutation(playerPID, "YA_IncrementPlayerStatsCounter",
		realIncrementRequest("Customize", "Captain", 7)); err != nil {
		t.Fatal(err)
	}

	payload := buildMmogPlayerStatsCounterDataPayload(playerPID)

	// The client sends these as strings, so they have to come back as strings.
	if !bytes.Contains(payload, protocol.AppendStringField(nil, "counterId", "Customize")) {
		t.Error("counterId is not the string the client sent")
	}
	if !bytes.Contains(payload, protocol.AppendStringField(nil, "counterSubId", "Captain")) {
		t.Error("counterSubId is not the string the client sent")
	}
	// And the value as a numeric string: an int32 reads back as 0 through this
	// client's value accessors.
	if !bytes.Contains(payload, protocol.AppendStringField(nil, "value", strconv.Itoa(7))) {
		t.Error("value is not carried as a numeric string")
	}
	if bytes.Contains(payload, protocol.AppendInt32Field(nil, "counterId", 0)) {
		t.Error("the hardcoded int32 counterId row is back")
	}
}

// UYGoalManager::GetCurrentAmount reads the goal's amount straight out of the
// dynamic career response, so this number is the goal's progress. It used to be
// a constant zero, which meant no goal could ever advance.
//
// CHANGED 2026-11-09: a counter the server counts itself is no longer moved by
// a client-reported counter of the same name -- any client could have
// reported "MatchesWon" and completed the goal. Every configured goal is
// server-counted. The client's own reports still count where they are the
// evidence: its "FinishTutorial" counter (one of the two this client sends)
// finishes GOAL_TUTORIAL.
func TestCareerGoalProgressFollowsReportedCounters(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	const playerPID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for _, goal := range careerGoalsConfig() {
		if goal.id != "UnlockAllModes" && goal.counterID != "" && !serverCountsCareerCounter(goal.counterID) {
			t.Errorf("%s counts %q from the client; a client could complete it", goal.id, goal.counterID)
		}
	}

	if got := careerGoalProgressForPlayer(playerPID, "GOAL_TUTORIAL"); got != 0 {
		t.Fatalf("a fresh player starts GOAL_TUTORIAL at %d, want 0", got)
	}
	// The goal's own counter name, reported by a client: the server counts
	// this one, so the report changes nothing.
	if err := persistMmogPlayerMutation(playerPID, "YA_IncrementPlayerStatsCounter",
		realIncrementRequest(counterTutorialFinished, "", 4)); err != nil {
		t.Fatal(err)
	}
	if got := careerGoalProgressForPlayer(playerPID, "GOAL_TUTORIAL"); got != 0 {
		t.Errorf("GOAL_TUTORIAL = %d after a client reported the server-counted %q, want 0", got, counterTutorialFinished)
	}
	// The client's real report.
	if err := persistMmogPlayerMutation(playerPID, "YA_IncrementPlayerStatsCounter",
		realIncrementRequest("FinishTutorial", "", 1)); err != nil {
		t.Fatal(err)
	}
	if got := careerGoalProgressForPlayer(playerPID, "GOAL_TUTORIAL"); got != 1 {
		t.Errorf("GOAL_TUTORIAL = %d after the client reported FinishTutorial, want 1", got)
	}

	// UnlockAllModes stays satisfied regardless, or the client locks every mode.
	if got := careerGoalProgressForPlayer(playerPID, "UnlockAllModes"); got < 1 {
		t.Errorf("UnlockAllModes = %d, want at least its final stage amount", got)
	}
}
