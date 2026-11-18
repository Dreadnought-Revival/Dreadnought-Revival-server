package main

import (
	"strconv"
	"strings"
	"testing"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

// An empty captain slot makes a captain-screen button that does nothing, so
// empty slots (and the old default's untemplated suit) get the gender's
// default; everything the client saved is kept.
func TestRepairedCaptainDisplayInfo(t *testing.T) {
	cases := map[string]string{
		// The old server default: untemplated suit, no headwear.
		"GENDER_MALE;#iiS=872349903#iiH=872349840#iiHw=-1#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0": "GENDER_MALE;#iiS=872349706#iiH=872349840#iiHw=872349736#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0",
		// A bought suit is kept; only the empty headwear is filled.
		"GENDER_MALE;#iiS=872349895#iiH=872349841#iiHw=-1#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572537#iiMn=855572504#bIam=0": "GENDER_MALE;#iiS=872349895#iiH=872349841#iiHw=872349736#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572537#iiMn=855572504#bIam=0",
		// Female defaults.
		"GENDER_FEMALE;#iiS=-1#iiH=-1#iiHw=-1#iiEw=-1#iiMs=-1#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0": "GENDER_FEMALE;#iiS=872349703#iiH=872349769#iiHw=872349736#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0",
		// Complete and unknown strings are untouched.
		"GENDER_FEMALE;#iiS=872349703#iiH=872349769#iiHw=872349736#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0": "GENDER_FEMALE;#iiS=872349703#iiH=872349769#iiHw=872349736#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0",
		"something else": "something else",
	}
	for in, want := range cases {
		if got := repairedCaptainDisplayInfo(in); got != want {
			t.Errorf("repairedCaptainDisplayInfo(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	if got := normalizedCaptainDisplayInfo(""); got != defaultCaptainDisplayInfo || repairedCaptainDisplayInfo(got) != got {
		t.Errorf("the default captain must need no repair: %q", got)
	}
}

// The hangar's own portrait (UI_Persistent_PlayerCard) is the live captain's
// HEAD item icon: GetPlayerUiData -> 0x140420340 -> captain->GetCaptainIconPath
// (0x1403006a0) reads the item in appearance slot [0] (captain+0xF8, the iiH
// key) and returns its YItemUIData.m_icon (+0x60); an empty path shows the
// fallback Head_Female_B02 icon (0x14041E3B0). So the only thing the server
// controls is that the iiH it sends is a real head item. Every default the
// server puts in a captain, and every head the client may have saved, must
// resolve to a Head_* asset in the item catalog.
func TestCaptainHeadsAreHeadItems(t *testing.T) {
	for gender, slots := range captainSlotDefaults {
		id, err := strconv.ParseInt(slots["iiH"], 10, 32)
		if err != nil {
			t.Fatalf("%s default head %q is not an item id", gender, slots["iiH"])
		}
		item, ok := dreadconfig.ItemByID(int32(id))
		if !ok {
			t.Errorf("%s default head %d is not in the item catalog", gender, id)
			continue
		}
		if !strings.Contains(item.AssetPath, "/Characters/Heads/Head_") {
			t.Errorf("%s default head %d is %q, not a head item", gender, id, item.AssetPath)
		}
	}
	for _, info := range []string{defaultCaptainDisplayInfo} {
		_, rest, _ := strings.Cut(info, ";")
		for _, f := range strings.Split(rest, "#") {
			if v, ok := strings.CutPrefix(f, "iiH="); ok {
				id, _ := strconv.ParseInt(v, 10, 32)
				item, found := dreadconfig.ItemByID(int32(id))
				if !found || !strings.Contains(item.AssetPath, "/Characters/Heads/Head_") {
					t.Errorf("default captain head %d is not a head item (%q)", id, item.AssetPath)
				}
			}
		}
	}
}
