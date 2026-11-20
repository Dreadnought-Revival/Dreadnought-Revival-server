package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// A client that failed with bad record MAC is offered only SHA-384 suites on
// its next handshake; everyone else keeps the normal (SHA-256) suite.
func TestBadMACClientGetsSHA384Next(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	base := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS10, MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384},
	}
	base.GetConfigForClient = sha384FallbackConfig(base, logrus.New())
	ln, err := tls.Listen("tcp", "127.0.0.1:0", base)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	suite := func() uint16 {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12,
			CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.ConnectionState().CipherSuite
	}
	t.Setenv("DN_TLS_SHA384", "adaptive") // the pre-2026-11-20 default
	t.Setenv("DN_TLS_SHA384_FILE", t.TempDir()+"/clients.json")
	badMACMu.Lock()
	badMACClients = map[string]time.Time{}
	badMACLoaded = false
	badMACMu.Unlock()

	if got := suite(); got != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("a fresh client got %s", tls.CipherSuiteName(got))
	}
	noteHandshakeFailure("127.0.0.1:5555", errors.New("local error: tls: other"))
	if got := suite(); got != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("an unrelated failure changed the suite to %s", tls.CipherSuiteName(got))
	}
	noteHandshakeFailure("127.0.0.1:5555", errors.New("local error: tls: bad record MAC"))
	if got := suite(); got != tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384 {
		t.Fatalf("after bad record MAC the client got %s, want the SHA-384 suite", tls.CipherSuiteName(got))
	}
	// A restart forgets the in-memory list; the file brings it back.
	badMACMu.Lock()
	badMACClients = map[string]time.Time{}
	badMACLoaded = false
	badMACMu.Unlock()
	if got := suite(); got != tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384 {
		t.Fatalf("after a restart the remembered client got %s", tls.CipherSuiteName(got))
	}
	t.Setenv("DN_TLS_SHA384", "off")
	if got := suite(); got != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("with DN_TLS_SHA384=off the client got %s", tls.CipherSuiteName(got))
	}
}

// helloServer starts a listener with the production-shaped config (both
// families, the fallback hook) and returns it with a function that dials it
// offering the given suites and reports the negotiated one. After each
// handshake the server calls onHandshake with the client's address, so a test
// can stand in for "the client's Finished failed".
func helloServer(t *testing.T, log *logrus.Logger, onHandshake func(remote string, suite uint16)) (dial func(suites ...uint16) uint16) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	base := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS10, MaxVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_RSA_WITH_AES_256_GCM_SHA384},
	}
	base.GetConfigForClient = sha384FallbackConfig(base, log)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	done := make(chan struct{}, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { done <- struct{}{} }()
				defer c.Close()
				if c.(*tls.Conn).Handshake() == nil && onHandshake != nil {
					onHandshake(c.RemoteAddr().String(), c.(*tls.Conn).ConnectionState().CipherSuite)
				}
			}()
		}
	}()
	return func(suites ...uint16) uint16 {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12, CipherSuites: suites})
		if err != nil {
			t.Fatal(err)
		}
		got := c.ConnectionState().CipherSuite
		c.Close()
		<-done
		return got
	}
}

func resetTLSState(t *testing.T) {
	t.Setenv("DN_TLS_SHA384_FILE", t.TempDir()+"/clients.json")
	badMACMu.Lock()
	badMACClients = map[string]time.Time{}
	badMACLoaded = false
	badMACMu.Unlock()
	offersMu.Lock()
	offers = map[string]offerRecord{}
	sha256Clients = map[string]time.Time{}
	offersMu.Unlock()
}

// The 2026-11-20 default: a client that has never been seen gets a SHA-384
// suite on its FIRST handshake (the one that used to fail for affected CPUs),
// a client that offers none still gets SHA-256, and an address whose SHA-384
// handshake failed with bad record MAC is moved back to the normal suites --
// and back again if those fail too.
func TestFirstAttemptPrefersSHA384(t *testing.T) {
	resetTLSState(t)
	var buf bytes.Buffer
	log := logrus.New()
	log.SetOutput(&buf)
	failNext := false
	dial := helloServer(t, log, func(remote string, suite uint16) {
		if failNext { // stands in for "the client's Finished did not verify"
			noteHandshakeFailure(remote, errors.New("local error: tls: bad record MAC"))
		}
	})
	both := []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}
	want := func(step string, got, w uint16) {
		t.Helper()
		if got != w {
			t.Fatalf("%s: got %s, want %s", step, tls.CipherSuiteName(got), tls.CipherSuiteName(w))
		}
	}
	want("first handshake", dial(both...), tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
	want("client without a SHA-384 suite", dial(tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256), tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256)
	if !strings.Contains(buf.String(), "offer=sha384-only") || !strings.Contains(buf.String(), "ClientHello") {
		t.Fatalf("no ClientHello line with the offer in the log:\n%s", buf.String())
	}

	// SHA-384 fails with bad MAC -> the address gets the normal suites.
	failNext = true
	want("the failing SHA-384 handshake", dial(both...), tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)
	failNext = false
	want("after a SHA-384 failure", dial(both...), tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256)
	badMACMu.Lock()
	n := len(badMACClients)
	badMACMu.Unlock()
	if n != 0 {
		t.Fatalf("a SHA-384 failure went into the legacy SHA-256-bad list (%d)", n)
	}

	// The normal suites fail as well -> back to SHA-384, not stuck.
	failNext = true
	want("the failing SHA-256 handshake", dial(both...), tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256)
	failNext = false
	want("after a SHA-256 failure", dial(both...), tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384)

	// The operator switches.
	t.Setenv("DN_TLS_SHA384", "off")
	want("DN_TLS_SHA384=off", dial(both...), tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256)
}
