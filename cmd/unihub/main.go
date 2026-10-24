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
	"unihub/internal/database"
	"unihub/internal/httpapi"
	"unihub/internal/ledger"
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
	if mode == "worker" {
		work(ctx, w)
		return nil
	}
	v, e := auth.NewOIDC(startup, c.Issuer, c.Audience)
	if e != nil {
		return e
	}
	server := &http.Server{Addr: c.Listen, Handler: httpapi.New(v, a, w, httpapi.Options{Sandbox: c.Sandbox, Merchant: c.Merchant, SigningKey: c.SigningKey}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan struct{})
	go func() { defer close(done); work(ctx, w) }()
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
func work(ctx context.Context, w *wallet.Service) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			run, cancel := context.WithTimeout(ctx, 30*time.Second)
			e := w.Recover(run)
			cancel()
			if e != nil && !errors.Is(e, context.Canceled) {
				log.Print("wallet recovery cycle incomplete; durable commands will be retried")
			}
		}
	}
}
