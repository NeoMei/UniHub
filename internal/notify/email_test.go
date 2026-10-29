package notify

import (
	"bufio"
	"context"
	"io"
	"mime"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// loopbackSMTP is a minimal no-STARTTLS, no-auth relay stub. It records the
// envelope and payload exactly as a relay would observe them.
func loopbackSMTP(t *testing.T) (addr string, transcript func() string, done func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var collected strings.Builder
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		io.WriteString(conn, "220 stub ready\r\n")
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if inData {
				if trimmed == "." {
					inData = false
					io.WriteString(conn, "250 accepted\r\n")
				} else {
					collected.WriteString(trimmed + "\n")
				}
				continue
			}
			collected.WriteString(trimmed + "\n")
			upper := strings.ToUpper(trimmed)
			switch {
			case strings.HasPrefix(upper, "DATA"):
				inData = true
				io.WriteString(conn, "354 go ahead\r\n")
			case strings.HasPrefix(upper, "QUIT"):
				io.WriteString(conn, "221 bye\r\n")
				return
			default:
				io.WriteString(conn, "250 ok\r\n")
			}
		}
	}()
	return listener.Addr().String(), func() string { return collected.String() }, func() { listener.Close(); <-closed }
}

func TestRecoveryMailerRequiresConfiguration(t *testing.T) {
	if NewRecoveryMailer(SMTPOptions{}) != nil {
		t.Fatal("unconfigured options must produce no mailer")
	}
	var nilMailer *RecoveryMailer
	if err := nilMailer.DeliverIdentityRecovery(context.Background(), "a@b", "https://x", "r1"); err == nil {
		t.Fatal("nil mailer must refuse delivery")
	}
}

func TestDeliverIdentityRecoverySendsSingleUseLink(t *testing.T) {
	addr, transcript, done := loopbackSMTP(t)
	defer done()
	mailer := NewRecoveryMailer(SMTPOptions{Addr: addr, From: "UniHub <recovery@quukk.com>"})
	if mailer == nil {
		t.Fatal("configured options must produce a mailer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	link := "https://unihub.quukk.com/auth/recovery#token=secret"
	if err := mailer.DeliverIdentityRecovery(ctx, "user@example.test", link, "identity_operation_1"); err != nil {
		t.Fatal(err)
	}
	seen := transcript()
	if got := decodedSubject(t, seen); got != "UniHub 密码恢复 / Password recovery · identity_operation_1" {
		t.Fatalf("decoded subject = %q", got)
	}
	for _, want := range []string{
		"MAIL FROM:<recovery@quukk.com>",
		"RCPT TO:<user@example.test>",
		"Content-Type: text/plain; charset=utf-8",
		"你好，",
		"有人为你的 UniHub 账号发起密码恢复。",
		"请求编号：identity_operation_1",
		link,
		"The link is valid for 20 minutes and can be used once:",
		"请打开链接设置新密码。若非本人操作，请忽略此邮件，账号不会改变。",
	} {
		if !strings.Contains(seen, want) {
			t.Fatalf("transcript missing %q\n%s", want, seen)
		}
	}
}

func TestDeliverMailboxVerificationSendsBilingualCode(t *testing.T) {
	addr, transcript, done := loopbackSMTP(t)
	defer done()
	mailer := NewRecoveryMailer(SMTPOptions{Addr: addr, From: "UniHub <recovery@quukk.com>"})
	if err := mailer.DeliverMailboxVerification(context.Background(), "user@example.test", "UH-test-code"); err != nil {
		t.Fatal(err)
	}
	seen := transcript()
	if got := decodedSubject(t, seen); got != "UniHub 邮箱验证 / Mailbox verification" {
		t.Fatalf("decoded subject = %q", got)
	}
	for _, want := range []string{
		"UniHub 邮箱验证校验码：UH-test-code",
		"请将此校验码回复到本次 UniHub 验收对话，以继续密码恢复验收。",
		"UniHub mailbox verification code: UH-test-code",
		"Please send this code in the current UniHub acceptance conversation to continue.",
	} {
		if !strings.Contains(seen, want) {
			t.Fatalf("transcript missing %q\n%s", want, seen)
		}
	}
}

func TestDeliverIdentityActivationSendsBilingualActivation(t *testing.T) {
	addr, transcript, done := loopbackSMTP(t)
	defer done()
	mailer := NewRecoveryMailer(SMTPOptions{Addr: addr, From: "UniHub <recovery@quukk.com>"})
	if err := mailer.DeliverIdentityActivation(context.Background(), "user@example.test", "https://unihub.quukk.com/auth/recovery#token=secret", "activation_operation_1"); err != nil {
		t.Fatal(err)
	}
	seen := transcript()
	if got := decodedSubject(t, seen); got != "UniHub 账户激活 / Account activation · activation_operation_1" {
		t.Fatalf("decoded subject = %q", got)
	}
	for _, want := range []string{
		"你的 UniHub 账户已准备好激活。",
		"请打开链接完成激活并设置密码。",
		"Your UniHub account is ready to be activated.",
		"Open the link to activate the account and set a password.",
		"activation_operation_1",
	} {
		if !strings.Contains(seen, want) {
			t.Fatalf("transcript missing %q\n%s", want, seen)
		}
	}
}

func decodedSubject(t *testing.T, transcript string) string {
	t.Helper()
	for _, line := range strings.Split(transcript, "\n") {
		if strings.HasPrefix(line, "Subject: ") {
			decoded, err := (&mime.WordDecoder{}).DecodeHeader(strings.TrimPrefix(line, "Subject: "))
			if err != nil {
				t.Fatalf("decode subject: %v", err)
			}
			return decoded
		}
	}
	t.Fatal("subject header missing")
	return ""
}

func TestDeliverIdentityRecoveryRejectsIncompleteInput(t *testing.T) {
	mailer := NewRecoveryMailer(SMTPOptions{Addr: "127.0.0.1:25", From: "recovery@quukk.com"})
	for _, input := range [][3]string{{"", "https://x", "r"}, {"a@b", "", "r"}, {"a@b", "https://x", ""}} {
		if err := mailer.DeliverIdentityRecovery(context.Background(), input[0], input[1], input[2]); err == nil {
			t.Fatalf("incomplete input accepted: %v", input)
		}
	}
	if _, err := mail.ParseAddress("not-an-address"); err == nil {
		t.Fatal("address parser assumption changed")
	}
}
