package main

import "testing"

func TestMachineLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/battle/result":         true,
		"/battle/tune":           true,
		"/battle":                true,
		"/online":                true,
		"/x/../battle/result":    true,
		"//battle/loadout":       true,
		"/battleship":            false,
		"/auth/":                 false,
		"/dn/ca.crt":             false,
		"/v2/dreadnought/online": false,
	} {
		if got := machineLocalPath(p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}
