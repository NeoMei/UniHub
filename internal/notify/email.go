// Package notify delivers platform-generated account mail through the
// operator-configured SMTP relay. It exists only for single-purpose account
// recovery and activation mail: no templating, no marketing, no HTML.
package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SMTPOptions come from private runtime configuration. Addr is host:port of
// the submission relay; From must be an address the relay accepts as sender.
type SMTPOptions struct {
	Addr     string
	From     string
	Username string
	Password string
}

func (o SMTPOptions) Enabled() bool { return o.Addr != "" && o.From != "" }

// RecoveryMailer implements identityapi.RecoveryDelivery. A nil *RecoveryMailer
// is the unconfigured state; callers must convert it to a nil interface so the
// administration capability gate stays closed.
type RecoveryMailer struct{ options SMTPOptions }

func NewRecoveryMailer(options SMTPOptions) *RecoveryMailer {
	if !options.Enabled() {
		return nil
	}
	return &RecoveryMailer{options: options}
}

func (m *RecoveryMailer) DeliverIdentityRecovery(ctx context.Context, email, recoveryURL, request string) error {
	if m == nil {
		return errors.New("recovery email delivery is not configured")
	}
	if email == "" || recoveryURL == "" || request == "" {
		return errors.New("recovery email requires recipient, link and request")
	}
	if len(request) > 64 {
		request = request[:64]
	}
	from, err := mail.ParseAddress(m.options.From)
	if err != nil {
		return errors.New("configured recovery sender address is invalid")
	}
	to, err := mail.ParseAddress(email)
	if err != nil {
		return errors.New("recovery recipient address is invalid")
	}
	body := strings.Join([]string{
		"Hello,",
		"",
		"A password recovery was requested for your UniHub account.",
		"Request: " + request,
		"The link is valid for 20 minutes and can be used once:",
		"",
		recoveryURL,
		"",
		"Open the link to set a new password. If you did not request this,",
		"ignore this email; your account stays unchanged.",
		"",
	}, "\r\n")
	return m.deliver(ctx, *from, *to, request, body)
}

func (m *RecoveryMailer) deliver(ctx context.Context, from, to mail.Address, request, body string) error {
	host, _, err := net.SplitHostPort(m.options.Addr)
	if err != nil {
		return errors.New("configured recovery SMTP address needs host:port")
	}
	timeout := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	conn, err := new(net.Dialer).DialContext(ctx, "tcp", m.options.Addr)
	if err != nil {
		return errors.New("recovery SMTP relay unreachable")
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return errors.New("recovery SMTP relay deadline failed")
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return errors.New("recovery SMTP relay greeting failed")
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err = client.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return errors.New("recovery SMTP relay TLS failed")
		}
	} else if !isLoopback(host) {
		return errors.New("recovery SMTP relay refuses plaintext outside loopback")
	}
	if m.options.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", m.options.Username, m.options.Password, host)); err != nil {
			return errors.New("recovery SMTP relay authentication failed")
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return errors.New("recovery SMTP relay rejected sender")
	}
	if err = client.Rcpt(to.Address); err != nil {
		return errors.New("recovery SMTP relay rejected recipient")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("recovery SMTP relay rejected message")
	}
	headers := []string{
		"From: " + from.String(),
		"To: " + to.String(),
		"Subject: UniHub account recovery " + request,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
	}
	if _, err = writer.Write([]byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n")); err != nil {
		return errors.New("recovery email write failed")
	}
	if err = writer.Close(); err != nil {
		return errors.New("recovery email accept failed")
	}
	return client.Quit()
}

func isLoopback(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}
