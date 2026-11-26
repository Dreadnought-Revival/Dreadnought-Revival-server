package handlers

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

// A server that offers no STARTTLS gets nothing: the code never travels in
// clear text.
func TestSMTPRefusesAServerWithoutSTARTTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	gotData := make(chan bool, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		_, _ = c.Write([]byte("220 test ESMTP\r\n"))
		data := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				_, _ = c.Write([]byte("250-test\r\n250 AUTH PLAIN\r\n"))
			case strings.HasPrefix(cmd, "DATA"), strings.HasPrefix(cmd, "MAIL"):
				data = true
				_, _ = c.Write([]byte("250 ok\r\n"))
			case strings.HasPrefix(cmd, "QUIT"):
				_, _ = c.Write([]byte("221 bye\r\n"))
			default:
				_, _ = c.Write([]byte("250 ok\r\n"))
			}
		}
		gotData <- data
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	m := &SMTPMailer{Host: "127.0.0.1", Port: port, From: "Test <noreply@example.org>"}
	err = m.Send("pilot@example.com", "code", "12345678")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("sent without STARTTLS (err %v)", err)
	}
	_ = ln.Close()
	if <-gotData {
		t.Error("the mail itself reached a plain-text server")
	}
}

func TestSMTPMailerFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k, d string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return d
	}
	if m, err := NewSMTPMailerFromEnv(get); m != nil || err != nil {
		t.Errorf("no SMTP_HOST: %v %v", m, err)
	}
	env["SMTP_HOST"], env["MAIL_FROM"] = "smtp.example.org", "not an address"
	if _, err := NewSMTPMailerFromEnv(get); err == nil {
		t.Error("a bad MAIL_FROM was accepted")
	}
	env["MAIL_FROM"] = "Dreadnought Revival <noreply@example.org>"
	if m, err := NewSMTPMailerFromEnv(get); err != nil || m.Port != "587" {
		t.Errorf("mailer %+v %v", m, err)
	}
}
