// unihub-credit-adoption-production moves approved SuperWagie legacy lots
// into product credit scope without changing quantities, expiry, or cash.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/ledger"
)

const (
	maxPlanSize        = 2 << 20
	currentLedgerName  = "unihub-live"
	retainedLedgerName = "unihub"
)

type listFlag []string

func (ids *listFlag) String() string { return strings.Join(*ids, ",") }
func (ids *listFlag) Set(id string) error {
	*ids = append(*ids, id)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

func localDatabaseHost(host string) bool {
	return strings.HasPrefix(host, "/") || loopback(host)
}

func productionConfig(get func(string) string, mode string) (*pgxpool.Config, string, string, error) {
	if get("APP_ENV") != "production" {
		return nil, "", "", errors.New("production adoption requires APP_ENV=production")
	}
	dsn := get("UNIHUB_ADOPTION_DATABASE_URL")
	cfg, err := pgxpool.ParseConfig(dsn)
	if dsn == "" || err != nil || !localDatabaseHost(cfg.ConnConfig.Host) || cfg.ConnConfig.Database != "unihub" {
		return nil, "", "", errors.New("production adoption requires the local UniHub database")
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if !localDatabaseHost(fallback.Host) {
			return nil, "", "", errors.New("production adoption requires local PostgreSQL hosts")
		}
	}
	if path := cfg.ConnConfig.RuntimeParams["search_path"]; path != "" && path != "public" {
		return nil, "", "", errors.New("production adoption requires the public database schema")
	}
	if mode != "apply" {
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	target, name := get("UNIHUB_ADOPTION_LEDGER_URL"), get("UNIHUB_ADOPTION_LEDGER_NAME")
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || !loopback(u.Hostname()) ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || name != currentLedgerName {
		return nil, "", "", errors.New("production adoption requires the local UniHub Formance ledger")
	}
	return cfg, target, name, nil
}

func bytesDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func readApproval(path, expected string, request billing.LegacyCreditAdoptionRequest) (billing.LegacyCreditAdoptionPlan, error) {
	var plan billing.LegacyCreditAdoptionPlan
	original, err := os.Lstat(path)
	if err != nil || !original.Mode().IsRegular() || original.Mode().Perm()&0o077 != 0 || original.Size() > maxPlanSize {
		return plan, errors.New("approval must be a private regular plan file")
	}
	file, err := os.Open(path)
	if err != nil {
		return plan, errors.New("cannot open the approved plan")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(original, opened) {
		return plan, errors.New("approved plan identity changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPlanSize+1))
	if err != nil || len(raw) > maxPlanSize || expected != bytesDigest(raw) {
		return plan, errors.New("approval file digest mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return plan, errors.New("invalid approved plan")
	}
	if !plan.CanApply || len(plan.Blockers) != 0 || plan.Activity == nil || plan.Asset != billing.CreditAsset ||
		!plan.HasValidDigest() || plan.ProfileVersion < 1 || plan.Request.ActorID != request.ActorID || plan.Request.AccountID != request.AccountID ||
		plan.Request.ProductID != request.ProductID || !slices.Equal(plan.Request.BatchIDs, request.BatchIDs) {
		return plan, errors.New("approved plan does not match the selected payer and lots")
	}
	return plan, nil
}

func run(args []string, get func(string) string, dest io.Writer) error {
	flags := flag.NewFlagSet("unihub-credit-adoption-production", flag.ContinueOnError)
	mode := flags.String("mode", "preview", "preview, apply, or verify")
	actor := flags.String("actor", "", "active payer owner or billing administrator ID")
	account := flags.String("account", "", "payer account ID")
	var batches listFlag
	flags.Var(&batches, "batch", "exact legacy lot ID, repeatable")
	approval := flags.String("approved-plan", "", "reviewed private plan JSON file")
	approvalDigest := flags.String("approved-plan-sha256", "", "SHA-256 of the exact reviewed plan file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *actor == "" || *account == "" || len(batches) == 0 || (*mode != "preview" && *mode != "apply" && *mode != "verify") {
		return errors.New("provide preview, apply, or verify with an actor, payer and exact lot IDs")
	}
	slices.Sort(batches)
	request := billing.LegacyCreditAdoptionRequest{ActorID: *actor, AccountID: *account, ProductID: "superwagie", BatchIDs: batches}
	var approved billing.LegacyCreditAdoptionPlan
	var err error
	if *mode == "preview" {
		if *approval != "" || *approvalDigest != "" {
			return errors.New("preview accepts lot selection only")
		}
	} else {
		approved, err = readApproval(*approval, *approvalDigest, request)
		if err != nil {
			return err
		}
	}
	cfg, target, name, err := productionConfig(get, *mode)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("invalid production database configuration")
	}
	defer pool.Close()
	var schema string
	if err = pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil || schema != "public" {
		return errors.New("production UniHub schema is unavailable")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	service := billing.New(pool, accounts.New(pool), ledger.NewFormance(target, name, client))
	service.ConfigureLegacyCreditProofLedgers(name, retainedLedgerName, ledger.NewFormance(target, retainedLedgerName, client))
	output := json.NewEncoder(dest)
	if *mode == "preview" {
		plan, err := service.PreviewLegacyCreditAdoption(ctx, request)
		if err != nil {
			return err
		}
		return output.Encode(plan)
	}
	var ready bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='migrations/035_legacy_credit_adoptions.sql')`).Scan(&ready); err != nil || !ready {
		return errors.New("production adoption requires migration 035")
	}
	if *mode == "verify" {
		result, err := service.VerifyLegacyCreditAdoption(ctx, request, approved.Digest)
		if err != nil {
			return err
		}
		return output.Encode(result)
	}
	result, err := service.ApplyLegacyCreditAdoption(ctx, request, approved.Digest)
	if err != nil {
		return err
	}
	return output.Encode(result)
}
