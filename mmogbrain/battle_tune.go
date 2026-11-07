package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sirupsen/logrus"
)

// Balancing on the battle servers (issue #16 step 2, 2026-11-07).
//
// A battle server never logs in, so it never gets the tune reply. At map load
// its YTuneManager::LoadBackupDataTablesFromAssets (0x3C83D0) turns each
// cooked data table into JSON text (UDataTable::GetTableAsJSON, 0x18F69C0),
// parses it and keeps the parse as its tune data -- the slots the client's
// tune reply fills (weapons +0xD0, ...; verified in the decompile). The
// battle-server mod hooks that JSON step: it asks GET /battle/tune for the
// live balancing version and writes its values into the table's JSON before
// the game parses it, so the host and an "apply" client run the same version.
//
// The reply is text, one change a line, tab-separated:
//
//	version	1.0.<n>
//	<data table>	<row>	<field>	<n|b|s>	<value>
//
// with the host's own table names (DN_Weapons_OTS_DT, ...). A version of
// "none" (nothing published, or battle servers switched off on the
// dashboard) means: keep the game's values.

// tuneHostTables maps an editor table to the data table the battle server
// exports to JSON.
var tuneHostTables = map[string]string{
	"WeaponsTune":     "DN_Weapons_OTS_DT",
	"ProjectilesTune": "DN_Projectile_OTS_DT",
	"OfficersTune":    "DN_Officers_OTS_DT",
}

// tuneBattleServers: whether battle servers apply the live version (on by
// default; server_flags tune_battle_servers = "0" turns it off).
var tuneBattleServers struct {
	loaded atomic.Bool
	off    atomic.Bool
}

func tuneOnBattleServers() bool {
	if !tuneBattleServers.loaded.Load() {
		if database := currentMmogPlayerStateDB(); database != nil {
			var v string
			if database.QueryRow(`SELECT value FROM server_flags WHERE name='tune_battle_servers'`).Scan(&v) == nil {
				tuneBattleServers.off.Store(v == "0")
			}
		}
		tuneBattleServers.loaded.Store(true)
	}
	return !tuneBattleServers.off.Load()
}

// battleTuneText is the /battle/tune reply.
func battleTuneText() string {
	loadTuneEditor()
	tuneEditor.mu.RLock()
	id := tuneEditor.versionID
	changes := sortedTuneChanges(tuneEditor.published)
	tuneEditor.mu.RUnlock()
	if id == 0 || len(changes) == 0 || !tuneOnBattleServers() {
		return "version\tnone\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "version\t1.0.%d\n", id)
	for _, c := range changes {
		table, ok := tuneHostTables[c.Table]
		if !ok {
			continue
		}
		kind, value := "s", fmt.Sprint(c.Value)
		switch v := c.Value.(type) {
		case json.Number:
			kind, value = "n", v.String()
		case float64:
			kind, value = "n", strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			kind, value = "b", strconv.FormatBool(v)
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", table, c.Row, c.Field, kind, value)
	}
	return b.String()
}

// battleTuneHandler answers the battle-server mod (loopback only, like the
// other /battle/ routes).
func battleTuneHandler(w http.ResponseWriter, r *http.Request) {
	if refuseUnlessLoopback(w, r) {
		return
	}
	text := battleTuneText()
	lines := strings.Count(text, "\n") - 1
	logrus.WithFields(logrus.Fields{"version": strings.TrimPrefix(strings.SplitN(text, "\n", 2)[0], "version\t"),
		"changes": lines}).Info("battle tune: served")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	_, _ = w.Write([]byte(text))
}

// adminAPITuneBattleServers switches applying the live version on battle
// servers on or off ({"on": bool}); from the next match on.
func adminAPITuneBattleServers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.On == nil {
		writeAdminError(w, http.StatusBadRequest, `send {"on": true|false}`)
		return
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	v := "1"
	if !*req.On {
		v = "0"
	}
	if _, err := database.Exec(`INSERT OR REPLACE INTO server_flags(name,value) VALUES('tune_battle_servers',?)`, v); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not save")
		return
	}
	tuneBattleServers.off.Store(!*req.On)
	tuneBattleServers.loaded.Store(true)
	adminAudit(database, r, "tune battle servers", "", map[string]any{"on": *req.On})
	adminAPITune(w, r)
}

// tuneHostTableNames lists the tables the battle servers apply (for the page).
func tuneHostTableNames() []string {
	out := []string{}
	for t := range tuneHostTables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
