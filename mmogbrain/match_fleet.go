package main

import (
	"github.com/sirupsen/logrus"
)

// The fleet a player takes into a match.
//
// How the original game kept player, fleet and match in step, read from the
// client exe 2026-11-11 (the battle server is the same exe):
//
//   - Solo: AYMenu::FindAMatch (0x4AD6E0) makes the selected fleet the active
//     one and queues with its id (YA_EnterMatchmaking "FleetID"). The refusal
//     tokens the client turns into a message (0x2A497C0 / 0xAA7870) include
//     "invalid_fleet" and "empty_fleet" ("Unable to enter matchmaking with an
//     empty fleet.").
//   - Squad: the leader sends the fleet TYPE (YA_SquadEnterMatchmaking
//     "FleetType", built by 0x2A4A4A0). On YA_SquadInfo State 1 each member's
//     client looks up ITS OWN fleet of that type (0x2A0ECA0); found, it makes
//     that fleet active and shows "{0} started matchmaking!"
//     (OnSquadPulledIntoMatchmaking 0x4BD920); not found, nothing happens on
//     that client. A reply result of "invalid_fleet_type" gives the leader
//     the popup "Matchmaking aborted / The selected fleet is not available
//     for all squad members" (OnSquadMatchmakingFailed 0x4BD280).
//   - In the match the battle server tells the client which fleet is active:
//     AYPlayerControllerBase::OnFleetInitializationComplete (0x5BCDC0) sends
//     ClientSetActiveFleetId(FName) (sender 0x5E5B40), and a ship outside
//     that fleet is not accepted (SetActiveLoadout 0x593980: "Loadout [%s] is
//     not valid. Loadout will not be set as active").
//
// Our server queued any fleet it could name and put every squad member in
// the leader's fleet type unchecked (found live 2026-11-10/11: a member whose
// Veteran fleet held no ship played a Veteran match on Recruit ships, and the
// rewards were written for an empty fleet). So: the queue is refused as the
// original's tokens say, and the battle server is told each player's fleet
// (GET /battle/player "fleet=", sent on by battle-server-mod).

// clientFleets is the fleet list the client is sent (YA_PlayerFleets): the
// stored fleets, or the starter fleet for an account without any, less the
// locked ones -- a fleet is unlocked exactly when it is sent (unlockedFleets).
func clientFleets(state mmogPlayerState, playerPID string) []mmogFleetSeed {
	fleets := state.fleets
	if len(fleets) == 0 {
		fleets = []mmogFleetSeed{starterFleetState()}
	}
	return unlockedFleets(state, playerPID, fleets)
}

// clientFleetOfType is the player's fleet of a fleet type (EYFleetType 1-3)
// as their client knows it, or false when the client was sent none. Every
// account has at most one fleet per type (188 of 188, 2026-11-11).
func clientFleetOfType(playerPID string, fleetType int32) (mmogFleetSeed, bool) {
	pid := normalizedPlayerStatePID(playerPID)
	for _, fleet := range clientFleets(mmogPlayerStateForPID(pid), pid) {
		if fleet.fleetType == fleetType {
			return fleet, true
		}
	}
	return mmogFleetSeed{}, false
}

// queueFleetProblem is why a player cannot take a fleet of this type into a
// match, as the client's refusal token for a SOLO queue, or "" when they can:
//
//	invalid_fleet  the client was never sent a fleet of this type (locked),
//	               so only a hand-built request names it
//	empty_fleet    the fleet holds no ship: nothing in it could be picked,
//	               and the match's rewards have no ship to go to
//
// The stock client does not offer either (the hangar's own reason for an
// empty fleet is "Add at least 1 ship to the fleet in order to take it to
// battle!"), so a stock solo player should never see these. A squad member
// can meet both without doing anything wrong -- the LEADER picks the fleet
// type -- and that queue is refused as "invalid_fleet_type"
// (enterSquadMatchmaking).
func queueFleetProblem(playerPID string, fleetType int32) string {
	fleet, ok := clientFleetOfType(playerPID, fleetType)
	if !ok {
		return "invalid_fleet"
	}
	if len(fleet.shipLoadouts) == 0 {
		return "empty_fleet"
	}
	return ""
}

// battleMatchFleetFID is the id of the fleet a player fields in a match, as
// their client knows it: the FID their fleet of the match's fleet type went
// out under (fleetFID). The client's fleet manager keys fleets by the FName
// of exactly that string (0x2A1D450: FName(FID text, FNAME_Add) unless the
// GUID is all zero), so it is what ClientSetActiveFleetId must carry.
func battleMatchFleetFID(playerPID, matchID string) (string, bool) {
	pid := normalizedPlayerStatePID(playerPID)
	fleetType, ok := battleMatchFleetType(pid, matchID)
	if !ok {
		return "", false
	}
	fleet, ok := clientFleetOfType(pid, fleetType)
	if !ok {
		logrus.WithFields(logrus.Fields{"player": pid, "match": matchID, "fleet_type": fleetType}).
			Warn("battle player: the player has no fleet of the match's fleet type")
		return "", false
	}
	return fleetFID(pid, fleet.fleetID), true
}
