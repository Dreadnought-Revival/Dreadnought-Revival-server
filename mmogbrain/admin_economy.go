package main

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gorilla/mux"
)

// Economy overview for the admin dashboard: how much of each currency is in
// circulation, who holds it, what is bought with it and what the server pays
// out. Every figure comes from a table the game already writes:
//
//   - player_state: soft_currency (CR, credits), premium_currency (GP, the
//     client's "SP_regular"), free_xp.
//   - player_purchases: one row per item a player owns; currency says how it
//     was acquired: CR / SP_regular (store), freexp (research unlock),
//     gp (legacy default), bundle, admin (provisioned starter or admin grant,
//     price 0) and refunded (vanity refund, vanity_ownership.go). purchased_at
//     is a datetime('now') string.
//   - battle_results: credits / xp paid per reported result.
//   - player_daily_contracts: paid=1 marks a claimed contract, reward is its
//     credits, gp_paid the GP paid with it (economy_settings.go).
//   - market_sales: sales, start_at / end_at are Unix seconds.

func init() { adminRegisterAPI(registerAdminEconomy) }

func registerAdminEconomy(api *mux.Router) {
	api.HandleFunc("/economy", adminAPIEconomy).Methods(http.MethodGet)
}

type adminEconomyHolder struct {
	PID   string `json:"pid"`
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

type adminEconomySupply struct {
	Total   int64                `json:"total"`
	Median  int64                `json:"median"`
	Max     int64                `json:"max"`
	Holders int                  `json:"holders"` // accounts with more than 0
	Richest []adminEconomyHolder `json:"richest"`
}

type adminEconomyPurchaseDay struct {
	Day      string `json:"day"`
	Currency string `json:"currency"`
	Count    int    `json:"count"`
	Spent    int64  `json:"spent"` // sum of price_paid
}

type adminEconomyItem struct {
	ItemID   int32  `json:"item_id"`
	Name     string `json:"name"` // "item N" when no table names it
	Type     string `json:"type"`
	Count    int    `json:"count"`
	Currency string `json:"currency"` // the currency most of the purchases used
	Spent    int64  `json:"spent"`
}

type adminEconomyPayDay struct {
	Day     string `json:"day"`
	Results int    `json:"results"`
	Credits int64  `json:"credits"`
	XP      int64  `json:"xp"`
}

type adminEconomyContractDay struct {
	Day     string `json:"day"`
	Claims  int    `json:"claims"`
	Credits int64  `json:"credits"`
	GP      int64  `json:"gp"`
}

type adminEconomySale struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Percent int    `json:"percent"`
	Scope   string `json:"scope"`
	StartAt int64  `json:"start_at"`
	EndAt   int64  `json:"end_at"`
	Status  string `json:"status"` // active, upcoming, ended
}

// GET /admin/api/economy?days=N (1..90, default 30).
func adminAPIEconomy(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 {
		days = 30
	}
	if days > 90 {
		days = 90
	}
	writeAdminJSON(w, adminCollectEconomy(database, days, time.Now().UTC()))
}

func adminCollectEconomy(database *sql.DB, days int, now time.Time) map[string]any {
	first := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	since := first.Format(adminSampleTimeFormat)
	// One connection: every query is read to the end before the next starts.
	each := func(q string, scan func(rows *sql.Rows), args ...any) {
		rows, err := database.Query(q, args...)
		if err != nil {
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			scan(rows)
		}
	}
	dayList := make([]string, days)
	index := map[string]int{}
	for i := range dayList {
		dayList[i] = first.AddDate(0, 0, i).Format("2006-01-02")
		index[dayList[i]] = i
	}

	// Circulation. Column names are fixed strings, never request input.
	supply := map[string]*adminEconomySupply{}
	for key, col := range map[string]string{"credits": "soft_currency", "gp": "premium_currency", "free_xp": "free_xp"} {
		s := &adminEconomySupply{Richest: []adminEconomyHolder{}}
		var values []int64
		each(`SELECT `+col+` FROM player_state`, func(rows *sql.Rows) {
			var v int64
			if rows.Scan(&v) == nil {
				values = append(values, v)
				s.Total += v
				if v > 0 {
					s.Holders++
				}
			}
		})
		if n := len(values); n > 0 {
			sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
			s.Median = values[n/2]
			if n%2 == 0 {
				s.Median = (values[n/2-1] + values[n/2]) / 2
			}
			s.Max = values[n-1]
		}
		each(`SELECT user_id, display_name, `+col+` FROM player_state WHERE `+col+` > 0 ORDER BY `+col+` DESC, user_id LIMIT 15`, func(rows *sql.Rows) {
			var h adminEconomyHolder
			if rows.Scan(&h.PID, &h.Name, &h.Value) == nil {
				if h.Name == "Local" {
					h.Name = ""
				}
				s.Richest = append(s.Richest, h)
			}
		})
		supply[key] = s
	}

	purchases := []adminEconomyPurchaseDay{}
	each(`SELECT substr(purchased_at,1,10), currency, COUNT(*), COALESCE(SUM(price_paid),0) FROM player_purchases
		WHERE purchased_at >= ? GROUP BY 1,2 ORDER BY 1,2`, func(rows *sql.Rows) {
		var p adminEconomyPurchaseDay
		if rows.Scan(&p.Day, &p.Currency, &p.Count, &p.Spent) == nil {
			purchases = append(purchases, p)
		}
	}, since)
	// Currency totals over the window, largest count first.
	currencyTotals := []adminEconomyPurchaseDay{}
	each(`SELECT '', currency, COUNT(*), COALESCE(SUM(price_paid),0) FROM player_purchases
		WHERE purchased_at >= ? GROUP BY 2 ORDER BY 3 DESC, 2`, func(rows *sql.Rows) {
		var p adminEconomyPurchaseDay
		if rows.Scan(&p.Day, &p.Currency, &p.Count, &p.Spent) == nil {
			currencyTotals = append(currencyTotals, p)
		}
	}, since)

	// Top items: real acquisitions only (admin = provisioned starters, refunded
	// = given back), by item id; names resolve through the tech tree tables.
	type itemKey struct {
		id  int32
		cur string
	}
	itemRows := map[itemKey]*adminEconomyItem{}
	each(`SELECT item_id, item_type, currency, COUNT(*), COALESCE(SUM(price_paid),0) FROM player_purchases
		WHERE purchased_at >= ? AND currency NOT IN ('admin','refunded') GROUP BY 1,2,3`, func(rows *sql.Rows) {
		var it adminEconomyItem
		if rows.Scan(&it.ItemID, &it.Type, &it.Currency, &it.Count, &it.Spent) == nil {
			c := it
			itemRows[itemKey{it.ItemID, it.Currency}] = &c
		}
	}, since)
	merged := map[int32]*adminEconomyItem{}
	best := map[int32]int{}
	for _, it := range itemRows {
		m := merged[it.ItemID]
		if m == nil {
			c := *it
			c.Count, c.Spent = 0, 0
			m = &c
			merged[it.ItemID] = m
		}
		m.Count += it.Count
		m.Spent += it.Spent
		if it.Count > best[it.ItemID] || (it.Count == best[it.ItemID] && it.Currency < m.Currency) {
			best[it.ItemID], m.Currency, m.Type = it.Count, it.Currency, it.Type
		}
	}
	items := make([]adminEconomyItem, 0, len(merged))
	for _, it := range merged {
		items = append(items, *it)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].ItemID < items[j].ItemID
	})
	if len(items) > 15 {
		items = items[:15]
	}
	for i := range items {
		items[i].Name = techTreeItemName(items[i].ItemID)
	}

	pay := make([]adminEconomyPayDay, days)
	for i := range pay {
		pay[i].Day = dayList[i]
	}
	each(`SELECT substr(created_at,1,10), COUNT(*), COALESCE(SUM(credits),0), COALESCE(SUM(xp),0)
		FROM battle_results WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) {
		var d string
		var n int
		var c, xp int64
		if rows.Scan(&d, &n, &c, &xp) == nil {
			if i, ok := index[d]; ok {
				pay[i].Results, pay[i].Credits, pay[i].XP = n, c, xp
			}
		}
	}, since)

	// Contract claims. The claim time is not stored; completed_at (Unix
	// seconds) is the nearest day, and a contract is claimed when paid=1 and
	// it was completed. States acknowledged/reset are both "claimed, slot
	// cleared" (daily_contracts.go moves acknowledged to reset at the day reset).
	contracts := make([]adminEconomyContractDay, days)
	for i := range contracts {
		contracts[i].Day = dayList[i]
	}
	each(`SELECT strftime('%Y-%m-%d', completed_at, 'unixepoch'), COUNT(*), COALESCE(SUM(reward),0), COALESCE(SUM(gp_paid),0)
		FROM player_daily_contracts WHERE paid=1 AND completed_at >= ? AND state IN ('acknowledged','reset') GROUP BY 1`, func(rows *sql.Rows) {
		var d string
		var n int
		var c, gp int64
		if rows.Scan(&d, &n, &c, &gp) == nil {
			if i, ok := index[d]; ok {
				contracts[i].Claims, contracts[i].Credits, contracts[i].GP = n, c, gp
			}
		}
	}, first.Unix())
	var unclaimed int
	_ = database.QueryRow(`SELECT COUNT(*) FROM player_daily_contracts WHERE state='completed'`).Scan(&unclaimed)

	sales := []adminEconomySale{}
	nowUnix := now.Unix()
	each(`SELECT id, name, percent, scope, start_at, end_at FROM market_sales WHERE end_at >= ? ORDER BY end_at DESC, id DESC LIMIT 50`, func(rows *sql.Rows) {
		var s adminEconomySale
		if rows.Scan(&s.ID, &s.Name, &s.Percent, &s.Scope, &s.StartAt, &s.EndAt) == nil {
			switch {
			case s.StartAt > nowUnix:
				s.Status = "upcoming"
			case s.EndAt > nowUnix:
				s.Status = "active"
			default:
				s.Status = "ended"
			}
			sales = append(sales, s)
		}
	}, nowUnix-30*86400)

	return map[string]any{
		"days":                days,
		"since":               first.Format(time.RFC3339),
		"day_list":            dayList,
		"supply":              supply,
		"purchases_daily":     purchases,
		"purchase_currencies": currencyTotals,
		"top_items":           items,
		"match_pay_daily":     pay,
		"contract_daily":      contracts,
		"contracts_unclaimed": unclaimed,
		"sales":               sales,
		"settings":            adminSettingsJSON(),
	}
}
