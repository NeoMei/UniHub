package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"
)

type smtpObservation struct {
	commands [][]string
	versions []uint16
}
type tlsSMTPFixture struct {
	listener     net.Listener
	config       *tls.Config
	mu           sync.Mutex
	observations smtpObservation
	connections  []net.Conn
	wg           sync.WaitGroup
	mode         string
}

func newTLSSMTPFixture(t *testing.T, mode string) (*tlsSMTPFixture, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.test"}, DNSNames: []string{"smtp.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(mustParseCertificate(t, der))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &tlsSMTPFixture{listener: listener, config: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, mode: mode}
	fixture.wg.Add(1)
	go func() {
		defer fixture.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			fixture.mu.Lock()
			index := len(fixture.observations.commands)
			fixture.observations.commands = append(fixture.observations.commands, nil)
			fixture.observations.versions = append(fixture.observations.versions, 0)
			fixture.connections = append(fixture.connections, conn)
			fixture.mu.Unlock()
			fixture.wg.Add(1)
			go func() { defer fixture.wg.Done(); fixture.serve(conn, index) }()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		fixture.mu.Lock()
		for _, conn := range fixture.connections {
			conn.Close()
		}
		fixture.mu.Unlock()
		fixture.wg.Wait()
	})
	return fixture, &tls.Config{ServerName: "smtp.test", RootCAs: roots, MinVersion: tls.VersionTLS12}
}
func mustParseCertificate(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
func (s *tlsSMTPFixture) record(index int, command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations.commands[index] = append(s.observations.commands[index], command)
}
func (s *tlsSMTPFixture) snapshot() smtpObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := smtpObservation{versions: append([]uint16(nil), s.observations.versions...)}
	for _, commands := range s.observations.commands {
		out.commands = append(out.commands, append([]string(nil), commands...))
	}
	return out
}
func (s *tlsSMTPFixture) serve(conn net.Conn, index int) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	io.WriteString(conn, "220 local SMTP ready\r\n")
	encrypted := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		s.record(index, command)
		switch {
		case strings.HasPrefix(command, "EHLO"):
			if encrypted && s.mode == "stall13" && conn.(*tls.Conn).ConnectionState().Version == tls.VersionTLS13 {
				continue
			}
			if !encrypted && s.mode != "dropData" && s.mode != "rejectAuth" {
				io.WriteString(conn, "250-local SMTP\r\n250 STARTTLS\r\n")
			} else {
				io.WriteString(conn, "250-local SMTP\r\n250 AUTH PLAIN\r\n")
			}
		case command == "STARTTLS":
			if s.mode == "rejectTLS" {
				io.WriteString(conn, "454 TLS temporarily unavailable\r\n")
				continue
			}
			io.WriteString(conn, "220 start TLS\r\n")
			secure := tls.Server(conn, s.config)
			conn = secure
			if err := secure.Handshake(); err != nil {
				return
			}
			encrypted = true
			reader = bufio.NewReader(conn)
			s.mu.Lock()
			s.observations.versions[index] = secure.ConnectionState().Version
			s.mu.Unlock()
		case strings.HasPrefix(command, "AUTH "):
			if s.mode == "rejectAuth" {
				io.WriteString(conn, "535 authentication failed\r\n")
			} else {
				io.WriteString(conn, "235 authenticated\r\n")
			}
		case strings.HasPrefix(command, "MAIL "), strings.HasPrefix(command, "RCPT "):
			io.WriteString(conn, "250 ok\r\n")
		case command == "DATA":
			io.WriteString(conn, "354 send message\r\n")
			for {
				line, err = reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(line) == "." {
					break
				}
			}
			if s.mode == "dropData" {
				return
			}
			io.WriteString(conn, "250 queued\r\n")
		case command == "QUIT":
			io.WriteString(conn, "221 goodbye\r\n")
			return
		default:
			io.WriteString(conn, "250 ok\r\n")
		}
	}
}

func TestSMTPPostTLS13TimeoutFallsBackBeforeAuthentication(t *testing.T) {
	fixture, config := newTLSSMTPFixture(t, "stall13")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	client, closeClient, err := openSMTP(ctx, fixture.listener.Addr().String(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	if config.MaxVersion != 0 {
		t.Fatal("caller TLS config mutated")
	}
	if err = client.Auth(smtp.PlainAuth("", "test-user", "test-password", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	if err = client.Mail("sender@example.test"); err != nil {
		t.Fatal(err)
	}
	if err = client.Rcpt("recipient@example.test"); err != nil {
		t.Fatal(err)
	}
	body, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(body, "Subject: probe\r\n\r\nhello\r\n"); err != nil {
		t.Fatal(err)
	}
	if err = body.Close(); err != nil {
		t.Fatal(err)
	}
	if err = client.Quit(); err != nil {
		t.Fatal(err)
	}
	got := fixture.snapshot()
	if len(got.commands) != 2 || got.versions[0] != tls.VersionTLS13 || got.versions[1] != tls.VersionTLS12 {
		t.Fatalf("connections/versions: %+v", got)
	}
	for _, cmd := range got.commands[0] {
		if strings.HasPrefix(cmd, "AUTH ") || strings.HasPrefix(cmd, "MAIL ") || strings.HasPrefix(cmd, "RCPT ") || cmd == "DATA" {
			t.Fatalf("first connection dispatched user material: %q", cmd)
		}
	}
	count := 0
	for _, cmd := range got.commands[1] {
		if cmd == "DATA" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("DATA submissions = %d", count)
	}
}
func TestSMTPCertificateFailureDoesNotFallback(t *testing.T) {
	fixture, config := newTLSSMTPFixture(t, "normal")
	config.RootCAs = x509.NewCertPool()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, closeClient, err := openSMTP(ctx, fixture.listener.Addr().String(), config)
	if closeClient != nil {
		defer closeClient()
	}
	if err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if got := fixture.snapshot(); len(got.commands) != 1 {
		t.Fatalf("certificate failure retried: %+v", got)
	}
}
func TestSMTPRejectedSTARTTLSDoesNotFallback(t *testing.T) {
	fixture, config := newTLSSMTPFixture(t, "rejectTLS")
	_, closeClient, err := openSMTP(context.Background(), fixture.listener.Addr().String(), config)
	if closeClient != nil {
		defer closeClient()
	}
	if err == nil {
		t.Fatal("STARTTLS rejection accepted")
	}
	if got := fixture.snapshot(); len(got.commands) != 1 {
		t.Fatalf("STARTTLS rejection retried: %+v", got)
	}
}
func TestSMTPCancellationInterruptsNegotiationWithoutFallback(t *testing.T) {
	fixture, config := newTLSSMTPFixture(t, "stall13")
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(150*time.Millisecond, cancel)
	defer timer.Stop()
	start := time.Now()
	_, closeClient, err := openSMTP(ctx, fixture.listener.Addr().String(), config)
	if closeClient != nil {
		defer closeClient()
	}
	if err == nil {
		t.Fatal("cancelled negotiation accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("context cancellation did not interrupt negotiation")
	}
	if got := fixture.snapshot(); len(got.commands) != 1 {
		t.Fatalf("cancelled negotiation retried: %+v", got)
	}
}
func TestSMTPDispatchFailureDoesNotReconnect(t *testing.T) {
	fixture, _ := newTLSSMTPFixture(t, "dropData")
	mailer := NewRecoveryMailer(SMTPOptions{Addr: fixture.listener.Addr().String(), From: "sender@example.test"})
	err := mailer.DeliverMailboxVerification(context.Background(), "recipient@example.test", "test-code")
	if err == nil {
		t.Fatal("ambiguous DATA completion claimed success")
	}
	if got := fixture.snapshot(); len(got.commands) != 1 {
		t.Fatalf("mail dispatch retried: %+v", got)
	}
}

func TestSMTPAuthenticationFailureDoesNotReconnect(t *testing.T) {
	fixture, _ := newTLSSMTPFixture(t, "rejectAuth")
	mailer := NewRecoveryMailer(SMTPOptions{Addr: fixture.listener.Addr().String(), From: "sender@example.test", Username: "test-user", Password: "test-password"})
	err := mailer.DeliverMailboxVerification(context.Background(), "recipient@example.test", "test-code")
	if err == nil || err.Error() != "recovery SMTP relay authentication failed" {
		t.Fatalf("expected authentication refusal, got %v", err)
	}
	got := fixture.snapshot()
	if len(got.commands) != 1 {
		t.Fatalf("authentication rejection retried: %+v", got)
	}
	for _, cmd := range got.commands[0] {
		if strings.HasPrefix(cmd, "MAIL ") || cmd == "DATA" {
			t.Fatalf("sent after authentication rejection: %s", cmd)
		}
	}
}
func TestSMTPWrongCertificateHostnameDoesNotFallback(t *testing.T) {
	fixture, config := newTLSSMTPFixture(t, "normal")
	config.ServerName = "different.test"
	_, closeClient, err := openSMTP(context.Background(), fixture.listener.Addr().String(), config)
	if closeClient != nil {
		defer closeClient()
	}
	if err == nil {
		t.Fatal("wrong certificate hostname accepted")
	}
	if got := fixture.snapshot(); len(got.commands) != 1 {
		t.Fatalf("hostname failure retried: %+v", got)
	}
}
