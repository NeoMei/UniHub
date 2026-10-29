// Package notify delivers platform-generated account mail through the
// operator-configured SMTP relay. It exists only for single-purpose account
// recovery, activation and verification mail, with a plain-text fallback.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
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
	return m.deliverIdentity(ctx, email, recoveryURL, request, false)
}

func (m *RecoveryMailer) DeliverIdentityActivation(ctx context.Context, email, activationURL, request string) error {
	return m.deliverIdentity(ctx, email, activationURL, request, true)
}

func (m *RecoveryMailer) deliverIdentity(ctx context.Context, email, link, request string, activation bool) error {
	if m == nil {
		return errors.New("recovery email delivery is not configured")
	}
	if email == "" || link == "" || request == "" {
		return errors.New("recovery email requires recipient, link and request")
	}
	if !safeMessageLink(link) {
		return errors.New("identity email requires a secure action link")
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
	subject := "UniHub 密码恢复 / Password recovery · " + request
	titleZH, titleEN := "重设你的密码", "Reset your password"
	buttonZH, buttonEN := "设置新密码", "Reset password"
	introZH := "有人为你的 UniHub 账号发起密码恢复。"
	actionZH := "请打开链接设置新密码。若非本人操作，请忽略此邮件，账号不会改变。"
	introEN := "A password recovery was requested for your UniHub account."
	actionEN := "Open the link to set a new password. If you did not request this, ignore this email; your account stays unchanged."
	if activation {
		titleZH, titleEN = "激活你的账户", "Activate your account"
		buttonZH, buttonEN = "激活账户并设置密码", "Activate account"
		subject = "UniHub 账户激活 / Account activation · " + request
		introZH = "你的 UniHub 账户已准备好激活。"
		actionZH = "请打开链接完成激活并设置密码。若非本人操作，请忽略此邮件，账户将保持未激活状态。"
		introEN = "Your UniHub account is ready to be activated."
		actionEN = "Open the link to activate the account and set a password. If you did not request this, ignore this email; the account will remain inactive."
	}
	body := strings.Join([]string{
		"你好，",
		"",
		introZH,
		"请求编号：" + request,
		"此链接 20 分钟内有效且只能使用一次：",
		"",
		link,
		"",
		actionZH,
		"",
		"--- English ---",
		"Hello,",
		"",
		introEN,
		"Request: " + request,
		"The link is valid for 20 minutes and can be used once:",
		"",
		link,
		"",
		actionEN,
		"",
	}, "\r\n")
	rich, err := renderIdentityMail(identityMailView{TitleZH: titleZH, TitleEN: titleEN, IntroZH: introZH, IntroEN: introEN, ActionZH: actionZH, ActionEN: actionEN, ButtonZH: buttonZH, ButtonEN: buttonEN, Link: link, Request: request})
	if err != nil {
		return err
	}
	return m.deliver(ctx, *from, *to, subject, body, rich)
}

// DeliverMailboxVerification sends a bilingual operator verification message.
// It is intentionally separate from recovery delivery so a mailbox check cannot
// be mistaken for a password-reset request.
func (m *RecoveryMailer) DeliverMailboxVerification(ctx context.Context, email, code string) error {
	if m == nil {
		return errors.New("recovery email delivery is not configured")
	}
	if email == "" || code == "" {
		return errors.New("mailbox verification requires recipient and code")
	}
	from, err := mail.ParseAddress(m.options.From)
	if err != nil {
		return errors.New("configured recovery sender address is invalid")
	}
	to, err := mail.ParseAddress(email)
	if err != nil {
		return errors.New("verification recipient address is invalid")
	}
	body := strings.Join([]string{
		"你好，",
		"",
		"UniHub 邮箱验证校验码：" + code,
		"有效期：20 分钟，仅用于本次测试邮箱验证。",
		"请将此校验码回复到本次 UniHub 验收的 Codex 对话，以继续密码恢复验收。",
		"若非本人操作，请忽略此邮件。你的日常账号密码保持不变。",
		"",
		"--- English ---",
		"Hello,",
		"",
		"UniHub mailbox verification code: " + code,
		"Expires in 20 minutes. This code is only for this test mailbox verification.",
		"Please send this code in the current UniHub acceptance conversation in Codex to continue.",
		"If you did not request this test, ignore this email. Your everyday account password is unchanged.",
		"",
	}, "\r\n")
	rich, err := renderVerificationMail(verificationMailView{Code: code})
	if err != nil {
		return err
	}
	return m.deliver(ctx, *from, *to, "UniHub 邮箱验证 / Mailbox verification", body, rich)
}

func (m *RecoveryMailer) deliver(ctx context.Context, from, to mail.Address, subject, body, rich string) error {
	var message bytes.Buffer
	parts := multipart.NewWriter(&message)
	for _, alternative := range []struct{ kind, body string }{{"text/plain", body}, {"text/html", rich}} {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", alternative.kind+"; charset=utf-8")
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		part, err := parts.CreatePart(header)
		if err != nil {
			return errors.New("identity email MIME creation failed")
		}
		encoded := quotedprintable.NewWriter(part)
		if _, err = encoded.Write([]byte(alternative.body)); err != nil {
			return errors.New("identity email MIME encoding failed")
		}
		if err = encoded.Close(); err != nil {
			return errors.New("identity email MIME encoding failed")
		}
	}
	if err := parts.Close(); err != nil {
		return errors.New("identity email MIME creation failed")
	}
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
		"Subject: " + mime.QEncoding.Encode("UTF-8", subject),
		"MIME-Version: 1.0",
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Content-Type: multipart/alternative; boundary=" + parts.Boundary(),
	}
	if _, err = writer.Write([]byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + message.String())); err != nil {
		return errors.New("recovery email write failed")
	}
	if err = writer.Close(); err != nil {
		return errors.New("recovery email accept failed")
	}
	return client.Quit()
}

func safeMessageLink(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || strings.TrimSpace(raw) != raw {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && isLoopback(u.Hostname())
}

func isLoopback(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}
