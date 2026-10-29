package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"time"
)

// openSMTP negotiates a connection before any authentication or envelope data
// is sent. Some relays complete TLS 1.3 but stall on the following EHLO. Only
// that timeout may reconnect once using verified TLS 1.2; message submission is
// never retried here or by the caller.
func openSMTP(ctx context.Context, addr string, config *tls.Config) (*smtp.Client, func(), error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, errors.New("configured recovery SMTP address needs host:port")
	}
	deadline := time.Now().Add(15 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	config = config.Clone()
	if config.MinVersion == 0 {
		config.MinVersion = tls.VersionTLS12
	}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, nil, errors.New("recovery SMTP relay cancelled or timed out")
		}
		negotiationDeadline := time.Now().Add(4 * time.Second)
		if deadline.Before(negotiationDeadline) {
			negotiationDeadline = deadline
		}
		conn, err := (&net.Dialer{Deadline: negotiationDeadline}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, nil, errors.New("recovery SMTP relay unreachable")
		}
		stopCancellation := context.AfterFunc(ctx, func() { conn.Close() })
		closeConnection := func() { stopCancellation(); conn.Close() }
		if err = conn.SetDeadline(negotiationDeadline); err != nil {
			closeConnection()
			return nil, nil, errors.New("recovery SMTP relay deadline failed")
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			closeConnection()
			return nil, nil, errors.New("recovery SMTP relay greeting failed")
		}
		if err = client.Hello("localhost"); err != nil {
			closeConnection()
			return nil, nil, errors.New("recovery SMTP relay greeting failed")
		}
		if startTLS, _ := client.Extension("STARTTLS"); startTLS {
			if err = client.StartTLS(config); err != nil {
				state, encrypted := client.TLSConnectionState()
				var networkError net.Error
				fallback := attempt == 0 && encrypted && state.HandshakeComplete && state.Version == tls.VersionTLS13 && errors.As(err, &networkError) && networkError.Timeout() && ctx.Err() == nil && time.Now().Before(deadline) && config.MinVersion <= tls.VersionTLS12
				closeConnection()
				if fallback {
					config = config.Clone()
					config.MinVersion = tls.VersionTLS12
					config.MaxVersion = tls.VersionTLS12
					continue
				}
				return nil, nil, errors.New("recovery SMTP relay TLS failed")
			}
		} else if !isLoopback(host) {
			closeConnection()
			return nil, nil, errors.New("recovery SMTP relay refuses plaintext outside loopback")
		}
		if ctx.Err() != nil {
			closeConnection()
			return nil, nil, errors.New("recovery SMTP relay cancelled or timed out")
		}
		if err = conn.SetDeadline(deadline); err != nil {
			closeConnection()
			return nil, nil, errors.New("recovery SMTP relay deadline failed")
		}
		return client, func() { stopCancellation(); client.Close() }, nil
	}
	return nil, nil, errors.New("recovery SMTP relay TLS failed")
}
