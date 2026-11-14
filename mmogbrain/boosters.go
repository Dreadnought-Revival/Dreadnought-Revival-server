package main

// The game's match boosters: Win, First Win of the Day, the three Battle Ready
// fleet boosters and Elite Status. Values copied verbatim from the cooked
// assets (read 2026-11-14 with a tagged-property reader):
//
//	DevGroup/Meta/Booster/COMMONBOOSTER_Booster_{Win,First_Win_Of_The_Day,
//	  BattleReadyRecruit,BattleReadyVeteran,BattleReadyLegendary}_DA
//	DevGroup/Meta/GoldMembership/SHOP_Booster_GoldMember_DA
//
// The five common ones are the client's m_activeCommonBoosters
// (DefaultProgression.ini, YBoosterComponent); Elite is
// YGoldMembershipComponent m_goldMembershipBoosterID = 536805377. Every effect
// applies to the Scoring pool (m_appliesToCreditsPool EYCreditsPoolType::
// Scoring, m_appliesToReputationPool EYRPT_Scoring): a booster is a
// percentage of the match's base score, shown as its own line on the
// end-of-match rewards screen. The client draws that percentage as
// (multiplier - 1) * 100 (UYEndOfMatchDataManager 0x3456B0) and the Elite
// credits line as Scoring * (multiplier - 1) itself (GatherEomReward
// 0x340C40), so each line is base * (multiplier - 1), summed -- not the
// multipliers multiplied together.
//
// Enum ordinals (registration order of the names in the exe):
//
//	EYBoosterType      0 WIN, 1 FIRSTWINOFTHEDAY, 2 GOLDMEMBERSHIP,
//	                   3 BATTLEREADYRECRUIT, 4 BATTLEREADYVETERAN,
//	                   5 BATTLEREADYLEGENDARY
//	EYBoostedCurrency  0 NONE, 1 REPUTATION, 2 CREDITS, 3 SHIPXP, 4 FREEXP
//	EYBoostedTarget    0 NONE, 1 SELF, 2 TEAMMATES
//	EYCreditsPoolType  0 RankUp, 1 Scoring, 2 ScoringBase, 3 ScoringPerformance,
//	                   4 Quest, 5 BoosterWin, 6 BoosterFirstWinOfTheDay,
//	                   7 GoldMembership, 8 TeammatesGoldMembership,
//	                   9 BattleReadyRecruit, 10 BattleReadyVeteran,
//	                   11 BattleReadyLegendary, 12 None
//	EYReputationPoolType 0 EYRPT_Scoring, ...
//
// The original backend computed rewards from the same table (its export path,
// BoosterTable.cfg, is in DefaultProgression.ini); the client receives it as
// YA_GetBoosterData and uses it only to label the rewards screen.

const (
	boosterTypeWin = iota
	boosterTypeFirstWin
	boosterTypeGold
	boosterTypeBattleReadyRecruit
	boosterTypeBattleReadyVeteran
	boosterTypeBattleReadyLegendary
)

const (
	boostedReputation = 1
	boostedCredits    = 2
	boostedShipXP     = 3
	boostedFreeXP     = 4
)

const (
	boostSelf      = 1
	boostTeammates = 2
)

const (
	creditsPoolScoring = 1 // EYCreditsPoolType::Scoring
	repPoolScoring     = 0 // EYReputationPoolType::EYRPT_Scoring
)

// Booster item ids.
const (
	boosterIDWin                = 520028165
	boosterIDFirstWin           = 520028166
	boosterIDBattleReadyLegend  = 520028167
	boosterIDBattleReadyRecruit = 520028169
	boosterIDBattleReadyVeteran = 520028170
	boosterIDGoldMembership     = 536805377
)

type boosterEffect struct {
	currency     int32
	target       int32
	creditsPools []int32
	repPools     []int32
	multiplier   float64
}

type booster struct {
	id          int32
	boosterType int32
	effects     []boosterEffect
}

var scoringPools = []int32{creditsPoolScoring}
var scoringRepPools = []int32{repPoolScoring}

// commonBoosters is the client's BoosterTable, in the order of
// m_activeCommonBoosters.
var commonBoosters = []booster{
	{boosterIDWin, boosterTypeWin, []boosterEffect{
		{boostedFreeXP, boostSelf, scoringPools, scoringRepPools, 1.25},
		{boostedCredits, boostSelf, scoringPools, scoringRepPools, 1.25},
		{boostedShipXP, boostSelf, scoringPools, scoringRepPools, 1.25},
	}},
	{boosterIDFirstWin, boosterTypeFirstWin, []boosterEffect{
		{boostedReputation, boostSelf, scoringPools, scoringRepPools, 2.0},
		{boostedCredits, boostSelf, scoringPools, scoringRepPools, 2.0},
		{boostedShipXP, boostSelf, scoringPools, scoringRepPools, 2.0},
		{boostedFreeXP, boostSelf, nil, nil, 2.0},
	}},
	{boosterIDBattleReadyLegend, boosterTypeBattleReadyLegendary, []boosterEffect{
		{boostedCredits, boostSelf, scoringPools, scoringRepPools, 2.5},
		{boostedShipXP, boostSelf, nil, nil, 3.5},
		{boostedFreeXP, boostSelf, nil, nil, 1.3},
	}},
	{boosterIDBattleReadyRecruit, boosterTypeBattleReadyRecruit, []boosterEffect{
		{boostedCredits, boostSelf, scoringPools, nil, 1.5},
		{boostedShipXP, boostSelf, nil, nil, 2.0},
	}},
	{boosterIDBattleReadyVeteran, boosterTypeBattleReadyVeteran, []boosterEffect{
		{boostedCredits, boostSelf, scoringPools, nil, 1.75},
		{boostedShipXP, boostSelf, nil, nil, 2.25},
		{boostedFreeXP, boostSelf, nil, nil, 1.3},
	}},
}

// goldMembership is Elite Status, the client's GoldMembershipTable. The
// client reads the Elite credits multiplier from its FIRST entry (0x340C40
// walks entry 0's effects for SELF + CREDITS). The day packages
// (536805378..385) carry no effects of their own and are not listed.
var goldMembership = booster{boosterIDGoldMembership, boosterTypeGold, []boosterEffect{
	{boostedShipXP, boostSelf, scoringPools, nil, 2.0},
	{boostedShipXP, boostTeammates, scoringPools, nil, 1.1},
	{boostedFreeXP, boostSelf, nil, nil, 1.5},
	{boostedFreeXP, boostTeammates, nil, nil, 1.05},
	{boostedCredits, boostSelf, nil, nil, 2.0},
	{boostedCredits, boostTeammates, nil, nil, 1.1},
}}

// battleReadyBooster is the Battle Ready booster of a fleet type (1 Recruit,
// 2 Veteran, 3 Legendary; anything else is Recruit, as the reward code
// treats an unknown match).
func battleReadyBooster(fleetType int) booster {
	want := int32(boosterTypeBattleReadyRecruit)
	switch fleetType {
	case 2:
		want = boosterTypeBattleReadyVeteran
	case 3:
		want = boosterTypeBattleReadyLegendary
	}
	for _, b := range commonBoosters {
		if b.boosterType == want {
			return b
		}
	}
	return booster{}
}

func commonBooster(boosterType int32) booster {
	for _, b := range commonBoosters {
		if b.boosterType == boosterType {
			return b
		}
	}
	return booster{}
}

// bonus is what the booster adds for one currency and target, as a fraction
// of the base (multiplier - 1); 0 when it has no such effect.
func (b booster) bonus(currency, target int32) float64 {
	for _, e := range b.effects {
		if e.currency == currency && e.target == target {
			return e.multiplier - 1
		}
	}
	return 0
}
