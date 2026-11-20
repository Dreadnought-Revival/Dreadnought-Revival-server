package main

import (
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// SHA-384 fallback for clients whose TLS handshake fails with "bad record MAC".
//
// Observed 2026-10-02: one player's client failed every Firmament handshake
// (17 in 15 minutes, "local error: tls: bad record MAC"), while every other
// client completed it with TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 and the
// same player's HTTPS login on :65443 worked. The server could not verify the
// client's Finished message: the client computed its handshake hash wrongly.
//
// GUESS at the cause (not verified on that machine): the game's Firmament
// client links an old OpenSSL whose CPU-feature detection enables the SHA
// extensions (SHA-NI) incorrectly on newer CPUs (Intel 10th gen+, AMD Zen),
// so its SHA-1/SHA-256 results are wrong -- the known fault behind the
// OPENSSL_ia32cap="~0x20000000" workaround for old games. A TLS 1.2 suite
// that ends in SHA384 hashes the handshake with SHA-384, which SHA-NI does
// not cover.
//
// Every other client works with the SHA-256 suite, so it is not changed for
// them: a client that failed with bad record MAC is remembered by address for
// a day -- on disk too (DN_TLS_SHA384_FILE, default tls-sha384-clients.json),
// so a server restart does not cost those players another failed login (seen
// 2026-10-02: one failed again after a deploy) -- and its next handshake (the game retries: "Unable to connect to the
// server. Try again in a moment") is offered only the SHA-384 suites, if its
// ClientHello lists one. DN_TLS_SHA384=off disables this, =all applies it to
// every client.
//
// 2026-11-20 (first-attempt fix): the list above is per address and only learns
// AFTER a failure, so every new address (and every address after its 24 h
// memory ran out -- the memory is refreshed by failures only, so a player
// who plays daily failed once a day) cost the player one failed handshake and
// the client's ~30 s "Firmament handshake timed out" wait. The ClientHello
// cannot tell the broken CPUs apart (it is the same OpenSSL 1.0.2g build with
// the same cipher list on every machine -- GUESS, see the investigation
// notes), so the default is now the other way round: every client that offers
// a SHA-384 suite gets only those, and an address whose SHA-384 handshake
// itself failed with bad record MAC is remembered for a day and gets the
// normal SHA-256-first suites. DN_TLS_SHA384=adaptive restores the old
// behaviour (SHA-256 until a failure), =off disables the whole thing.

var (
	sha384Suites = []uint16{
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	}
	badMACMu      sync.Mutex
	badMACClients = map[string]time.Time{}

	// offers remembers what GetConfigForClient offered each connection (key:
	// ip:port), so a failed handshake can be attributed to the suite family
	// that was used. sha256Clients: addresses whose SHA-384 handshake failed
	// with bad record MAC (24 h, in memory only -- GUESS: rare enough that a
	// restart forgetting them is cheaper than another file with addresses).
	offersMu      sync.Mutex
	offers        = map[string]offerRecord{}
	sha256Clients = map[string]time.Time{}
)

type offerRecord struct {
	sha384 bool
	at     time.Time
}

const offerMemory = 10 * time.Minute

// tlsMode is DN_TLS_SHA384: "" (default, SHA-384 first), "adaptive" (the old
// SHA-256-first behaviour), "all" (SHA-384 always) or "off".
func tlsMode() string { return os.Getenv("DN_TLS_SHA384") }

func noteOffer(remote string, sha384 bool) {
	offersMu.Lock()
	defer offersMu.Unlock()
	now := time.Now()
	for k, o := range offers {
		if now.Sub(o.at) > offerMemory {
			delete(offers, k)
		}
	}
	offers[remote] = offerRecord{sha384: sha384, at: now}
}

// takeOffer returns whether the connection was offered SHA-384 only, and
// forgets the record.
func takeOffer(remote string) (sha384, known bool) {
	offersMu.Lock()
	defer offersMu.Unlock()
	o, ok := offers[remote]
	delete(offers, remote)
	return o.sha384, ok
}

// needsSHA256 reports an address whose SHA-384 handshake failed recently.
func needsSHA256(remote string) bool {
	offersMu.Lock()
	defer offersMu.Unlock()
	host := remoteHost(remote)
	at, ok := sha256Clients[host]
	if ok && time.Since(at) > badMACMemory {
		delete(sha256Clients, host)
		return false
	}
	return ok
}

const badMACMemory = 24 * time.Hour

func remoteHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// noteHandshakeFailure remembers a client whose handshake failed with a bad
// record MAC.
func noteHandshakeFailure(remote string, err error) {
	if err == nil || !strings.Contains(err.Error(), "bad record MAC") {
		return
	}
	// A bad MAC on a connection that was offered SHA-384 suites means this
	// address is not helped by them: give it the normal suites next time and
	// leave the SHA-384 list alone.
	if sha384, known := takeOffer(remote); known && tlsMode() == "" {
		offersMu.Lock()
		if sha384 {
			sha256Clients[remoteHost(remote)] = time.Now()
		} else {
			// The normal suites failed too: try SHA-384 again rather than
			// leaving the address stuck on the family that does not work.
			delete(sha256Clients, remoteHost(remote))
		}
		offersMu.Unlock()
		return
	}
	badMACMu.Lock()
	loadBadMACClientsLocked()
	badMACClients[remoteHost(remote)] = time.Now()
	saveBadMACClientsLocked()
	badMACMu.Unlock()
}

var badMACLoaded bool

func badMACFile() string {
	if f := os.Getenv("DN_TLS_SHA384_FILE"); f != "" {
		return f
	}
	return "tls-sha384-clients.json"
}

// loadBadMACClientsLocked reads the remembered clients once (badMACMu held).
func loadBadMACClientsLocked() {
	if badMACLoaded {
		return
	}
	badMACLoaded = true
	raw, err := os.ReadFile(badMACFile())
	if err != nil {
		return
	}
	var stored map[string]time.Time
	if json.Unmarshal(raw, &stored) != nil {
		return
	}
	for host, at := range stored {
		if time.Since(at) <= badMACMemory {
			badMACClients[host] = at
		}
	}
}

// saveBadMACClientsLocked writes the remembered clients (badMACMu held).
// Best effort: the file only saves a retry.
func saveBadMACClientsLocked() {
	raw, err := json.Marshal(badMACClients)
	if err != nil {
		return
	}
	tmp := badMACFile() + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, badMACFile())
	}
}

func needsSHA384(remote string) bool {
	switch tlsMode() {
	case "off":
		return false
	case "all":
		return true
	case "":
		// Default: SHA-384 for everyone, except an address it already failed for.
		return !needsSHA256(remote)
	}
	badMACMu.Lock()
	defer badMACMu.Unlock()
	loadBadMACClientsLocked()
	at, ok := badMACClients[remoteHost(remote)]
	if ok && time.Since(at) > badMACMemory {
		delete(badMACClients, remoteHost(remote))
		return false
	}
	return ok
}

// sha384FallbackConfig is the listener's GetConfigForClient: the base config,
// or for a remembered client one restricted to the SHA-384 suites it offers.
func sha384FallbackConfig(base *tls.Config, log *logrus.Logger) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		remote := ""
		if hello.Conn != nil {
			remote = hello.Conn.RemoteAddr().String()
		}
		if !needsSHA384(remote) {
			noteOffer(remote, false)
			logClientHello(log, hello, "sha256-first")
			return nil, nil // the base config
		}
		var offered []uint16
		for _, want := range sha384Suites {
			for _, have := range hello.CipherSuites {
				if have == want {
					offered = append(offered, want)
					break
				}
			}
		}
		if len(offered) == 0 {
			noteOffer(remote, false)
			logClientHello(log, hello, "sha256-first (no SHA-384 offered)")
			return nil, nil
		}
		cfg := base.Clone()
		cfg.GetConfigForClient = nil
		cfg.CipherSuites = offered
		noteOffer(remote, true)
		logClientHello(log, hello, "sha384-only")
		return cfg, nil
	}
}

// logClientHello logs what a client's hello offers and what we answer with, one
// line per handshake, so the live log can show whether every client really
// sends the same hello (the premise of the first-attempt fix) and which suite
// family each handshake got. fp is a short hash of suites+curves+points+
// versions+signature schemes, not of anything personal.
func logClientHello(log *logrus.Logger, hello *tls.ClientHelloInfo, offer string) {
	h := sha1.New()
	fmt.Fprint(h, hello.CipherSuites, hello.SupportedCurves, hello.SupportedPoints, hello.SupportedVersions, hello.SignatureSchemes)
	has384 := false
	for _, c := range hello.CipherSuites {
		for _, want := range sha384Suites {
			if c == want {
				has384 = true
			}
		}
	}
	remote := ""
	if hello.Conn != nil {
		remote = hello.Conn.RemoteAddr().String()
	}
	log.WithFields(logrus.Fields{
		"remote":     remote,
		"offer":      offer,
		"suites":     len(hello.CipherSuites),
		"has_sha384": has384,
		"curves":     len(hello.SupportedCurves),
		"sigalgs":    len(hello.SignatureSchemes),
		"versions":   len(hello.SupportedVersions),
		"sni":        hello.ServerName != "",
		"fp":         hex.EncodeToString(h.Sum(nil))[:12],
	}).Info("firmament: ClientHello")
}
