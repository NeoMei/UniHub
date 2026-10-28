package platformadmin

// Overview timeseries gives administrators trend and risk visibility without
// direct database access. Every query below is a fixed, parameterized,
// half-open UTC time window; no user input ever enters the SQL text.
import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

const (
	trendDays        = 14
	modelFailureDays = 7
	maxFailedModels  = 5
)

type TrendDay struct {
	Date        string `json:"date"`
	NewUsers    int64  `json:"new_users"`
	Calls       int64  `json:"calls"`
	FailedCalls int64  `json:"failed_calls"`
}

type FailedModel struct {
	Model       string  `json:"model"`
	Calls       int64   `json:"calls"`
	FailedCalls int64   `json:"failed_calls"`
	FailureRate float64 `json:"failure_rate"`
}

type OperationTimeseries struct {
	AsOf            time.Time     `json:"as_of"`
	Timezone        string        `json:"timezone"`
	FinanceScope    bool          `json:"finance_scope"`
	Days            []TrendDay    `json:"days"`
	PendingRefunds  int64         `json:"pending_refunds"`
	TopFailedModels []FailedModel `json:"top_failed_models"`
}

func dayStartUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// trendBuckets returns trendDays consecutive UTC day buckets ending on the
// current day; index 0 is the oldest day.
func trendBuckets(now time.Time) []TrendDay {
	first := dayStartUTC(now).AddDate(0, 0, -(trendDays - 1))
	days := make([]TrendDay, trendDays)
	for i := range days {
		days[i] = TrendDay{Date: first.AddDate(0, 0, i).Format("2006-01-02")}
	}
	return days
}

// dayWindow is the exclusive half-open window covering the trend buckets.
func dayWindow(now time.Time) (from, to time.Time) {
	today := dayStartUTC(now)
	return today.AddDate(0, 0, -(trendDays - 1)), today.AddDate(0, 0, 1)
}

// rankFailedModels keeps models with at least one failed call, orders them by
// failure rate (ties broken by call volume then name) and caps the result.
// Rates round to 4 decimals so 1/3 style ratios render deterministically.
func rankFailedModels(rows []FailedModel) []FailedModel {
	out := []FailedModel{}
	for _, r := range rows {
		if r.Model == "" || r.Calls <= 0 || r.FailedCalls <= 0 {
			continue
		}
		r.FailureRate = math.Round(float64(r.FailedCalls)/float64(r.Calls)*10000) / 10000
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FailureRate != out[j].FailureRate {
			return out[i].FailureRate > out[j].FailureRate
		}
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Model < out[j].Model
	})
	if len(out) > maxFailedModels {
		out = out[:maxFailedModels]
	}
	return out
}

// Mirrors billing/query.go callStatusJoin: an unresolved billing command forces
// the effective call status to UNKNOWN so in-flight calls are never miscounted.
const operationCallStatusJoin = " LEFT JOIN LATERAL (SELECT 'UNKNOWN'::text AS status FROM billing_commands bc WHERE bc.account_id=c.account_id AND bc.status='UNKNOWN' AND bc.kind IN ('reserve','topup','settle','release') AND bc.effect#>>'{call,call_id}'=c.id LIMIT 1) unc ON true"

// Failed platform calls are terminal REJECTED/CANCELED outcomes, matching the
// rejected bucket of the billing call report. UNKNOWN outcomes stay excluded.
const operationFailedStatuses = "COALESCE(unc.status,c.snapshot->>'status','UNKNOWN') IN ('REJECTED','CANCELED')"

func scanDayCounts(ctx context.Context, tx pgx.Tx, sql string, from, to time.Time) (map[string][2]int64, error) {
	counts := map[string][2]int64{}
	rows, e := tx.Query(ctx, sql, from, to)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var total, failed int64
		if e = rows.Scan(&day, &total, &failed); e != nil {
			return nil, e
		}
		counts[day.Format("2006-01-02")] = [2]int64{total, failed}
	}
	return counts, rows.Err()
}

// OperationTimeseries aggregates the admin trend window in one read-only
// snapshot. Call, refund and model risk data follow the overview convention
// and require finance:read in addition to the endpoint's system:read gate.
func (s *Store) OperationTimeseries(ctx context.Context, actor string) (OperationTimeseries, error) {
	out := OperationTimeseries{Timezone: "UTC", TopFailedModels: []FailedModel{}}
	if e := s.Require(ctx, actor, "system:read"); e != nil {
		return out, e
	}
	principal, e := s.Principal(ctx, actor)
	if e != nil {
		return out, e
	}
	if !principal.Enabled {
		return out, accounts.ErrForbidden
	}
	finance := false
	for _, permission := range principal.Permissions {
		if permission == "finance:read" {
			finance = true
		}
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer database.Rollback(tx)
	now := time.Now().UTC()
	out.AsOf = now
	out.Days = trendBuckets(now)
	out.FinanceScope = finance
	from, to := dayWindow(now)
	users, e := scanDayCounts(ctx, tx, "SELECT (created_at AT TIME ZONE 'UTC')::date,count(*),0::bigint FROM users WHERE created_at>=$1 AND created_at<$2 GROUP BY 1", from, to)
	if e != nil {
		return out, e
	}
	for i := range out.Days {
		out.Days[i].NewUsers = users[out.Days[i].Date][0]
	}
	if finance {
		calls, e := scanDayCounts(ctx, tx, "SELECT (c.created_at AT TIME ZONE 'UTC')::date,count(*),count(*) FILTER (WHERE "+operationFailedStatuses+") FROM billing_calls c"+operationCallStatusJoin+" WHERE c.created_at>=$1 AND c.created_at<$2 GROUP BY 1", from, to)
		if e != nil {
			return out, e
		}
		modelFrom := to.AddDate(0, 0, -modelFailureDays)
		modelRows := []FailedModel{}
		rows, e := tx.Query(ctx, "SELECT c.snapshot#>>'{rate,model}',count(*),count(*) FILTER (WHERE "+operationFailedStatuses+") FROM billing_calls c"+operationCallStatusJoin+" WHERE c.created_at>=$1 AND c.created_at<$2 AND COALESCE(c.snapshot#>>'{rate,model}','')<>'' GROUP BY 1", modelFrom, to)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var m FailedModel
			if e = rows.Scan(&m.Model, &m.Calls, &m.FailedCalls); e != nil {
				break
			}
			modelRows = append(modelRows, m)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return out, e
		}
		var refunds int64
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM billing_orders WHERE snapshot->>'refund_status'='MANUAL_REVIEW'").Scan(&refunds); e != nil {
			return out, e
		}
		for i := range out.Days {
			out.Days[i].Calls = calls[out.Days[i].Date][0]
			out.Days[i].FailedCalls = calls[out.Days[i].Date][1]
		}
		out.PendingRefunds = refunds
		out.TopFailedModels = rankFailedModels(modelRows)
	}
	return out, tx.Commit(ctx)
}
