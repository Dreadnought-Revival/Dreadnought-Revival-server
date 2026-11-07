package main

import (
	"net"
	"net/http"
)

// refuseUnlessLoopback answers 403 and returns true unless the request came
// straight from this machine: the battle-server mod's /battle/ calls and
// the operator's /online.
//
// The connection address alone is not enough. The public gateway (:443)
// proxies Host mmog.greybox.sixfoot.live to this port from 127.0.0.1, so
// until 2026-11-07 anyone on the internet passed every "loopback only"
// check -- /battle/result included, which pays out (verified that day: a
// request through the gateway got /battle/scoring with 200; the gateway
// logs back to 2026-10-31 show no such request before it). The gateway now
// refuses these paths too; a request it forwarded still carries
// X-Forwarded-For (httputil.ReverseProxy adds it), and the mod sends none.
func refuseUnlessLoopback(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		http.Error(w, "loopback only", http.StatusForbidden)
		return true
	}
	return false
}
