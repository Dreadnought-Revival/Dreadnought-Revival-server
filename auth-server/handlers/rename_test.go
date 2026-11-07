package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func adminRename(t *testing.T, h *Handler, pid, name string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.AdminRename(rec, httptest.NewRequest(http.MethodPost, "/admin/rename", strings.NewReader(`{"user_id":"`+pid+`","username":"`+name+`"}`)))
	return rec
}

// An admin rename: the new name signs in, the old one no longer does, the
// email still does; taken and invalid names are refused.
func TestAdminRename(t *testing.T) {
	h := newTestHandler(t)
	register(t, h, "testpilot", "pilot@example.com", "hunter2x")
	register(t, h, "otherpilot", "other@example.com", "hunter2x")
	var id string
	if err := h.DB.QueryRow(`SELECT id FROM users WHERE username='testpilot'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	pid := strings.ToLower(strings.ReplaceAll(id, "-", ""))

	for name, want := range map[string]int{
		"OtherPilot":            http.StatusConflict, // taken, ignoring case
		"other@example.com":     http.StatusBadRequest,
		"ab":                    http.StatusBadRequest,
		"Name#1234":             http.StatusBadRequest,
		"two  spaces":           http.StatusBadRequest,
		"abcdefghijklmnopqrstu": http.StatusBadRequest,
	} {
		if rec := adminRename(t, h, pid, name); rec.Code != want {
			t.Errorf("rename to %q: %d, want %d (%s)", name, rec.Code, want, rec.Body.String())
		}
	}
	if rec := adminRename(t, h, "ffffffffffffffffffffffffffffffff", "Fresh Name"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d, want 404", rec.Code)
	}

	rec := adminRename(t, h, pid, "New_Pilot.1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"from":"testpilot"`) {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if rec := passwordLogin(t, h, "New_Pilot.1", "hunter2x"); rec.Code != http.StatusOK {
		t.Errorf("new name does not sign in: %d", rec.Code)
	}
	if rec := passwordLogin(t, h, "testpilot", "hunter2x"); rec.Code == http.StatusOK {
		t.Error("old name still signs in")
	}
	if rec := passwordLogin(t, h, "pilot@example.com", "hunter2x"); rec.Code != http.StatusOK {
		t.Errorf("email no longer signs in: %d", rec.Code)
	}
	// A change of case only.
	if rec := adminRename(t, h, pid, "new_pilot.1"); rec.Code != http.StatusOK {
		t.Errorf("case-only rename: %d %s", rec.Code, rec.Body.String())
	}
}
