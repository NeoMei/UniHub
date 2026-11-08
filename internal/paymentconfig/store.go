package paymentconfig

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/payments"
	"unihub/internal/platformadmin"
)

type Manager struct {
	pool            *pgxpool.Pool
	authority       *platformadmin.Store
	keyFile, origin string
}

func New(pool *pgxpool.Pool, authority *platformadmin.Store, keyFile, origin string) (*Manager, error) {
	if pool == nil || authority == nil || !validOrigin(origin) {
		return nil, ErrUnavailable
	}
	if _, e := readKey(keyFile); e != nil {
		return nil, e
	}
	return &Manager{pool: pool, authority: authority, keyFile: keyFile, origin: origin}, nil
}

type revision struct {
	id, identity string
	version      int64
	config       credential
	provider     payments.Provider
}
type snapshot struct {
	version  int64
	current  map[string]revision
	recovery []payments.Provider
}

func (m *Manager) load(ctx context.Context, q pgx.Tx, key []byte) (snapshot, error) {
	s := snapshot{current: map[string]revision{}, recovery: []payments.Provider{}}
	if e := q.QueryRow(ctx, `SELECT version FROM payment_configuration_state WHERE singleton`).Scan(&s.version); e != nil {
		return s, ErrUnavailable
	}
	// Latest credentials for every immutable merchant/application identity remain
	// available for original queries, notifications, close and refunds.
	rows, e := q.Query(ctx, `SELECT DISTINCT ON(provider,configuration_id) provider,configuration_id,version,encrypted_payload FROM payment_provider_revisions ORDER BY provider,configuration_id,version DESC`)
	if e != nil {
		return s, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var r revision
		var ciphertext []byte
		if e = rows.Scan(&r.id, &r.identity, &r.version, &ciphertext); e != nil {
			return s, ErrUnavailable
		}
		if r.version > s.version {
			return s, ErrUnavailable
		}
		raw, e := unseal(key, r.id, r.version, ciphertext)
		if e != nil || json.Unmarshal(raw, &r.config) != nil {
			return s, ErrUnavailable
		}
		if r.identity != "" {
			r.provider, e = m.build(r.id, r.config)
			if e != nil {
				return s, ErrUnavailable
			}
			p, ok := r.provider.(interface{ ConfigurationID() string })
			if !ok || p.ConfigurationID() != r.identity {
				return s, ErrUnavailable
			}
			s.recovery = append(s.recovery, r.provider)
		} else if r.config.Enabled || len(m.providerStatus(r.id, r.config, false).MissingFields) == 0 {
			return s, ErrUnavailable
		}
		if old, ok := s.current[r.id]; !ok || old.version < r.version {
			s.current[r.id] = r
		}
	}
	var maxVersion int64
	for _, r := range s.current {
		if r.version > maxVersion {
			maxVersion = r.version
		}
	}
	if maxVersion != s.version {
		return s, ErrUnavailable
	}
	if rows.Err() != nil {
		return s, ErrUnavailable
	}
	return s, nil
}
func (m *Manager) read(ctx context.Context, actor string) (snapshot, error) {
	tx, e := m.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return snapshot{}, ErrUnavailable
	}
	defer database.Rollback(tx)
	if actor != "" {
		if e = m.authority.RequireAdminOn(ctx, tx, actor, "finance:read"); e != nil {
			return snapshot{}, authorityError(e)
		}
	}
	key, e := readKey(m.keyFile)
	if e != nil {
		return snapshot{}, e
	}
	s, e := m.load(ctx, tx, key)
	if e != nil {
		return s, e
	}
	if e = tx.Commit(ctx); e != nil {
		return s, ErrUnavailable
	}
	return s, nil
}
func (m *Manager) status(s snapshot) Status {
	out := Status{Version: s.version, Mode: "direct", Managed: true, StoreReady: true, Providers: []ProviderStatus{}}
	for _, id := range []string{"wechat", "alipay"} {
		r := s.current[id]
		p := m.providerStatus(id, r.config, r.provider != nil)
		out.Providers = append(out.Providers, p)
		out.Configured = out.Configured || (p.Enabled && p.Configured)
	}
	return out
}
func (m *Manager) Status(ctx context.Context, actor string) (Status, error) {
	if actor == "" {
		return Status{}, accounts.ErrForbidden
	}
	s, e := m.read(ctx, actor)
	if e != nil {
		return Status{}, e
	}
	return m.status(s), nil
}

// RuntimeStatus is safe metadata for already-authorized runtime capability reads.
func (m *Manager) RuntimeStatus(ctx context.Context) (Status, error) {
	s, e := m.read(ctx, "")
	if e != nil {
		return Status{}, e
	}
	return m.status(s), nil
}
func (m *Manager) Providers(ctx context.Context) ([]payments.Provider, []payments.Provider, error) {
	s, e := m.read(ctx, "")
	if e != nil {
		return nil, nil, e
	}
	active := []payments.Provider{}
	for _, id := range []string{"wechat", "alipay"} {
		r := s.current[id]
		if r.config.Enabled && r.provider != nil {
			active = append(active, r.provider)
		}
	}
	return active, s.recovery, nil
}
func authorityError(e error) error {
	if errors.Is(e, accounts.ErrForbidden) {
		return accounts.ErrForbidden
	}
	return ErrUnavailable
}
func (m *Manager) Update(ctx context.Context, actor, id string, r UpdateRequest) (Status, error) {
	// Basic shape validation precedes database access and cannot expose credentials.
	if _, e := merge(id, credential{}, r); e != nil {
		return Status{}, e
	}
	tx, e := m.pool.Begin(ctx)
	if e != nil {
		return Status{}, ErrUnavailable
	}
	defer database.Rollback(tx)
	// Same lock used by role and active-status mutations prevents authority revocation
	// racing the durable financial configuration write.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`); e != nil {
		return Status{}, ErrUnavailable
	}
	if e = m.authority.RequireAdminOn(ctx, tx, actor, "finance:write"); e != nil {
		return Status{}, authorityError(e)
	}
	var version int64
	if e = tx.QueryRow(ctx, `SELECT version FROM payment_configuration_state WHERE singleton FOR UPDATE`).Scan(&version); e != nil {
		return Status{}, ErrUnavailable
	}
	if version != *r.ExpectedVersion {
		return Status{}, accounts.ErrConflict
	}
	if version == math.MaxInt64 {
		return Status{}, ErrUnavailable
	}
	key, e := readKey(m.keyFile)
	if e != nil {
		return Status{}, e
	}
	s, e := m.load(ctx, tx, key)
	if e != nil {
		return Status{}, e
	}
	old := s.current[id]
	c, e := merge(id, old.config, r)
	if e != nil {
		return Status{}, e
	}
	p, e := m.build(id, c)
	identity := ""
	// An entirely blank disabled channel is a valid initial administration state.
	if e != nil {
		if c.Enabled || c.AppID != "" || c.MerchantID != "" || c.SellerID != "" || c.CertificateSerial != "" || c.PrivateKeyPEM != "" || c.APIv3Key != "" || c.PublicKeyPEM != "" || len(c.PlatformPublicKeys) > 0 {
			return Status{}, accounts.ErrInvalid
		}
	} else {
		identity = p.(interface{ ConfigurationID() string }).ConfigurationID()
	}
	if e = normalizePublicKeys(&c); e != nil {
		return Status{}, e
	}
	next := version + 1
	raw, e := json.Marshal(c)
	if e != nil {
		return Status{}, ErrUnavailable
	}
	encrypted, e := seal(key, id, next, raw)
	if e != nil {
		return Status{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO payment_provider_revisions(provider,version,configuration_id,encrypted_payload) VALUES($1,$2,$3,$4)`, id, next, identity, encrypted); e != nil {
		return Status{}, ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `UPDATE payment_configuration_state SET version=$1 WHERE singleton`, next); e != nil {
		return Status{}, ErrUnavailable
	}
	detail := map[string]any{"expected_version": version, "version": next, "before": m.providerStatus(id, old.config, old.provider != nil), "after": m.providerStatus(id, c, p != nil), "private_key_replaced": r.PrivateKeyPEM != nil, "api_v3_key_replaced": r.APIv3Key != nil, "public_key_replaced": r.PublicKeyPEM != nil, "platform_keys_replaced": len(r.PlatformPublicKeys) > 0}
	if e = m.authority.WriteAudit(ctx, tx, actor, "finance.payment-provider", id, r.Reason, detail); e != nil {
		return Status{}, ErrUnavailable
	}
	s.version = next
	s.current[id] = revision{id: id, identity: identity, version: next, config: c, provider: p}
	if e = tx.Commit(ctx); e != nil {
		return Status{}, ErrUnavailable
	}
	return m.status(s), nil
}
