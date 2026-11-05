package main

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// Fair-play checks (2026-11-05, operator: "add a speed check ... and a damage
// received check, maybe the server has some damage counters wrong"). The
// battle-server mod measures each player's ship speed against the bots flying
// the same class, and the damage the player dealt and received through
// YPawn::TakeDamage against the player state's own counters, and reports both
// with the match result (battle-server-mod TakeFairPlayChecks):
//
//	chk_spd=class.sustained.fastest.bot_sustained.bot_samples,...  (units/s)
//	chk_jumps, chk_dmg_measured, chk_dmg_hits, chk_recv, chk_recv_raw,
//	chk_recv_shield, chk_recv_measured, chk_recv_hits, chk_flags=speed.dealt.
//
// Received damage is reported, not flagged: the first live match (2026-11-05)
// showed the player state's received counters count something else than the
// TakeDamage sum (0.65-0.76x and ~2x), while dealt matched to within 2. An
// older DLL's "received" flag is still read.
//
// Nothing here acts on a player: the figures are stored per match in
// battle_result_stats (stat names "check_..."), and a flagged result is logged
// as a warning. The mod's thresholds are GUESSES until real matches show what
// normal looks like.

// fairPlayCheckStats turns the mod's chk_ parameters into per-match stats,
// and returns the flags it raised ("" when none, or no checks were sent).
func fairPlayCheckStats(q url.Values) (map[string]int64, []string) {
	stats := map[string]int64{}
	if !q.Has("chk_flags") {
		return stats, nil
	}
	for param, stat := range map[string]string{
		"chk_jumps":         "check_jumps",
		"chk_dmg_measured":  "check_damage_measured",
		"chk_dmg_hits":      "check_damage_hits",
		"chk_recv":          "check_received",
		"chk_recv_raw":      "check_received_raw",
		"chk_recv_shield":   "check_received_shield",
		"chk_recv_measured": "check_received_measured",
		"chk_recv_hits":     "check_received_hits",
	} {
		if n, err := strconv.ParseInt(strings.TrimSpace(q.Get(param)), 10, 64); err == nil {
			stats[stat] = n
		}
	}
	for _, entry := range strings.Split(q.Get("chk_spd"), ",") {
		f := strings.Split(strings.TrimSpace(entry), ".")
		if len(f) != 5 {
			continue
		}
		var n [5]int64
		ok := true
		for i := range f {
			v, err := strconv.ParseInt(f[i], 10, 64)
			if err != nil || v < 0 {
				ok = false
				break
			}
			n[i] = v
		}
		if !ok || n[0] <= 0 || n[0] > 20 {
			continue
		}
		c := "check_speed_c" + strconv.FormatInt(n[0], 10)
		stats[c] = n[1]
		stats[c+"_fastest"] = n[2]
		stats[c+"_bots"] = n[3]
		stats[c+"_bot_samples"] = n[4]
	}
	var flags []string
	for _, f := range strings.Split(q.Get("chk_flags"), ".") {
		switch f = strings.TrimSpace(f); f {
		case "speed", "dealt", "received":
			flags = append(flags, f)
			stats["check_flag_"+f] = 1
		}
	}
	return stats, flags
}

// logFairPlayFlags warns about a flagged match result.
func logFairPlayFlags(match, pid string, flags []string, stats map[string]int64) {
	if len(flags) == 0 {
		return
	}
	logrus.WithFields(logrus.Fields{"match": match, "player": pid, "flags": flags, "checks": stats}).
		Warn("fair play: match result flagged (a measurement, not proof: see battle-server-mod TakeFairPlayChecks)")
}
