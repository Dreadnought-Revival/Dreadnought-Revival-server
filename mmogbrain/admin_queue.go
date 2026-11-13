package main

// The matchmaking queue as the dashboard's Queue page shows it. The source is
// the queue_entries table: the matchmaker (matchmaker.go) reads and writes it
// and keeps no other queue in memory; an entry is 'waiting' until the
// matchmaker places it, 'matched' while its match is being started (it goes
// back to 'waiting' if the start fails). Matches still 'forming' are listed
// with their slots. Read-only: no action exists on the queue server-side.

import (
	"net/http"
	"sort"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminQueue) }

func registerAdminQueue(api *mux.Router) {
	api.HandleFunc("/queue", adminAPIQueue).Methods(http.MethodGet)
}

type adminQueueEntry struct {
	ID        string `json:"id"`
	PID       string `json:"pid"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	FleetType int    `json:"fleet_type"`
	TierMin   int    `json:"tier_min"`
	Party     string `json:"party,omitempty"`
	PartySize int    `json:"party_size"`
	Since     string `json:"since"`
	Status    string `json:"status"`
}

type adminQueueBucket struct {
	Mode      string `json:"mode"`
	FleetType int    `json:"fleet_type"`
	Waiting   int    `json:"waiting"`
	Oldest    string `json:"oldest"`
}

func adminAPIQueue(w http.ResponseWriter, _ *http.Request) {
	database := currentMmogPlayerStateDB()
	entries := []*adminQueueEntry{}
	buckets := []*adminQueueBucket{}
	forming := []map[string]any{}
	if database == nil {
		writeAdminJSON(w, map[string]any{"entries": entries, "totals": buckets, "forming": forming, "waiting": 0})
		return
	}
	// Names joined in the one query: the store has ONE connection.
	rows, err := database.Query(`SELECT q.id, q.user_id, COALESCE(p.display_name,''), q.game_mode, q.fleet_type, q.tier_min,
		COALESCE(q.party_id,''), q.queued_at, q.status
		FROM queue_entries q LEFT JOIN player_state p ON p.user_id=q.user_id
		WHERE q.status IN ('waiting','matched') ORDER BY q.queued_at, q.id LIMIT 500`)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		e := &adminQueueEntry{}
		if rows.Scan(&e.ID, &e.PID, &e.Name, &e.Mode, &e.FleetType, &e.TierMin, &e.Party, &e.Since, &e.Status) == nil {
			e.Name = adminShownName(e.Name)
			entries = append(entries, e)
		}
	}
	_ = rows.Close()

	parties := map[string]int{}
	byBucket := map[[2]any]*adminQueueBucket{}
	waiting := 0
	for _, e := range entries {
		if e.Party != "" {
			parties[e.Party]++
		}
		if e.Status != "waiting" {
			continue
		}
		waiting++
		k := [2]any{e.Mode, e.FleetType}
		b := byBucket[k]
		if b == nil {
			b = &adminQueueBucket{Mode: e.Mode, FleetType: e.FleetType, Oldest: e.Since}
			byBucket[k] = b
			buckets = append(buckets, b)
		}
		b.Waiting++
	}
	for _, e := range entries {
		e.PartySize = 1
		if e.Party != "" {
			e.PartySize = parties[e.Party]
		}
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Waiting != buckets[j].Waiting {
			return buckets[i].Waiting > buckets[j].Waiting
		}
		return buckets[i].Mode < buckets[j].Mode
	})

	type formingMatch struct {
		id, mode, mapName, created string
		fleet                      int
	}
	var fm []formingMatch
	mrows, err := database.Query(`SELECT id, game_mode, map, created_at, COALESCE(fleet_type,0) FROM matches WHERE status='forming' ORDER BY created_at DESC LIMIT 50`)
	if err == nil {
		for mrows.Next() {
			var f formingMatch
			if mrows.Scan(&f.id, &f.mode, &f.mapName, &f.created, &f.fleet) == nil {
				fm = append(fm, f)
			}
		}
		_ = mrows.Close()
	}
	slots := map[string][]map[string]any{}
	if len(fm) > 0 {
		srows, err := database.Query(`SELECT s.match_id, s.user_id, COALESCE(p.display_name,''), s.team FROM match_slots s
			JOIN matches m ON m.id=s.match_id AND m.status='forming' LEFT JOIN player_state p ON p.user_id=s.user_id ORDER BY s.team, s.joined_at LIMIT 1000`)
		if err == nil {
			for srows.Next() {
				var mid, pid, name string
				var team int
				if srows.Scan(&mid, &pid, &name, &team) == nil {
					slots[mid] = append(slots[mid], map[string]any{"pid": pid, "name": adminShownName(name), "team": team})
				}
			}
			_ = srows.Close()
		}
	}
	for _, f := range fm {
		pl := slots[f.id]
		if pl == nil {
			pl = []map[string]any{}
		}
		forming = append(forming, map[string]any{"id": f.id, "mode": f.mode, "map": adminMapName(f.mapName), "created": f.created, "fleet_type": f.fleet, "players": pl})
	}
	writeAdminJSON(w, map[string]any{"entries": entries, "totals": buckets, "forming": forming, "waiting": waiting})
}
