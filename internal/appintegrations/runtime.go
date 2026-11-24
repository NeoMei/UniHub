package appintegrations

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

func (s *Store) FindManifest(ctx context.Context, q accounts.Querier, product, instance string) (productusers.Manifest, bool, error) {
	app, err := s.scan(q.QueryRow(ctx, `SELECT `+applicationFields+` FROM application_integrations
		WHERE product_id=$1 AND instance_id=$2 AND state IN ('pilot','active','paused')`, product, productusers.Instance(instance)))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && s.reserved(app) {
		return productusers.Manifest{}, false, nil
	}
	if err != nil {
		return productusers.Manifest{}, false, err
	}
	return app.Manifest(), true, nil
}

func (s *Store) RegisteredManifests(ctx context.Context) ([]productusers.Manifest, error) {
	apps, err := s.list(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]productusers.Manifest, 0, len(apps))
	for _, app := range apps {
		if app.State != "draft" {
			out = append(out, app.Manifest())
		}
	}
	return out, nil
}

func (s *Store) LaunchManifests(ctx context.Context, user string) ([]productusers.Manifest, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+applicationFields+` FROM application_integrations i
		WHERE i.state IN ('pilot','active') AND EXISTS (
			SELECT 1 FROM product_runtime_bindings b JOIN product_auth_subjects p ON p.binding_id=b.id
			WHERE b.product_id=i.product_id AND b.instance_id=i.instance_id AND b.user_id=$1
				AND NOT p.deleted AND NOT p.native_locked)`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []productusers.Manifest{}
	for rows.Next() {
		app, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		if !s.reserved(app) {
			out = append(out, app.Manifest())
		}
	}
	return out, rows.Err()
}

func (s *Store) BrowserClient(ctx context.Context, q accounts.Querier, id string, includePaused bool) (products.Client, bool) {
	if !strings.HasPrefix(id, "bridge.") {
		return products.Client{}, false
	}
	app, err := s.scan(q.QueryRow(ctx, `SELECT `+applicationFields+` FROM application_integrations
		WHERE client_id=$1 AND credential_hash IS NOT NULL AND (state IN ('pilot','active') OR ($2 AND state='paused'))`, id, includePaused))
	if err != nil || s.reserved(app) {
		return products.Client{}, false
	}
	return app.Client(), true
}

func (s *Store) PublicBrowserClients(ctx context.Context) map[string]products.Client {
	out := map[string]products.Client{}
	apps, err := s.list(ctx, true)
	if err == nil {
		for _, app := range apps {
			if app.State == "active" {
				out[app.ClientID] = app.Client()
			}
		}
	}
	return out
}

func NativeIdentityOperations() []string {
	return []string{"identity:link", "identity:session", "identity:source-lifecycle", "identity:browser", "identity:authenticate", "identity:lookup"}
}

func (s *Store) Authenticate(ctx context.Context, token string, allowPaused bool) (Application, bool) {
	if !strings.HasPrefix(token, "uha_") || len(token) != 47 {
		return Application{}, false
	}
	hash := sha256.Sum256([]byte(token))
	app, err := s.scan(s.pool.QueryRow(ctx, `SELECT `+applicationFields+` FROM application_integrations
		WHERE credential_hash=$1 AND (state IN ('pilot','active') OR (state='paused' AND $2))`,
		hash[:], allowPaused))
	return app, err == nil && !s.reserved(app)
}

// AcceptBrowserWork pins validation to the pilot in which the browser artifact
// was created. A writer locks the integration row through its final commit.
func (s *Store) AcceptBrowserWork(ctx context.Context, q accounts.Querier, id string, issuedAt time.Time, receipt productauth.BrowserReceipt) error {
	if !strings.HasPrefix(id, "bridge.") || s.reservedClients[id] {
		return nil
	}
	query := `SELECT state,pilot_started_at FROM application_integrations WHERE client_id=$1`
	tx, transactional := q.(pgx.Tx)
	if transactional {
		query += " FOR UPDATE"
	}
	var state string
	var started *time.Time
	err := q.QueryRow(ctx, query, id).Scan(&state, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounts.ErrForbidden
	}
	if err != nil {
		return err
	}
	if started == nil || issuedAt.Before(*started) || (state != "pilot" && state != "active") {
		return accounts.ErrForbidden
	}
	if state != "pilot" || receipt == productauth.CheckBrowserWork {
		return nil
	}
	if !transactional {
		return accounts.ErrForbidden
	}
	_, err = tx.Exec(ctx, `UPDATE application_integrations SET inbound_verified=inbound_verified OR $2,
		outbound_verified=outbound_verified OR NOT $2 WHERE client_id=$1
		AND (CASE WHEN $2 THEN NOT inbound_verified ELSE NOT outbound_verified END)`, id, receipt == productauth.RecordInbound)
	return err
}
