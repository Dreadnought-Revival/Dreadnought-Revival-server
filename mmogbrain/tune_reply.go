package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// The tune reply (YA_TuneReturn) for test accounts (issue #16, step 1).
//
// What YTuneManager::Set (0x3D5160-0x3D5527) does with it, read in the exe
// 2026-11-07:
//
//   - doc "Returning" -> "MetaData" -> "Version" (as a string). If the
//     version EQUALS the one the manager holds (+0x70; the client logs it as
//     "Requesting tuning values from mmog (version: 0.0.0)"), Set returns
//     without touching a table (0x3D535D -> 0x3D5510). Otherwise it stores
//     the version and copies every table node from Returning, then rebuilds
//     its lookup maps (0x3D2130) and marks server data loaded (+0x530).
//   - Tables by field (FName globals 0x3D1C048..0x3D1C0B8): WeaponsTune ->
//     +0xD0 -> map +0x3A0 (LoadWeaponRow 0x3CA770 -> 0x3BFD40 searches it),
//     BattleReadyTune -> +0x120 -> map +0x350, ProjectilesTune -> +0x170 (via
//     0x3D6340) -> map +0x3F0, AbilitiesTune -> +0x260, OfficersTune ->
//     +0x1C0 -> map +0x440, FeatsTune -> +0x210, HavocTune -> +0x2B0,
//     GameModifiersTune -> +0x300.
//   - The rebuild walks each table node's CHILDREN as an array of row
//     objects and keys them by each row's "Name". We sent each table as ONE
//     STRING of JSON text: no children, every map empty, every lookup failed
//     ("LoadWeaponRow() Weapon Data for '...' Couldn't be found", August).
//     And the rows were keyed "RowName".
//   - Row fields are read by name through the node accessors: 0x238000
//     (int: double, int32 or a numeric string), 0x238060 (double, same
//     kinds), 0x237EF0 (string). Bools go through the string accessor and
//     0x3CF3F0, which is true only for "1" or "True". So every scalar is
//     sent as a string, and a bool as "True"/"False".
//
// Modes per test account (dashboard, player panel):
//
//	check  version "0.0.0" = the client's own: Set parses the reply, logs
//	       "setting tune values (version: 0.0.0)" and changes NOTHING. Proves
//	       the reply is read, at no risk.
//	apply  version tuneApplyVersion: Set replaces the client's tables with
//	       ours. Weapons, projectiles and officers are the client's own
//	       cooked tables; abilities, feats and game modifiers are still our
//	       reshaped subsets (GUESS shape, their consumers are not traced), so
//	       abilities may misbehave for that account. Battle servers keep
//	       their own tables either way (#16 step 2).
//
// Everyone else gets the reply under the request name, which the client
// drops (the safe state since 2026-09-24). DN_TUNE_SEND=1 still sends
// "apply" to everyone.

const (
	tuneCheckVersion = "0.0.0"
	tuneApplyVersion = "1.0.0" // with no balancing version published (tune_editor.go)
)

var tuneTestAccounts struct {
	once sync.Once
	mu   sync.RWMutex
	mode map[string]string // pid -> check | apply
}

func loadTuneTestAccounts() {
	tuneTestAccounts.once.Do(func() {
		m := map[string]string{}
		if database := currentMmogPlayerStateDB(); database != nil {
			if rows, err := database.Query(`SELECT name, value FROM server_flags WHERE name LIKE 'tune\_test:%' ESCAPE '\'`); err == nil {
				for rows.Next() {
					var k, v string
					if rows.Scan(&k, &v) == nil && (v == "check" || v == "apply") {
						m[strings.TrimPrefix(k, "tune_test:")] = v
					}
				}
				_ = rows.Close()
			}
		}
		tuneTestAccounts.mu.Lock()
		tuneTestAccounts.mode = m
		tuneTestAccounts.mu.Unlock()
	})
}

// tuneTestMode is the account's tune test mode, "" for none.
func tuneTestMode(pid string) string {
	loadTuneTestAccounts()
	tuneTestAccounts.mu.RLock()
	defer tuneTestAccounts.mu.RUnlock()
	return tuneTestAccounts.mode[normalizedPlayerStatePID(pid)]
}

// buildMmogTuneReplyFor answers YA_Tune for one player.
func buildMmogTuneReplyFor(pid string) []byte {
	mode := tuneTestMode(pid)
	if mode == "" {
		return buildMmogTunePayload() // dropped by the client unless DN_TUNE_SEND=1 (see there)
	}
	version := liveTuneVersion() // tune_editor.go: "1.0.<live version>"
	if mode == "check" {
		version = tuneCheckVersion
	}
	doc := buildMmogTuneDocumentVersion(version)
	logrus.WithFields(logrus.Fields{"player": normalizedPlayerStatePID(pid), "mode": mode, "version": version,
		"bytes": len(doc)}).Warn("tune: sending YA_TuneReturn (test); the client should log \"setting tune values (version: " + version + ")\"")
	var b []byte
	b = protocol.AppendStringField(b, "RT", "YA_TuneReturn")
	return protocol.AppendBytesField(b, "packed", compressMmogDocument(doc))
}

// appendTuneTable writes one tune table as the client walks it: a named
// array of unnamed row objects, "Name" first, every field by its own name.
func appendTuneTable(b []byte, stack []int, name, rowsJSON string) ([]byte, []int) {
	dec := json.NewDecoder(strings.NewReader(rowsJSON))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		logrus.WithError(err).WithField("table", name).Warn("tune: table is not a JSON array of rows; sending it empty")
		rows = nil
	}
	b, stack = protocol.AppendArrayStart(b, stack, name)
	for _, row := range rows {
		b, stack = protocol.AppendUnnamedObjectStart(b, stack)
		if n, ok := row["Name"]; ok {
			b, stack = appendTuneValue(b, stack, "Name", n)
		}
		keys := make([]string, 0, len(row))
		for k := range row {
			if k != "Name" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			b, stack = appendTuneValue(b, stack, k, row[k])
		}
		b, stack = protocol.AppendObjectEnd(b, stack)
	}
	return protocol.AppendObjectEnd(b, stack)
}

// appendTuneValue writes one JSON value; name "" writes it unnamed (an
// array element).
func appendTuneValue(b []byte, stack []int, name string, v any) ([]byte, []int) {
	str := func(s string) []byte {
		if name == "" {
			return protocol.AppendUnnamedStringField(b, s)
		}
		return protocol.AppendStringField(b, name, s)
	}
	switch x := v.(type) {
	case map[string]any:
		if name == "" {
			b, stack = protocol.AppendUnnamedObjectStart(b, stack)
		} else {
			b, stack = protocol.AppendObjectStart(b, stack, name)
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b, stack = appendTuneValue(b, stack, k, x[k])
		}
		return protocol.AppendObjectEnd(b, stack)
	case []any:
		if name == "" {
			b, stack = protocol.AppendUnnamedArrayStart(b, stack)
		} else {
			b, stack = protocol.AppendArrayStart(b, stack, name)
		}
		for _, e := range x {
			b, stack = appendTuneValue(b, stack, "", e)
		}
		return protocol.AppendObjectEnd(b, stack)
	case bool:
		if x {
			return str("True"), stack
		}
		return str("False"), stack
	case json.Number:
		return str(x.String()), stack
	case float64:
		return str(strconv.FormatFloat(x, 'f', -1, 64)), stack
	case string:
		return str(x), stack
	case nil:
		return str(""), stack
	default:
		return str(fmt.Sprint(x)), stack
	}
}

// --- admin API ---------------------------------------------------------------------

func registerAdminTuneTest(api *mux.Router) {
	api.HandleFunc("/players/{pid}/tune-test", adminAPISetTuneTest).Methods(http.MethodPost)
}

// adminAPISetTuneTest sets an account's tune test mode ("" = off).
func adminAPISetTuneTest(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil ||
		(req.Mode != "" && req.Mode != "check" && req.Mode != "apply") {
		writeAdminError(w, http.StatusBadRequest, "mode must be \"\", check or apply")
		return
	}
	from := tuneTestMode(pid)
	var err error
	if req.Mode == "" {
		_, err = database.Exec(`DELETE FROM server_flags WHERE name=?`, "tune_test:"+pid)
	} else {
		_, err = database.Exec(`INSERT OR REPLACE INTO server_flags(name,value) VALUES(?,?)`, "tune_test:"+pid, req.Mode)
	}
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not save")
		return
	}
	tuneTestAccounts.mu.Lock()
	if req.Mode == "" {
		delete(tuneTestAccounts.mode, pid)
	} else {
		tuneTestAccounts.mode[pid] = req.Mode
	}
	tuneTestAccounts.mu.Unlock()
	adminAudit(database, r, "tune test", pid, map[string]any{"from": from, "to": req.Mode})
	adminAPIPlayerDetail(w, r)
}
