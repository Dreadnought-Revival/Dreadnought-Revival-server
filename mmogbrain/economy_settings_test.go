package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// useDefaultEconomySettings puts the cached settings back to their defaults
// for one test (the cache outlives the test's database).
func useDefaultEconomySettings(t *testing.T) {
	t.Helper()
	loadEconomySettings()
	economySettings.contractGPReward.Store(defaultContractGPReward)
	t.Cleanup(func() { economySettings.contractGPReward.Store(defaultContractGPReward) })
}

// The dashboard changes the contract GP reward; it is saved, audited, read
// back after a restart, and paid by the next claim.
func TestAdminContractGPRewardSetting(t *testing.T) {
	database := contractTestDB(t)
	r := adminTestRouter()

	if rec := adminPost(r, "/admin/api/settings", "", `{"contract_gp_reward":5}`); rec.Code != http.StatusForbidden {
		t.Fatalf("setting without the key: %d, want 403", rec.Code)
	}
	if rec := adminGet(r, "/admin/api/settings", "wrong"); rec.Code != http.StatusForbidden {
		t.Fatalf("reading with a wrong key: %d, want 403", rec.Code)
	}
	var got map[string]int
	rec := adminGet(r, "/admin/api/settings", "test-admin-key")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got["contract_gp_reward"] != 200 {
		t.Fatalf("settings: %d %s, want contract_gp_reward 200", rec.Code, rec.Body.String())
	}

	for _, bad := range []string{`{"contract_gp_reward":-1}`, `{"contract_gp_reward":100001}`, `{"contract_gp_reward":"x"}`} {
		if rec := adminPost(r, "/admin/api/settings", "test-admin-key", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
	if contractGPReward() != 200 {
		t.Fatalf("a refused value changed the setting to %d", contractGPReward())
	}

	if rec := adminPost(r, "/admin/api/settings", "test-admin-key", `{"contract_gp_reward":350}`); rec.Code != http.StatusOK {
		t.Fatalf("set 350: %d %s", rec.Code, rec.Body.String())
	}
	var stored string
	_ = database.QueryRow(`SELECT value FROM server_flags WHERE name='contract_gp_reward'`).Scan(&stored)
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='setting' AND target='contract_gp_reward'`).Scan(&audits)
	if stored != "350" || audits != 1 {
		t.Errorf("stored %q, %d audit rows; want \"350\" and 1", stored, audits)
	}

	// A restart reads the saved value.
	economySettings.contractGPReward.Store(0)
	economySettings.once = sync.Once{}
	if n := contractGPReward(); n != 350 {
		t.Errorf("after a reload: %d, want 350", n)
	}

	now := time.Now()
	setContracts(t, database, now, "YMPQ_WinMatches")
	applyContractProgress(contractTestPID, contractMatch{mode: "TDM", outcome: "win"}, now)
	if ok, _, gp := removeDailyContract(contractTestPID, contractBySlot(t, 0).entryID); !ok || gp != 350 {
		t.Errorf("claim after the change: ok %v GP %d, want 350", ok, gp)
	}

	// 0 turns the GP off; the credits still pay.
	if rec := adminPost(r, "/admin/api/settings", "test-admin-key", `{"contract_gp_reward":0}`); rec.Code != http.StatusOK {
		t.Fatalf("set 0: %d", rec.Code)
	}
	setContracts(t, database, now, "YMPQ_WinMatches")
	applyContractProgress(contractTestPID, contractMatch{mode: "TDM", outcome: "win"}, now)
	if ok, credits, gp := removeDailyContract(contractTestPID, contractBySlot(t, 0).entryID); !ok || gp != 0 || credits != 5000 {
		t.Errorf("claim with GP off: ok %v credits %d GP %d, want 5000 and 0", ok, credits, gp)
	}
}

// A new account starts with 10000 GP (operator 2026-11-07).
func TestNewPlayerStartsWithGP(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "0a0b0c0d0e0f40108011aabbccddeeff"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	st := mmogPlayerStateForPID(pid)
	if st.premiumCurrency != 10000 || st.softCurrency != 10000 {
		t.Errorf("new player: %d GP, %d credits; want 10000 and 10000", st.premiumCurrency, st.softCurrency)
	}
}
