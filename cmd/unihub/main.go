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
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/console"
	"unihub/internal/database"
	"unihub/internal/httpapi"
	"unihub/internal/ledger"
	"unihub/internal/modelaccess"
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
	w := wallet.New(pool, a, l)
	b := billing.New(pool, a, l)
	b.ConfigureOperators(c.OperatorIDs)
	b.ConfigureTrustedServices([]string{"llmrouter"})
	b.ConfigureSandbox(c.Sandbox && !c.Production)
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
	productVerifiers := map[string]auth.Verifier{}
	for product, audience := range c.ProductClients {
		productVerifiers[product], e = auth.NewOIDC(startup, c.Issuer, audience)
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
	var handler http.Handler = httpapi.New(v, a, w, httpapi.Options{Sandbox: c.Sandbox, Merchant: c.Merchant, SigningKey: c.SigningKey, Billing: b, ServiceToken: c.ModelServiceToken, ModelAccess: modelAccess, OperatorVerifier: operatorVerifier, ProductVerifiers: productVerifiers})
	if c.PublicURL != "" {
		browser, err := console.New(startup, pool, v, console.Options{Issuer: c.Issuer, ClientID: c.Audience, ClientSecret: c.ClientSecret, PublicURL: c.PublicURL, Production: c.Production, Sandbox: c.Sandbox, StaticDir: c.StaticDir})
		if err != nil {
			return err
		}
		handler = browser.Handler(handler)
	}
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
