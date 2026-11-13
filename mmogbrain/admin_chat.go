package main

// Chat log for the dashboard (page "chat"): every line the server stored in
// chat_messages (persistMmogChatMessage), filterable and pageable.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminChat) }

func registerAdminChat(api *mux.Router) {
	api.HandleFunc("/chat", adminAPIChat).Methods(http.MethodGet)
}

// adminChatChannel names a channel for the admin. Channel names are
// "<id>.<type>" (social.go: "dreadnought.global", "english.language",
// "<match>.all", "<match>-<team>.team", "<squad>.squad"). match is the match id
// part for match rooms, else "".
func adminChatChannel(ch string) (label, match string) {
	switch ch {
	case "global", "dreadnought.global":
		return "Global", ""
	}
	dot := strings.LastIndex(ch, ".")
	if dot <= 0 {
		return ch, ""
	}
	id, typ := ch[:dot], ch[dot+1:]
	switch typ {
	case "language":
		return strings.ToUpper(id[:1]) + id[1:], ""
	case "all":
		return "Match (all)", id
	case "team":
		if i := strings.LastIndex(id, "-"); i > 0 {
			if n, err := strconv.Atoi(id[i+1:]); err == nil {
				return "Match team " + strconv.Itoa(n), id[:i]
			}
		}
		return "Match team", id
	case "squad":
		return "Squad", ""
	}
	return ch, ""
}

type adminChatLine struct {
	Seq     int64  `json:"seq"`
	Channel string `json:"channel"`
	PID     string `json:"pid"`
	Name    string `json:"name"`
	Text    string `json:"text"`
	Sent    string `json:"sent"`
	Muted   bool   `json:"muted"`
}

// GET /admin/api/chat?channel=&q=&pid=&before=<seq>&limit= : newest first.
// seq is the table's rowid (ids are random UUIDs, so they cannot page).
func adminAPIChat(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	type channelInfo struct {
		Channel string `json:"channel"`
		Label   string `json:"label"`
		Match   string `json:"match,omitempty"`
		Count   int    `json:"count"`
		Last    string `json:"last"`
	}
	messages := []adminChatLine{}
	channels := []channelInfo{}
	if database == nil {
		writeAdminJSON(w, map[string]any{"messages": messages, "channels": channels, "more": false})
		return
	}
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeAdminError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, 500)
	}
	where := []string{"1=1"}
	args := []any{}
	if v := q.Get("channel"); v != "" {
		where = append(where, "c.channel = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("pid")); v != "" {
		where = append(where, "c.sender_id = ?")
		args = append(args, strings.ToLower(v))
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
		where = append(where, `c.content LIKE ? ESCAPE '\'`)
		args = append(args, "%"+esc+"%")
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeAdminError(w, http.StatusBadRequest, "before must be a message number")
			return
		}
		where = append(where, "c.rowid < ?")
		args = append(args, n)
	}
	args = append(args, limit+1)
	rows, err := database.Query(`SELECT c.rowid, c.channel, c.sender_id, COALESCE(ps.display_name,''), c.content, c.sent_at,
		EXISTS(SELECT 1 FROM admin_chat_mutes m WHERE m.user_id = c.sender_id AND (m.expires_at='' OR m.expires_at > datetime('now')))
		FROM chat_messages c LEFT JOIN player_state ps ON ps.user_id = c.sender_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY c.rowid DESC LIMIT ?`, args...)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var m adminChatLine
		if rows.Scan(&m.Seq, &m.Channel, &m.PID, &m.Name, &m.Text, &m.Sent, &m.Muted) == nil {
			if m.Name == "Local" {
				m.Name = ""
			}
			messages = append(messages, m)
		}
	}
	_ = rows.Close()
	more := len(messages) > limit
	if more {
		messages = messages[:limit]
	}

	crows, err := database.Query(`SELECT channel, COUNT(*), MAX(sent_at) FROM chat_messages
		WHERE sent_at >= datetime('now','-7 days') GROUP BY channel ORDER BY MAX(sent_at) DESC LIMIT 200`)
	if err == nil {
		for crows.Next() {
			var c channelInfo
			if crows.Scan(&c.Channel, &c.Count, &c.Last) == nil {
				c.Label, c.Match = adminChatChannel(c.Channel)
				channels = append(channels, c)
			}
		}
		_ = crows.Close()
	}
	writeAdminJSON(w, map[string]any{"messages": messages, "channels": channels, "more": more})
}
