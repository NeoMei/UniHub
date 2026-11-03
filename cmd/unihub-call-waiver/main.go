// unihub-call-waiver executes one explicitly approved operator case. It does not
// start the server, migrate the application, seed products, or grant authority.
// The root operator must preflight here, stage the exact same approval in the
// gateway, apply here, then finalize there. This process has no gateway write
// access and therefore cannot independently prove the gateway stage.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"os"
	"syscall"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/platformadmin"
)

func main() {
	if e := run(os.Args[1:], os.Getenv, os.Geteuid()); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

func readPrivateApproval(name string) ([]byte, error) {
	f, e := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, errors.New("approval must be an existing nonsymlink 0600 file")
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 {
		return nil, errors.New("approval must be a regular 0600 file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return nil, errors.New("approval exceeds the 4 MiB limit or is unreadable")
	}
	return raw, nil
}
func run(args []string, getenv func(string) string, euid int) error {
	if euid != 0 {
		return errors.New("the waiver command requires local root")
	}
	fs := flag.NewFlagSet("unihub-call-waiver", flag.ContinueOnError)
	file := fs.String("approval", "", "private approval JSON file")
	digest := fs.String("approval-sha256", "", "independently approved file SHA-256")
	dryRun := fs.Bool("dry-run", false, "check current authority, exact central row and hold without changes")
	apply := fs.Bool("apply", false, "accept and execute exactly this customer waiver")
	install := fs.Bool("install-schema", false, "install only migration 023 evidence schema")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || (*apply && *install) || (*dryRun && (*apply || *install)) {
		return errors.New("select one of --apply, --dry-run, or --install-schema")
	}
	raw, e := readPrivateApproval(*file)
	if e != nil {
		return e
	}
	approval, e := billing.ParsePlatformFaultWaiverApproval(raw, *digest)
	if e != nil {
		return errors.New("approval schema, scope, evidence, or SHA-256 is invalid")
	}
	operator := getenv("UNIHUB_WAIVER_OPERATOR_ID")
	if operator == "" || operator != approval.OperatorID {
		return errors.New("UNIHUB_WAIVER_OPERATOR_ID must match the approved operator")
	}
	if !*apply && !*install && !*dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"approval_valid": true, "approval_sha256": *digest, "case_id": approval.CaseID, "provider_state": approval.ProviderState, "action": "validation_only"})
	}
	if getenv("DATABASE_URL") == "" {
		return errors.New("DATABASE_URL is required")
	}
	if (*apply || *dryRun) && (getenv("LEDGER_URL") == "" || getenv("LEDGER_NAME") == "") {
		return errors.New("LEDGER_URL and LEDGER_NAME are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, getenv("DATABASE_URL"))
	if e != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	if e = pool.Ping(ctx); e != nil {
		return errors.New("database unavailable")
	}
	authority := platformadmin.New(pool, nil)
	if e = authority.Require(ctx, operator, "finance:write"); e != nil {
		return e
	}
	if *install {
		if e = database.InstallPlatformFaultWaiverSchema(ctx, pool); e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"schema": "023_platform_fault_waivers", "status": "installed"})
	}
	l := ledger.NewFormance(getenv("LEDGER_URL"), getenv("LEDGER_NAME"), &http.Client{Timeout: 10 * time.Second})
	s := billing.New(pool, accounts.New(pool), l)
	if *dryRun {
		call, e := s.PreviewAdminPlatformFaultWaiver(ctx, authority, operator, raw, *digest)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"case_id": approval.CaseID, "call_id": call.CallID, "status": call.Status, "held": call.Held, "asset": call.ChargeAsset, "validated_scope": true, "provider_state": approval.ProviderState, "approval_sha256": *digest, "action": "dry_run"})
	}
	call, e := s.AdminPlatformFaultWaiver(ctx, authority, operator, raw, *digest)
	if e != nil {
		return e
	}
	var commandID, commandStatus, transactionID string
	var originalCommands json.RawMessage
	if e = pool.QueryRow(ctx, `SELECT w.release_command_id,c.status,COALESCE(c.ledger_transaction_id,''),w.original_commands FROM billing_platform_fault_waivers w JOIN billing_commands c ON c.id=w.release_command_id WHERE w.case_id=$1 AND w.call_id=$2 AND w.operator_id=$3 AND w.approval_sha256=$4`, approval.CaseID, approval.CallID, operator, *digest).Scan(&commandID, &commandStatus, &transactionID, &originalCommands); e != nil {
		return e
	}
	if call.Status != "RELEASED" || call.Charged != "0" || commandStatus != "APPLIED" || transactionID == "" {
		return errors.New("waiver remains unresolved; inspect its durable release command")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"case_id": approval.CaseID, "call_id": call.CallID, "status": call.Status, "charged": call.Charged, "returned_amount": approval.Held, "asset": call.ChargeAsset, "provider_state": approval.ProviderState, "approval_sha256": *digest, "release_command_id": commandID, "release_command_status": commandStatus, "release_ledger_transaction_id": transactionID, "release_reference": "unihub:billing:" + commandID, "original_hold_commands": originalCommands})
}
