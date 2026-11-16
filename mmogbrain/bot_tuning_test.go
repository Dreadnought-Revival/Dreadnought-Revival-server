package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/sirupsen/logrus"
)

func battleBots(t *testing.T, mode, remote string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/battle/bots?mode="+mode, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	battleBotsHandler(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// A mode's bots are the original game's until the dashboard changes them; the
// battle server reads the stored values, and only from loopback.
func TestBotTuning(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	log := logrus.New()
	mm := matchmaker.New(database, log, "http://127.0.0.1:1", "", 2)
	old := activeMatchmaker
	activeMatchmaker = mm
	t.Cleanup(func() { activeMatchmaker = old })

	if code, _ := battleBots(t, "TDM", "203.0.113.9:4000"); code != http.StatusForbidden {
		t.Fatalf("from outside: %d, want 403", code)
	}
	if code, body := battleBots(t, "TDM", "127.0.0.1:4000"); code != http.StatusOK || body != "100 100 100" {
		t.Fatalf("nothing stored: %d %q, want the original values", code, body)
	}

	r := adminTestRouter()
	for body, want := range map[string]int{
		`{"bot_tuning":{"Nope":{"aim":50,"reaction":50,"abilities":150}}}`:       http.StatusBadRequest,
		`{"bot_tuning":{"CustomGame":{"aim":50,"reaction":50,"abilities":150}}}`: http.StatusBadRequest,
		`{"bot_tuning":{"PVE":{"aim":50,"reaction":50,"abilities":150}}}`:        http.StatusBadRequest,
		`{"bot_tuning":{"TDM":{"aim":-1,"reaction":50,"abilities":150}}}`:        http.StatusBadRequest,
		`{"bot_tuning":{"TDM":{"aim":50,"reaction":0,"abilities":150}}}`:         http.StatusBadRequest,
		`{"bot_tuning":{"TDM":{"aim":50,"reaction":50,"abilities":501}}}`:        http.StatusBadRequest,
		`{"bot_tuning":{"TDM":{"aim":50}}}`:                                      http.StatusBadRequest, // reaction 0
	} {
		if rec := adminPost(r, "/admin/api/modes", "test-admin-key", body); rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}

	rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"bot_tuning":{"TDM":{"aim":50,"reaction":60,"abilities":150}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if _, body := battleBots(t, "TDM", "127.0.0.1:4000"); body != "50 60 150" {
		t.Errorf("TDM after the save: %q, want 50 60 150", body)
	}
	if _, body := battleBots(t, "Onslaught", "127.0.0.1:4000"); body != "100 100 100" {
		t.Errorf("another mode changed with it: %q", body)
	}

	// The page shows it on the queue hosted as TDM, and on no lobby or PvE card.
	var out struct {
		Queues []struct {
			Queue string `json:"queue"`
			Runs  string `json:"runs"`
			Bots  *struct {
				Aim, Reaction, Abilities int
				Dashboard                bool
			} `json:"bots"`
		} `json:"queues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, q := range out.Queues {
		switch {
		case q.Queue == "TDM":
			seen = true
			if q.Bots == nil || q.Bots.Aim != 50 || q.Bots.Reaction != 60 || q.Bots.Abilities != 150 || !q.Bots.Dashboard {
				t.Errorf("TDM card bots = %+v", q.Bots)
			}
		case q.Queue == "Onslaught":
			if q.Bots == nil || q.Bots.Aim != 100 || q.Bots.Dashboard {
				t.Errorf("Onslaught card bots = %+v, want the original values", q.Bots)
			}
		case q.Queue == matchmaker.CustomGameClientName || q.Queue == "PVE":
			if q.Bots != nil {
				t.Errorf("%s card has bot tuning", q.Queue)
			}
		}
	}
	if !seen {
		t.Fatal("no TDM queue listed")
	}

	// The original values, or null, hand the mode back.
	if rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"bot_tuning":{"TDM":{"aim":100,"reaction":100,"abilities":100}}}`); rec.Code != http.StatusOK {
		t.Fatalf("back to the original: %d", rec.Code)
	}
	if _, set := botTuningFor(database, "TDM"); set {
		t.Error("the original values are still stored as a dashboard setting")
	}
	_ = adminPost(r, "/admin/api/modes", "test-admin-key", `{"bot_tuning":{"TDM":{"aim":0,"reaction":10,"abilities":500}}}`)
	if rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"bot_tuning":{"TDM":null}}`); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	if _, body := battleBots(t, "TDM", "127.0.0.1:4000"); body != "100 100 100" {
		t.Errorf("after the reset: %q", body)
	}
}
