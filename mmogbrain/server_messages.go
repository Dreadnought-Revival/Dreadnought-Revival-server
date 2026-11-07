package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// Server messages from the admin dashboard (issue #19): a notice to everyone
// online now (maintenance, restarts), and a message of the day each player
// gets when they come online. Both are chat lines from the "Server" sender
// in the Global channel (server_chat.go), the way the online-count and queue
// lines have reached players since 2026-10-03.

const maxServerMessage = 300

// motdDelay holds the message of the day back after the chat connection is
// set up (channels named, friends pushed). GUESS: 5 s, so the line lands
// after the client has its chat rooms; not traced in the client.
var motdDelay = 5 * time.Second

var motdCache struct {
	once sync.Once
	text atomic.Value // string
}

// messageOfTheDay is the text each player gets on coming online; "" = none.
func messageOfTheDay() string {
	motdCache.once.Do(func() {
		motdCache.text.Store("")
		if database := currentMmogPlayerStateDB(); database != nil {
			var v string
			if database.QueryRow(`SELECT value FROM server_flags WHERE name='motd'`).Scan(&v) == nil {
				motdCache.text.Store(v)
			}
		}
	})
	return motdCache.text.Load().(string)
}

func setMessageOfTheDay(database *sql.DB, text string) error {
	messageOfTheDay()
	if database != nil {
		var err error
		if text == "" {
			_, err = database.Exec(`DELETE FROM server_flags WHERE name='motd'`)
		} else {
			_, err = database.Exec(`INSERT OR REPLACE INTO server_flags(name,value) VALUES('motd',?)`, text)
		}
		if err != nil {
			return err
		}
	}
	motdCache.text.Store(text)
	return nil
}

// sendMessageOfTheDay schedules the message of the day for a player whose
// chat connection was just set up.
func sendMessageOfTheDay(peer *socialPeer) {
	text := messageOfTheDay()
	if text == "" {
		return
	}
	time.AfterFunc(motdDelay, func() {
		if err := serverLineTo(peer, text); err != nil {
			return // gone again, or not reading
		}
		logrus.WithField("player", peer.playerID).Info("server chat: message of the day sent")
	})
}

// cleanServerMessage makes the text one chat line, or says why it cannot be.
func cleanServerMessage(text string) (string, string) {
	text = strings.Join(strings.Fields(text), " ")
	if !utf8.ValidString(text) {
		return "", "the text is not valid UTF-8"
	}
	if utf8.RuneCountInString(text) > maxServerMessage {
		return "", "the message is too long (300 characters at most)"
	}
	return text, ""
}

func registerAdminServerMessages(api *mux.Router) {
	api.HandleFunc("/messages", adminAPIMessages).Methods(http.MethodGet)
	api.HandleFunc("/messages/send", adminAPISendMessage).Methods(http.MethodPost)
	api.HandleFunc("/messages/motd", adminAPISetMOTD).Methods(http.MethodPost)
}

// adminAPIMessages: the message of the day and the messages sent lately.
func adminAPIMessages(w http.ResponseWriter, _ *http.Request) {
	type sent struct {
		At      string `json:"at"`
		Action  string `json:"action"`
		Details string `json:"details"`
		Actor   string `json:"actor"`
	}
	out := []sent{}
	if database := currentMmogPlayerStateDB(); database != nil {
		rows, err := database.Query(`SELECT created_at, action, details, actor FROM admin_audit
			WHERE action IN ('server message','message of the day') ORDER BY id DESC LIMIT 20`)
		if err == nil {
			for rows.Next() {
				var x sent
				if rows.Scan(&x.At, &x.Action, &x.Details, &x.Actor) == nil {
					out = append(out, x)
				}
			}
			_ = rows.Close()
		}
	}
	writeAdminJSON(w, map[string]any{"motd": messageOfTheDay(), "recent": out, "max": maxServerMessage,
		"online": len(socialHubInstance.onlinePlayerIDs())})
}

func adminMessageText(w http.ResponseWriter, r *http.Request, allowEmpty bool) (string, bool) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid body")
		return "", false
	}
	text, why := cleanServerMessage(req.Text)
	if why != "" {
		writeAdminError(w, http.StatusBadRequest, why)
		return "", false
	}
	if text == "" && !allowEmpty {
		writeAdminError(w, http.StatusBadRequest, "the message is empty")
		return "", false
	}
	return text, true
}

// adminAPISendMessage posts a Server line to everyone online now.
func adminAPISendMessage(w http.ResponseWriter, r *http.Request) {
	text, ok := adminMessageText(w, r, false)
	if !ok {
		return
	}
	n := socialHubInstance.serverLine(text)
	adminAudit(currentMmogPlayerStateDB(), r, "server message", "", map[string]any{"text": text, "recipients": n})
	writeAdminJSON(w, map[string]any{"ok": true, "recipients": n})
}

// adminAPISetMOTD sets the message of the day ("" turns it off).
func adminAPISetMOTD(w http.ResponseWriter, r *http.Request) {
	text, ok := adminMessageText(w, r, true)
	if !ok {
		return
	}
	database := currentMmogPlayerStateDB()
	from := messageOfTheDay()
	if err := setMessageOfTheDay(database, text); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not save the message of the day")
		return
	}
	adminAudit(database, r, "message of the day", "", map[string]any{"from": from, "text": text})
	writeAdminJSON(w, map[string]any{"ok": true, "motd": text})
}
