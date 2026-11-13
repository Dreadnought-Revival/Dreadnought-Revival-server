package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every route the Balancing and Tech tree pages added: key required, answers
// within 3 s on a seeded store (single connection), validation, audit rows.
func TestAdminBalanceRoutes(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanTuneEditor(t)
	useCleanTechTreeCosts(t)
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	r := adminTestRouter()
	const weapon = "WP_DreadnoughtMPri01_weapon01_T1_BP"
	if rec := adminPost(r, "/admin/api/tune/draft", "test-admin-key", `{"changes":[{"table":"WeaponsTune","row":"`+weapon+`","field":"m_damageHigh","value":999}]}`); rec.Code != http.StatusOK {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	if rec := adminPost(r, "/admin/api/tune/publish", "test-admin-key", `{"note":"n"}`); rec.Code != http.StatusOK {
		t.Fatalf("publish: %d", rec.Code)
	}

	gets := []string{"/admin/api/tune/battle", "/admin/api/tune/versions/1", "/admin/api/tune/table/WeaponsTune?names=1&limit=500", "/admin/api/techtree/costs", "/admin/api/techtree/costs?q=a&tier=2&state=overridden&limit=5"}
	for _, p := range gets {
		for _, key := range []string{"", "wrong"} {
			if rec := adminGet(r, p, key); rec.Code != http.StatusForbidden {
				t.Errorf("%s with key %q: %d, want 403", p, key, rec.Code)
			}
		}
		done := make(chan int, 1)
		go func() { done <- adminGet(r, p, "test-admin-key").Code }()
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Errorf("%s: %d", p, code)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not answer in 3 s: the single-connection store is deadlocked", p)
		}
	}
	for p, want := range map[string]int{
		"/admin/api/tune/versions/0":                  http.StatusBadRequest,
		"/admin/api/tune/versions/99":                 http.StatusNotFound,
		"/admin/api/tune/versions/x":                  http.StatusBadRequest,
		"/admin/api/techtree/costs?tier=99":           http.StatusBadRequest,
		"/admin/api/techtree/costs?state=bogus":       http.StatusBadRequest,
		"/admin/api/tune/table/AbilitiesTune?names=1": http.StatusNotFound,
	} {
		if rec := adminGet(r, p, "test-admin-key"); rec.Code != want {
			t.Errorf("%s: %d, want %d", p, rec.Code, want)
		}
	}

	var names struct {
		Total int                     `json:"total"`
		Names []struct{ Name string } `json:"names"`
	}
	rec := adminGet(r, "/admin/api/tune/table/WeaponsTune?names=1&limit=500", "test-admin-key")
	_ = json.Unmarshal(rec.Body.Bytes(), &names)
	if len(names.Names) < 31 || len(names.Names) > 500 || names.Total < len(names.Names) {
		t.Errorf("names list: %d of %d", len(names.Names), names.Total)
	}
	var ver struct {
		Changes []tuneDiffRow `json:"changes"`
	}
	_ = json.Unmarshal(adminGet(r, "/admin/api/tune/versions/1", "test-admin-key").Body.Bytes(), &ver)
	if len(ver.Changes) != 1 || ver.Changes[0].Original == nil {
		t.Errorf("version 1 preview: %+v", ver.Changes)
	}
	var bs struct {
		On    bool  `json:"on"`
		Live  int64 `json:"live"`
		Error string
	}
	_ = json.Unmarshal(adminGet(r, "/admin/api/tune/battle", "test-admin-key").Body.Bytes(), &bs)
	if !bs.On || bs.Live != 1 {
		t.Errorf("battle status: %+v", bs)
	}

	// The cost browser: an override shows up, filters find it, reset removes it.
	var list struct {
		Rows []struct {
			Node techTreeNode `json:"node"`
		} `json:"rows"`
		Total   int `json:"total"`
		Summary struct {
			Overridden int `json:"overridden"`
		} `json:"summary"`
	}
	get := func(p string) {
		list.Rows, list.Total, list.Summary.Overridden = nil, 0, 0
		rec := adminGet(r, p, "test-admin-key")
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	get("/admin/api/techtree/costs?limit=3")
	if len(list.Rows) != 3 || list.Total < 10 {
		t.Fatalf("browser: %d rows of %d", len(list.Rows), list.Total)
	}
	first := list.Rows[0].Node
	field := "xp"
	if first.XP == nil {
		field = "price"
	}
	body := `{"values":[{"item_id":` + strconv.Itoa(int(first.ItemID)) + `,"field":"` + field + `","amount":1234,"status":"accepted","source":""}]}`
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", body); rec.Code != http.StatusOK {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	get("/admin/api/techtree/costs?state=overridden")
	if list.Total != 1 || list.Summary.Overridden != 1 {
		t.Errorf("overridden filter: %d rows, summary %d", list.Total, list.Summary.Overridden)
	}
	reset := `{"values":[{"item_id":` + strconv.Itoa(int(first.ItemID)) + `,"field":"` + field + `","amount":null,"status":"guess","source":""}]}`
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", reset); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	get("/admin/api/techtree/costs?state=overridden")
	if list.Total != 0 {
		t.Errorf("after reset %d overrides", list.Total)
	}
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", strings.Replace(body, "1234", "-5", 1)); rec.Code != http.StatusBadRequest {
		t.Errorf("negative cost: %d", rec.Code)
	}

	// Audit rows for the draft edit, the cost edits.
	rows, err := adminAuditRows(database, `WHERE action IN ('tune draft edit','tech tree costs')`)
	if err != nil || len(rows) < 3 {
		t.Errorf("audit rows %d (%v), want the draft edit and two cost edits", len(rows), err)
	}
}
