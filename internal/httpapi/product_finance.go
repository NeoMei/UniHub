package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unihub/internal/accounts"
	"unihub/internal/productusers"
)

// These routes deliberately point at the same actions as the public financial
// API. The private prefix adds actor authentication; it does not create a
// second set of account, role, ledger, or model-key rules.
var productFinanceRoutes = []string{
	"GET /v1/me", "GET /v1/accounts", "GET /v1/capabilities", "GET /v1/catalog", "GET /v1/billing-presets", "GET /v1/products",
	"GET /v1/products/{product}/billing-profile", "GET /v1/products/{product}/model-consistency",
	"GET /v1/accounts/{account}/resources", "GET /v1/accounts/{account}/wallets", "GET /v1/accounts/{account}/subscriptions", "GET /v1/accounts/{account}/orders", "GET /v1/accounts/{account}/usage-budget",
	"GET /v1/accounts/{account}/api-keys", "GET /v1/accounts/{account}/calls", "GET /v1/accounts/{account}/calls/{call}",
	"GET /v1/accounts/{account}/usage/summary", "GET /v1/accounts/{account}/operations", "GET /v1/accounts/{account}/operations/{operation}",
	"GET /v1/accounts/{account}/recharges/{order}",
	"POST /v1/accounts/{account}/recharges", "POST /v1/accounts/{account}/recharges/{order}/sandbox-settle",
	"POST /v1/accounts/{account}/api-keys", "DELETE /v1/accounts/{account}/api-keys/{key}", "PUT /v1/accounts/{account}/usage-budget",
}

// productFinanceWriteKey is set only by the verified runtime-service lane.
type productFinanceWriteKey struct{}

var financeIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func financeOperation(method string) string {
	if method == http.MethodGet {
		return "finance:read"
	}
	return "finance:write"
}

func (s *api) productFinanceRoutes(m *http.ServeMux, actions map[string]action) {
	enabled := false
	for _, svc := range s.options.ProductRuntime {
		if slices.Contains(svc.AllowedOps, "finance:read") || slices.Contains(svc.AllowedOps, "finance:write") {
			enabled = true
			break
		}
	}
	if !enabled {
		return
	}
	for _, original := range productFinanceRoutes {
		f, ok := actions[original]
		if !ok {
			continue
		}
		method, path, _ := strings.Cut(original, " ")
		privatePattern := method + " /internal/v1/product-finance" + strings.TrimPrefix(path, "/v1")
		original := original
		m.HandleFunc(privatePattern, s.financeAuthenticated(original, financeOperation(method), f))
	}
}

func (s *api) financeAuthenticated(original, op string, next action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query, queryErr := url.ParseQuery(r.URL.RawQuery)
		idempotencyValues := r.Header.Values("Idempotency-Key")
		idempotencyInvalid := len(idempotencyValues) > 1 || (len(idempotencyValues) == 1 && !financeIdempotencyKey.MatchString(idempotencyValues[0]))
		if original == "POST /v1/accounts/{account}/recharges" && len(idempotencyValues) != 1 {
			idempotencyInvalid = true
		}
		if len(r.Header.Values("Origin")) != 0 || queryErr != nil || len(r.URL.RawQuery) > 4096 || len(query) > 20 || r.ContentLength > 1<<20 || ((r.Method == http.MethodGet || r.Method == http.MethodDelete) && r.ContentLength != 0) || idempotencyInvalid {
			fail(w, 403, "forbidden")
			return
		}
		s.runtimeAuthenticated(op, func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
			if len(r.Header.Values("Origin")) != 0 || service.FinanceClientID == "" {
				return accounts.ErrForbidden
			}
			actor, ok := runtimeBearer(r, "X-UniHub-Actor-Authorization")
			if !ok || !strings.HasPrefix(actor, "upa_") {
				return errRuntimeActor
			}
			if s.options.ProductAuth == nil {
				return errRuntimeActor
			}
			policy, ok := s.options.ProductAccess[service.FinanceClientID]
			if !ok || policy.ClientID != service.FinanceClientID || policy.ProductID != service.ProductID || productusers.Instance(policy.InstanceID) != productusers.Instance(service.InstanceID) || !productScopeAllowed(original, policy) {
				return accounts.ErrForbidden
			}
			u, err := s.options.ProductAuth.VerifyActor(r.Context(), actor, service.ProductID, service.InstanceID)
			if err != nil {
				return errRuntimeActor
			}
			if s.options.ProductUsers == nil {
				return accounts.ErrForbidden
			}
			access, err := s.options.ProductUsers.Resolve(r.Context(), u, service.ProductID, service.InstanceID)
			if err != nil {
				return err
			}
			if !access.Active || !slices.Contains(access.Permissions, "product:use") {
				return accounts.ErrForbidden
			}
			ctx := context.WithValue(r.Context(), productFinanceWriteKey{}, slices.Contains(service.AllowedOps, "finance:write"))
			ctx = context.WithValue(ctx, productPolicyKey{}, policy)
			ctx = context.WithValue(ctx, productIdentityKey{}, policy.ProductID)
			business := r.Clone(ctx)
			business.Pattern = original
			business.URL.Path = strings.Replace(business.URL.Path, "/internal/v1/product-finance", "/v1", 1)
			business.URL.RawPath = ""
			business.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			buffered := &boundedFinanceResponse{header: make(http.Header)}
			if err = next(buffered, business, u); err != nil {
				return err
			}
			if buffered.exceeded {
				fail(w, http.StatusBadGateway, "finance_response_too_large")
				return nil
			}
			if buffered.status == 0 {
				buffered.status = http.StatusOK
			}
			for key, values := range buffered.header {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(buffered.status)
			_, err = w.Write(buffered.body.Bytes())
			return err
		})(w, r)
	}
}

type boundedFinanceResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	exceeded bool
}

func (w *boundedFinanceResponse) Header() http.Header { return w.header }
func (w *boundedFinanceResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *boundedFinanceResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(data) > 1<<20 {
		w.exceeded = true
		return len(data), nil
	}
	return w.body.Write(data)
}
