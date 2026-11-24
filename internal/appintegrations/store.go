package appintegrations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/platformadmin"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

const applicationFields = `product_id, instance_id, name, origin, entry_url, callback_url,
	native_login_url, client_id, state, version, credential_hash IS NOT NULL, inbound_verified, outbound_verified`

type Store struct {
	pool            *pgxpool.Pool
	admin           *platformadmin.Store
	hub             string
	allowLoopback   bool
	deployed        map[string]Application
	reservedClients map[string]bool
}

func appKey(product, instance string) string { return product + "/" + productusers.Instance(instance) }

func New(pool *pgxpool.Pool, admin *platformadmin.Store, hub string, allowLoopback bool,
	manifests productusers.Registry, clients map[string]products.Client, runtimes map[string]bool, reserved []string) *Store {
	s := &Store{pool: pool, admin: admin, hub: hub, allowLoopback: allowLoopback,
		deployed: map[string]Application{}, reservedClients: map[string]bool{}}
	for _, id := range reserved {
		s.reservedClients[id] = true
	}
	for _, manifest := range manifests.List() {
		key := appKey(manifest.ProductID, manifest.InstanceID)
		s.deployed[key] = Application{Input: Input{ProductID: manifest.ProductID, InstanceID: manifest.InstanceID,
			Name: manifest.Name, EntryURL: manifest.EntryURL, Version: manifest.Version}, Source: "deployment",
			State: "configured", HubOrigin: hub}
	}
	ids := make([]string, 0, len(clients))
	for id := range clients {
		s.reservedClients[id] = true
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		client := clients[id]
		key := appKey(client.ProductID, client.InstanceID)
		app, exists := s.deployed[key]
		if !exists {
			app = Application{Input: Input{ProductID: client.ProductID, InstanceID: productusers.Instance(client.InstanceID),
				Name: client.ProductID}, Source: "deployment", State: "configured", HubOrigin: hub}
		}
		if app.ClientID == "" || (!app.BrowserSSO && client.BrowserSSO) {
			app.ClientID, app.BrowserSSO, app.NativeLoginURL = id, client.BrowserSSO, client.NativeLoginURL
			if len(client.Origins) > 0 {
				app.Origin = client.Origins[0]
			}
			if len(client.RedirectURIs) > 0 {
				app.CallbackURL = client.RedirectURIs[0]
			}
		}
		if runtimes[key] {
			app.RuntimeConfigured, app.CredentialConfigured = true, true
		}
		s.deployed[key] = app
	}
	for key := range runtimes {
		app, ok := s.deployed[key]
		if ok {
			app.RuntimeConfigured, app.CredentialConfigured = true, true
			s.deployed[key] = app
		}
	}
	return s
}

func (s *Store) scan(row interface{ Scan(...any) error }) (Application, error) {
	app := Application{Source: "managed", BrowserSSO: true, HubOrigin: s.hub}
	err := row.Scan(&app.ProductID, &app.InstanceID, &app.Name, &app.Origin, &app.EntryURL, &app.CallbackURL,
		&app.NativeLoginURL, &app.ClientID, &app.State, &app.Version, &app.CredentialConfigured, &app.InboundVerified, &app.OutboundVerified)
	app.RuntimeConfigured = app.CredentialConfigured && app.State != "draft"
	return app, err
}

func (s *Store) reserved(app Application) bool {
	_, deployed := s.deployed[appKey(app.ProductID, app.InstanceID)]
	return deployed || s.reservedClients[app.ClientID]
}

func (s *Store) list(ctx context.Context, liveOnly bool) ([]Application, error) {
	query := `SELECT ` + applicationFields + ` FROM application_integrations`
	if liveOnly {
		query += ` WHERE state IN ('pilot','active')`
	}
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		app, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		if !s.reserved(app) {
			out = append(out, app)
		}
	}
	return out, rows.Err()
}

func (s *Store) List(ctx context.Context) ([]Application, error) {
	out, err := s.list(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, app := range s.deployed {
		out = append(out, app)
	}
	slices.SortFunc(out, func(a, b Application) int {
		return strings.Compare(appKey(a.ProductID, a.InstanceID), appKey(b.ProductID, b.InstanceID))
	})
	return out, nil
}

func (s *Store) change(ctx context.Context, actor, action string, fn func(pgx.Tx) (Application, error)) (Application, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Application{}, err
	}
	defer database.Rollback(tx)
	if err = s.admin.RequireApplicationChange(ctx, tx, actor); err != nil {
		return Application{}, err
	}
	app, err := fn(tx)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			err = accounts.ErrConflict
		}
		return Application{}, err
	}
	err = s.admin.WriteAudit(ctx, tx, actor, "application_integrations."+action, appKey(app.ProductID, app.InstanceID),
		"platform application integration", map[string]any{"product_id": app.ProductID, "instance_id": app.InstanceID, "status": app.State, "version": app.Version})
	if err != nil {
		return Application{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Application{}, err
	}
	return app, nil
}

func (s *Store) Create(ctx context.Context, actor string, input Input) (Application, error) {
	app, err := normalize(input, s.hub, s.allowLoopback)
	if err != nil || input.Version != 0 {
		return Application{}, accounts.ErrInvalid
	}
	if s.reserved(app) {
		return Application{}, accounts.ErrConflict
	}
	return s.change(ctx, actor, "create", func(tx pgx.Tx) (Application, error) {
		return s.scan(tx.QueryRow(ctx, `INSERT INTO application_integrations
			(product_id,instance_id,name,origin,entry_url,callback_url,native_login_url,client_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+applicationFields,
			app.ProductID, app.InstanceID, app.Name, app.Origin, app.EntryURL, app.CallbackURL, app.NativeLoginURL, app.ClientID))
	})
}

func (s *Store) current(ctx context.Context, tx pgx.Tx, product, instance string, version int64) (Application, error) {
	app, err := s.scan(tx.QueryRow(ctx, `SELECT `+applicationFields+` FROM application_integrations WHERE product_id=$1 AND instance_id=$2 FOR UPDATE`, product, instance))
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, accounts.ErrNotFound
	}
	if err != nil {
		return Application{}, err
	}
	if version < 1 {
		return Application{}, accounts.ErrInvalid
	}
	if s.reserved(app) || version != app.Version {
		return Application{}, accounts.ErrConflict
	}
	return app, nil
}

func (s *Store) Update(ctx context.Context, actor, product, instance string, input Input) (Application, error) {
	if (input.ProductID != "" && input.ProductID != product) || (input.InstanceID != "" && input.InstanceID != instance) {
		return Application{}, accounts.ErrInvalid
	}
	input.ProductID, input.InstanceID = product, instance
	next, err := normalize(input, s.hub, s.allowLoopback)
	if err != nil {
		return Application{}, err
	}
	return s.change(ctx, actor, "update", func(tx pgx.Tx) (Application, error) {
		app, err := s.current(ctx, tx, product, instance, input.Version)
		if err != nil {
			return Application{}, err
		}
		if app.State != "draft" {
			return Application{}, accounts.ErrConflict
		}
		return s.scan(tx.QueryRow(ctx, `UPDATE application_integrations SET
			name=$3,origin=$4,entry_url=$5,callback_url=$6,native_login_url=$7,version=version+1,
			credential_hash=CASE WHEN origin=$4 AND callback_url=$6 THEN credential_hash ELSE NULL END
			WHERE product_id=$1 AND instance_id=$2 RETURNING `+applicationFields,
			product, instance, next.Name, next.Origin, next.EntryURL, next.CallbackURL, next.NativeLoginURL))
	})
}

type Credential struct {
	Application Application `json:"application"`
	Token       string      `json:"token"`
}

func (s *Store) Issue(ctx context.Context, actor, product, instance string, version int64) (Credential, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Credential{}, err
	}
	token := "uha_" + base64.RawURLEncoding.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(token))
	app, err := s.change(ctx, actor, "credential", func(tx pgx.Tx) (Application, error) {
		app, err := s.current(ctx, tx, product, instance, version)
		if err != nil {
			return Application{}, err
		}
		if app.State != "draft" && app.State != "paused" {
			return Application{}, accounts.ErrConflict
		}
		return s.scan(tx.QueryRow(ctx, `UPDATE application_integrations SET credential_hash=$3,version=version+1
			WHERE product_id=$1 AND instance_id=$2 RETURNING `+applicationFields, product, instance, hash[:]))
	})
	if err != nil {
		return Credential{}, err
	}
	return Credential{Application: app, Token: token}, nil
}

func (s *Store) Transition(ctx context.Context, actor, product, instance string, version int64, state string) (Application, error) {
	if state != "pilot" && state != "paused" && state != "active" {
		return Application{}, accounts.ErrInvalid
	}
	return s.change(ctx, actor, "state", func(tx pgx.Tx) (Application, error) {
		app, err := s.current(ctx, tx, product, instance, version)
		if err != nil {
			return Application{}, err
		}
		if !app.allows(state) {
			return Application{}, accounts.ErrConflict
		}
		return s.scan(tx.QueryRow(ctx, `UPDATE application_integrations SET state=$3,version=version+1,
			pilot_started_at=CASE WHEN $3='pilot' THEN clock_timestamp() ELSE pilot_started_at END,
			inbound_verified=inbound_verified AND $3<>'pilot',outbound_verified=outbound_verified AND $3<>'pilot'
			WHERE product_id=$1 AND instance_id=$2 RETURNING `+applicationFields, product, instance, state))
	})
}
