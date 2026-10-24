package billing

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"

	"github.com/jackc/pgx/v5"
)

// CallFilter has inclusive From and exclusive To bounds. A zero bound is open;
// unknown historical times are included only when neither time bound is set.
// The HTTP product boundary must supply its verified product as OriginProductID.
type CallFilter struct {
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
	AccountID       string `json:"account_id"`
	OriginProductID string `json:"origin_product_id,omitempty"`
	ActorID         string `json:"actor_id,omitempty"`
	Visibility      string `json:"visibility"` // actor or account; never an implied team total
}

// CallView intentionally has no provider-cost, platform-difference, digest,
// credential, allocation or request/response-content fields.
type CallView struct {
	Scope            QueryScope `json:"scope"`
	FinancialDetails bool       `json:"financial_details"`
	Asset            string     `json:"asset"`
	CallID           string     `json:"call_id"`
	AccountID        string     `json:"account_id"`
	ActorID          string     `json:"actor_id"`
	OriginProductID  string     `json:"origin_product_id"`
	Model            string     `json:"model"`
	Status           string     `json:"status"`
	CreatedAt        *time.Time `json:"created_at"`
	TimeSource       string     `json:"time_source"`
	TaskID           string     `json:"task_id,omitempty"`
	Held             string     `json:"held,omitempty"`
	Charged          string     `json:"charged,omitempty"`
	Usage            *Usage     `json:"usage,omitempty"`
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
type UsageSummary struct {
	Scope             QueryScope        `json:"scope"`
	Filter            CallFilter        `json:"filter"`
	FinancialDetails  bool              `json:"financial_details"`
	CallsTotal        string            `json:"calls_total"`
	SettledCalls      string            `json:"settled_calls"`
	PendingCalls      string            `json:"pending_calls"`
	MissingUsageCalls string            `json:"missing_usage_calls"`
	UnknownCalls      string            `json:"unknown_calls"`
	RejectedCalls     string            `json:"rejected_calls"`
	ReleasedCalls     string            `json:"released_calls"`
	ByStatus          map[string]string `json:"by_status"`
	Charged           string            `json:"charged,omitempty"`
	Asset             string            `json:"asset"`
	Usage             Usage             `json:"usage"`
	UsageComplete     bool              `json:"usage_complete"`
	UnknownTime       string            `json:"unknown_time"`
	AsOf              time.Time         `json:"as_of"`
	Accuracy          string            `json:"accuracy"`
	Consistency       string            `json:"consistency"`
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
	if (!f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To)) || len(f.Model) > 256 || len(f.OriginProductID) > 64 || strings.TrimSpace(f.Model) != f.Model || strings.TrimSpace(f.OriginProductID) != f.OriginProductID {
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
	scope := QueryScope{AccountID: account, OriginProductID: f.OriginProductID, Visibility: "account"}
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
	return []any{scope.AccountID, scope.OriginProductID, scope.ActorID, from, to, f.Model}
}

const callBaseWhere = `c.account_id=$1 AND ($2='' OR c.snapshot->>'origin_product_id'=$2) AND ($3='' OR c.snapshot->>'actor_id'=$3) AND ($6='' OR c.snapshot#>>'{rate,model}'=$6)`
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
	if json.Unmarshal(b, &c) != nil || c.Version != 1 || !keyOK(c.ID) || !time.Now().Before(c.Expires) {
		return c, ErrInvalid
	}
	return c, nil
}

const callReportStatus = `COALESCE(uncertain.status,c.snapshot->>'status','UNKNOWN')`
const callStatusJoin = ` LEFT JOIN LATERAL (SELECT 'UNKNOWN'::text AS status FROM billing_commands bc WHERE bc.account_id=c.account_id AND bc.status='UNKNOWN' AND bc.kind IN ('reserve','topup','settle','release') AND bc.effect#>>'{call,call_id}'=c.id LIMIT 1) uncertain ON true `
const callViewColumns = `c.id,c.snapshot,c.created_at,c.time_source,COALESCE(mc.task_id,''),` + callReportStatus
const callViewJoin = ` LEFT JOIN model_credentials mc ON mc.id=c.snapshot->>'credential_id' AND mc.account_id=c.account_id AND mc.actor_id=c.snapshot->>'actor_id' AND mc.origin_product_id=c.snapshot->>'origin_product_id' `

func scanCallView(row pgx.Row, scope QueryScope, finance bool) (CallView, error) {
	var c Call
	var data []byte
	var out CallView
	if err := row.Scan(&out.CallID, &data, &out.CreatedAt, &out.TimeSource, &out.TaskID, &out.Status); err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return out, err
	}
	out.Scope = scope
	out.FinancialDetails = finance
	out.Asset = CreditAsset
	out.AccountID = scope.AccountID
	out.ActorID = c.ActorID
	out.OriginProductID = c.OriginProductID
	out.Model = c.Rate.Model
	out.Usage = c.Usage
	if finance {
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
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM billing_calls c WHERE `+callBaseWhere+callTimeWhere, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM billing_calls c WHERE `+callBaseWhere+` AND $4::timestamptz IS NOT DISTINCT FROM $4 AND $5::timestamptz IS NOT DISTINCT FROM $5 AND c.created_at IS NULL`, args...).Scan(&out.UnknownTime); err != nil {
		return out, err
	}
	query := `SELECT ` + callViewColumns + ` FROM billing_calls c ` + callViewJoin + callStatusJoin + ` WHERE ` + callBaseWhere + callTimeWhere
	binding := cursorBinding(actor, out.Scope, f)
	if page.Cursor != "" {
		cursor, e := decodeUsageCursor(page.Cursor, binding)
		if e != nil {
			return out, e
		}
		if cursor.At == nil {
			query += ` AND c.created_at IS NULL AND c.id < $7`
			args = append(args, cursor.ID)
		} else {
			query += ` AND (c.created_at < $7 OR (c.created_at = $7 AND c.id < $8) OR c.created_at IS NULL)`
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
		out.NextCursor, err = encodeUsageCursor(usageCursor{Version: 1, Binding: binding, At: last.CreatedAt, ID: last.CallID, Expires: time.Now().Add(time.Hour), Filter: f})
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

func (s *Service) QueryCall(ctx context.Context, actor, account, callID, origin string) (CallView, error) {
	var out CallView
	if !keyOK(callID) {
		return out, ErrInvalid
	}
	f, err := normalizeCallFilter(CallFilter{OriginProductID: origin})
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
	out, err = scanCallView(tx.QueryRow(ctx, `SELECT `+callViewColumns+` FROM billing_calls c `+callViewJoin+callStatusJoin+` WHERE `+callBaseWhere+callTimeWhere+` AND c.id=$7`, errArgs...), scope, finance)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, accounts.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// PostgreSQL numeric sums do not overflow int64 across many valid per-call
// int64 counters. No float conversion, rounding, held amounts, or estimated rows.
const validUsageSQL = `(c.snapshot->'usage' IS NOT NULL AND c.snapshot->'usage'<>'null'::jsonb AND (c.snapshot#>>'{usage,input}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,output}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,cache_read}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,cache_write}') ~ '^(0|[1-9][0-9]*)$' AND (c.snapshot#>>'{usage,reasoning}') ~ '^(0|[1-9][0-9]*)$')`

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
	selects := []string{`count(*)::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='SETTLED')::text`, `count(*) FILTER(WHERE ` + callReportStatus + ` IN ('PENDING','RESERVED'))::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='MISSING_USAGE')::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='UNKNOWN')::text`, `count(*) FILTER(WHERE ` + callReportStatus + ` IN ('REJECTED','CANCELED'))::text`, `count(*) FILTER(WHERE ` + callReportStatus + `='RELEASED')::text`, `COALESCE(sum(CASE WHEN ` + callReportStatus + `='SETTLED' AND c.snapshot->>'charged' ~ '^(0|[1-9][0-9]*)$' THEN (c.snapshot->>'charged')::numeric ELSE 0 END),0)::text`}
	for _, name := range []string{"input", "output", "cache_read", "cache_write", "reasoning"} {
		selects = append(selects, `COALESCE(sum(CASE WHEN `+callReportStatus+`='SETTLED' AND `+validUsageSQL+` THEN (c.snapshot#>>'{usage,`+name+`}')::numeric ELSE 0 END),0)::text`)
	}
	selects = append(selects, `COALESCE(bool_and(`+callReportStatus+` IN ('SETTLED','RELEASED','REJECTED','CANCELED') AND (`+callReportStatus+`<>'SETTLED' OR `+validUsageSQL+`)),true)`, `count(*) FILTER(WHERE `+callReportStatus+`='SETTLED' AND (NOT COALESCE(c.snapshot->>'charged' ~ '^(0|[1-9][0-9]*)$',false) OR (c.snapshot->'usage' IS NOT NULL AND c.snapshot->'usage'<>'null'::jsonb AND NOT COALESCE(`+validUsageSQL+`,false))))`)
	var charged string
	var invalid int64
	err = tx.QueryRow(ctx, `SELECT `+strings.Join(selects, ",")+` FROM billing_calls c `+callStatusJoin+` WHERE `+callBaseWhere+callTimeWhere, args...).Scan(&out.CallsTotal, &out.SettledCalls, &out.PendingCalls, &out.MissingUsageCalls, &out.UnknownCalls, &out.RejectedCalls, &out.ReleasedCalls, &charged, &out.Usage.Input, &out.Usage.Output, &out.Usage.CacheRead, &out.Usage.CacheWrite, &out.Usage.Reasoning, &out.UsageComplete, &invalid)
	if err != nil {
		return out, err
	}
	if invalid > 0 {
		return out, fmt.Errorf("billing: invalid stored call counters")
	}
	if out.FinancialDetails {
		out.Charged = charged
	}
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM billing_calls c WHERE `+callBaseWhere+` AND $4::timestamptz IS NOT DISTINCT FROM $4 AND $5::timestamptz IS NOT DISTINCT FROM $5 AND c.created_at IS NULL`, args...).Scan(&out.UnknownTime); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT `+callReportStatus+`,count(*)::text FROM billing_calls c `+callStatusJoin+` WHERE `+callBaseWhere+callTimeWhere+` GROUP BY `+callReportStatus, args...)
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
