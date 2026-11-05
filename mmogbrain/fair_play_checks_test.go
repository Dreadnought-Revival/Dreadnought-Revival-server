package main

import (
	"net/url"
	"testing"
)

func TestFairPlayCheckStats(t *testing.T) {
	q, _ := url.ParseQuery("chk_spd=7.540.880.410.312,3.300.350.0.0,bad,99.1.1.1.1&chk_jumps=2" +
		"&chk_dmg_measured=877000&chk_dmg_hits=950&chk_recv=120000&chk_recv_raw=150000" +
		"&chk_recv_measured=118000&chk_recv_hits=300&chk_flags=speed.received.")
	stats, flags := fairPlayCheckStats(q)
	want := map[string]int64{
		"check_jumps": 2, "check_damage_measured": 877000, "check_damage_hits": 950,
		"check_received": 120000, "check_received_raw": 150000, "check_received_measured": 118000,
		"check_received_hits": 300,
		"check_speed_c7":      540, "check_speed_c7_fastest": 880, "check_speed_c7_bots": 410, "check_speed_c7_bot_samples": 312,
		"check_speed_c3": 300, "check_speed_c3_fastest": 350, "check_speed_c3_bots": 0, "check_speed_c3_bot_samples": 0,
		"check_flag_speed": 1, "check_flag_received": 1,
	}
	if len(stats) != len(want) {
		t.Fatalf("stats = %v", stats)
	}
	for k, v := range want {
		if stats[k] != v {
			t.Errorf("%s = %d, want %d", k, stats[k], v)
		}
	}
	if len(flags) != 2 || flags[0] != "speed" || flags[1] != "received" {
		t.Errorf("flags = %v", flags)
	}

	// An older DLL sends no checks: nothing is stored.
	q, _ = url.ParseQuery("kills=3")
	if stats, flags := fairPlayCheckStats(q); len(stats) != 0 || flags != nil {
		t.Errorf("without checks: %v %v", stats, flags)
	}
}
