package main

import (
	"encoding/hex"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

func TestDumpFleetHex(t *testing.T) {
	pid := defaultMmogPlayerPID
	var reqID [16]byte

	fleet := buildMmogPlayerFleetsPayload(pid)
	fleetFrame := protocol.BuildResponseFrame(reqID, 0x0320, fleet)
	t.Logf("PlayerFleets frame (%d bytes): %s", len(fleetFrame), hex.EncodeToString(fleetFrame))

	staticFleet := buildMmogStaticFleetDataPayload()
	staticFrame := protocol.BuildResponseFrame(reqID, 0x0320, staticFleet)
	t.Logf("StaticFleetData frame (%d bytes): %s", len(staticFrame), hex.EncodeToString(staticFrame))
}
