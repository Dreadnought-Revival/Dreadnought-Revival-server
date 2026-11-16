package main

import (
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/sirupsen/logrus"
)

// A fleet a new ship unlocks reaches the client without a restart: the fleets
// go out again as a reply to the client's latest YA_PlayerFleets request (the
// client picks that handler by request id), never as the YA_FleetUpdate push
// it has no handler for.
func TestFleetsSentAgainUnderTheLastFleetsRequestID(t *testing.T) {
	conn := &captureConn{}
	state := &mmogConnState{playerPID: defaultMmogPlayerPID, loginResponseSent: true, playerGetResponded: true}

	// Before the client ever asked: nothing to answer.
	if err := refreshPlayerFleets(logrus.New(), conn, "test-remote", nil, false, state, "a test"); err != nil {
		t.Fatal(err)
	}
	if conn.Len() != 0 {
		t.Fatal("fleets sent although the client never asked for them")
	}

	// Two fleet requests (login, then back from a match): the later id counts.
	req := protocol.AppendRootEnd(protocol.AppendStringField(nil, "RT", "YA_PlayerFleets"))
	first, last := syntheticRequestID(0xa1), syntheticRequestID(0xb2)
	for _, id := range [][16]byte{first, last} {
		if err := processMmogAppFrames(logrus.New(), conn, "test-remote", []protocol.AppFrame{{MsgType: 0x0320, RequestID: id, Payload: req}}, nil, false, state); err != nil {
			t.Fatal(err)
		}
	}
	if !state.fleetsRequestSeen || state.fleetsRequestID != last {
		t.Fatalf("remembered fleets request %x (seen %v), want %x", state.fleetsRequestID, state.fleetsRequestSeen, last)
	}
	conn.Reset()

	if err := refreshPlayerFleets(logrus.New(), conn, "test-remote", nil, false, state, "a test"); err != nil {
		t.Fatal(err)
	}
	frames, rest := protocol.ParseAppFrames(conn.Bytes())
	if len(rest) != 0 || len(frames) != 1 {
		t.Fatalf("refresh wrote %d frames (+%d bytes), want 1", len(frames), len(rest))
	}
	if got := protocol.ExtractRequestName(frames[0].Payload); got != "YA_PlayerFleets" {
		t.Errorf("refresh frame is %q, want YA_PlayerFleets", got)
	}
	if frames[0].RequestID != last {
		t.Errorf("refresh frame id %x, want the last fleets request %x", frames[0].RequestID, last)
	}
}
