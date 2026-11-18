package protocol

import "testing"

// A custom room create as the client sends it (logged 2026-11-04..17:
// "InitialSettings|Setting|ID&|Value|8.000000|..."): an array of Setting
// objects, ID a 1-byte int (tag 0x26), Value a string.
func TestIDValuePairsCustomRoomSettings(t *testing.T) {
	var b []byte
	var stack []int
	b = AppendStringField(b, "RT", "YA_CustomRoomCreate")
	b = AppendStringField(b, "GameMode", "TDM")
	b, stack = AppendArrayStart(b, stack, "InitialSettings")
	for id, v := range []string{"8.000000", "1.000000", "0.000000"} {
		b, stack = AppendObjectStart(b, stack, "Setting")
		b = append(b, 2, 'I', 'D', 0x26, byte(id))
		b = AppendStringField(b, "Value", v)
		b, stack = AppendObjectEnd(b, stack)
	}
	b, _ = AppendObjectEnd(b, stack)
	got := IDValuePairs(b, "ID", "Value")
	want := map[int]string{0: "8.000000", 1: "1.000000", 2: "0.000000"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
