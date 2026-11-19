// unihub-credit-adoption audits local historical credit lots. Applying a plan
// is limited to isolated fixture schemas and ledgers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/ledger"
)

func main() {
	if e := run(os.Args[1:], os.Getenv, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

type batchFlags []string

func (f *batchFlags) String() string { return strings.Join(*f, ",") }
func (f *batchFlags) Set(v string) error {
	*f = append(*f, v)
	return nil
}

var fixtureSchema = regexp.MustCompile(`^legacy_credit_fixture_[a-z0-9_]{1,40}$`)
var fixtureLedger = regexp.MustCompile(`^legacy-credit-fixture-[a-z0-9-]{1,40}$`)

func localHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func localDatabaseHost(host string) bool {
	return strings.HasPrefix(host, "/") || localHost(host)
}

func openOptions(getenv func(string) string, apply bool) (*pgxpool.Config, string, string, error) {
	dsn := getenv("UNIHUB_ADOPTION_DATABASE_URL")
	target := getenv("UNIHUB_ADOPTION_LEDGER_URL")
	name := getenv("UNIHUB_ADOPTION_LEDGER_NAME")
	if dsn == "" || target == "" || name == "" {
		return nil, "", "", errors.New("set UNIHUB_ADOPTION_DATABASE_URL, UNIHUB_ADOPTION_LEDGER_URL and UNIHUB_ADOPTION_LEDGER_NAME")
	}
	db, e := pgxpool.ParseConfig(dsn)
	if e != nil || !localDatabaseHost(db.ConnConfig.Host) {
		return nil, "", "", errors.New("use a local PostgreSQL host or Unix socket")
	}
	for _, fallback := range db.ConnConfig.Fallbacks {
		if !localDatabaseHost(fallback.Host) {
			return nil, "", "", errors.New("all PostgreSQL hosts must be local")
		}
	}
	u, e := url.Parse(target)
	if e != nil || u.Scheme != "http" && u.Scheme != "https" || !localHost(u.Hostname()) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, "", "", errors.New("use a local Formance HTTP(S) origin")
	}
	if apply && (!fixtureSchema.MatchString(db.ConnConfig.RuntimeParams["search_path"]) || !fixtureLedger.MatchString(name)) {
		return nil, "", "", errors.New("apply requires a legacy_credit_fixture_* search path and legacy-credit-fixture-* ledger")
	}
	if !apply {
		db.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	return db, target, name, nil
}

func run(args []string, getenv func(string) string, dest io.Writer) error {
	flags := flag.NewFlagSet("unihub-credit-adoption", flag.ContinueOnError)
	actor := flags.String("actor", "", "payer owner or billing administrator ID")
	account := flags.String("account", "", "payer account ID")
	product := flags.String("product", "", "origin product ID")
	var batches batchFlags
	flags.Var(&batches, "batch", "original legacy batch ID (repeatable)")
	dryRun := flags.Bool("dry-run", false, "report the plan without changes (default)")
	apply := flags.String("apply-digest", "", "apply an approved preflight digest in an isolated fixture")
	verify := flags.String("verify-digest", "", "verify the receipt for an applied preflight digest")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 || *actor == "" || *account == "" || *product == "" ||
		(*apply != "" && (*verify != "" || *dryRun)) || (*verify != "" && *dryRun) ||
		((*apply != "" || *verify != "") && len(batches) == 0) {
		return errors.New("provide --actor, --account and --product; apply or verify also requires exact --batch IDs")
	}
	cfg, target, name, e := openOptions(getenv, *apply != "")
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return errors.New("invalid local database configuration")
	}
	defer pool.Close()
	if e = pool.Ping(ctx); e != nil {
		return errors.New("local database unavailable")
	}
	if *apply != "" {
		var schema string
		if e = pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); e != nil || schema != cfg.ConnConfig.RuntimeParams["search_path"] {
			return errors.New("isolated fixture schema is unavailable")
		}
	}
	httpClient := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{}}
	service := billing.New(pool, accounts.New(pool), ledger.NewFormance(target, name, httpClient))
	request := billing.LegacyCreditAdoptionRequest{ActorID: *actor, AccountID: *account, ProductID: *product, BatchIDs: batches}
	output := json.NewEncoder(dest)
	if *apply != "" {
		result, e := service.ApplyLegacyCreditAdoption(ctx, request, *apply)
		if e != nil {
			return e
		}
		return output.Encode(result)
	}
	if *verify != "" {
		result, e := service.VerifyLegacyCreditAdoption(ctx, request, *verify)
		if e != nil {
			return e
		}
		return output.Encode(result)
	}
	plan, e := service.PreviewLegacyCreditAdoption(ctx, request)
	if e != nil {
		return e
	}
	return output.Encode(plan)
}
