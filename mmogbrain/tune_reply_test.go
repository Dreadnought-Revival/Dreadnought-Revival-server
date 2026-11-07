package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

// mmogNode is a decoded field of the binary document (test-only reader).
type mmogNode struct {
	name     string
	tag      byte
	str      string
	children []*mmogNode
}

func (n *mmogNode) child(name string) *mmogNode {
	for _, c := range n.children {
		if strings.EqualFold(c.name, name) { // the client compares names case-insensitively
			return c
		}
	}
	return nil
}

// decodeMmogNodes reads fields until the end of b or a container end marker.
func decodeMmogNodes(t *testing.T, b []byte, i int) ([]*mmogNode, int) {
	t.Helper()
	var out []*mmogNode
	for i+1 < len(b) {
		if b[i] == 0 && b[i+1] == 0x0e { // container end (or the root's)
			return out, i + 6
		}
		nameLen := int(b[i])
		n := &mmogNode{name: string(b[i+1 : i+1+nameLen]), tag: b[i+1+nameLen]}
		i += 2 + nameLen
		switch n.tag {
		case 0x0c, 0x0d:
			i += 4
			n.children, i = decodeMmogNodes(t, b, i)
		case 0x09, 0x0a:
			l := int(binary.LittleEndian.Uint32(b[i:]))
			n.str = string(b[i+4 : i+4+l])
			i += 4 + l
		case 0x05:
			i++
		case 0x56, 0x57:
			i += 4
		default:
			t.Fatalf("unknown tag 0x%02x for %q at %d", n.tag, n.name, i)
		}
		out = append(out, n)
	}
	return out, i
}

func tuneDocumentOf(t *testing.T, payload []byte) *mmogNode {
	t.Helper()
	if rt := protocol.ExtractStringField(payload, "RT"); rt != "YA_TuneReturn" {
		t.Fatalf("RT = %q, want YA_TuneReturn", rt)
	}
	blob, ok := protocol.ExtractBytesField(payload, "packed")
	if !ok {
		t.Fatal("no packed field")
	}
	r, err := zlib.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	nodes, end := decodeMmogNodes(t, doc, 0)
	if end != len(doc) {
		t.Fatalf("document decoded to %d of %d bytes", end, len(doc))
	}
	root := &mmogNode{children: nodes}
	ret := root.child("Returning")
	if ret == nil {
		t.Fatal("no Returning")
	}
	return ret
}

// The reply as YTuneManager::Set and its rebuild read it: Returning ->
// MetaData -> Version, and each table an ARRAY of row objects keyed by
// "Name", scalars as strings, bools "True"/"False".
func TestTuneReplyIsWhatTheClientWalks(t *testing.T) {
	t.Setenv("DN_TUNE_SEND", "1")
	ret := tuneDocumentOf(t, buildMmogTunePayload())
	if md := ret.child("MetaData"); md == nil || md.child("Version") == nil || md.child("Version").str != tuneApplyVersion {
		t.Fatalf("MetaData.Version missing or not %s", tuneApplyVersion)
	}
	for _, table := range []string{"WeaponsTune", "BattleReadyTune", "ProjectilesTune", "AbilitiesTune",
		"OfficersTune", "FeatsTune", "HavocTune", "GameModifiersTune"} {
		n := ret.child(table)
		if n == nil || n.tag != 0x0d {
			t.Fatalf("%s is not an array (the rebuild walks its children)", table)
		}
	}
	rows := func(table string) map[string]*mmogNode {
		out := map[string]*mmogNode{}
		for _, row := range ret.child(table).children {
			if row.tag != 0x0c || row.name != "" {
				t.Fatalf("%s row is not an unnamed object", table)
			}
			name := row.child("Name")
			if name == nil || name.str == "" {
				t.Fatalf("%s row without Name", table)
			}
			out[name.str] = row
		}
		return out
	}
	weapons := rows("WeaponsTune")
	for _, want := range clientRequestedWeaponRows {
		row := weapons[want]
		if row == nil {
			t.Errorf("WeaponsTune has no row %q", want)
			continue
		}
		if dmg := row.child("m_damageHigh"); dmg == nil || dmg.tag != 0x09 || dmg.str == "" {
			t.Errorf("%s m_damageHigh is not a numeric string: %+v", want, dmg)
		}
		if b := row.child("m_ignoreShields"); b == nil || (b.str != "True" && b.str != "False") {
			t.Errorf("%s m_ignoreShields is not True/False: %+v", want, b)
		}
	}
	projectiles := rows("ProjectilesTune")
	if len(projectiles) < 300 {
		t.Errorf("ProjectilesTune has %d rows, want the cooked table's 393", len(projectiles))
	}
	for name, row := range projectiles {
		if len(row.children) < 10 {
			t.Errorf("projectile %s carries only %d fields (the old builder sent names only)", name, len(row.children))
		}
		break
	}
	if len(rows("OfficersTune")) == 0 {
		t.Error("OfficersTune has no rows")
	}
	t.Logf("weapons %d, projectiles %d, compressed %d bytes", len(weapons), len(projectiles),
		len(compressMmogDocument(buildMmogTuneDocument())))
}

// Only test accounts get the real reply; "check" carries the client's own
// version so Set changes nothing.
func TestTuneReplyPerTestAccount(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	t.Setenv("DN_TUNE_SEND", "")
	loadTuneTestAccounts()
	tuneTestAccounts.mu.Lock()
	tuneTestAccounts.mode = map[string]string{}
	tuneTestAccounts.mu.Unlock()
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	if rt := protocol.ExtractStringField(buildMmogTuneReplyFor(adminTestPID), "RT"); rt != "YA_Tune" {
		t.Fatalf("an ordinary account got RT %q; it must get the dropped reply", rt)
	}
	r := adminTestRouter()
	if rec := adminPost(r, "/admin/api/players/"+adminTestPID+"/tune-test", "test-admin-key", `{"mode":"sometimes"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad mode: %d", rec.Code)
	}
	if rec := adminPost(r, "/admin/api/players/"+adminTestPID+"/tune-test", "test-admin-key", `{"mode":"check"}`); rec.Code != http.StatusOK {
		t.Fatalf("set check: %d %s", rec.Code, rec.Body.String())
	}
	if v := tuneDocumentOf(t, buildMmogTuneReplyFor(adminTestPID)).child("MetaData").child("Version").str; v != tuneCheckVersion {
		t.Errorf("check reply version %q, want %q", v, tuneCheckVersion)
	}
	if rec := adminPost(r, "/admin/api/players/"+adminTestPID+"/tune-test", "test-admin-key", `{"mode":"apply"}`); rec.Code != http.StatusOK {
		t.Fatalf("set apply: %d", rec.Code)
	}
	if v := tuneDocumentOf(t, buildMmogTuneReplyFor(adminTestPID)).child("MetaData").child("Version").str; v != tuneApplyVersion {
		t.Errorf("apply reply version %q, want %q", v, tuneApplyVersion)
	}
	if rec := adminPost(r, "/admin/api/players/"+adminTestPID+"/tune-test", "test-admin-key", `{"mode":""}`); rec.Code != http.StatusOK {
		t.Fatalf("set off: %d", rec.Code)
	}
	if rt := protocol.ExtractStringField(buildMmogTuneReplyFor(adminTestPID), "RT"); rt != "YA_Tune" {
		t.Errorf("after off the account got RT %q", rt)
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='tune test'`).Scan(&audits)
	if audits != 3 {
		t.Errorf("%d audit rows, want 3", audits)
	}
}
