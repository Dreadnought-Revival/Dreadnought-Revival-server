package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
	"github.com/gorilla/mux"
)

// The balancing editor on the admin dashboard (issue #16 step 3): change
// fields of the game's own tune tables, keep them as a draft, publish the
// draft as a numbered tune version, restore an earlier version.
//
// Only the tables whose shape the client is known to read are editable
// (tune_reply.go): WeaponsTune, ProjectilesTune and OfficersTune, which are
// the client's own cooked tables. A value must have the type of the game's
// value for that field.
//
// A published version goes into the tune reply ("1.0.<n>"), which reaches
// only accounts in tune test mode "apply" (or everyone with DN_TUNE_SEND=1).
// Battle servers keep their own tables until #16 step 2, so a change shows in
// the client (stats panels) but does not yet change what happens in a match.

var tuneEditableTables = []string{"WeaponsTune", "ProjectilesTune", "OfficersTune"}

func tuneEditableTable(name string) bool {
	for _, t := range tuneEditableTables {
		if t == name {
			return true
		}
	}
	return false
}

// tuneOriginalRows is a table as the game ships it: row name -> field -> value
// (json.Number, bool, string, or nested).
var tuneOriginal = sync.OnceValue(func() map[string]map[string]map[string]any {
	src := map[string]string{
		"WeaponsTune":     dreadconfig.WeaponsTuneJSON(),
		"ProjectilesTune": dreadconfig.ProjectilesTuneJSON(),
		"OfficersTune":    dreadconfig.OfficersTuneJSON(),
	}
	out := map[string]map[string]map[string]any{}
	for table, js := range src {
		dec := json.NewDecoder(strings.NewReader(js))
		dec.UseNumber()
		var rows []map[string]any
		_ = dec.Decode(&rows)
		m := map[string]map[string]any{}
		for _, r := range rows {
			if n, _ := r["Name"].(string); n != "" {
				m[n] = r
			}
		}
		out[table] = m
	}
	return out
})

// tuneChange is one field's value set in the editor.
type tuneChange struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Field string `json:"field"`
	Value any    `json:"value"`
}

func tuneChangeKey(table, row, field string) string { return table + "\x00" + row + "\x00" + field }

var tuneEditor struct {
	once      sync.Once
	mu        sync.RWMutex
	published map[string]tuneChange // the live version's changes
	versionID int64                 // 0 = none published
	tables    map[string]string     // tuned table JSON for the live version, by table
}

// loadTuneEditor reads the live version at startup (main) -- the tune reply
// is built inside a request, where a query could deadlock the one
// connection. Lazy for tests.
func loadTuneEditor() {
	tuneEditor.once.Do(func() {
		published := map[string]tuneChange{}
		var id int64
		if database := currentMmogPlayerStateDB(); database != nil {
			var snapshot string
			if database.QueryRow(`SELECT id, changes FROM tune_versions ORDER BY id DESC LIMIT 1`).Scan(&id, &snapshot) == nil {
				for _, c := range decodeTuneChanges(snapshot) {
					published[tuneChangeKey(c.Table, c.Row, c.Field)] = c
				}
			}
		}
		setLiveTuneVersion(id, published)
	})
}

func setLiveTuneVersion(id int64, published map[string]tuneChange) {
	tables := map[string]string{}
	for _, t := range tuneEditableTables {
		tables[t] = tunedTableJSON(t, published)
	}
	tuneEditor.mu.Lock()
	tuneEditor.versionID, tuneEditor.published, tuneEditor.tables = id, published, tables
	tuneEditor.mu.Unlock()
}

// tunedTableJSON is a table with the changes applied, as the reply sends it.
func tunedTableJSON(table string, changes map[string]tuneChange) string {
	orig := tuneOriginal()[table]
	names := make([]string, 0, len(orig))
	for n := range orig {
		names = append(names, n)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, n := range names {
		row := make(map[string]any, len(orig[n]))
		for k, v := range orig[n] {
			row[k] = v
		}
		for _, c := range changes {
			if c.Table == table && c.Row == n {
				row[c.Field] = c.Value
			}
		}
		rows = append(rows, row)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// liveTuneTableJSON is what the reply sends for a table: the live version's
// table, or the game's own.
func liveTuneTableJSON(name, original string) string {
	if !tuneEditableTable(name) {
		return original
	}
	loadTuneEditor()
	tuneEditor.mu.RLock()
	defer tuneEditor.mu.RUnlock()
	if tuneEditor.versionID == 0 || len(tuneEditor.published) == 0 {
		return original
	}
	if js, ok := tuneEditor.tables[name]; ok {
		return js
	}
	return original
}

// liveTuneVersion is the version the reply carries in apply mode.
func liveTuneVersion() string {
	loadTuneEditor()
	tuneEditor.mu.RLock()
	defer tuneEditor.mu.RUnlock()
	return "1.0." + strconv.FormatInt(tuneEditor.versionID, 10)
}

func decodeTuneChanges(s string) []tuneChange {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var out []tuneChange
	_ = dec.Decode(&out)
	return out
}

// tuneValueKind is a value's type for validation: number, bool, string or other.
func tuneValueKind(v any) string {
	switch v.(type) {
	case json.Number, float64:
		return "number"
	case bool:
		return "bool"
	case string:
		return "string"
	}
	return "other"
}

// checkTuneChange validates one change against the game's row.
func checkTuneChange(c tuneChange) string {
	if !tuneEditableTable(c.Table) {
		return c.Table + " cannot be edited (only " + strings.Join(tuneEditableTables, ", ") + ")"
	}
	row, ok := tuneOriginal()[c.Table][c.Row]
	if !ok {
		return c.Table + " has no row " + c.Row
	}
	orig, ok := row[c.Field]
	if !ok || c.Field == "Name" {
		return c.Row + " has no field " + c.Field
	}
	kind := tuneValueKind(orig)
	if kind == "other" {
		return c.Field + " is not a plain value and cannot be edited here"
	}
	if tuneValueKind(c.Value) != kind {
		return fmt.Sprintf("%s.%s is a %s", c.Row, c.Field, kind)
	}
	if kind == "number" {
		f, err := strconv.ParseFloat(fmt.Sprint(c.Value), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 1e9 {
			return c.Row + "." + c.Field + ": not a usable number"
		}
		// A whole number in the game's table is read with the int accessor
		// (0x238000), which would cut 1.5 to 1.
		if !strings.ContainsAny(fmt.Sprint(orig), ".eE") && f != math.Trunc(f) {
			return c.Row + "." + c.Field + " is a whole number"
		}
	}
	if kind == "string" && utf8.RuneCountInString(c.Value.(string)) > 200 {
		return c.Row + "." + c.Field + ": too long"
	}
	return ""
}

// --- draft storage ------------------------------------------------------------------

func loadTuneDraft(database *sql.DB) (map[string]tuneChange, error) {
	out := map[string]tuneChange{}
	rows, err := database.Query(`SELECT table_name, row_name, field, value FROM tune_draft`)
	if err != nil {
		return nil, err
	}
	var all []tuneChange
	for rows.Next() {
		var c tuneChange
		var v string
		if rows.Scan(&c.Table, &c.Row, &c.Field, &v) == nil {
			dec := json.NewDecoder(strings.NewReader(v))
			dec.UseNumber()
			_ = dec.Decode(&c.Value)
			all = append(all, c)
		}
	}
	_ = rows.Close()
	for _, c := range all {
		out[tuneChangeKey(c.Table, c.Row, c.Field)] = c
	}
	return out, nil
}

func sortedTuneChanges(m map[string]tuneChange) []tuneChange {
	out := make([]tuneChange, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return tuneChangeKey(out[i].Table, out[i].Row, out[i].Field) < tuneChangeKey(out[j].Table, out[j].Row, out[j].Field)
	})
	return out
}

func sameTuneValue(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// --- admin API ---------------------------------------------------------------------

func registerAdminTuneEditor(api *mux.Router) {
	api.HandleFunc("/tune", adminAPITune).Methods(http.MethodGet)
	api.HandleFunc("/tune/table/{table}", adminAPITuneTable).Methods(http.MethodGet)
	api.HandleFunc("/tune/draft", adminAPITuneDraft).Methods(http.MethodPost)
	api.HandleFunc("/tune/publish", adminAPITunePublish).Methods(http.MethodPost)
	api.HandleFunc("/tune/discard", adminAPITuneDiscard).Methods(http.MethodPost)
	api.HandleFunc("/tune/restore", adminAPITuneRestore).Methods(http.MethodPost)
}

type tuneDiffRow struct {
	Table     string `json:"table"`
	Row       string `json:"row"`
	Field     string `json:"field"`
	Original  any    `json:"original"`
	Published any    `json:"published"` // nil = the game's value
	Draft     any    `json:"draft"`     // nil = the game's value
}

func adminTuneJSON(database *sql.DB) (map[string]any, error) {
	loadTuneEditor()
	draft, err := loadTuneDraft(database)
	if err != nil {
		return nil, err
	}
	tuneEditor.mu.RLock()
	published := tuneEditor.published
	versionID := tuneEditor.versionID
	tuneEditor.mu.RUnlock()

	keys := map[string]bool{}
	for k := range draft {
		keys[k] = true
	}
	for k := range published {
		keys[k] = true
	}
	diff := []tuneDiffRow{}
	pending := 0
	for k := range keys {
		var c tuneChange
		if d, ok := draft[k]; ok {
			c = d
		} else {
			c = published[k]
		}
		d := tuneDiffRow{Table: c.Table, Row: c.Row, Field: c.Field, Original: tuneOriginal()[c.Table][c.Row][c.Field]}
		if p, ok := published[k]; ok {
			d.Published = p.Value
		}
		if dv, ok := draft[k]; ok {
			d.Draft = dv.Value
		}
		if !sameTuneValue(d.Published, d.Draft) {
			pending++
		}
		diff = append(diff, d)
	}
	sort.Slice(diff, func(i, j int) bool {
		return tuneChangeKey(diff[i].Table, diff[i].Row, diff[i].Field) < tuneChangeKey(diff[j].Table, diff[j].Row, diff[j].Field)
	})

	versions := []map[string]any{}
	if rows, err := database.Query(`SELECT id, created_at, actor, note, changes FROM tune_versions ORDER BY id DESC LIMIT 20`); err == nil {
		for rows.Next() {
			var id int64
			var at, actor, note, changes string
			if rows.Scan(&id, &at, &actor, &note, &changes) == nil {
				versions = append(versions, map[string]any{"id": id, "version": "1.0." + strconv.FormatInt(id, 10),
					"at": at, "actor": actor, "note": note, "changes": len(decodeTuneChanges(changes)), "live": id == versionID})
			}
		}
		_ = rows.Close()
	}
	tables := []map[string]any{}
	for _, t := range []string{"WeaponsTune", "ProjectilesTune", "OfficersTune", "AbilitiesTune", "FeatsTune", "GameModifiersTune", "BattleReadyTune", "HavocTune"} {
		n := len(tuneOriginal()[t])
		tables = append(tables, map[string]any{"name": t, "rows": n, "editable": tuneEditableTable(t)})
	}
	loadTuneTestAccounts()
	tuneTestAccounts.mu.RLock()
	accounts := []map[string]any{}
	for pid, mode := range tuneTestAccounts.mode {
		accounts = append(accounts, map[string]any{"pid": pid, "mode": mode})
	}
	tuneTestAccounts.mu.RUnlock()
	sort.Slice(accounts, func(i, j int) bool { return accounts[i]["pid"].(string) < accounts[j]["pid"].(string) })
	for _, a := range accounts {
		a["name"] = adminPlayerName(a["pid"].(string))
	}
	live := "none (the game's own values)"
	if versionID > 0 {
		live = "1.0." + strconv.FormatInt(versionID, 10)
	}
	return map[string]any{"live": live, "live_id": versionID, "pending": pending, "diff": diff, "versions": versions,
		"tables": tables, "test_accounts": accounts, "everyone": tuneSendToEveryone()}, nil
}

func tuneSendToEveryone() bool { return strings.TrimSpace(getenv("DN_TUNE_SEND", "")) == "1" }

func adminAPITune(w http.ResponseWriter, _ *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	out, err := adminTuneJSON(database)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, out)
}

// adminAPITuneTable lists rows of a table matching q (by row name), up to
// 30, with every field's game, published and draft value.
func adminAPITuneTable(w http.ResponseWriter, r *http.Request) {
	table := mux.Vars(r)["table"]
	orig, ok := tuneOriginal()[table]
	if !ok {
		writeAdminError(w, http.StatusNotFound, "no editable table "+table)
		return
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	draft, err := loadTuneDraft(database)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	loadTuneEditor()
	tuneEditor.mu.RLock()
	published := tuneEditor.published
	tuneEditor.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	names := []string{}
	for n := range orig {
		if q == "" || strings.Contains(strings.ToLower(n), q) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	total := len(names)
	if len(names) > 30 {
		names = names[:30]
	}
	type field struct {
		Field     string `json:"field"`
		Kind      string `json:"kind"`
		Original  any    `json:"original"`
		Published any    `json:"published"`
		Draft     any    `json:"draft"`
	}
	type row struct {
		Name   string  `json:"name"`
		Fields []field `json:"fields"`
	}
	rows := []row{}
	for _, n := range names {
		keys := make([]string, 0, len(orig[n]))
		for k := range orig[n] {
			if k != "Name" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		x := row{Name: n}
		for _, k := range keys {
			f := field{Field: k, Kind: tuneValueKind(orig[n][k]), Original: orig[n][k]}
			if p, ok := published[tuneChangeKey(table, n, k)]; ok {
				f.Published = p.Value
			}
			if d, ok := draft[tuneChangeKey(table, n, k)]; ok {
				f.Draft = d.Value
			}
			x.Fields = append(x.Fields, f)
		}
		rows = append(rows, x)
	}
	writeAdminJSON(w, map[string]any{"table": table, "editable": tuneEditableTable(table), "total": total, "rows": rows})
}

// adminAPITuneDraft sets draft values: {changes: [{table,row,field,value}]};
// a value of null removes the field from the draft (back to the game's).
func adminAPITuneDraft(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	var req struct {
		Changes []tuneChange `json:"changes"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil || len(req.Changes) == 0 || len(req.Changes) > 2000 {
		writeAdminError(w, http.StatusBadRequest, "send 1 to 2000 changes")
		return
	}
	for _, c := range req.Changes {
		if c.Value == nil {
			if !tuneEditableTable(c.Table) {
				writeAdminError(w, http.StatusBadRequest, c.Table+" cannot be edited")
				return
			}
			continue
		}
		if why := checkTuneChange(c); why != "" {
			writeAdminError(w, http.StatusBadRequest, why)
			return
		}
	}
	tx, err := database.Begin()
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range req.Changes {
		if c.Value == nil || sameTuneValue(c.Value, tuneOriginal()[c.Table][c.Row][c.Field]) {
			_, err = tx.Exec(`DELETE FROM tune_draft WHERE table_name=? AND row_name=? AND field=?`, c.Table, c.Row, c.Field)
		} else {
			v, _ := json.Marshal(c.Value)
			_, err = tx.Exec(`INSERT INTO tune_draft(table_name,row_name,field,value) VALUES(?,?,?,?)
				ON CONFLICT(table_name,row_name,field) DO UPDATE SET value=excluded.value, updated_at=datetime('now')`,
				c.Table, c.Row, c.Field, string(v))
		}
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "could not save the draft")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not save the draft")
		return
	}
	adminAPITune(w, r)
}

// adminAPITunePublish makes the draft the live tune version.
func adminAPITunePublish(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	req.Note = strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(req.Note) > 300 {
		writeAdminError(w, http.StatusBadRequest, "the note is 300 characters at most")
		return
	}
	draft, err := loadTuneDraft(database)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	loadTuneEditor()
	tuneEditor.mu.RLock()
	published := tuneEditor.published
	tuneEditor.mu.RUnlock()
	if len(draft) == len(published) {
		same := true
		for k, d := range draft {
			if p, ok := published[k]; !ok || !sameTuneValue(p.Value, d.Value) {
				same = false
				break
			}
		}
		if same {
			writeAdminError(w, http.StatusConflict, "the draft is the same as the live version")
			return
		}
	}
	for _, c := range draft {
		if why := checkTuneChange(c); why != "" {
			writeAdminError(w, http.StatusBadRequest, "the draft has an invalid value: "+why)
			return
		}
	}
	changes := sortedTuneChanges(draft)
	snapshot, _ := json.Marshal(changes)
	actor := r.RemoteAddr
	if i := strings.LastIndex(actor, ":"); i > 0 {
		actor = strings.Trim(actor[:i], "[]")
	}
	res, err := database.Exec(`INSERT INTO tune_versions(actor, note, changes) VALUES(?,?,?)`, actor, req.Note, string(snapshot))
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not publish")
		return
	}
	id, _ := res.LastInsertId()
	setLiveTuneVersion(id, draft)
	adminAudit(database, r, "tune publish", "", map[string]any{"version": "1.0." + strconv.FormatInt(id, 10),
		"changes": len(changes), "note": req.Note})
	adminAPITune(w, r)
}

// adminAPITuneDiscard puts the draft back to the live version.
func adminAPITuneDiscard(w http.ResponseWriter, r *http.Request) {
	loadTuneEditor()
	tuneEditor.mu.RLock()
	published := tuneEditor.published
	tuneEditor.mu.RUnlock()
	if !replaceTuneDraft(w, published) {
		return
	}
	adminAudit(currentMmogPlayerStateDB(), r, "tune draft discarded", "", nil)
	adminAPITune(w, r)
}

// adminAPITuneRestore puts an earlier version (or, with id 0, the game's own
// values) into the draft; publishing it makes it live again.
func adminAPITuneRestore(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.ID < 0 {
		writeAdminError(w, http.StatusBadRequest, "send the version id (0 = the game's own values)")
		return
	}
	changes := map[string]tuneChange{}
	if req.ID > 0 {
		var snapshot string
		if err := database.QueryRow(`SELECT changes FROM tune_versions WHERE id=?`, req.ID).Scan(&snapshot); err != nil {
			writeAdminError(w, http.StatusNotFound, "no such version")
			return
		}
		for _, c := range decodeTuneChanges(snapshot) {
			changes[tuneChangeKey(c.Table, c.Row, c.Field)] = c
		}
	}
	if !replaceTuneDraft(w, changes) {
		return
	}
	adminAudit(database, r, "tune draft restored", "", map[string]any{"from_version": req.ID})
	adminAPITune(w, r)
}

func replaceTuneDraft(w http.ResponseWriter, changes map[string]tuneChange) bool {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return false
	}
	tx, err := database.Begin()
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return false
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM tune_draft`); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not reset the draft")
		return false
	}
	for _, c := range changes {
		v, _ := json.Marshal(c.Value)
		if _, err := tx.Exec(`INSERT INTO tune_draft(table_name,row_name,field,value) VALUES(?,?,?,?)`, c.Table, c.Row, c.Field, string(v)); err != nil {
			writeAdminError(w, http.StatusInternalServerError, "could not reset the draft")
			return false
		}
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not reset the draft")
		return false
	}
	return true
}
