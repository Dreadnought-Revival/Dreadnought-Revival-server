package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/gorilla/mux"
)

// Economy settings the admin dashboard can change without a restart, kept in
// server_flags and cached here: the claim path must not query the database
// for them (one SQLite connection, see dreadnought-single-connection-db).
//
// contract_gp_reward: GP paid on top of a daily contract's credits when the
// player claims it (operator 2026-11-07, 200). Not an original rule -- the
// original contracts paid credits only (the quest assets' one reward field is
// m_FPReward). The contract card cannot show it: the card reads its reward
// from the client's own quest asset. The GP reaches the hangar through the
// YA_RewardCurrencies push the claim already sends (its "Points" field).

const (
	defaultContractGPReward = 200
	maxContractGPReward     = 100000
)

var economySettings struct {
	once             sync.Once
	contractGPReward atomic.Int32
}

func loadEconomySettings() {
	economySettings.once.Do(func() {
		economySettings.contractGPReward.Store(defaultContractGPReward)
		database := currentMmogPlayerStateDB()
		if database == nil {
			return
		}
		var v string
		if database.QueryRow(`SELECT value FROM server_flags WHERE name='contract_gp_reward'`).Scan(&v) == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= maxContractGPReward {
				economySettings.contractGPReward.Store(int32(n))
			}
		}
	})
}

// contractGPReward is the GP a claimed daily contract pays.
func contractGPReward() int32 {
	loadEconomySettings()
	return economySettings.contractGPReward.Load()
}

func setContractGPReward(database *sql.DB, n int32) error {
	loadEconomySettings()
	if database != nil {
		if _, err := database.Exec(`INSERT OR REPLACE INTO server_flags(name,value) VALUES('contract_gp_reward',?)`, strconv.Itoa(int(n))); err != nil {
			return err
		}
	}
	economySettings.contractGPReward.Store(n)
	return nil
}

func registerAdminEconomySettings(api *mux.Router) {
	api.HandleFunc("/settings", adminAPISettings).Methods(http.MethodGet)
	api.HandleFunc("/settings", adminAPISetSettings).Methods(http.MethodPost)
}

func adminSettingsJSON() map[string]any {
	return map[string]any{
		"contract_gp_reward":         contractGPReward(),
		"contract_gp_reward_default": defaultContractGPReward,
		"contract_gp_reward_max":     maxContractGPReward,
	}
}

func adminAPISettings(w http.ResponseWriter, _ *http.Request) {
	writeAdminJSON(w, adminSettingsJSON())
}

// adminAPISetSettings changes the settings named in the body; the change
// applies to the next claim.
func adminAPISetSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ContractGPReward *int64 `json:"contract_gp_reward"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	database := currentMmogPlayerStateDB()
	if req.ContractGPReward != nil {
		n := *req.ContractGPReward
		if n < 0 || n > maxContractGPReward {
			http.Error(w, `{"error":"contract GP reward must be between 0 and `+strconv.Itoa(maxContractGPReward)+`"}`, http.StatusBadRequest)
			return
		}
		from := contractGPReward()
		if err := setContractGPReward(database, int32(n)); err != nil {
			http.Error(w, `{"error":"could not save the setting"}`, http.StatusInternalServerError)
			return
		}
		adminAudit(database, r, "setting", "contract_gp_reward", map[string]any{"from": from, "to": n})
	}
	writeAdminJSON(w, adminSettingsJSON())
}
