package billing

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/metering"

	"github.com/jackc/pgx/v5"
)

// CallFilter has inclusive From and exclusive To bounds. A zero bound is open;
// unknown historical times are included only when neither time bound is set.
// The HTTP product boundary supplies its verified product and instance; a
// model-pool scope may clear the product, never the instance.
type CallFilter struct {
	InstanceID      string    `json:"instance_id,omitempty"`
	Asset           string    `json:"asset,omitempty"`
	From            time.Time `json:"from,omitempty"`
	To              time.Time `json:"to,omitempty"`
	Model           string    `json:"model,omitempty"`
	OriginProductID string    `json:"origin_product_id,omitempty"`
}
type CallPageRequest struct {
	Limit  int
	Cursor string
}
type QueryScope struct {
	InstanceID      string `json:"instance_id,omitempty"`
	AccountID       string `json:"account_id"`
	OriginProductID string `json:"origin_product_id,omitempty"`
	ActorID         string `json:"actor_id,omitempty"`
	Visibility      string `json:"visibility"` // actor or account; never an implied team total
}

// CallWaiverView separates customer release from unresolved supplier liability.
type CallWaiverView struct {
	CaseID         string `json:"case_id"`
	ProviderState  string `json:"provider_state"`
	CustomerStatus string `json:"customer_status"`
}

// CallView excludes provider costs, credentials, digests and raw product usage.
type CallView struct {
	Source           string          `json:"source"`
	SourceCallID     string          `json:"source_call_id"`
	InstanceID       string          `json:"instance_id"`
	Operation        string          `json:"operation"`
	FundingMode      string          `json:"funding_mode"`
	FundingProductID string          `json:"funding_product_id"`
	Refunded         string          `json:"refunded,omitempty"`
	PriceVersion     string          `json:"price_version,omitempty"`
	PriceOwner       string          `json:"price_owner"`
	UsageStatus      string          `json:"usage_status"`
	Waiver           *CallWaiverView `json:"waiver,omitempty"`
	Rate             *Rate           `json:"rate,omitempty"`
	Scope            QueryScope      `json:"scope"`
	FinancialDetails bool            `json:"financial_details"`
	Asset            string          `json:"asset"`
	CallID           string          `json:"call_id"`
	AccountID        string          `json:"account_id"`
	ActorID          string          `json:"actor_id"`
	OriginProductID  string          `json:"origin_product_id"`
	Model            string          `json:"model"`
	Status           string          `json:"status"`
	CreatedAt        *time.Time      `json:"created_at"`
	TimeSource       string          `json:"time_source"`
	TaskID           string          `json:"task_id,omitempty"`
	Held             string          `json:"held,omitempty"`
	Charged          string          `json:"charged,omitempty"`
	Usage            *Usage          `json:"usage,omitempty"`
	reportID         string
}
type CallPage struct {
	Scope            QueryScope `json:"scope"`
	Filter           CallFilter `json:"filter"`
	FinancialDetails bool       `json:"financial_details"`
	Items            []CallView `json:"items"`
	NextCursor       string     `json:"next_cursor,omitempty"`
	HasMore          bool       `json:"has_more"`
	Total            string     `json:"total"`
	UnknownTime      string     `json:"unknown_time"`
	AsOf             time.Time  `json:"as_of"`
	Accuracy         string     `json:"accuracy"`
	Consistency      string     `json:"consistency"`
}
type NativeUsageTotal struct {
	Operation    string                 `json:"operation"`
	Measurements []metering.Measurement `json:"measurements"`
}

type UsageSummary struct {
	NativeUsage       []NativeUsageTotal `json:"native_usage,omitempty"`
	ChargesByAsset    map[string]string  `json:"charges_by_asset,omitempty"`
	RefundsByAsset    map[string]string  `json:"refunds_by_asset,omitempty"`
	Refunded          string             `json:"refunded,omitempty"`
	Scope             QueryScope         `json:"scope"`
	Filter            CallFilter         `json:"filter"`
	FinancialDetails  bool               `json:"financial_details"`
	CallsTotal        string             `json:"calls_total"`
	SettledCalls      string             `json:"settled_calls"`
	PendingCalls      string             `json:"pending_calls"`
	MissingUsageCalls string             `json:"missing_usage_calls"`
	UnknownCalls      string             `json:"unknown_calls"`
	RejectedCalls     string             `json:"rejected_calls"`
	ReleasedCalls     string             `json:"released_calls"`
	ByStatus          map[string]string  `json:"by_status"`
	Charged           string             `json:"charged,omitempty"`
	Asset             string             `json:"asset"`
	Usage             Usage              `json:"usage"`
	UsageComplete     bool               `json:"usage_complete"`
	UnknownTime       string             `json:"unknown_time"`
	AsOf              time.Time          `json:"as_of"`
	Accuracy          string             `json:"accuracy"`
	Consistency       string             `json:"consistency"`
}

// Process-local signing keeps cursors opaque and prevents callers from changing
// their filter/scope/position. A restart expires traversal; clients restart at
// the first page. This is a live traversal, not a frozen cross-request report.
var usageCursorKey = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("billing: unavailable cursor entropy")
	}
	return b
}()

type usageCursor struct {
	Version int        `json:"v"`
	Binding string     `json:"b"`
	At      *time.Time `json:"at"`
	ID      string     `json:"id"`
	Expires time.Time  `json:"exp"`
	Filter  CallFilter `json:"filter"`
}

func normalizeCallFilter(f CallFilter) (CallFilter, error) {
	if (f.Asset != "" && f.Asset != CreditAsset && f.Asset != CashAsset && f.Asset != RuntimeAsset) || (!f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To)) || len(f.InstanceID) > 128 || strings.TrimSpace(f.InstanceID) != f.InstanceID || len(f.Model) > 256 || len(f.OriginProductID) > 64 || strings.TrimSpace(f.Model) != f.Model || strings.TrimSpace(f.OriginProductID) != f.OriginProductID {
		return f, ErrInvalid
	}
	if !f.From.IsZero() {
		f.From = f.From.UTC()
	}
	if !f.To.IsZero() {
		f.To = f.To.UTC()
	}
	return f, nil
}
func (s *Service) usageScope(ctx context.Context, q accounts.Querier, actor, account string, f CallFilter) (QueryScope, bool, error) {
	role, err := s.accounts.RoleOn(ctx, q, actor, account)
	if err != nil {
		return QueryScope{}, false, err
	}
	scope := QueryScope{InstanceID: f.InstanceID, AccountID: account, OriginProductID: f.OriginProductID, Visibility: "account"}
	finance := role == "owner" || role == "billing_admin"
	if !finance {
		scope.Visibility = "actor"
		scope.ActorID = actor
	}
	return scope, finance, nil
}
func callQueryArgs(scope QueryScope, f CallFilter) []any {
	var from, to *time.Time
	if !f.From.IsZero() {
		from = &f.From
	}
	if !f.To.IsZero() {
		to = &f.To
	}
	return []any{scope.AccountID, scope.OriginProductID, scope.ActorID, from, to, f.Model, f.Asset, f.InstanceID}
}

const callAssetSQL = `COALESCE(NULLIF(c.snapshot->>'charge_asset',''),'CREDIT/6')`

const callBaseWhere = `c.account_id=$1 AND ($2='' OR c.snapshot->>'origin_product_id'=$2) AND ($3='' OR c.snapshot->>'actor_id'=$3) AND ($6='' OR c.snapshot#>>'{rate,model}'=$6) AND ($7='' OR ` + callAssetSQL + `=$7) AND ($8='' OR COALESCE(NULLIF(c.snapshot->>'instance_id',''),'default')=$8)`
const callTimeWhere = ` AND ($4::timestamptz IS NULL OR c.created_at >= $4) AND ($5::timestamptz IS NULL OR c.created_at < $5)`

func cursorBinding(actor string, scope QueryScope, f CallFilter) string {
	return hash(struct {
		Actor  string
		Scope  QueryScope
		Filter CallFilter
	}{actor, scope, f})
}
func encodeUsageCursor(c usageCursor) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, usageCursorKey)
	_, _ = mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func decodeUsageCursor(raw, binding string) (usageCursor, error) {
	c, err := readUsageCursor(raw)
	if err != nil || c.Binding != binding {
		return c, ErrInvalid
	}
	return c, nil
}

// RestoreCallFilter restores signed time bounds for a continuation that omitted
// them. Callers must still pass their trusted product filter to QueryCalls;
// restoring a cursor does not authorize its account, actor or product scope.
func RestoreCallFilter(raw string) (CallFilter, error) {
	c, err := readUsageCursor(raw)
	if err != nil {
		return CallFilter{}, err
	}
	return normalizeCallFilter(c.Filter)
}
func readUsageCursor(raw string) (usageCursor, error) {
	var c usageCursor
	if len(raw) > 4096 {
		return c, ErrInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return c, ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, ErrInvalid
	}
	mac := hmac.New(sha256.New, usageCursorKey)
	_, _ = mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, ErrInvalid
	}
	if json.Unmarshal(b, &c) != nil || c.Version != 1 || (c.ID == "" || len(c.ID) > 256) || !time.Now().Before(c.Expires) {
		return c, ErrInvalid
	}
	return c, nil
}

const callReportStatus = `c.report_status`
const callReportTable = `(` + ReportCallsSQL + `) c `
const callViewColumns = `c.id,c.snapshot,c.created_at,c.time_source,COALESCE(NULLIF(c.snapshot->>'task_id',''),mc.task_id,''),` + callReportStatus
const callViewJoin = ` LEFT JOIN model_credentials mc ON mc.id=c.snapshot->>'credential_id' AND mc.account_id=c.account_id AND mc.actor_id=c.snapshot->>'actor_id' AND mc.origin_product_id=c.snapshot->>'origin_product_id' `

func scanCallView(row pgx.Row, scope QueryScope, finance bool) (CallView, error) {
	var c Call
	var data []byte
	var out CallView
	if err := row.Scan(&out.reportID, &data, &out.CreatedAt, &out.TimeSource, &out.TaskID, &out.Status); err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return out, err
	}
	var metadata struct {
		QueryCallID  string          `json:"query_call_id"`
		Source       string          `json:"source"`
		SourceCallID string          `json:"source_call_id"`
		Operation    string          `json:"operation"`
		Refunded     string          `json:"refunded"`
		PriceVersion string          `json:"price_version"`
		PriceOwner   string          `json:"price_owner"`
		Waiver       *CallWaiverView `json:"waiver"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return out, err
	}
	out.CallID, out.Source, out.SourceCallID = metadata.QueryCallID, metadata.Source, metadata.SourceCallID
	out.InstanceID, out.Operation = c.InstanceID, metadata.Operation
	out.FundingMode, out.FundingProductID = c.FundingMode, c.FundingProductID
	if out.FundingMode == "" {
		if callAsset(c) == CashAsset {
			out.FundingMode = FundingPrepaidCash
		} else {
			out.FundingMode = FundingCredits
		}
	}
	out.PriceOwner, out.Waiver = metadata.PriceOwner, metadata.Waiver
	out.UsageStatus = "unavailable"
	if c.Usage != nil {
		out.UsageStatus = "available"
	}
	out.Scope = scope
	out.FinancialDetails = finance
	out.Asset = callAsset(c)
	if out.Asset != CreditAsset && out.Asset != CashAsset && out.Asset != RuntimeAsset {
		return out, ErrInvalid
	}
	out.AccountID = scope.AccountID
	out.ActorID = c.ActorID
	out.OriginProductID = c.OriginProductID
	out.Model = c.Rate.Model
	out.Usage = c.Usage
	if finance {
		if out.Source == "managed_model" {
			out.Rate = &c.Rate
		}
		out.Refunded, out.PriceVersion = metadata.Refunded, metadata.PriceVersion
		out.Held = c.Held
		out.Charged = c.Charged
	}
	return out, nil
}

// QueryCalls returns exact counts for its own database snapshot. New calls and
// changing statuses may change counts on subsequent pages of this live query.
func (s *Service) QueryCalls(ctx context.Context, actor, account string, f CallFilter, page CallPageRequest) (CallPage, error) {
	var out CallPage
	f, err := normalizeCallFilter(f)
	if err != nil || page.Limit < 1 || page.Limit > 100 {
		return out, ErrInvalid
	}
	out.Filter = f
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer database.Rollback(tx)
	out.Scope, out.FinancialDetails, err = s.usageScope(ctx, tx, actor, account, f)
	if err != nil {
		return out, err
	}
	args := callQueryArgs(out.Scope, f)
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM `+callReportTable+` WHERE `+callBaseWhere+callTimeWhere, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM `+callReportTable+` WHERE `+callBaseWhere+` AND $4::timestamptz IS NOT DISTINCT FROM $4 AND $5::timestamptz IS NOT DISTINCT FROM $5 AND c.created_at IS NULL`, args...).Scan(&out.UnknownTime); err != nil {
		return out, err
	}
	query := `SELECT ` + callViewColumns + ` FROM ` + callReportTable + callViewJoin + ` WHERE ` + callBaseWhere + callTimeWhere
	binding := cursorBinding(actor, out.Scope, f)
	if page.Cursor != "" {
		cursor, e := decodeUsageCursor(page.Cursor, binding)
		if e != nil {
			return out, e
		}
		if cursor.At == nil {
			query += ` AND c.created_at IS NULL AND c.id < $9`
			args = append(args, cursor.ID)
		} else {
			query += ` AND (c.created_at < $9 OR (c.created_at = $9 AND c.id < $10) OR c.created_at IS NULL)`
			args = append(args, cursor.At, cursor.ID)
		}
	}
	args = append(args, page.Limit+1)
	query += fmt.Sprintf(` ORDER BY c.created_at DESC NULLS LAST,c.id DESC LIMIT $%d`, len(args))
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return out, err
	}
	out.Items = []CallView{}
	for rows.Next() {
		view, e := scanCallView(rows, out.Scope, out.FinancialDetails)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Items = append(out.Items, view)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Items) > page.Limit {
		out.HasMore = true
		out.Items = out.Items[:page.Limit]
		last := out.Items[len(out.Items)-1]
		out.NextCursor, err = encodeUsageCursor(usageCursor{Version: 1, Binding: binding, At: last.CreatedAt, ID: last.reportID, Expires: time.Now().Add(time.Hour), Filter: f})
		if err != nil {
			return out, err
		}
	}
	out.AsOf = time.Now().UTC()
	out.Accuracy = "exact_for_query_snapshot"
	out.Consistency = "live"
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) QueryCall(ctx context.Context, actor, account, callID, origin string, instance ...string) (CallView, error) {
	var out CallView
	if !keyOK(callID) {
		return out, ErrInvalid
	}
	if len(instance) > 1 {
		return out, ErrInvalid
	}
	filter := CallFilter{OriginProductID: origin}
	if len(instance) == 1 {
		filter.InstanceID = instance[0]
	}
	f, err := normalizeCallFilter(filter)
	if err != nil {
		return out, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer database.Rollback(tx)
	scope, finance, err := s.usageScope(ctx, tx, actor, account, f)
	if err != nil {
		return out, err
	}
	errArgs := callQueryArgs(scope, f)
	errArgs = append(errArgs, callID)
	rows, err := tx.Query(ctx, `SELECT `+callViewColumns+` FROM `+callReportTable+callViewJoin+` WHERE `+callBaseWhere+callTimeWhere+` AND c.snapshot->>'query_call_id'=$9 LIMIT 2`, errArgs...)
	if err != nil {
		return out, err
	}
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			rows.Close()
			return out, ErrConflict
		}
		out, err = scanCallView(rows, scope, finance)
		if err != nil {
			rows.Close()
			return out, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if count == 0 {
		return out, accounts.ErrNotFound
	}
	return out, tx.Commit(ctx)
}

// PostgreSQL numeric sums do not overflow int64 across many valid per-call
// int64 counters. No float conversion, rounding, held amounts, or estimated rows.
const validLegacyUsageSQL = `(c.snapshot->'usage' IS NOT NULL AND c.snapshot->'usage'<>'null'::jsonb AND (c.snapshot#>>'{usage,input}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,output}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,cache_read}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,cache_write}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,reasoning}') ~ '^(0|[1-9][0-9]*)$')`

const nativeUsageSQL = `(c.snapshot#>>'{rate,semantics}'='metered_v2' AND c.snapshot#>>'{usage,metered,state}'='confirmed')`
const validUsageSQL = `(` + validLegacyUsageSQL + ` OR ` + nativeUsageSQL + `)`

func (s *Service) QueryUsageSummary(ctx context.Context, actor, account string, f CallFilter) (UsageSummary, error) {
	out := UsageSummary{Asset: CreditAsset, ByStatus: map[string]string{}}
	f, err := normalizeCallFilter(f)
	if err != nil {
		return out, err
	}
	out.Filter = f
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer database.Rollback(tx)
	out.Scope, out.FinancialDetails, err = s.usageScope(ctx, tx, actor, account, f)
	if err != nil {
		return out, err
	}
	args := callQueryArgs(out.Scope, f)
	selects := []string{`count(*)::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='SETTLED')::text`, `count(*) FILTER(WHERE ` + callReportStatus + ` IN ('PENDING','RESERVED'))::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='MISSING_USAGE')::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='UNKNOWN')::text`, `count(*) FILTER(WHERE ` + callReportStatus + ` IN ('REJECTED','CANCELED'))::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='RELEASED')::text`}
	for _, name := range []string{"input", "output", "cache_read", "cache_write", "reasoning"} {
		selects = append(selects, `COALESCE(sum(CASE WHEN `+callReportStatus+`='SETTLED' AND `+validLegacyUsageSQL+` THEN (c.snapshot#>>'{usage,`+name+`}')::numeric ELSE 0 END),0)::text`)
	}
	selects = append(selects, `COALESCE(bool_and(`+callReportStatus+` IN ('SETTLED','RELEASED','REJECTED','CANCELED') AND (`+callReportStatus+`<>'SETTLED' OR COALESCE(`+validUsageSQL+`,false))),true)`, `count(*) FILTER(WHERE `+callReportStatus+`='SETTLED' AND (NOT COALESCE(c.snapshot->>'charged' ~ '^(0|[1-9][0-9]*)$',false) OR (c.snapshot->'usage' IS NOT NULL AND c.snapshot->'usage'<>'null'::jsonb AND NOT COALESCE(`+validUsageSQL+`,false))))`)
	var invalid int64
	err = tx.QueryRow(ctx, `SELECT `+strings.Join(selects, ",")+` FROM `+callReportTable+` WHERE `+callBaseWhere+callTimeWhere, args...).Scan(&out.CallsTotal, &out.SettledCalls, &out.PendingCalls, &out.MissingUsageCalls, &out.UnknownCalls, &out.RejectedCalls, &out.ReleasedCalls, &out.Usage.Input, &out.Usage.Output, &out.Usage.CacheRead, &out.Usage.CacheWrite, &out.Usage.Reasoning, &out.UsageComplete, &invalid)
	if err != nil {
		return out, err
	}
	if invalid > 0 {
		return out, fmt.Errorf("billing: invalid stored call counters")
	}
	// Aggregate native quantities independently of money and legacy counters.
	nativeRows, nativeErr := tx.Query(ctx, `SELECT c.snapshot FROM `+callReportTable+` WHERE `+callBaseWhere+callTimeWhere+` AND `+callReportStatus+`='SETTLED' AND c.snapshot#>>'{rate,semantics}'='metered_v2'`, args...)
	if nativeErr != nil {
		return out, nativeErr
	}
	type dimension struct{ operation, key, unit, provider string }
	totals := map[dimension]*big.Rat{}
	for nativeRows.Next() {
		var data []byte
		var call Call
		if nativeErr = nativeRows.Scan(&data); nativeErr != nil {
			break
		}
		if nativeErr = json.Unmarshal(data, &call); nativeErr != nil {
			break
		}
		if call.Usage == nil {
			nativeErr = ErrInvalid
			break
		}
		if _, nativeErr = CalculateCharge(call.Rate, *call.Usage); nativeErr != nil {
			break
		}
		for _, measurement := range call.Usage.Metered.Measurements {
			d := dimension{call.Usage.Metered.Operation, measurement.Key, measurement.Unit, measurement.ProviderUnit}
			n, _ := new(big.Int).SetString(measurement.Quantity.Value, 10)
			q := new(big.Rat).SetFrac(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(measurement.Quantity.Scale)), nil))
			if totals[d] == nil {
				totals[d] = new(big.Rat)
			}
			totals[d].Add(totals[d], q)
		}
	}
	if nativeErr == nil {
		nativeErr = nativeRows.Err()
	}
	nativeRows.Close()
	if nativeErr != nil {
		return out, fmt.Errorf("billing: invalid stored native usage: %w", nativeErr)
	}
	dimensions := make([]dimension, 0, len(totals))
	for d := range totals {
		dimensions = append(dimensions, d)
	}
	sort.Slice(dimensions, func(i, j int) bool {
		a, b := dimensions[i], dimensions[j]
		if a.operation != b.operation {
			return a.operation < b.operation
		}
		if a.key != b.key {
			return a.key < b.key
		}
		if a.unit != b.unit {
			return a.unit < b.unit
		}
		return a.provider < b.provider
	})
	for _, d := range dimensions {
		scale := 9
		n := new(big.Int).Mul(totals[d].Num(), big.NewInt(1000000000))
		n.Quo(n, totals[d].Denom())
		for scale > 0 && new(big.Int).Mod(n, big.NewInt(10)).Sign() == 0 {
			n.Quo(n, big.NewInt(10))
			scale--
		}
		if len(out.NativeUsage) == 0 || out.NativeUsage[len(out.NativeUsage)-1].Operation != d.operation {
			out.NativeUsage = append(out.NativeUsage, NativeUsageTotal{Operation: d.operation, Measurements: []metering.Measurement{}})
		}
		index := len(out.NativeUsage) - 1
		out.NativeUsage[index].Measurements = append(out.NativeUsage[index].Measurements, metering.Measurement{Key: d.key, Unit: d.unit, ProviderUnit: d.provider, Quantity: metering.Quantity{Value: n.String(), Scale: scale}})
	}

	// Keep confirmed gross charges even when a later command is UNKNOWN.
	// Pending effects never replace confirmed refunds, and assets stay separate.
	assetRows, e := tx.Query(ctx, `SELECT `+callAssetSQL+`,COALESCE(sum(CASE WHEN c.snapshot->>'status'='SETTLED' AND c.snapshot->>'charged' ~ '^(0|[1-9][0-9]*)$' THEN (c.snapshot->>'charged')::numeric ELSE 0 END),0)::text,COALESCE(sum(CASE WHEN c.snapshot->>'refunded' ~ '^(0|[1-9][0-9]*)$' THEN (c.snapshot->>'refunded')::numeric ELSE 0 END),0)::text FROM `+callReportTable+` WHERE `+callBaseWhere+callTimeWhere+` GROUP BY `+callAssetSQL, args...)
	if e != nil {
		return out, e
	}
	amounts := map[string]string{}
	refunds := map[string]string{}
	for assetRows.Next() {
		var asset, amount, refunded string
		if e = assetRows.Scan(&asset, &amount, &refunded); e != nil {
			assetRows.Close()
			return out, e
		}
		if asset != CreditAsset && asset != CashAsset && asset != RuntimeAsset {
			assetRows.Close()
			return out, ErrInvalid
		}
		amounts[asset] = amount
		refunds[asset] = refunded
	}
	e = assetRows.Err()
	assetRows.Close()
	if e != nil {
		return out, e
	}
	if len(amounts) == 0 {
		asset := f.Asset
		if asset == "" {
			asset = CreditAsset
		}
		amounts[asset] = "0"
		refunds[asset] = "0"
	}
	if len(amounts) == 1 {
		for asset, amount := range amounts {
			out.Asset = asset
			if out.FinancialDetails {
				out.Charged = amount
				out.Refunded = refunds[asset]
			}
		}
	} else {
		out.Asset = "mixed"
	}
	if out.FinancialDetails {
		out.ChargesByAsset = amounts
		out.RefundsByAsset = refunds
	}
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM `+callReportTable+` WHERE `+callBaseWhere+` AND $4::timestamptz IS NOT DISTINCT FROM $4 AND $5::timestamptz IS NOT DISTINCT FROM $5 AND c.created_at IS NULL`, args...).Scan(&out.UnknownTime); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT `+callReportStatus+`,count(*)::text FROM `+callReportTable+` WHERE `+callBaseWhere+callTimeWhere+` GROUP BY `+callReportStatus, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var status, count string
		if err = rows.Scan(&status, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.ByStatus[status] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	out.AsOf = time.Now().UTC()
	out.Accuracy = "exact_for_query_snapshot"
	out.Consistency = "live"
	return out, tx.Commit(ctx)
}
