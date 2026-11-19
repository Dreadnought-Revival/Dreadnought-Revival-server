package main

import "testing"

// The client's YA_GetBoosterData dispatcher (0x2A2C83A) takes the root's
// "result" child and gives it to the table reader (0x2A65FE0), which reads
// BoosterTable and GoldMembershipTable from THAT node. Tables at the root are
// never seen: the client's booster tables stay empty and the end-of-match
// rewards page shows every booster (Elite included) as INACTIVE.
func TestBoosterDataTablesLiveInsideResult(t *testing.T) {
	payload := buildMmogBoosterDataPayload()
	root, _ := decodeMmogNodes(t, payload, 0)
	rootNode := &mmogNode{children: root}
	if rootNode.child("BoosterTable") != nil || rootNode.child("GoldMembershipTable") != nil {
		t.Fatal("a table at the payload root is invisible to the client; both belong inside result")
	}
	result := rootNode.child("result")
	if result == nil {
		t.Fatal("no result object")
	}
	bt := result.child("BoosterTable")
	if bt == nil || len(bt.children) != len(commonBoosters) {
		t.Fatalf("result.BoosterTable = %v, want %d entries", bt, len(commonBoosters))
	}
	gt := result.child("GoldMembershipTable")
	if gt == nil || len(gt.children) != 1 {
		t.Fatalf("result.GoldMembershipTable = %v, want the one Elite entry", gt)
	}
	if id := gt.children[0].child("ID"); id == nil {
		t.Fatal("Elite entry has no ID")
	}
	if st := result.child(fieldStatus); st == nil || st.str != "ok" {
		t.Fatal("result.status must stay ok")
	}
}
