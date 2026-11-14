package main

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/handlers"
	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/sirupsen/logrus"
)

// Match results (2026-09-26). Nothing ever reported a finished match: the
// battle server is the client exe, and the original backend's server build --
// which told mmogbrain who won -- is not in it. So no match ever paid XP or
// credits, and no career goal or quest could advance (audit 2026-09-26).
//
// battle-server-mod reads each connected player's result from the host at the
// end of the match (AYPlayerReplicationInfo m_kills/m_deaths/m_assists/damage/
// m_team, AYGameState m_finalMatchResult) and reports it here, once per player.
//
// GET /battle/result?match=&pid=&team=&final=&kills=&deaths=&assists=&damage=&ships=
//
//	team   EYTeam of the player (1, 2)
//	final  EYMatchResult of the match (1 team 1 won, 2 team 2 won, 3 draw, 0 unknown)
//	ships  comma-separated loadout ids the player picked this match
//
// Loopback only, like /battle/loadout. Idempotent: battle_results is keyed by
// (match_id, user_id), so a repeated report grants nothing.
//
// GUESS (the operator's placeholder values, 2026-09-26; the original reward
// tables are lost -- GitHub issue #71): credits 1500 for a win / 750
// otherwise, +100 per kill; XP 1000 / 500, +50 per kill. Each is overridable.
//
// CHANGED 2026-11-14 (operator chose "the game's boosters, base raised"):
// the match's base -- base + performance, the Scoring pool -- now earns the
// game's own boosters (boosters.go), each as its own line, base x
// (multiplier - 1), summed:
//
//	Win Bonus            +25% ship XP, credits, free XP (won matches)
//	First Win of the Day +100% each (first won match since the daily reset)
//	Battle Ready         the match's fleet: Recruit ship XP +100% / credits
//	                     +50%; Veteran +125% / +75% / free XP +30%;
//	                     Legendary +250% / +150% / free XP +30%
//	Elite Status         the player's own: ship XP +100%, credits +100%,
//	                     free XP +50%
//	Elite from Team      per elite ally on the player's team: ship XP +10%,
//	                     credits +10%, free XP +5%
//
// It replaces the operator's 2026-09-28 formula (base x (1 + 1.25 + 0.25 +
// fleet 1.00/1.25/1.50), credits 0.75 for the 1.25), whose fixed terms were
// paid to everyone in the GoldMembership and TeammatesGoldMembership pools --
// so every player got "elite" bonuses and actual Elite Status added nothing.
// To keep a non-elite player's pay where it was, the base is scaled
// (DN_REWARD_XP_SCALE 1.4, DN_REWARD_CREDIT_SCALE 1.6, fitted to the 1128
// results of 2026-11-07..14) and free XP's base is 50% of the XP base
// (DN_REWARD_FREE_XP_PERCENT; it was 25% of the whole payout). GUESS: the
// scales and the free share are calibration, not original data.
type battleRewards struct {
	winCredits, lossCredits, killCredits int32
	winXP, lossXP, killXP                int32
	xpScale, creditScale                 float64
	// freeXPPct is free XP's base as a share of the (scaled) XP base; free
	// XP then earns its own booster percentages.
	freeXPPct int32
	// unplayedShipXPPct is what every ship of the match's fleet that was NOT
	// flown earns: this share of the match's ship XP WITHOUT the performance
	// part, so the performance reward goes only to the ships played. The
	// game's own text: "Whichever fleet you choose to play ... every ship in
	// that fleet will earn Ship XP after you finish a match." GUESS: 10% is
	// the operator's figure (2026-09-30); only flown ships earned anything
	// before.
	unplayedShipXPPct int32
	// eventXP is the match's XP from the ORIGINAL scoring table when the
	// battle server reported it (event_xp, summed by battle-server-mod from
	// AssignScoringReward, plus the end-of-match awards from
	// endOfMatchEventXP). It replaces the win/loss + per-kill XP base;
	// credits keep the placeholder base (the original credit payout,
	// ScoringParamsTable CrBuckets, is not in the datamine).
	eventXP    int32
	useEventXP bool
	// ribbonCredits is what the match's ribbons pay (ribbonCredits in
	// ribbons.go), plus the event credits mmogbrain pays itself
	// (eventCredits: tech cache pickups), added to the credit performance
	// part like kill credits.
	ribbonCredits int32
	boosts        matchBoosts
}

// matchBoosts is which player-specific boosters a result earns. Win and
// Battle Ready follow from the outcome and the match's fleet.
type matchBoosts struct {
	elite       bool // the player's Elite Status was active
	eliteAllies int  // other players on the player's team with Elite Status
	firstWin    bool // the player's first won match since the daily reset
}

// withEventXP is r paying xp as the match's XP base (see eventXP).
func (r battleRewards) withEventXP(xp int32) battleRewards {
	if xp < 0 {
		xp = 0
	}
	r.eventXP, r.useEventXP = xp, true
	return r
}

func (r battleRewards) withBoosts(b matchBoosts) battleRewards {
	if b.eliteAllies < 0 {
		b.eliteAllies = 0
	}
	r.boosts = b
	return r
}

func rewardScale(env string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 100 {
		logrus.WithField("value", v).Warn(env + ": not a number from 0 to 100; using the default")
		return def
	}
	return f
}

func currentBattleRewards() battleRewards {
	n := func(env string, def int32) int32 {
		if v, err := strconv.Atoi(os.Getenv(env)); err == nil && v >= 0 {
			return int32(v)
		}
		return def
	}
	return battleRewards{
		winCredits: n("DN_REWARD_WIN_CREDITS", 1500), lossCredits: n("DN_REWARD_LOSS_CREDITS", 750), killCredits: n("DN_REWARD_KILL_CREDITS", 100),
		winXP: n("DN_REWARD_WIN_XP", 1000), lossXP: n("DN_REWARD_LOSS_XP", 500), killXP: n("DN_REWARD_KILL_XP", 50),
		xpScale:     rewardScale("DN_REWARD_XP_SCALE", 1.4),
		creditScale: rewardScale("DN_REWARD_CREDIT_SCALE", 1.6),
		freeXPPct:   n("DN_REWARD_FREE_XP_PERCENT", 50),

		unplayedShipXPPct: n("DN_REWARD_UNPLAYED_SHIP_XP_PERCENT", 10),
	}
}

// battleOutcome turns the host's numbers into win / loss / draw. Unknown (the
// match result not yet set) pays as a loss.
func battleOutcome(team, final int) string {
	switch {
	case final == 3:
		return "draw"
	case team < 1 || team > 2:
		// The host could not say which side the player was on (the first
		// live report read YT_NONE); a result against "no team" is unknown,
		// not a loss.
		return "unknown"
	case final >= 1 && final <= 2 && final == team:
		return "win"
	case final >= 1 && final <= 2:
		return "loss"
	default:
		return "unknown"
	}
}

// forOutcome is what a result pays: credits and ship XP (also the rank XP
// base, see rankXP).
func (r battleRewards) forOutcome(outcome string, kills int32, fleetType int) (credits, xp int32) {
	p := r.poolsFor(outcome, kills, fleetType)
	return p.creditTotal(), p.shipXP.total()
}

// rewardPools is a payout split the way the client's end-of-match screen
// shows it, 13 entries. The two currencies use DIFFERENT pool enums (names
// in registration order, verified in the exe 2026-11-14):
//
//	EYXPPoolType (ship XP, free XP):
//	  0 Scoring  1 ScoringBase  2 ScoringPerformance  3 BoosterWin
//	  4 BoosterFirstWinOfTheDay  5 GoldMembership  6 TeammatesGoldMembership
//	  7 BattleReadyRecruit  8 BattleReadyVeteran  9 BattleReadyLegendary
//	  10 FreeXPFleetBonus  11 FreeXPFleetBonusBoosted  12 None
//	EYCreditsPoolType (credits):
//	  0 RankUp  1 Scoring  2 ScoringBase  3 ScoringPerformance  4 Quest
//	  5 BoosterWin  6 BoosterFirstWinOfTheDay  7 GoldMembership
//	  8 TeammatesGoldMembership  9-11 BattleReady Recruit/Veteran/Legendary
//	  12 None
//
// CHANGED 2026-11-14: credits were written in the XP order, so the screen
// read our Scoring as RankUp (not counted) and every booster one line off.
// The client's credit total (GatherEomReward 0x340C40, FUN_1405b7ab0) is
// every entry but 2-3 (the breakdown of Scoring), less RankUp and Quest,
// and counts each booster line only when that booster is active for the
// player (ClientSetActiveBoosterIDs, sent by battle-server-mod from
// booster_ids=); the XP total is every entry but 1-2 and 10-11 (0x3FAFA0).
const rewardPoolCount = 13

type rewardPools [rewardPoolCount]int32

// poolLayout is where each line goes in one pool enum.
type poolLayout struct {
	scoring, base, performance, win, firstWin, gold, teamGold int
	battleReady                                               [3]int // Recruit, Veteran, Legendary
	notCounted                                                []int  // entries the total leaves out
}

var xpPoolLayout = poolLayout{scoring: 0, base: 1, performance: 2, win: 3, firstWin: 4, gold: 5, teamGold: 6,
	battleReady: [3]int{7, 8, 9}, notCounted: []int{1, 2, 10, 11}}

var creditPoolLayout = poolLayout{scoring: 1, base: 2, performance: 3, win: 5, firstWin: 6, gold: 7, teamGold: 8,
	battleReady: [3]int{9, 10, 11}, notCounted: []int{2, 3}}

// matchPools is one result's three currencies.
type matchPools struct {
	credits, shipXP, freeXP rewardPools
}

// total is what the player is paid in one currency.
func (p rewardPools) total(layout ...poolLayout) int32 {
	l := xpPoolLayout
	if len(layout) > 0 {
		l = layout[0]
	}
	var t int32
	for i, v := range p {
		if containsInt(l.notCounted, i) {
			continue
		}
		t += v
	}
	return t
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// creditTotal is a credit pool array's total (its own layout).
func (p matchPools) creditTotal() int32 { return p.credits.total(creditPoolLayout) }

// scaled is p (an XP layout) with every pool multiplied by pct/100, rounding
// differences going to pool 0 so the counted total is exactly want.
func (p rewardPools) scaled(pct int32, want int32) rewardPools {
	var q rewardPools
	for i, v := range p {
		q[i] = int32(math.Round(float64(v) * float64(pct) / 100))
	}
	q[0] += want - q.total()
	return q
}

// unplayedShipPools is what each fleet ship that was not flown earns: the
// match's ship XP pools without the performance part, scaled to
// unplayedShipXPPct.
func (r battleRewards) unplayedShipPools(outcome string, fleetType int) rewardPools {
	base := r.poolsFor(outcome, 0, fleetType).shipXP
	want := int32(math.Round(float64(base.total()) * float64(r.unplayedShipXPPct) / 100))
	return base.scaled(r.unplayedShipXPPct, want)
}

// rankXP is the rank (reputation) XP of a result: the ship XP without the
// Elite lines -- Elite Status has no reputation effect (boosters.go).
// GUESS: rank XP otherwise followed the whole match XP before 2026-11-14 and
// still does; the game's own reputation boost is First Win of the Day's.
func rankXP(ship rewardPools) int32 {
	return ship.total() - ship[xpPoolLayout.gold] - ship[xpPoolLayout.teamGold]
}

func (p rewardPools) csv() string {
	parts := make([]string, len(p))
	for i, v := range p {
		parts[i] = strconv.Itoa(int(v))
	}
	return strings.Join(parts, ",")
}

func (r battleRewards) poolsFor(outcome string, kills int32, fleetType int) matchPools {
	if kills < 0 {
		kills = 0
	}
	if kills > 500 { // no match has that many; caps a malformed report
		kills = 500
	}
	baseCredits, baseXP := r.lossCredits, r.lossXP
	if outcome == "win" {
		baseCredits, baseXP = r.winCredits, r.winXP
	}
	xpPerf := r.killXP * kills
	if r.useEventXP {
		// All of it as base: the event XP is not split into a win/loss base
		// and a kill performance, and unplayedShipPools takes its share of
		// the base.
		baseXP, xpPerf = r.eventXP, 0
	}
	xpBase, xpPerfScaled := scaledParts(baseXP, xpPerf, r.xpScale)
	freeBase, freePerf := scaledParts(xpBase, xpPerfScaled, float64(r.freeXPPct)/100)
	crBase, crPerf := scaledParts(baseCredits, r.killCredits*kills+r.ribbonCredits, r.creditScale)
	return matchPools{
		credits: r.boost(crBase, crPerf, boostedCredits, outcome, fleetType, creditPoolLayout),
		shipXP:  r.boost(xpBase, xpPerfScaled, boostedShipXP, outcome, fleetType, xpPoolLayout),
		freeXP:  r.boost(freeBase, freePerf, boostedFreeXP, outcome, fleetType, xpPoolLayout),
	}
}

// scaledParts scales a base and a performance part, rounding their sum once
// so the breakdown adds up to the Scoring line.
func scaledParts(base, performance int32, scale float64) (int32, int32) {
	b := int32(math.Round(float64(base) * scale))
	sum := int32(math.Round(float64(base+performance) * scale))
	return b, sum - b
}

// boost lays out one currency: the Scoring line (base + performance, with
// its breakdown) and each booster the result earns as base x (multiplier -
// 1) in its own line.
func (r battleRewards) boost(base, performance, currency int32, outcome string, fleetType int, l poolLayout) rewardPools {
	var p rewardPools
	scoring := base + performance
	p[l.scoring], p[l.base], p[l.performance] = scoring, base, performance
	part := func(f float64) int32 { return int32(math.Round(float64(scoring) * f)) }
	if outcome == "win" {
		p[l.win] = part(commonBooster(boosterTypeWin).bonus(currency, boostSelf))
		if r.boosts.firstWin {
			p[l.firstWin] = part(commonBooster(boosterTypeFirstWin).bonus(currency, boostSelf))
		}
	}
	if r.boosts.elite {
		p[l.gold] = part(goldMembership.bonus(currency, boostSelf))
	}
	if r.boosts.eliteAllies > 0 {
		p[l.teamGold] = part(goldMembership.bonus(currency, boostTeammates) * float64(r.boosts.eliteAllies))
	}
	ft := fleetType
	if ft < 1 || ft > 3 {
		ft = 1
	}
	p[l.battleReady[ft-1]] = part(battleReadyBooster(ft).bonus(currency, boostSelf))
	return p
}

// activeBoosterIDs is what battle-server-mod sends the player as
// ClientSetActiveBoosterIDs: the boosters this result earned. The rewards
// screen counts and labels a booster line only for an id listed here
// (0x345980 sets the flag of each listed id's booster type).
func (r battleRewards) activeBoosterIDs(outcome string, fleetType int) []int32 {
	var ids []int32
	if outcome == "win" {
		ids = append(ids, boosterIDWin)
		if r.boosts.firstWin {
			ids = append(ids, boosterIDFirstWin)
		}
	}
	if r.boosts.elite {
		ids = append(ids, boosterIDGoldMembership)
	}
	return append(ids, battleReadyBooster(fleetType).id)
}

func battleResultHandler(w http.ResponseWriter, r *http.Request) {
	if refuseUnlessLoopback(w, r) {
		return
	}
	q := r.URL.Query()
	match := strings.TrimSpace(q.Get("match"))
	// Not normalizedPlayerStatePID: it falls back to the default dev player,
	// and a report with a bad pid must pay nobody.
	pid := protocol.NormalizePlayerPID(q.Get("pid"))
	if match == "" || pid == "" {
		http.Error(w, "match and pid are required", http.StatusBadRequest)
		return
	}
	num := func(k string) int { v, _ := strconv.Atoi(q.Get(k)); return v }
	res := battleResult{
		match: match, pid: pid, team: num("team"),
		outcome: battleOutcome(num("team"), num("final")),
		kills:   int32(num("kills")), deaths: int32(num("deaths")), assists: int32(num("assists")), damage: int32(num("damage")),
	}
	for _, id := range strings.Split(q.Get("ships"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			res.ships = append(res.ships, id)
		}
	}
	rewards := currentBattleRewards()
	// The match's fleet tier, for the log line and the pools below.
	// recordBattleResult resolves it again on its own copy of res; the
	// handler's copy stayed 0, so the "battle result" line said fleet_type=0
	// for every match (149 of 149 on 2026-11-09; the rows were right).
	fleetType := 0
	if database := currentMmogPlayerStateDB(); database != nil {
		fleetType = matchFleetType(database, res.match)
	}
	res.fleetType = fleetType
	var eventXP, eomXP int32
	matchMode, _ := matchModeAndStart(currentMmogPlayerStateDB(), match)
	scoringMode := scoringModeCode(matchMode)
	// The ribbons the match's event counts earn (ribbons.go), and their XP.
	eventCounts := parseScoringEventCounts(q.Get("ev"))
	ribbons, ribbonXP := matchRibbons(eventCounts, scoringMode)
	ribbonCR := ribbonCredits(ribbons, scoringMode)
	if ribbonCR > 0 && os.Getenv("DN_REWARD_RIBBON_CREDITS") != "0" {
		rewards.ribbonCredits = ribbonCR
	}
	paidEventCR := eventCredits(eventCounts, scoringMode)
	if paidEventCR > 0 && os.Getenv("DN_REWARD_EVENT_CREDITS") != "0" {
		rewards.ribbonCredits += paidEventCR
	}
	if v := strings.TrimSpace(q.Get("event_xp")); v != "" && os.Getenv("DN_REWARD_EVENT_XP") != "0" {
		eventXP = int32(num("event_xp"))
		eomXP, scoringMode = endOfMatchEventXP(currentMmogPlayerStateDB(), match)
		rewards = rewards.withEventXP(eventXP + eomXP + ribbonXP)
	}
	// Elite Status, elite allies and First Win of the Day (boosters.go).
	// Read before recordBattleResult: the store has one connection.
	boosts, eliteShare := battleBoosts(currentMmogPlayerStateDB(), res, time.Now())
	rewards = rewards.withBoosts(boosts)
	credits, xp, gains, fresh, err := recordBattleResult(res, rewards)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{"match": match, "player": pid}).Error("battle result: not recorded")
		http.Error(w, "not recorded", http.StatusInternalServerError)
		return
	}
	if fresh {
		if database := currentMmogPlayerStateDB(); database != nil {
			recordRibbons(database, pid, ribbons)
			// The Statistics screen's score, healing, double kills, first
			// strikes and time per ship type (match_statistics.go).
			stats := matchStatValues(q, eventCounts, scoringMode, res.ships, matchPlaySeconds(database, match))
			// The mod's fair-play checks, stored beside them (fair_play_checks.go).
			checks, flags := fairPlayCheckStats(q)
			for k, v := range checks {
				stats[k] = v
			}
			logFairPlayFlags(match, pid, flags, checks)
			recordMatchStats(database, match, pid, stats)
			logrus.WithFields(logrus.Fields{"match": match, "player": pid, "stats": stats,
				"from_mod": q.Has("ct")}).Info("statistics: match recorded")
		}
		// Daily contracts: this match's progress. Completed ones pay when the
		// player claims them, not here (daily_contracts.go).
		if applyContractProgress(pid, battleContractMatch(res, q), time.Now()) {
			squadHubInstance.push(pid, buildMmogContractRefreshPush(pid))
		}
		// The hangar's credits only change through YA_RewardCurrencies, whose
		// handler ASSIGNS Credits/Points (buildMmogRewardCurrenciesPayload); it
		// was sent at login only, so the hangar kept the pre-match balance
		// until a restart (operator, 2026-09-29).
		squadHubInstance.push(pid, buildMmogRewardCurrenciesPayload(pid))
		// ...and free XP and ship XP, which reach the client only through
		// YA_PlayerGet (login) and never refreshed after a match ("the free xp
		// is not getting updated ... i need to restart the game", operator
		// 2026-09-30). See buildMmogShipXPSyncPush.
		squadHubInstance.push(pid, buildMmogShipXPSyncPush(pid, gains))
	}
	logrus.WithFields(logrus.Fields{"match": match, "player": pid, "outcome": res.outcome, "fleet_type": res.fleetType, "kills": res.kills,
		"deaths": res.deaths, "credits": credits, "xp": xp, "ships": res.ships, "new": fresh,
		"event_xp": eventXP, "eom_xp": eomXP, "scoring_mode": scoringMode, "events": q.Get("events"),
		"event_credits": q.Get("event_credits"), "ribbons": ribbons, "ribbon_xp": ribbonXP, "ribbon_credits": ribbonCR, "paid_event_credits": paidEventCR,
		"event_counts": scoringEventNames(eventCounts), "elite": boosts.elite, "elite_allies": boosts.eliteAllies,
		"first_win": boosts.firstWin}).Info("battle result")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "outcome=%s\ncredits=%d\nxp=%d\nnew=%v\n", res.outcome, credits, xp, fresh)
	// The same payout split into pools, for the end-of-match screen: the mod
	// writes these into the player's m_creditsInfo / m_matchXPInfo (the data
	// the missing server build used to fill), so the screen shows what was
	// paid. See rewardPools.
	pools := rewards.poolsFor(res.outcome, res.kills, fleetType)
	flown := flownFleetShipIDs(res.pid, res.ships)
	shipPools := pools.shipXP
	if len(flown) > 1 {
		for i := range shipPools {
			shipPools[i] /= int32(len(flown))
		}
	}
	// credit_pools are in EYCreditsPoolType order, xp_pools (the screen's
	// FREE XP, m_matchXPInfo.m_freeXP) and the ship pools in EYXPPoolType
	// order (rewardPools). unplayed_ship_xp_pools is what every fleet ship
	// NOT flown earned (see unplayedShipXPPct).
	_, _ = fmt.Fprintf(w, "credit_pools=%s\nxp_pools=%s\nship_xp_pools=%s\nunplayed_ship_xp_pools=%s\nfleet_ships=%s\nflown_ships=%s\n",
		pools.credits.csv(), pools.freeXP.csv(), shipPools.csv(), rewards.unplayedShipPools(res.outcome, fleetType).csv(),
		joinInt32s(battleFleetShipIDs(res.pid, fleetType)), joinInt32s(flown))
	// booster_ids= / elite_share= are what the mod sends the player as
	// ClientSetActiveBoosterIDs (PRI+0x7B8) and ClientSetEliteSharePlayers
	// (PRI+0x790; its count +0x798 is the "Elite from Team" multiplier): the
	// rewards screen counts and labels a booster line only when its booster
	// is listed, and shows the elite allies by name ("Includes Elite Bonus
	// from {0}"). The original server build sent both; nothing in this exe
	// does (2026-11-14).
	_, _ = fmt.Fprintf(w, "booster_ids=%s\nelite_share=%s\n",
		joinInt32s(rewards.activeBoosterIDs(res.outcome, fleetType)), strings.Join(eliteShare, ","))
}

type battleResult struct {
	match, pid, outcome            string
	team                           int
	fleetType                      int // EYFleetType of the match; 0 = match not found (pays Recruit)
	kills, deaths, assists, damage int32
	ships                          []string // loadout ids picked this match
}

// recordBattleResult stores the result and grants its rewards in one
// transaction. fresh is false when this (match, player) was already recorded.
// gains is the ship XP each ship earned, by pawn id.
func recordBattleResult(res battleResult, rewards battleRewards) (credits, xp int32, gains map[int32]int32, fresh bool, err error) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return 0, 0, nil, false, fmt.Errorf("database unavailable")
	}
	if err := seedMmogPlayerState(database, res.pid); err != nil {
		return 0, 0, nil, false, err
	}
	res.fleetType = matchFleetType(database, res.match)
	pools := rewards.poolsFor(res.outcome, res.kills, res.fleetType)
	credits, xp = pools.creditTotal(), pools.shipXP.total()

	// Ship XP goes to the hulls actually flown, resolved to pawn ids the way
	// player_ship_xp keys them, and a smaller share to the rest of the fleet.
	// Resolved BEFORE the transaction: the store has one connection, and a
	// query inside an open transaction waits for itself.
	ships := flownShipIDs(res.pid, res.ships)
	unplayed := unplayedFleetPawnIDs(res.pid, res.fleetType, ships)
	unplayedXP := rewards.unplayedShipPools(res.outcome, res.fleetType).total()

	gains = map[int32]int32{}
	if len(ships) > 0 {
		for _, ship := range ships {
			gains[ship] += xp / int32(len(ships))
		}
	}
	for _, ship := range unplayed {
		gains[ship] += unplayedXP
	}

	tx, err := database.Begin()
	if err != nil {
		return 0, 0, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	ins, err := tx.Exec(`INSERT OR IGNORE INTO battle_results(match_id,user_id,team,outcome,kills,deaths,assists,damage,credits,xp,fleet_type)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, res.match, res.pid, res.team, res.outcome, res.kills, res.deaths, res.assists, res.damage, credits, xp, res.fleetType)
	if err != nil {
		return 0, 0, nil, false, err
	}
	if n, _ := ins.RowsAffected(); n == 0 {
		return credits, xp, nil, false, nil // already paid
	}
	if err := grantBattleRewards(tx, res.pid, credits, rankXP(pools.shipXP), xp, pools.freeXP.total(), ships, unplayed, unplayedXP); err != nil {
		return 0, 0, nil, false, err
	}
	return credits, xp, gains, true, tx.Commit()
}

// matchFleetType is the EYFleetType of the match a result was reported for,
// or 0 when no match record has that battle-server match id (a match formed
// before battle_match_id was recorded, or a host started by hand). The mod
// appends "-r<n>" to the id for a second round on the same host.
func matchFleetType(database *sql.DB, battleMatchID string) int {
	if i := strings.LastIndex(battleMatchID, "-r"); i > 0 {
		if _, err := strconv.Atoi(battleMatchID[i+2:]); err == nil {
			battleMatchID = battleMatchID[:i]
		}
	}
	var fleetType int
	if err := database.QueryRow(`SELECT fleet_type FROM matches WHERE battle_match_id=? AND battle_match_id!=''`, battleMatchID).Scan(&fleetType); err != nil {
		return 0
	}
	return fleetType
}

// flownShipIDs resolves the loadout ids a player picked to pawn (ship item)
// ids, once each.
func flownShipIDs(pid string, loadoutIDs []string) []int32 {
	var ships []int32
	seen := map[int32]bool{}
	for _, id := range loadoutIDs {
		if loadout, ok := battleLoadoutFor(pid, id); ok && loadout.ship.id != 0 && !seen[loadout.ship.id] {
			seen[loadout.ship.id] = true
			ships = append(ships, loadout.ship.id)
		}
	}
	return ships
}

// battleFleetShipIDs lists the ships of the fleet the player fought with (the
// active fleet of the match's fleet type), by FLEET SHIP id. The client's
// rewards screen walks every ship of its active fleet and looks each up by
// that id (GatherEomRewardsData 0x340C40 -> 0x3FB0D0); a ship with no entry
// is reported as "Ship XP pools were not gathered correctly. ... Ship ID {1}"
// (YA_LogSpecial, client_reports).
//
// VERIFIED 2026-09-28: keyed by pawn id (184483981...), the client filed
// ShipXpError for Ship IDs 33489265/67/68/69/70 -- exactly the fleetShipID
// (= precast loadout id) of the five ships in that player's active fleet.
func battleFleetShipIDs(pid string, fleetType int) []int32 {
	var ids []int32
	seen := map[int32]bool{}
	for _, l := range battleFleetLoadouts(pid, fleetType) {
		if id := fleetShipKey(l); id != 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// battleFleetLoadouts is the fleet the player fought with: the active fleet
// of the match's fleet type.
//
// Chosen among ALL the player's fleets, not the "active" ones: active marks
// the single fleet selected in the hangar, so Veteran and Legendary fleets are
// normally inactive (every account, measured 2026-10-01). Looking only at
// active fleets found no fleet of type 2 or 3, fell back to the Recruit
// fleet, and after every Veteran match the rewards were written for the
// Recruit ships -- the client then reported "Ship XP pools were not gathered
// correctly" for each Veteran ship it fielded (client reports, 2026-10-01),
// and the unflown-ship XP went to ships that were not in the match.
func battleFleetLoadouts(pid string, fleetType int) []mmogShipLoadoutSeed {
	state := mmogPlayerStateForPID(pid)
	var match *mmogFleetSeed
	for i := range state.fleets {
		if int(state.fleets[i].fleetType) != fleetType {
			continue
		}
		if match == nil || state.fleets[i].active {
			match = &state.fleets[i]
		}
	}
	if match != nil {
		return match.shipLoadouts
	}
	return state.activeFleet().shipLoadouts
}

// unplayedFleetPawnIDs is the fleet's ships that were not flown, by pawn id
// (how player_ship_xp keys them), once each.
func unplayedFleetPawnIDs(pid string, fleetType int, flown []int32) []int32 {
	skip := map[int32]bool{}
	for _, id := range flown {
		skip[id] = true
	}
	var ids []int32
	for _, l := range battleFleetLoadouts(pid, fleetType) {
		if id := l.ship.id; id != 0 && !skip[id] {
			skip[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func fleetShipKey(l mmogShipLoadoutSeed) int32 {
	if l.fleetShipID != 0 {
		return l.fleetShipID
	}
	return l.precastLoadoutID
}

// flownFleetShipIDs is flownShipIDs keyed the way the rewards screen looks
// ships up (fleet ship id), not by pawn id.
func flownFleetShipIDs(pid string, loadoutIDs []string) []int32 {
	var ids []int32
	seen := map[int32]bool{}
	for _, id := range loadoutIDs {
		if loadout, ok := battleLoadoutFor(pid, id); ok {
			if k := fleetShipKey(loadout); k != 0 && !seen[k] {
				seen[k] = true
				ids = append(ids, k)
			}
		}
	}
	return ids
}

// battleBoosts is which player-specific boosters a result earns, and the
// names of the elite allies (for ClientSetEliteSharePlayers).
//
// Elite: the player's membership was active (player_membership). Elite
// allies: the other players of the match on the player's team (the team the
// host reported, else the matchmaker's) whose membership was active -- the
// asset's YBTT_TEAMMATES, "Allies who join you in battle will also receive a
// small bonus ... Go into battle together with other Elite Status Captains
// to stack the bonuses"; the client multiplies the ally percentage by their
// number (0x340C40 x PRI+0x798). Players come from the matchmaker's slots
// (deleted as each player leaves) and the results already reported, so an
// ally who left before the end still counts if their result is in.
// First win: no won match since the daily reset (contractPeriodStart).
// GUESS: the original's day boundary for First Win of the Day is not in
// the client; the daily contracts' reset is used.
func battleBoosts(database *sql.DB, res battleResult, now time.Time) (matchBoosts, []string) {
	var b matchBoosts
	if database == nil {
		return b, nil
	}
	b.elite = int64(membershipExpiresAt(res.pid)) > now.Unix()
	if res.outcome == "win" {
		var wins int
		_ = database.QueryRow(`SELECT COUNT(*) FROM battle_results WHERE user_id=? AND outcome='win' AND match_id!=? AND created_at>=?`,
			res.pid, res.match, contractPeriodStart(now).UTC().Format("2006-01-02 15:04:05")).Scan(&wins)
		b.firstWin = wins == 0
	}
	battleID := res.match
	if i := strings.LastIndex(battleID, "-r"); i > 0 {
		if _, err := strconv.Atoi(battleID[i+2:]); err == nil {
			battleID = battleID[:i]
		}
	}
	teams := map[string]int{}
	if rows, err := database.Query(`SELECT s.user_id, s.team FROM match_slots s JOIN matches m ON m.id=s.match_id
		WHERE m.battle_match_id=? AND m.battle_match_id!=''`, battleID); err == nil {
		for rows.Next() {
			var id string
			var team int
			if rows.Scan(&id, &team) == nil {
				teams[id] = team
			}
		}
		_ = rows.Close()
	}
	// The host's own report wins over the matchmaker's slot.
	if rows, err := database.Query(`SELECT user_id, team FROM battle_results WHERE match_id=? AND team>0`, res.match); err == nil {
		for rows.Next() {
			var id string
			var team int
			if rows.Scan(&id, &team) == nil {
				teams[id] = team
			}
		}
		_ = rows.Close()
	}
	team := res.team
	if team < 1 {
		team = teams[res.pid]
	}
	if team < 1 {
		return b, nil
	}
	var allies []string
	for id, t := range teams {
		if id != res.pid && t == team {
			allies = append(allies, id)
		}
	}
	sort.Strings(allies)
	var names []string
	for _, id := range allies {
		if int64(membershipExpiresAt(id)) <= now.Unix() {
			continue
		}
		b.eliteAllies++
		var name string
		_ = database.QueryRow(`SELECT display_name FROM player_state WHERE user_id=?`, id).Scan(&name)
		// One line, comma-separated: names cannot hold a comma (the rename
		// pattern) but an old one might. An ally without a name is still
		// listed (empty): the screen multiplies the ally bonus by the
		// number of entries.
		name = strings.TrimSpace(strings.NewReplacer(",", " ", "\n", " ", "\r", " ").Replace(name))
		if name == "Local" {
			name = ""
		}
		names = append(names, name)
	}
	return b, names
}

func joinInt32s(v []int32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.Itoa(int(x))
	}
	return strings.Join(parts, ",")
}

// grantBattleRewards pays a result: credits, rank XP (gained), ship XP xp
// split over the ships flown, free XP, and unplayedXP to each unflown fleet
// ship.
func grantBattleRewards(tx *sql.Tx, pid string, credits, gained, xp, freeXP int32, ships, unplayed []int32, unplayedXP int32) error {
	var currentXP, rank, rankXP int32
	if err := tx.QueryRow(`SELECT current_xp, current_rank, rank_xp FROM player_state WHERE user_id=?`, pid).
		Scan(&currentXP, &rank, &rankXP); err != nil {
		return err
	}
	// Rank progression with the same thresholds as /internal/progression.
	rankXP += gained
	for {
		threshold := handlers.RankXPThreshold(rank + 1)
		if threshold <= 0 || rankXP < threshold {
			break
		}
		rankXP -= threshold
		rank++
	}
	if _, err := tx.Exec(`UPDATE player_state SET soft_currency=soft_currency+?, free_xp=free_xp+?,
		current_xp=current_xp+?, current_rank=?, rank_xp=?, updated_at=datetime('now') WHERE user_id=?`,
		credits, freeXP, gained, rank, rankXP, pid); err != nil {
		return err
	}
	addShipXP := func(ship, amount int32) error {
		if amount <= 0 {
			return nil
		}
		_, err := tx.Exec(`INSERT INTO player_ship_xp(user_id,ship_id,xp) VALUES(?,?,?)
			ON CONFLICT(user_id,ship_id) DO UPDATE SET xp=xp+?, updated_at=datetime('now')`, pid, ship, amount, amount)
		return err
	}
	if len(ships) > 0 {
		share := xp / int32(len(ships))
		for _, ship := range ships {
			if err := addShipXP(ship, share); err != nil {
				return err
			}
		}
	}
	for _, ship := range unplayed {
		if err := addShipXP(ship, unplayedXP); err != nil {
			return err
		}
	}
	return nil
}

// battleResultCounter is the server's own count for a career-goal counter,
// from recorded results: matches played, won, and enemy ships destroyed.
func battleResultCounter(playerPID, counterID string) int32 {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return 0
	}
	var query string
	switch counterID {
	case "MatchesPlayed":
		query = `SELECT COUNT(*) FROM battle_results WHERE user_id=?`
	case "MatchesWon":
		query = `SELECT COUNT(*) FROM battle_results WHERE user_id=? AND outcome='win'`
	case "ShipsDestroyed":
		query = `SELECT COALESCE(SUM(kills),0) FROM battle_results WHERE user_id=?`
	default:
		return 0
	}
	var v int32
	if err := database.QueryRow(query, normalizedPlayerStatePID(playerPID)).Scan(&v); err != nil {
		return 0
	}
	return v
}

// battleContractMatch is what this result counts towards daily contracts. The
// battle server adds kl= (one "victim.killer" EYShipClass pair per kill) and
// score= once it reports them; their presence turns the class and score
// contracts on (markContractDetailReported).
func battleContractMatch(res battleResult, q url.Values) contractMatch {
	m := contractMatch{outcome: res.outcome, kills: res.kills}
	database := currentMmogPlayerStateDB()
	if database != nil {
		_ = database.QueryRow(`SELECT game_mode FROM matches WHERE battle_match_id=? AND battle_match_id<>'' LIMIT 1`, res.match).Scan(&m.mode)
	}
	if q.Has("kl") {
		m.hasDetail = true
		m.detail = parseContractKills(q.Get("kl"))
		markContractDetailReported(database)
	}
	if v := strings.TrimSpace(q.Get("score")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			m.score, m.hasScore = int32(n), true
		}
	}
	return m
}
