package main

// Admin notes on an account (table admin_player_notes, db/db.go): free text
// the player never sees. GET lists, POST adds, DELETE removes one; add and
// delete are audited.

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminPlayerNotes) }

func registerAdminPlayerNotes(api *mux.Router) {
	api.HandleFunc("/players/{pid}/notes", adminAPIPlayerNotes).Methods(http.MethodGet)
	api.HandleFunc("/players/{pid}/notes", adminAPIPlayerNoteAdd).Methods(http.MethodPost)
	api.HandleFunc("/players/{pid}/notes/{id}", adminAPIPlayerNoteDelete).Methods(http.MethodDelete)
}

const adminNoteMaxLen = 2000

type adminPlayerNote struct {
	ID    int64  `json:"id"`
	Note  string `json:"note"`
	Actor string `json:"actor"`
	At    string `json:"created_at"`
}

// adminPlayerNotes is the player's newest 200 notes. The result set is closed
// before it returns, so callers may query on.
func adminPlayerNotes(database *sql.DB, pid string) []adminPlayerNote {
	out := []adminPlayerNote{}
	adminProfileRows(database, `SELECT id, note, actor, created_at FROM admin_player_notes WHERE user_id=? ORDER BY id DESC LIMIT 200`, []any{pid}, func(rs *sql.Rows) {
		var n adminPlayerNote
		if rs.Scan(&n.ID, &n.Note, &n.Actor, &n.At) == nil {
			out = append(out, n)
		}
	})
	return out
}

// adminRequestActor is the caller's address, as adminAudit records it.
func adminRequestActor(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func adminAPIPlayerNotes(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	writeAdminJSON(w, map[string]any{"notes": adminPlayerNotes(database, pid)})
}

func adminAPIPlayerNoteAdd(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Note == "" {
		writeAdminError(w, http.StatusBadRequest, "the note is empty")
		return
	}
	if len([]rune(req.Note)) > adminNoteMaxLen {
		writeAdminError(w, http.StatusBadRequest, "a note is limited to "+strconv.Itoa(adminNoteMaxLen)+" characters")
		return
	}
	res, err := database.Exec(`INSERT INTO admin_player_notes(user_id,note,actor) VALUES(?,?,?)`, pid, req.Note, adminRequestActor(r))
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	preview := []rune(req.Note)
	if len(preview) > 200 {
		preview = preview[:200]
	}
	adminAudit(database, r, "player note", pid, map[string]any{"id": id, "note": string(preview)})
	writeAdminJSON(w, map[string]any{"id": id, "notes": adminPlayerNotes(database, pid)})
}

func adminAPIPlayerNoteDelete(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		writeAdminError(w, http.StatusBadRequest, "note id must be a positive number")
		return
	}
	res, err := database.Exec(`DELETE FROM admin_player_notes WHERE id=? AND user_id=?`, id, pid)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeAdminError(w, http.StatusNotFound, "no such note on this player")
		return
	}
	adminAudit(database, r, "player note deleted", pid, map[string]any{"id": id})
	writeAdminJSON(w, map[string]any{"notes": adminPlayerNotes(database, pid)})
}
