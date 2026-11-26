package handlers

// SMTP for the password reset mails (email_reset.go). Any provider that offers
// SMTP works (Brevo, Resend, Amazon SES, Mailgun, a Google Workspace or
// Microsoft 365 mailbox ...). Configured in run/secrets.env:
//
//	SMTP_HOST   the provider's server (unset = no email; reset by admins only)
//	SMTP_PORT   587 (STARTTLS, default) or 465 (TLS from the start)
//	SMTP_USER   login, if the provider needs one
//	SMTP_PASS   password / API key (never logged)
//	MAIL_FROM   the sender, e.g. "Dreadnought Revival <noreply@example.org>";
//	            its domain needs the provider's SPF/DKIM DNS records or the
//	            mail lands in spam
//
// The connection is always encrypted: port 465 is TLS from the start, any
// other port must offer STARTTLS or nothing is sent.

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SMTPMailer sends through one SMTP server.
type SMTPMailer struct {
	Host, Port, User, Pass string
	From                   string // "Name <address>"
}

// NewSMTPMailerFromEnv builds the mailer from SMTP_* / MAIL_FROM, or returns
// nil when SMTP_HOST is unset. getenv is the service's own.
func NewSMTPMailerFromEnv(getenv func(key, fallback string) string) (*SMTPMailer, error) {
	host := strings.TrimSpace(getenv("SMTP_HOST", ""))
	if host == "" {
		return nil, nil
	}
	m := &SMTPMailer{Host: host, Port: getenv("SMTP_PORT", "587"), User: getenv("SMTP_USER", ""),
		Pass: getenv("SMTP_PASS", ""), From: getenv("MAIL_FROM", "")}
	if _, err := mail.ParseAddress(m.From); err != nil {
		return nil, fmt.Errorf("MAIL_FROM %q is not an address: %w", m.From, err)
	}
	return m, nil
}

func (m *SMTPMailer) Send(to, subject, body string) error {
	from, err := mail.ParseAddress(m.From)
	if err != nil {
		return err
	}
	rcpt, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("subject has a line break")
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\n",
		from.String(), rcpt.String(), mime.QEncoding.Encode("utf-8", subject), time.Now().UTC().Format(time.RFC1123Z),
		hex.EncodeToString(id), domain)
	msg.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	msg.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))

	addr := net.JoinHostPort(m.Host, m.Port)
	tlsCfg := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if m.Port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if m.Port != "465" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server offers no STARTTLS; refusing to send unencrypted")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if m.User != "" {
		if err := c.Auth(smtp.PlainAuth("", m.User, m.Pass, m.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write([]byte(msg.String())); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}
