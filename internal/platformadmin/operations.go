package platformadmin

// Operational queries deliberately select a safe projection before filtering or
// exporting. Raw snapshots, credentials and provider content never enter a page.
import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
)

type OperationsOptions struct {
	ListOptions
	AccountID string
	ProductID string
	UserID    string
	From      time.Time
	To        time.Time
}

var operationEpoch = time.Unix(0, 0).UTC()
var operationCursorKey = func() []byte {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return b
}()

type operationCursor struct {
	Binding string
	At      time.Time
	ID      string
	AsOf    time.Time
	Expires time.Time
}

func operationBinding(actor, resource string, o OperationsOptions) string {
	o.Cursor = ""
	b, _ := json.Marshal([]any{actor, resource, o})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func encodeOperationCursor(c operationCursor) (string, error) {
	b, e := json.Marshal(c)
	m := hmac.New(sha256.New, operationCursorKey)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), e
}
func decodeOperationCursor(raw, binding string) (operationCursor, error) {
	var c operationCursor
	p := strings.Split(raw, ".")
	if len(raw) > 4096 || len(p) != 2 {
		return c, billing.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil {
		return c, billing.ErrInvalid
	}
	sig, e := base64.RawURLEncoding.DecodeString(p[1])
	m := hmac.New(sha256.New, operationCursorKey)
	m.Write(b)
	if e != nil || !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(b, &c) != nil || c.Binding != binding || c.ID == "" || time.Now().After(c.Expires) {
		return c, billing.ErrInvalid
	}
	return c, nil
}

type operationProjection struct{ Permission, SQL string }

// Every projection has id,at,account_id,product_id,user_id,status,data columns.
// NULL historical times sort at epoch solely for traversal; data preserves null.
func operationProjectionFor(resource string) (operationProjection, error) {
	p := operationProjection{Permission: "finance:read"}
	switch resource {
	case "users":
		p.Permission = "users:read"
		p.SQL = `SELECT id,created_at AS at,''::text AS account_id,''::text AS product_id,id AS user_id,CASE WHEN active THEN 'enabled' ELSE 'disabled' END AS status,jsonb_build_object('id',id,'name',name,'email',email,'active',active,'version',status_version,'created_at',created_at,'last_authenticated_at',last_authenticated_at) AS data FROM users`
	case "accounts":
		p.Permission = "accounts:read"
		p.SQL = `SELECT a.id,a.created_at AS at,a.id AS account_id,''::text AS product_id,COALESCE(a.personal_user_id,'') AS user_id,a.kind AS status,jsonb_build_object('id',a.id,'kind',a.kind,'name',a.name,'personal_user_id',a.personal_user_id,'created_at',a.created_at,'members',(SELECT COALESCE(jsonb_agg(jsonb_build_object('user_id',m.user_id,'name',u.name,'role',m.role,'active',u.active) ORDER BY m.user_id),'[]'::jsonb) FROM account_members m JOIN users u ON u.id=m.user_id WHERE m.account_id=a.id),'management_recovery_required',a.kind='team' AND NOT EXISTS(SELECT 1 FROM account_members m JOIN users u ON u.id=m.user_id WHERE m.account_id=a.id AND m.role='owner' AND u.active)) AS data FROM accounts a`
	case "products":
		p.Permission = "products:read"
		p.SQL = `SELECT id,'epoch'::timestamptz AS at,''::text AS account_id,id AS product_id,''::text AS user_id,CASE WHEN enabled THEN 'enabled' ELSE 'disabled' END AS status,jsonb_build_object('id',id,'name',name,'enabled',enabled,'configuration_source','deployment') AS data FROM products`
	case "api-keys":
		p.Permission = "keys:read"
		p.SQL = `SELECT id,created_at AS at,account_id,origin_product_id AS product_id,actor_id AS user_id,CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at<=now() THEN 'expired' ELSE 'active' END AS status,jsonb_build_object('id',id,'name',name,'account_id',account_id,'user_id',actor_id,'actor_id',actor_id,'product_id',origin_product_id,'origin_product_id',origin_product_id,'instance_id',instance_id,'models',models,'funding_mode',funding_mode,'asset',asset,'max_total',max_total,'max_per_call',max_per_call,'expires_at',CASE WHEN expires_at >= '9999-12-31' THEN NULL ELSE expires_at END,'revoked_at',revoked_at,'created_at',created_at,'version',CASE WHEN revoked_at IS NULL THEN 0 ELSE 1 END) AS data FROM model_credentials WHERE credential_kind='api_key'`
	case "calls":
		p.Permission = "calls:read"
		p.SQL = `SELECT c.id,COALESCE(c.created_at,'epoch') AS at,c.account_id,c.snapshot->>'origin_product_id' AS product_id,c.snapshot->>'actor_id' AS user_id,CASE WHEN EXISTS(SELECT 1 FROM billing_commands bc WHERE bc.account_id=c.account_id AND bc.status='UNKNOWN' AND bc.effect#>>'{call,call_id}'=c.id) THEN 'UNKNOWN' ELSE c.snapshot->>'status' END AS status,jsonb_build_object('id',c.id,'call_id',c.id,'account_id',c.account_id,'user_id',c.snapshot->>'actor_id','product_id',c.snapshot->>'origin_product_id','instance_id',c.snapshot->>'instance_id','model',c.snapshot#>>'{rate,model}','asset',COALESCE(NULLIF(c.snapshot->>'charge_asset',''),'CREDIT/6'),'held',c.snapshot->>'held','charged',c.snapshot->>'charged','usage',c.snapshot->'usage','created_at',c.created_at,'time_source',c.time_source) AS data FROM billing_calls c`
	case "transactions":
		p.SQL = `SELECT 'wallet:'||id AS id,created_at AS at,account_id,CASE WHEN command#>>'{postings,0,destination}' LIKE 'cash:%:product:%' THEN split_part(command#>>'{postings,0,destination}',':',4) ELSE '' END AS product_id,actor_id AS user_id,status,jsonb_build_object('id',id,'source','wallet_operations','account_id',account_id,'actor_id',actor_id,'kind',kind,'status',status,'version',status,'ledger_transaction_id',ledger_transaction_id,'error_code',error_code,'attempts',attempts,'created_at',created_at,'updated_at',updated_at,'reference','unihub:'||id) AS data FROM wallet_operations UNION ALL SELECT 'billing:'||id,created_at,account_id,COALESCE(effect#>>'{call,origin_product_id}',effect#>>'{order,product_id}',''),actor_id,status,jsonb_build_object('id',id,'source','billing_commands','account_id',account_id,'actor_id',actor_id,'kind',kind,'status',status,'version',status,'ledger_transaction_id',ledger_transaction_id,'error_code',CASE WHEN error_code IN ('insufficient_funds','authorization_revoked','funding_or_renewal_revoked','service_revoked','membership_revoked','batch_no_longer_eligible','') THEN error_code ELSE 'dependency_error' END,'created_at',created_at,'call_id',effect#>>'{call,call_id}','order_id',effect#>>'{order,id}','reference','unihub:billing:'||id) FROM billing_commands`
	case "recharges":
		p.SQL = `SELECT r.id,r.created_at AS at,r.account_id,COALESCE(r.product_id,'') AS product_id,r.actor_id AS user_id,COALESCE(o.status,'AWAITING_RECEIPT') AS status,jsonb_build_object('id',r.id,'account_id',r.account_id,'product_id',r.product_id,'actor_id',r.actor_id,'amount',r.amount::text,'asset','CNY/6','currency',r.currency,'merchant',r.merchant,'sandbox',r.merchant='sandbox','operation_id',r.operation_id,'ledger_transaction_id',o.ledger_transaction_id,'receipt_received_at',pr.received_at,'created_at',r.created_at) AS data FROM recharge_orders r LEFT JOIN wallet_operations o ON o.id=r.operation_id LEFT JOIN payment_receipts pr ON pr.order_id=r.id`
	case "orders", "refunds":
		p.SQL = `SELECT id,COALESCE((snapshot->>'created_at')::timestamptz,'epoch') AS at,account_id,COALESCE(snapshot->>'product_id','') AS product_id,actor_id AS user_id,` + map[bool]string{true: `COALESCE(snapshot->>'refund_status','')`, false: `COALESCE(snapshot->>'payment_status','')`}[resource == "refunds"] + ` AS status,(snapshot-'refund_key')||jsonb_build_object('id',id,'asset','CNY/6','version',md5(snapshot::text)) AS data FROM billing_orders`
		if resource == "refunds" {
			p.SQL += ` WHERE COALESCE(snapshot->>'refund_status','')<>''`
		}
	case "subscriptions":
		p.SQL = `SELECT id,COALESCE((snapshot->>'anchor')::timestamptz,'epoch') AS at,account_id,product_id,''::text AS user_id,COALESCE(snapshot->>'status','') AS status,snapshot AS data FROM billing_subscriptions`
	case "credit-batches":
		p.SQL = `SELECT id,COALESCE((snapshot->>'created_at')::timestamptz,'epoch') AS at,account_id,COALESCE(snapshot->>'product_id','') AS product_id,''::text AS user_id,CASE WHEN (snapshot->>'frozen')::boolean THEN 'frozen' WHEN (snapshot->>'expires_at')::timestamptz<=now() THEN 'expired' ELSE 'active' END AS status,(snapshot-'available'-'reserved'-'consumed'-'expired'-'withdrawn'-'source_key')||jsonb_build_object('asset','CREDIT/6','quantities_status','not_loaded') AS data FROM billing_batches`
	case "offers":
		p.SQL = `SELECT id,'epoch'::timestamptz AS at,''::text AS account_id,product_id,''::text AS user_id,status,snapshot||jsonb_build_object('status',status,'version',md5(snapshot::text),'asset','CNY/6') AS data FROM billing_offers`
	case "rates":
		p.SQL = `SELECT id,'epoch'::timestamptz AS at,''::text AS account_id,''::text AS product_id,''::text AS user_id,status,snapshot||jsonb_build_object('status',status,'version',md5(snapshot::text)) AS data FROM billing_rates`
	case "billing-profiles":
		p.SQL = `SELECT p.id,COALESCE(latest.created_at,'epoch') AS at,''::text AS account_id,p.id AS product_id,''::text AS user_id,CASE WHEN p.enabled THEN 'published' ELSE 'product_disabled' END AS status,COALESCE(latest.snapshot,jsonb_build_object('product_id',p.id,'version',0,'configuration_source','implicit_legacy'))||jsonb_build_object('actor_id',latest.actor_id,'created_at',latest.created_at) AS data FROM products p LEFT JOIN LATERAL (SELECT * FROM product_billing_profiles WHERE product_id=p.id ORDER BY version DESC LIMIT 1) latest ON true`
	case "usage-budgets":
		p.SQL = `SELECT account_id||':'||origin_product_id||':'||instance_id||':'||asset AS id,updated_at AS at,account_id,origin_product_id AS product_id,updated_by AS user_id,'active'::text AS status,jsonb_build_object('account_id',account_id,'product_id',origin_product_id,'instance_id',instance_id,'asset',asset,'funding_mode',CASE WHEN asset='CNY/6' THEN 'prepaid_cash' ELSE 'credits' END,'version',version,'max_total',max_total,'max_per_call',max_per_call,'updated_by',updated_by,'updated_at',updated_at) AS data FROM usage_budgets`
	case "audit-events":
		p.Permission = "audit:read"
		p.SQL = `SELECT 'platform:'||id AS id,created_at AS at,''::text AS account_id,''::text AS product_id,actor_id AS user_id,'recorded'::text AS status,jsonb_build_object('id',id::text,'source','platform_admin_audit','actor_id',actor_id,'target_id',target_id,'action',action,'reason',reason,'change',detail,'created_at',created_at) AS data FROM platform_admin_audit UNION ALL SELECT 'product:'||id,created_at,'',product_id,actor_id,'recorded',jsonb_build_object('id',id::text,'source','product_user_audit','actor_id',actor_id,'target_id',target_id,'product_id',product_id,'instance_id',instance_id,'action',action,'version',version,'created_at',created_at) FROM product_user_audit UNION ALL SELECT 'wallet:'||id,created_at,account_id,'',COALESCE(actor_id,''),'recorded',jsonb_build_object('id',id::text,'source','audit_events','actor_id',actor_id,'account_id',account_id,'operation_id',operation_id,'action',action,'created_at',created_at) FROM audit_events UNION ALL SELECT 'billing:'||id,created_at,account_id,'',actor_id,'recorded',jsonb_build_object('id',id::text,'source','billing_events','actor_id',actor_id,'account_id',account_id,'entity_id',entity_id,'action',action,'created_at',created_at) FROM billing_events`
	default:
		return p, billing.ErrUnsupported
	}
	return p, nil
}

func (s *Store) ListOperations(ctx context.Context, actor, resource string, o OperationsOptions) (Page[map[string]any], error) {
	out := Page[map[string]any]{Items: []map[string]any{}}
	p, e := operationProjectionFor(resource)
	if e != nil {
		return out, e
	}
	if e = s.Require(ctx, actor, p.Permission); e != nil {
		return out, e
	}
	if o.Limit == 0 {
		o.Limit = 50
	}
	if o.Limit < 1 || o.Limit > 100 || len(o.Search) > 256 || len(o.AccountID) > 256 || len(o.ProductID) > 256 || len(o.UserID) > 256 || (!o.From.IsZero() && !o.To.IsZero() && !o.From.Before(o.To)) {
		return out, billing.ErrInvalid
	}
	binding := operationBinding(actor, resource, o)
	out.AsOf = time.Now().UTC()
	posAt := time.Time{}
	posID := ""
	if o.Cursor != "" {
		c, e := decodeOperationCursor(o.Cursor, binding)
		if e != nil {
			return out, e
		}
		out.AsOf = c.AsOf
		posAt = c.At
		posID = c.ID
	}
	var from, to *time.Time
	if !o.From.IsZero() {
		from = &o.From
	}
	if !o.To.IsZero() {
		to = &o.To
	}
	args := []any{o.Search, o.Status, o.AccountID, o.ProductID, o.UserID, from, to, out.AsOf}
	where := `($1='' OR data::text ILIKE '%'||$1||'%') AND ($2='' OR status=$2) AND ($3='' OR account_id=$3) AND ($4='' OR product_id=$4) AND ($5='' OR user_id=$5` + map[bool]string{true: ` OR EXISTS(SELECT 1 FROM account_members member WHERE member.account_id=op.account_id AND member.user_id=$5)`, false: ``}[resource == "accounts"] + `) AND ($6::timestamptz IS NULL OR at >= $6) AND ($7::timestamptz IS NULL OR at < $7) AND at <= $8`
	if resource == "audit-events" {
		principal, e := s.Principal(ctx, actor)
		if e != nil {
			return out, e
		}
		all := false
		finance := false
		for _, r := range principal.Roles {
			if r == "super_admin" || r == "auditor" {
				all = true
			}
			if r == "finance_admin" {
				finance = true
			}
		}
		if !all {
			args = append(args, actor)
			scope := ``
			userSupport := false
			for _, role := range principal.Roles {
				if role == "user_admin" {
					userSupport = true
				}
			}
			if userSupport {
				scope += ` OR data->>'source' IN ('product_user_audit') OR (data->>'source'='platform_admin_audit' AND data->>'action' ~ '^(user|team|identity|access|invitation|session|product|key)')`
			}
			if finance {
				scope += ` OR data->>'source' IN ('billing_events','audit_events') OR (data->>'source'='platform_admin_audit' AND data->>'action' ~ '^(finance|offer|rate|profile|budget|refund)')`
			}
			where += fmt.Sprintf(` AND (user_id=$%d`+scope+`)`, len(args))
		}
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer database.Rollback(tx)
	base := ` FROM (` + p.SQL + `) op WHERE ` + where
	if e = tx.QueryRow(ctx, `SELECT count(*)`+base, args...).Scan(&out.Total); e != nil {
		return out, e
	}
	if posID != "" {
		args = append(args, posAt, posID)
		base += fmt.Sprintf(` AND (at,id)<($%d,$%d)`, len(args)-1, len(args))
	}
	args = append(args, o.Limit+1)
	rows, e := tx.Query(ctx, `SELECT id,at,data||jsonb_build_object('status',status)`+base+fmt.Sprintf(` ORDER BY at DESC,id DESC LIMIT $%d`, len(args)), args...)
	if e != nil {
		return out, e
	}
	var ids []string
	var ats []time.Time
	for rows.Next() {
		var id string
		var at time.Time
		var raw []byte
		var v map[string]any
		if e = rows.Scan(&id, &at, &raw); e != nil {
			break
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			break
		}
		if resource == "audit-events" {
			if change, ok := v["change"]; ok {
				v["change"] = safeOperationAudit(change)
			}
		}
		out.Items = append(out.Items, v)
		ids = append(ids, id)
		ats = append(ats, at)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Items) > o.Limit {
		out.Items = out.Items[:o.Limit]
		i := o.Limit - 1
		out.NextCursor, e = encodeOperationCursor(operationCursor{Binding: binding, ID: ids[i], At: ats[i], AsOf: out.AsOf, Expires: time.Now().Add(time.Hour)})
		if e != nil {
			return out, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	if resource == "products" && s.products != nil {
		for _, v := range out.Items {
			decl := []any{}
			for _, m := range s.products.Products() {
				if m.ProductID == v["id"] {
					decl = append(decl, m)
				}
			}
			v["declarations"] = decl
		}
	}
	if resource != "audit-events" && resource != "accounts" && resource != "products" && resource != "users" {
		if e = s.RecordOperationRead(ctx, actor, resource, o); e != nil {
			return out, e
		}
	}
	return out, nil
}

func (s *Store) RecordOperationRead(ctx context.Context, actor, resource string, o OperationsOptions) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if e = s.WriteAudit(ctx, tx, actor, "sensitive.read", resource, "administrative scoped read", map[string]any{"resource": resource, "account_id": o.AccountID, "product_id": o.ProductID, "target_id": o.UserID}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func safeCSVCell(v string) string {
	trim := strings.TrimLeft(v, " \t\r\n")
	if strings.ContainsAny(v[:len(v)-len(trim)], "\t\r\n") || len(trim) > 0 && strings.ContainsRune("=+-@", rune(trim[0])) {
		return "'" + v
	}
	return v
}
func (s *Store) ExportOperations(ctx context.Context, actor, resource string, o OperationsOptions) ([]byte, error) {
	if e := s.Require(ctx, actor, "export:read"); e != nil {
		return nil, e
	}
	if resource != "users" && resource != "accounts" && resource != "orders" && resource != "calls" && resource != "audit-events" {
		return nil, billing.ErrUnsupported
	}
	if o.Cursor != "" {
		return nil, billing.ErrInvalid
	}
	o.Limit = 100
	items := []map[string]any{}
	for {
		page, e := s.ListOperations(ctx, actor, resource, o)
		if e != nil {
			return nil, e
		}
		if page.Total > 1000 {
			return nil, billing.ErrInvalid
		}
		items = append(items, page.Items...)
		if len(items) > 1000 {
			return nil, billing.ErrInvalid
		}
		if page.NextCursor == "" {
			break
		}
		o.Cursor = page.NextCursor
	}
	columnsMap := map[string]bool{}
	for _, v := range items {
		for k := range v {
			columnsMap[k] = true
		}
	}
	columns := []string{}
	for k := range columnsMap {
		columns = append(columns, k)
	}
	sort.Strings(columns)
	if len(columns) == 0 {
		columns = []string{"id"}
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	if e := w.Write(columns); e != nil {
		return nil, e
	}
	for _, v := range items {
		row := make([]string, len(columns))
		for i, k := range columns {
			switch value := v[k].(type) {
			case nil:
			case string:
				row[i] = safeCSVCell(value)
			default:
				raw, _ := json.Marshal(value)
				row[i] = safeCSVCell(string(raw))
			}
		}
		if e := w.Write(row); e != nil {
			return nil, e
		}
	}
	w.Flush()
	if e := w.Error(); e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer database.Rollback(tx)
	if e = s.WriteAudit(ctx, tx, actor, "export.created", resource, "filtered administration CSV export", map[string]any{"resource": resource, "rows": len(items), "account_id": o.AccountID, "product_id": o.ProductID}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return []byte(b.String()), nil
}

// RequireAdminOn permits scoped domain adapters to recheck the exact live
// platform permission on their transaction while holding the authority lock.
func (s *Store) RequireAdminOn(ctx context.Context, q accounts.Querier, actor, permission string) error {
	return s.requireOn(ctx, q, actor, permission)
}

// Audit schemas can grow independently. Only recognized nonsecret change fields
// cross the administration boundary, including within nested before/after data.
func safeOperationAudit(value any) any {
	allowed := map[string]bool{}
	for _, k := range []string{"before", "after", "version", "expected_version", "account_id", "product_id", "instance_id", "user_id", "target_id", "actor_id", "id", "name", "active", "enabled", "status", "roles", "permissions", "source", "kind", "decision", "reason", "revoked_at", "created_at", "updated_at", "max_total", "max_per_call", "held", "used", "remaining", "amount", "asset", "funding_mode", "subscription", "included_credits", "addons", "usage", "models", "require_subscription", "funding_modes", "default_funding_mode", "price", "monthly_credit", "account_kind", "period", "sandbox", "rank", "validity_months", "refund_rule", "upgrade_rule", "payment_status", "fulfillment_status", "refund_status", "refund_reason"} {
		allowed[k] = true
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, child := range x {
				if allowed[k] {
					out[k] = clean(child)
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, child := range x {
				out[i] = clean(child)
			}
			return out
		default:
			return v
		}
	}
	return clean(value)
}
