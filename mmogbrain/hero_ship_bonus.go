package main

import (
	"math"
	"strconv"
	"strings"
)

// Hero ship bonus (added 2026-11-24; operator: "the hero ship bonus is not
// applying to the rewards"). It was never implemented: mmogbrain paid nothing
// for heroes, and battle-server-mod gave every fleet ship an all-zero free XP
// list.
//
// The original rule, Grey Box's "Introducing Retrofits" (Steam news, 29 Jun
// 2018): hero ships "give 10% bonus XP and credits. In addition, the XP
// earned is 70% convertible (able to be turned into free XP) and 30% free
// ... This is a bonus that's awarded even if your Hero Ship is just sitting
// idle in your fleet as you play other ships." This build's Market lists the
// benefits as "Earn more Free XP per battle", "Earn more Ship XP per battle"
// and exclusive vanity (UI_Library_MENU_TechTrees SID_heroShipBenefits*):
// no credits, and the credit pools (EYCreditsPoolType) have no hero line, so
// the bonus is XP only (operator, 2026-11-24).
//
// How the client shows it, verified in GatherEomReward (0x340C40, its chunk
// 0x340DD9-0x341C29): for every ship of the active fleet it reads the
// ship's two 13-pool lists from m_matchXPInfo.m_shipsXP (YMatchShipXP +0x08
// m_shipXp, +0x18 m_freeXp; lookup 0x3FB0D0). A ship whose id is a hero
// loadout (0x547970: the id's category is YShipLoadoutHero's, 3) is shown
// from its FREE list ("Free XP pools for heroship were not gathered
// correctly" when it is not 13 long), and 0x3FAFA0 adds to the match's free
// XP that list's total (index 13: every pool but 1-2 and 10-11), its pool 10
// FreeXPFleetBonus and its pool 11 FreeXPFleetBonusBoosted; the ship's own
// ship XP list still counts as ship XP.
//
// So, per hero ship in the match fleet:
//   - flown: its share of the match's ship XP is split heroShipXPPct (70)
//     ship XP, the rest (30) free XP -- the 2018 split;
//   - flown or idle: FreeXPFleetBonus = heroFleetBonusPct (10) % of the
//     match's Scoring line, FreeXPFleetBonusBoosted = the same % of the
//     booster lines, so the two make 10% of the match XP. GUESS: that split
//     between the two lines (the names say "boosted"; no value survives);
//   - at most heroFleetBonusMaxHeroes (5) heroes earn the fleet bonus, a 50%
//     cap. GUESS (operator's choice): the 2018 text sets no cap, and fleets
//     here hold up to 27 ships.

const (
	heroFleetBonusPct       = 10
	heroFleetBonusMaxHeroes = 5
	heroShipXPPct           = 70
	poolFreeXPFleetBonus    = 10 // EYXPPoolType::FreeXPFleetBonus
	poolFreeXPFleetBoosted  = 11 // EYXPPoolType::FreeXPFleetBonusBoosted
)

// isHeroFleetKey is the client's hero test (0x547970) on a fleet ship id: a
// YShipLoadoutHero item id, category 3.
func isHeroFleetKey(id int32) bool { return (id>>24)&0xff == 3 }

// heroXP is what a result's hero ships earn, by fleet ship id.
type heroXP struct {
	ship   map[int32]rewardPools // a flown hero's ship XP list (its 70%)
	free   map[int32]rewardPools // a hero's free XP list (its 30%, the fleet bonus)
	order  []int32               // the keys of free, in fleet order
	freeXP int32                 // the free XP these lists add: each total + pools 10 and 11
}

// perFlownShipPools is one flown ship's ship XP list: the match's, divided
// among the flown ships (the end-of-match screen's ship_xp_pools).
func perFlownShipPools(match rewardPools, flown int) rewardPools {
	p := match
	if flown > 1 {
		for i := range p {
			p[i] /= int32(flown)
		}
	}
	return p
}

// heroRewards: match is the result's ship XP lists (matchPools.shipXP),
// perShip one flown ship's (perFlownShipPools), fleet and flown the fleet's
// and the flown ships' fleet ship ids.
func heroRewards(match, perShip rewardPools, fleet, flown []int32) heroXP {
	h := heroXP{ship: map[int32]rewardPools{}, free: map[int32]rewardPools{}}
	pct := func(v int32, p int32) int32 { return int32(math.Round(float64(v) * float64(p) / 100)) }
	scoring := match[xpPoolLayout.scoring]
	bonus := pct(scoring, heroFleetBonusPct)
	bonusBoosted := pct(match.total()-scoring, heroFleetBonusPct)
	isFlown := map[int32]bool{}
	for _, k := range flown {
		isFlown[k] = true
	}
	earning := 0
	for _, key := range fleet {
		if !isHeroFleetKey(key) {
			continue
		}
		if _, dup := h.free[key]; dup {
			continue
		}
		bonusHere := earning < heroFleetBonusMaxHeroes
		if !isFlown[key] && !bonusHere {
			continue
		}
		var free rewardPools
		if isFlown[key] {
			total := perShip.total()
			shipPart := pct(total, heroShipXPPct)
			h.ship[key] = perShip.scaled(heroShipXPPct, shipPart)
			free = perShip.scaled(100-heroShipXPPct, total-shipPart)
		}
		if bonusHere {
			free[poolFreeXPFleetBonus], free[poolFreeXPFleetBoosted] = bonus, bonusBoosted
			earning++
		}
		h.free[key] = free
		h.order = append(h.order, key)
		h.freeXP += free.total() + free[poolFreeXPFleetBonus] + free[poolFreeXPFleetBoosted]
	}
	return h
}

// listsCSV is "id:p0,...,p12;id:..." for battle-server-mod, in fleet order.
func (h heroXP) listsCSV(lists map[int32]rewardPools) string {
	var parts []string
	for _, key := range h.order {
		if p, ok := lists[key]; ok {
			parts = append(parts, strconv.Itoa(int(key))+":"+p.csv())
		}
	}
	return strings.Join(parts, ";")
}

// matchHeroRewards is heroRewards for a result: the fleet of the match's
// fleet type and the ships the player picked.
func matchHeroRewards(pid string, picked []string, fleetType int, match rewardPools) heroXP {
	flown := flownFleetShipIDs(pid, picked)
	return heroRewards(match, perFlownShipPools(match, len(flown)), battleFleetShipIDs(pid, fleetType), flown)
}

// flownHeroPawns maps the pawn id of each flown hero (how player_ship_xp
// keys ship XP) to its fleet ship id.
func flownHeroPawns(pid string, picked []string) map[int32]int32 {
	out := map[int32]int32{}
	for _, id := range picked {
		if l, ok := battleLoadoutFor(pid, id); ok && l.ship.id != 0 {
			if k := fleetShipKey(l); isHeroFleetKey(k) {
				out[l.ship.id] = k
			}
		}
	}
	return out
}
