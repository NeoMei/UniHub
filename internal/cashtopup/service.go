// Package cashtopup implements real-money cash wallet topups. It is separate
// from billing: a verified payment here credits the user's cash wallet, while
// billing checkouts only settle commercial entitlements and never touch cash.
package cashtopup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/money"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

var (
	ErrInvalid            = errors.New("cashtopup: invalid")
	ErrConflict           = errors.New("cashtopup: conflict")
	ErrPaymentUnavailable = errors.New("cashtopup: payment_not_configured")
	ErrNotFound           = accounts.ErrNotFound
	checkoutIDRE          = regexp.MustCompile("^ct_[A-Za-z0-9]{1,32}$")
	providerIDRE          = regexp.MustCompile("^(wechat|alipay)$")
	keyRE                 = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
	// Cash checkout ids carry a prefix that the shared payment notify route uses
	// to dispatch to this service rather than billing.
	CheckoutIDPrefix = "ct_"
)

type Request struct {
	Provider  string `json:"provider"`
	ProductID string `json:"product_id,omitempty"`
	Amount    string `json:"amount"`
	Key       string `json:"-"`
	ReturnURL string `json:"-"`
}

type Checkout struct {
	ID            string            `json:"id"`
	AccountID     string            `json:"account_id"`
	ProductID     string            `json:"product_id"`
	Provider      string            `json:"provider"`
	Amount        string            `json:"amount"`
	Currency      string            `json:"currency"`
	PaymentStatus string            `json:"payment_status"`
	ExpiresAt     time.Time         `json:"expires_at"`
	CreatedAt     time.Time         `json:"created_at"`
	Session       *payments.Session `json:"session,omitempty"`
	ErrorCode     string            `json:"error_code,omitempty"`
	OperationID   string            `json:"operation_id,omitempty"`
}

type record struct {
	Checkout
	ActorID         string           `json:"actor_id"`
	MerchantID      string           `json:"merchant_id"`
	MerchantVersion string           `json:"merchant_version"`
	RequestDigest   string           `json:"request_digest"`
	Receipt         *payments.Result `json:"receipt,omitempty"`
}

type Service struct {
	pool      *pgxpool.Pool
	accounts  *accounts.Store
	wallet    *wallet.Service
	now       func() time.Time
	mu        sync.RWMutex
	providers []payments.Provider
	source    func(context.Context) ([]payments.Provider, []payments.Provider, error)
}

func New(p *pgxpool.Pool, a *accounts.Store, w *wallet.Service) *Service {
	return &Service{pool: p, accounts: a, wallet: w, now: time.Now}
}

// ConfigureProviders installs a static provider list (tests / simple deployments).
func (s *Service) ConfigureProviders(ps []payments.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]payments.Provider, 0, len(ps))
	for _, p := range ps {
		if p != nil && (p.ID() == "wechat" || p.ID() == "alipay") && p.MerchantID() != "" {
			out = append(out, p)
		}
	}
	s.providers = out
}

// ConfigurePaymentSource binds a shared durable provider authority with billing.
// The callback returns (active, all, error) as defined by billing.Service.
func (s *Service) ConfigurePaymentSource(src func(context.Context) ([]payments.Provider, []payments.Provider, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.source = src
}

func (s *Service) providerSnapshot(ctx context.Context) ([]payments.Provider, error) {
	s.mu.RLock()
	src, static := s.source, s.providers
	s.mu.RUnlock()
	if src != nil {
		active, _, e := src(ctx)
		return active, e
	}
	return static, nil
}

func (s *Service) provider(ctx context.Context, id string) (payments.Provider, error) {
	ps, e := s.providerSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	for _, p := range ps {
		if p.ID() == id {
			return p, nil
		}
	}
	return nil, ErrPaymentUnavailable
}

func (s *Service) notificationProviders(ctx context.Context, id string) ([]payments.Provider, error) {
	_, all, e := func() ([]payments.Provider, []payments.Provider, error) {
		s.mu.RLock()
		src := s.source
		s.mu.RUnlock()
		if src != nil {
			return src(ctx)
		}
		ps, err := s.providerSnapshot(ctx)
		return ps, ps, err
	}()
	if e != nil {
		return nil, e
	}
	return all, nil
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newID() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return CheckoutIDPrefix + hex.EncodeToString(b[:])
}

func (s *Service) locked(ctx context.Context, account string, fn func(*pgxpool.Conn) error) error {
	c, e := s.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer c.Release()
	unlock, e := database.LockAccount(ctx, c, account)
	if e != nil {
		return e
	}
	defer unlock(context.Background())
	return fn(c)
}

func (s *Service) finance(ctx context.Context, q accounts.Querier, actor, account string) error {
	r, e := s.accounts.RoleOn(ctx, q, actor, account)
	if e != nil {
		return e
	}
	if r != "owner" && r != "billing_admin" {
		return accounts.ErrForbidden
	}
	return nil
}

func encode(v any) []byte { b, _ := json.Marshal(v); return b }

func (s *Service) Create(ctx context.Context, actor, account string, r Request) (Checkout, error) {
	var out Checkout
	if !keyRE.MatchString(r.Key) || !providerIDRE.MatchString(r.Provider) {
		return out, ErrInvalid
	}
	n, e := money.ParseRecharge(r.Amount)
	if e != nil {
		return out, ErrInvalid
	}
	if e = s.finance(ctx, s.pool, actor, account); e != nil {
		return out, e
	}
	var replay bool
	if e = s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM cash_checkouts WHERE account_id=$1 AND request_key=$2)", account, r.Key).Scan(&replay); e != nil {
		return out, e
	}
	var p payments.Provider
	if !replay {
		p, e = s.provider(ctx, r.Provider)
		if e != nil {
			return out, e
		}
	}
	e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
		parts := []string{actor, r.Provider, r.ProductID, r.Amount}
		if r.ReturnURL != "" {
			parts = append(parts, r.ReturnURL)
		}
		d := digest(parts)
		var old record
		old, e = load(ctx, c, account, r.Key)
		if e == nil {
			if old.RequestDigest != d {
				return ErrConflict
			}
			out = old.Checkout
			return nil
		}
		if !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		if p == nil {
			return ErrPaymentUnavailable
		}
		now := s.now().UTC()
		expires := now.Add(15 * time.Minute)
		id := newID()
		returnURL := ""
		if r.ReturnURL != "" {
			u, err := url.Parse(r.ReturnURL)
			if err != nil {
				return ErrInvalid
			}
			q := u.Query()
			q.Set("cash_checkout", id)
			u.RawQuery = q.Encode()
			returnURL = u.String()
		}
		sess, e := p.Create(ctx, payments.Request{OrderID: id, Amount: r.Amount, Description: "Cash wallet topup", ExpiresAt: expires, ReturnURL: returnURL})
		if e != nil {
			return e
		}
		rec := record{Checkout: Checkout{ID: id, AccountID: account, ProductID: r.ProductID, Provider: r.Provider, Amount: r.Amount, Currency: "CNY", PaymentStatus: "CREATED", ExpiresAt: expires, CreatedAt: now, Session: &sess}, ActorID: actor, MerchantID: p.MerchantID(), MerchantVersion: paymentConfigurationID(p), RequestDigest: d}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if _, e = tx.Exec(ctx, "INSERT INTO cash_checkouts(id,account_id,actor_id,product_id,amount,currency,provider,merchant_id,merchant_version,request_key,request_digest,snapshot) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12)", rec.ID, rec.AccountID, rec.ActorID, rec.ProductID, n, "CNY", rec.Provider, rec.MerchantID, rec.MerchantVersion, r.Key, rec.RequestDigest, encode(rec)); e != nil {
			return dbErr(e)
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		out = rec.Checkout
		return nil
	})
	return out, e
}

func (s *Service) Get(ctx context.Context, actor, account, id string) (Checkout, error) {
	if !checkoutIDRE.MatchString(id) {
		return Checkout{}, ErrInvalid
	}
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Checkout{}, e
	}
	rec, e := loadByID(ctx, s.pool, account, id)
	if e != nil {
		return Checkout{}, e
	}
	return rec.Checkout, nil
}

func (s *Service) List(ctx context.Context, actor, account string) ([]Checkout, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	rows, e := s.pool.Query(ctx, "SELECT snapshot FROM cash_checkouts WHERE account_id=$1 ORDER BY created_at DESC,id LIMIT 100", account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Checkout{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var rec record
		if e = json.Unmarshal(b, &rec); e != nil {
			return nil, e
		}
		c := rec.Checkout
		c.Session = nil
		out = append(out, c)
	}
	return out, rows.Err()
}

func load(ctx context.Context, q accounts.Querier, account, key string) (record, error) {
	var b []byte
	e := q.QueryRow(ctx, "SELECT snapshot FROM cash_checkouts WHERE account_id=$1 AND request_key=$2", account, key).Scan(&b)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return record{}, accounts.ErrNotFound
		}
		return record{}, e
	}
	var rec record
	if e = json.Unmarshal(b, &rec); e != nil {
		return record{}, e
	}
	return rec, nil
}

func loadByID(ctx context.Context, q accounts.Querier, account, id string) (record, error) {
	var b []byte
	e := q.QueryRow(ctx, "SELECT snapshot FROM cash_checkouts WHERE account_id=$1 AND id=$2", account, id).Scan(&b)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return record{}, accounts.ErrNotFound
		}
		return record{}, e
	}
	var rec record
	if e = json.Unmarshal(b, &rec); e != nil {
		return record{}, e
	}
	return rec, nil
}

func loadAny(ctx context.Context, q accounts.Querier, id string) (record, error) {
	var b []byte
	e := q.QueryRow(ctx, "SELECT snapshot FROM cash_checkouts WHERE id=$1", id).Scan(&b)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return record{}, accounts.ErrNotFound
		}
		return record{}, e
	}
	var rec record
	if e = json.Unmarshal(b, &rec); e != nil {
		return record{}, e
	}
	return rec, nil
}

func dbErr(e error) error {
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return ErrConflict
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return accounts.ErrNotFound
	}
	return e
}
