package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func adminEconomyFixture(t *testing.T) time.Time {
	t.Helper()
	database := useTempMmogPlayerStateDB(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO player_state(user_id,display_name,soft_currency,premium_currency,free_xp,created_at) VALUES
			('p1','Pilot-01',100,5,0,'2026-10-01 10:00:00'),('p2','Pilot-02',300,0,40,'2026-10-08 10:00:00'),
			('p3','Local',200,0,0,'2026-10-02 10:00:00'),('p4','Pilot-04',0,10,0,'2026-10-09 09:00:00')`,
		`INSERT INTO player_purchases(user_id,item_id,item_type,price_paid,currency,purchased_at) VALUES
			('p1',1001,'ship',500,'CR','2026-10-09 01:00:00'),('p2',1001,'ship',500,'CR','2026-10-09 02:00:00'),
			('p2',1002,'vanity',90,'SP_regular','2026-10-08 02:00:00'),('p3',1003,'x',0,'admin','2026-10-09 03:00:00'),
			('p3',1004,'x',60,'freexp','2026-09-01 03:00:00')`,
		`INSERT INTO battle_results(match_id,user_id,outcome,credits,xp,created_at) VALUES
			('a','p1','win',100,200,'2026-10-09 01:12:00'),('a','p2','loss',50,100,'2026-10-09 01:12:00')`,
		`INSERT INTO player_daily_contracts(user_id,slot,quest_id,target,reward,state,assigned_at,completed_at,paid,gp_paid) VALUES
			('p1',0,'q',1,300,'acknowledged',1,1791507600,1,200),('p2',0,'q',1,100,'completed',1,1791507600,0,0)`,
		`INSERT INTO market_sales(name,percent,scope,start_at,end_at) VALUES('Sale',50,'all',1791400000,1791600000),('Old',10,'all',1,2)`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return now
}

// 1791507600 = 2026-10-09 01:40:00 UTC.
func TestAdminEconomyNumbers(t *testing.T) {
	now := adminEconomyFixture(t)
	e := adminCollectEconomy(currentMmogPlayerStateDB(), 3, now)
	sup := e["supply"].(map[string]*adminEconomySupply)
	if c := sup["credits"]; c.Total != 600 || c.Median != 150 || c.Max != 300 || c.Holders != 3 || c.Richest[0].Value != 300 || c.Richest[0].Name != "Pilot-02" {
		t.Errorf("credits: %+v", c)
	}
	if g := sup["gp"]; g.Total != 15 || g.Median != 2 || len(g.Richest) != 2 {
		t.Errorf("gp: %+v", g)
	}
	if sup["credits"].Richest[1].Name != "" && sup["credits"].Richest[1].PID == "p3" {
		t.Errorf("'Local' must read as unnamed")
	}
	pd := e["purchases_daily"].([]adminEconomyPurchaseDay)
	got := map[string]adminEconomyPurchaseDay{}
	for _, p := range pd {
		got[p.Day+p.Currency] = p
	}
	if p := got["2026-10-09CR"]; p.Count != 2 || p.Spent != 1000 {
		t.Errorf("CR purchases: %+v", pd)
	}
	if _, ok := got["2026-09-01freexp"]; ok {
		t.Errorf("purchase before the window counted")
	}
	items := e["top_items"].([]adminEconomyItem)
	if len(items) != 2 || items[0].ItemID != 1001 || items[0].Count != 2 || items[0].Currency != "CR" {
		t.Errorf("items (admin rows must be excluded): %+v", items)
	}
	pay := e["match_pay_daily"].([]adminEconomyPayDay)
	if len(pay) != 3 || pay[2].Credits != 150 || pay[2].XP != 300 || pay[2].Results != 2 {
		t.Errorf("pay: %+v", pay)
	}
	ct := e["contract_daily"].([]adminEconomyContractDay)
	if ct[2].Claims != 1 || ct[2].Credits != 300 || ct[2].GP != 200 || e["contracts_unclaimed"].(int) != 1 {
		t.Errorf("contracts: %+v unclaimed %v", ct, e["contracts_unclaimed"])
	}
	sales := e["sales"].([]adminEconomySale)
	if len(sales) != 1 || sales[0].Status != "active" {
		t.Errorf("sales: %+v", sales)
	}
}

func TestAdminEconomyRoute(t *testing.T) {
	adminEconomyFixture(t)
	r := adminTestRouter()
	for _, key := range []string{"", "wrong"} {
		if rec := adminGet(r, "/admin/api/economy", key); rec.Code != http.StatusForbidden {
			t.Errorf("key %q: %d", key, rec.Code)
		}
	}
	done := make(chan *struct {
		code int
		body []byte
	}, 1)
	go func() {
		rec := adminGet(r, "/admin/api/economy?days=999", "test-admin-key")
		done <- &struct {
			code int
			body []byte
		}{rec.Code, rec.Body.Bytes()}
	}()
	select {
	case res := <-done:
		if res.code != http.StatusOK {
			t.Fatalf("status %d", res.code)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(res.body, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"days", "day_list", "supply", "purchases_daily", "purchase_currencies", "top_items", "match_pay_daily", "contract_daily", "sales", "settings"} {
			if _, ok := m[k]; !ok {
				t.Errorf("missing key %s", k)
			}
		}
		if string(m["days"]) != "90" {
			t.Errorf("days clamp: %s", m["days"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("/economy did not answer in 3 s: store deadlocked")
	}
}

func TestAdminMetricsAdditions(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO matches(id,battle_match_id,game_mode,map,status,created_at) VALUES
			('m1','a','TDM','/Game/Maps/MP/Glacier/MP_G_P','ended','2026-10-09T01:00:00Z'),
			('m2','b','TDM','/Game/Maps/MP/Glacier/MP_G_P','ended','2026-10-09T02:00:00Z'),
			('m3','c','TDM','/Game/Maps/MP/Gorge/MP_X_P','ended','2026-10-08T02:00:00Z')`,
		`INSERT INTO battle_results(match_id,user_id,team,outcome,created_at) VALUES
			('a','p1',0,'win','2026-10-09 01:10:00'),('a','p2',1,'loss','2026-10-09 01:10:00'),
			('b','p1',1,'win','2026-10-09 02:10:00'),('b','p2',1,'win','2026-10-09 02:10:00'),
			('c','p1',1,'win','2026-10-08 02:10:00')`,
		`INSERT INTO battle_result_stats(match_id,user_id,stat,value) VALUES
			('a','p1','time_Destroyer',300),('b','p1','time_Destroyer',100),('b','p2','time_Corvette',50),('a','p1','score',9)`,
		// p1 created 10-07, played 10-08 = day 1: returned. p2 created 10-08: day 1 (10-09) is today, not
		// over, so not eligible. p5 created 10-06 never played: eligible, not returned. Day 7 is over for nobody.
		`INSERT INTO player_state(user_id,created_at) VALUES('p1','2026-10-07 10:00:00'),('p2','2026-10-08 10:00:00'),('p5','2026-10-06 10:00:00')`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	m := adminCollectMetrics(database, 7, now)
	maps := m["maps"].([]adminMetricsMap)
	if len(maps) != 2 || maps[0].Map != "Glacier" || maps[0].Matches != 2 || maps[1].Map != "Gorge" {
		t.Errorf("maps: %+v", maps)
	}
	sc := m["ship_classes"].([]adminMetricsShipClass)
	if len(sc) != 2 || sc[0].Class != "Destroyer" || sc[0].Seconds != 400 || sc[0].Players != 1 || sc[1].Class != "Corvette" {
		t.Errorf("ship classes: %+v", sc)
	}
	bal := m["team_balance"].([]adminMetricsBalance)
	if len(bal) != 1 || bal[0].Team0Wins != 1 || bal[0].Team1Wins != 2 {
		t.Errorf("balance: %+v", bal)
	}
	ret := m["retention"].(map[string]any)
	if ret["cohort"].(int) != 3 || ret["day1_eligible"].(int) != 2 || ret["day1_returned"].(int) != 1 || ret["day7_eligible"].(int) != 0 {
		t.Errorf("retention: %+v", ret)
	}
}

func TestAdminMetricsAdditionsRoute(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	done := make(chan []byte, 1)
	go func() {
		done <- adminGet(adminTestRouter(), "/admin/api/metrics?days=30", "test-admin-key").Body.Bytes()
	}()
	select {
	case b := <-done:
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"samples", "sample_minutes", "daily", "maps", "ship_classes", "team_balance", "retention", "top_players"} {
			if _, ok := m[k]; !ok {
				t.Errorf("missing key %s", k)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("/metrics did not answer in 3 s")
	}
}
