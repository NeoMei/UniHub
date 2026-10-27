package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	publicapi "unihub/api"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/console"
	"unihub/internal/database"
	"unihub/internal/httpapi"
	"unihub/internal/identityapi"
	"unihub/internal/ledger"
	"unihub/internal/modelaccess"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/wallet"
)

func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
func run() error {
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode != "serve" && mode != "worker" && mode != "migrate" {
		return errors.New("usage: unihub [serve|worker|migrate]")
	}
	c, e := readConfig(os.Getenv)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, e := pgxpool.New(startup, c.DB)
	if e != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	if e = pool.Ping(startup); e != nil {
		return errors.New("database connection failed")
	}
	if e = database.Migrate(startup, pool); e != nil {
		return e
	}
	if mode == "migrate" {
		return nil
	}
	l := ledger.NewFormance(c.LedgerURL, c.LedgerName, &http.Client{Timeout: 10 * time.Second})
	if e = l.Ensure(startup); e != nil {
		return e
	}
	a := accounts.New(pool)
	productUsers := productusers.New(pool, c.ProductUserManifests, c.PublicURL, c.UserAdminIDs)
	w := wallet.New(pool, a, l)
	b := billing.New(pool, a, l)
	b.ConfigureProductUsers(productUsers)
	b.ConfigureOperators(c.OperatorIDs)
	b.ConfigureTrustedServices([]string{"llmrouter"})
	b.ConfigureSandbox(c.Sandbox && !c.Production)
	if e = ensureConfiguredProducts(startup, pool, c.ProductAccess); e != nil {
		return e
	}
	if e = ensureConfiguredBillingProducts(startup, pool, c.BillingProfiles); e != nil {
		return e
	}
	if e = b.ApplyConfiguredProfiles(startup, c.BillingProfiles); e != nil {
		return errors.New("billing profile deployment conflicts with published configuration")
	}
	if c.SandboxCatalog {
		if _, e = b.SeedSandbox(startup); e != nil {
			return e
		}
	}
	if mode == "worker" {
		work(ctx, w, b)
		return nil
	}
	v, e := auth.NewOIDC(startup, c.Issuer, c.Audience)
	if e != nil {
		return e
	}
	modelAccess := modelaccess.New(pool, a, b)
	modelAccess.ConfigureProductUsers(productUsers)
	productVerifiers := map[string]auth.Verifier{}
	for product, audience := range c.ProductClients {
		productVerifiers[product], e = auth.NewOIDC(startup, c.Issuer, audience)
		if e != nil {
			return e
		}
	}
	for id := range c.ProductAccess {
		productVerifiers[id], e = auth.NewOIDC(startup, c.Issuer, id)
		if e != nil {
			return e
		}
	}
	var operatorVerifier auth.Verifier
	if c.OperatorAudience != "" {
		operatorVerifier, e = auth.NewOIDC(startup, c.Issuer, c.OperatorAudience)
		if e != nil {
			return e
		}
	}
	identity, err := identityapi.New(startup, pool, identityapi.Options{Issuer: c.Issuer, ManagementClientID: c.IdentityManagementClientID, ManagementClientSecret: c.IdentityManagementSecret, SigningKey: c.IdentityKey, Clients: c.ProductAccess, Production: c.Production, AuditRedactionVerified: c.IdentityAuditVerified, TrustLoopbackProxy: c.IdentityTrustLoopbackProxy})
	if err != nil {
		return err
	}
	var profileService httpapi.ProfileService
	if identity.ProfileEnabled() {
		profileService = identity
	}
	var productAuth *productauth.Service
	if len(c.ProductRuntime) > 0 {
		productAuth = productauth.New(pool, a, productUsers, c.PublicURL)
		productAuth.ConfigureBrowserClients(c.ProductAccess)
	}
	var handler http.Handler = httpapi.New(v, a, w, httpapi.Options{Sandbox: c.Sandbox, Merchant: c.Merchant, SigningKey: c.SigningKey, Billing: b, ServiceToken: c.ModelServiceToken, ModelAccess: modelAccess, OperatorVerifier: operatorVerifier, ProductVerifiers: productVerifiers, ProductAccess: c.ProductAccess, ProductUsers: productUsers, Identity: profileService, ProductRuntime: c.ProductRuntime, ProductAuth: productAuth})
	if c.PublicURL != "" {
		browser, err := console.New(startup, pool, v, console.Options{Issuer: c.Issuer, ClientID: c.Audience, ClientSecret: c.ClientSecret, PublicURL: c.PublicURL, Production: c.Production, Sandbox: c.Sandbox, StaticDir: c.StaticDir})
		if err != nil {
			return err
		}
		handler = browser.Handler(handler)
	}
	if productAuth != nil {
		handler = productAuth.BrowserHandler(handler)
	}
	handler = products.CORS(identity.Handler(handler), c.ProductAccess)
	handler = publicapi.Handler(handler)
	server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan struct{})
	go func() { defer close(done); work(ctx, w, b) }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("UniHub listening on %s (sandbox=%t)", c.Listen, c.Sandbox)
	e = server.ListenAndServe()
	stop()
	<-done
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func work(ctx context.Context, w *wallet.Service, b *billing.Service) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			run, cancel := context.WithTimeout(ctx, 30*time.Second)
			walletErr := w.Recover(run)
			_, billingErr := b.Recover(run)
			_, maintenanceErr := b.Maintain(run, time.Now().UTC())
			cancel()
			e := errors.Join(walletErr, billingErr, maintenanceErr)
			if e != nil && !errors.Is(e, context.Canceled) {
				log.Print("UniHub recovery cycle incomplete; durable commands will be retried")
			}
		}
	}
}
