package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The three game-config pages (modes, messages, market): every route needs
// the key, answers within 3 s on the one-connection store, audits its
// actions and refuses bad input.
func TestAdminGameCfgRoutesNeedTheKey(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	r := adminTestRouter()
	for _, path := range []string{"/admin/api/modes", "/admin/api/messages", "/admin/api/market"} {
		for _, key := range []string{"", "wrong"} {
			if rec := adminGet(r, path, key); rec.Code != http.StatusForbidden {
				t.Errorf("GET %s key %q: %d, want 403", path, key, rec.Code)
			}
		}
	}
	for _, path := range []string{"/admin/api/modes", "/admin/api/messages/send", "/admin/api/messages/motd", "/admin/api/market/prices", "/admin/api/market/sales", "/admin/api/market/sales/x/end"} {
		if rec := adminPost(r, path, "wrong", `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s with a wrong key: %d, want 403", path, rec.Code)
		}
	}
}

func TestAdminGameCfgDoesNotDeadlockTheStore(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanMarketPricing(t)
	now := time.Now().UTC()
	for i, mode := range []string{"TDM", "TDM", "TE"} {
		if _, err := database.Exec(`INSERT INTO matches(id,game_mode,map,created_at,battle_match_id) VALUES(?,?,?,?,?)`,
			"gc"+string(rune('a'+i)), mode, "Glacier", now.Add(-time.Hour).Format(time.RFC3339), "gcb"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO matches(id,game_mode,map,created_at,battle_match_id) VALUES('gcold','TDM','Glacier',?, 'gcbold')`,
		now.Add(-10*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	r := adminTestRouter()
	for _, path := range []string{"/admin/api/modes", "/admin/api/messages", "/admin/api/market"} {
		done := make(chan *http.Response, 1)
		var body string
		go func() {
			rec := adminGet(r, path, "test-admin-key")
			body = rec.Body.String()
			done <- &http.Response{StatusCode: rec.Code}
		}()
		select {
		case resp := <-done:
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: %d", path, resp.StatusCode)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not answer in 3 s: the single-connection store is deadlocked", path)
		}
		if path == "/admin/api/modes" {
			var d struct {
				M map[string]int `json:"matches_7d"`
			}
			if err := json.Unmarshal([]byte(body), &d); err != nil || d.M["TDM"] != 2 || d.M["TE"] != 1 {
				t.Errorf("matches_7d = %v (err %v), want TDM 2, TE 1 (the 10-day-old match is out)", d.M, err)
			}
		}
	}
}

func TestAdminGameCfgValidationAndAudit(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanMarketPricing(t)
	r := adminTestRouter()
	key := "test-admin-key"
	bad := map[string][]string{
		"/admin/api/modes":         {`{"team_sizes":{"TDM":0}}`, `{"team_sizes":{"Nope":3}}`, `{"escalation_queue":"Nope"}`, `{"reset":["bogus"]}`},
		"/admin/api/messages/send": {`{"text":""}`, `{"text":"` + strings.Repeat("x", 301) + `"}`},
		"/admin/api/messages/motd": {`{"text":"` + strings.Repeat("x", 301) + `"}`},
		"/admin/api/market/prices": {`{"vanity":"abc"}`, `{"vanity":0}`, `{"nope":5}`},
		"/admin/api/market/sales":  {`{"name":"","percent":10,"scope":"all","end":"2999-01-01T00:00:00Z"}`, `{"name":"x","percent":95,"scope":"all","end":"2999-01-01T00:00:00Z"}`, `{"name":"x","percent":10,"scope":"all","end":"2000-01-01T00:00:00Z"}`},
	}
	for path, bodies := range bad {
		for _, b := range bodies {
			if rec := adminPost(r, path, key, b); rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s %s: %d, want 400", path, b, rec.Code)
			}
		}
	}
	count := func(action string) int {
		var n int
		_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action=?`, action).Scan(&n)
		return n
	}
	if n := count("market prices") + count("market sale") + count("server message") + count("message of the day") + count("game modes"); n != 0 {
		t.Fatalf("refused requests wrote %d audit rows", n)
	}
	// Bulk reset = every overridden key sent as null: one audit row.
	if rec := adminPost(r, "/admin/api/market/prices", key, `{"vanity":150,"coating":99}`); rec.Code != http.StatusOK {
		t.Fatalf("set prices: %d", rec.Code)
	}
	if rec := adminPost(r, "/admin/api/market/prices", key, `{"vanity":null,"coating":null}`); rec.Code != http.StatusOK {
		t.Fatalf("reset prices: %d", rec.Code)
	}
	if n := count("market prices"); n != 2 {
		t.Errorf("market prices audit rows = %d, want 2", n)
	}
	var left int
	_ = database.QueryRow(`SELECT COUNT(*) FROM market_price_overrides`).Scan(&left)
	if left != 0 {
		t.Errorf("%d overrides left after the bulk reset", left)
	}
	if rec := adminPost(r, "/admin/api/messages/motd", key, `{"text":"hello"}`); rec.Code != http.StatusOK || count("message of the day") != 1 {
		t.Errorf("motd: %d, audit rows %d", rec.Code, count("message of the day"))
	}
	if rec := adminPost(r, "/admin/api/messages/motd", key, `{"text":""}`); rec.Code != http.StatusOK {
		t.Errorf("motd off: %d", rec.Code)
	}
}
