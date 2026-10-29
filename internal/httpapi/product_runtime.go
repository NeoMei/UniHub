package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/productusers"
)

// RuntimeService is private deployment policy. Tokens are never accepted from
// product configuration JSON and no legacy global billing token enables it.
type RuntimeService struct {
	Token           string   `json:"token"`
	ProductID       string   `json:"product_id"`
	InstanceID      string   `json:"instance_id"`
	FinanceClientID string   `json:"finance_client_id,omitempty"`
	AllowedOps      []string `json:"allowed_ops"`
}

func runtimeBearer(r *http.Request, name string) (string, bool) {
	if len(r.Header.Values(name)) != 1 {
		return "", false
	}
	h := r.Header.Get(name)
	if !strings.HasPrefix(h, "Bearer ") || len(h) > 16384 {
		return "", false
	}
	raw := strings.TrimPrefix(h, "Bearer ")
	return raw, raw != "" && !strings.ContainsAny(raw, " \r\n\t")
}
func (s *api) runtimeAuthenticated(op string, next func(http.ResponseWriter, *http.Request, RuntimeService) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := runtimeBearer(r, "Authorization")
		if !ok {
			fail(w, 401, "unauthorized")
			return
		}
		var service RuntimeService
		matches := 0
		for _, candidate := range s.options.ProductRuntime {
			if len(candidate.Token) >= 32 && subtle.ConstantTimeCompare([]byte(raw), []byte(candidate.Token)) == 1 {
				service = candidate
				matches++
			}
		}
		if matches != 1 {
			fail(w, 401, "unauthorized")
			return
		}
		if service.InstanceID == "" {
			service.InstanceID = "default"
		}
		if service.ProductID == "" || !slices.Contains(service.AllowedOps, op) || r.Header.Get("Origin") != "" {
			fail(w, 403, "forbidden")
			return
		}
		if e := next(w, r, service); e != nil {
			writeError(w, e)
		}
	}
}
func runtimeScope(service RuntimeService) billing.RuntimeScope {
	return billing.RuntimeScope{ProductID: service.ProductID, InstanceID: service.InstanceID}
}
func (s *api) runtimeActor(r *http.Request, service RuntimeService) (accounts.User, auth.Claims, error) {
	raw, ok := runtimeBearer(r, "X-UniHub-Actor-Authorization")
	if !ok {
		return accounts.User{}, auth.Claims{}, errRuntimeActor
	}
	if strings.HasPrefix(raw, "ubp_") {
		if r.Method != "POST" || r.URL.Path != "/internal/v1/product-runtime/bindings" {
			return accounts.User{}, auth.Claims{}, accounts.ErrForbidden
		}
		if s.options.ProductAuth == nil {
			return accounts.User{}, auth.Claims{}, errRuntimeActor
		}
		return s.options.ProductAuth.VerifyBrowserProof(r.Context(), raw, service.ProductID, service.InstanceID)
	}
	if strings.HasPrefix(raw, "upa_") {
		if s.options.ProductAuth == nil {
			return accounts.User{}, auth.Claims{}, errRuntimeActor
		}
		u, e := s.options.ProductAuth.VerifyActor(r.Context(), raw, service.ProductID, service.InstanceID)
		if e != nil {
			return accounts.User{}, auth.Claims{}, errRuntimeActor
		}
		return u, auth.Claims{}, nil
	}
	for id, verifier := range s.options.ProductVerifiers {
		policy, ok := s.options.ProductAccess[id]
		if !ok || policy.ProductID != service.ProductID || productusers.Instance(policy.InstanceID) != service.InstanceID {
			continue
		}
		claims, e := verifier.Verify(r.Context(), raw)
		if e != nil {
			continue
		}
		if claims.ExpiresAt <= time.Now().Unix() {
			return accounts.User{}, auth.Claims{}, errRuntimeActor
		}
		if s.options.Identity != nil && !s.options.Identity.SessionAllowed(r.Context(), raw) {
			return accounts.User{}, auth.Claims{}, errRuntimeActor
		}
		u, e := s.accounts.EnsureIdentity(r.Context(), accounts.Identity{Issuer: claims.Issuer, Subject: claims.Subject, Name: claims.Name, Email: claims.Email})
		if e != nil {
			return accounts.User{}, auth.Claims{}, e
		}
		if s.options.ProductUsers == nil {
			return accounts.User{}, auth.Claims{}, accounts.ErrForbidden
		}
		if e = s.options.ProductUsers.CheckActive(r.Context(), u.ID); e != nil {
			return accounts.User{}, auth.Claims{}, e
		}
		return u, claims, nil
	}
	return accounts.User{}, auth.Claims{}, errRuntimeActor
}

var errRuntimeActor = errors.New("runtime actor proof required")

func (s *api) productRuntimeRoutes(m *http.ServeMux) {
	if len(s.options.ProductRuntime) == 0 {
		return
	}
	route := func(pattern, op string, next func(http.ResponseWriter, *http.Request, RuntimeService) error) {
		m.HandleFunc(pattern, s.runtimeAuthenticated(op, next))
	}
	route("POST /internal/v1/product-runtime/bindings", "bindings:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in struct {
			LocalUserID string `json:"local_user_id"`
			BindingKey  string `json:"binding_key"`
		}
		if e := decode(w, r, &in); e != nil {
			return e
		}
		u, claims, e := s.runtimeActor(r, service)
		if e != nil {
			return e
		}
		if s.options.ProductUsers == nil {
			return accounts.ErrForbidden
		}
		b, e := s.options.ProductUsers.EnsureRuntimeBinding(r.Context(), service.ProductID, service.InstanceID, in.LocalUserID, in.BindingKey, accounts.Identity{Issuer: claims.Issuer, Subject: claims.Subject}, u)
		if e != nil {
			return e
		}
		if s.options.ProductAuth != nil {
			if e = s.options.ProductAuth.AdoptBinding(r.Context(), service.ProductID, service.InstanceID, in.LocalUserID, 1); e != nil {
				return e
			}
		}
		if raw := strings.TrimPrefix(r.Header.Get("X-UniHub-Actor-Authorization"), "Bearer "); strings.HasPrefix(raw, "ubp_") {
			if e = s.options.ProductAuth.ConsumeBrowserProof(r.Context(), raw); e != nil {
				return e
			}
		}
		return respond(w, 200, b)
	})
	route("GET /internal/v1/product-runtime/users/{local}/access", "access:read", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		if s.options.ProductUsers == nil {
			return accounts.ErrForbidden
		}
		a, e := s.options.ProductUsers.ResolveRuntime(r.Context(), service.ProductID, service.InstanceID, r.PathValue("local"))
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	route("GET /internal/v1/product-runtime/users", "users:read", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		u, _, e := s.runtimeActor(r, service)
		if e != nil {
			return e
		}
		if s.options.ProductUsers == nil {
			return accounts.ErrForbidden
		}
		out, e := s.options.ProductUsers.RuntimeUsers(r.Context(), u.ID, service.ProductID, service.InstanceID, r.URL.Query().Get("q"))
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("PATCH /internal/v1/product-runtime/users/{user}/access", "users:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in struct {
			UserID  string   `json:"user_id"`
			Roles   []string `json:"roles"`
			Active  *bool    `json:"active"`
			Version *int64   `json:"version"`
		}
		if e := decode(w, r, &in); e != nil {
			return e
		}
		if in.UserID != r.PathValue("user") || in.Active == nil || in.Version == nil || in.Roles == nil {
			return accounts.ErrInvalid
		}
		actor, _, e := s.runtimeActor(r, service)
		if e != nil {
			return e
		}
		if s.options.ProductUsers == nil {
			return accounts.ErrForbidden
		}
		out, e := s.options.ProductUsers.SetRuntimeAccess(r.Context(), actor.ID, in.UserID, service.ProductID, service.InstanceID, in.Roles, *in.Active, *in.Version)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	if s.options.Billing == nil {
		return
	}
	b := s.options.Billing
	m.HandleFunc("POST /internal/v1/product-runtime/awards", func(w http.ResponseWriter, r *http.Request) {
		var in billing.RuntimeAwardRequest
		if e := decode(w, r, &in); e != nil {
			writeError(w, e)
			return
		}
		op := "credits:award"
		if in.SourceKind == "payment" {
			op = "credits:payment"
		}
		s.runtimeAuthenticated(op, func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
			out, e := b.RuntimeAward(r.Context(), runtimeScope(service), in)
			if e != nil {
				return e
			}
			return respond(w, 200, out)
		})(w, r)
	})
	route("GET /internal/v1/product-runtime/users/{local}/budget", "budgets:read", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		out, e := b.GetRuntimeBudget(r.Context(), runtimeScope(service), r.PathValue("local"), r.URL.Query().Get("account_id"))
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("PUT /internal/v1/product-runtime/users/{local}/budget", "budgets:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in billing.RuntimeBudgetRequest
		if e := decode(w, r, &in); e != nil {
			return e
		}
		if in.LocalUserID != r.PathValue("local") {
			return accounts.ErrInvalid
		}
		actor, _, e := s.runtimeActor(r, service)
		if e != nil {
			return e
		}
		out, e := b.SetRuntimeBudget(r.Context(), runtimeScope(service), actor.ID, in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("GET /internal/v1/product-runtime/users/{local}/balance", "billing:read", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		out, e := b.RuntimeBalance(r.Context(), runtimeScope(service), r.PathValue("local"), r.URL.Query().Get("account_id"))
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("POST /internal/v1/product-runtime/calls/reserve", "billing:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in billing.RuntimeReserveRequest
		if e := decode(w, r, &in); e != nil {
			return e
		}
		if in.ParentCallID != "" && !slices.Contains(service.AllowedOps, "calls:reprice") {
			return accounts.ErrForbidden
		}
		out, e := b.RuntimeReserve(r.Context(), runtimeScope(service), in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("GET /internal/v1/product-runtime/calls/{call}", "billing:read", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		out, e := b.RuntimeGetCall(r.Context(), runtimeScope(service), r.PathValue("call"))
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("POST /internal/v1/product-runtime/calls/{call}/topup", "billing:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in billing.RuntimeTopUpRequest
		if e := decode(w, r, &in); e != nil {
			return e
		}
		if in.AcceptedCall && !slices.Contains(service.AllowedOps, "calls:reprice") {
			return accounts.ErrForbidden
		}
		out, e := b.RuntimeTopUp(r.Context(), runtimeScope(service), r.PathValue("call"), in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("POST /internal/v1/product-runtime/calls/{call}/settle", "billing:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in billing.RuntimeSettlementRequest
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := b.RuntimeSettle(r.Context(), runtimeScope(service), r.PathValue("call"), in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("POST /internal/v1/product-runtime/calls/{call}/release", "billing:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in billing.RuntimeReleaseRequest
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := b.RuntimeRelease(r.Context(), runtimeScope(service), r.PathValue("call"), in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("POST /internal/v1/product-runtime/credit-adjustments", "credits:adjust", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in billing.RuntimeAdjustmentRequest
		if e := decode(w, r, &in); e != nil {
			return e
		}
		u, _, e := s.runtimeActor(r, service)
		if e != nil {
			return e
		}
		out, e := b.RuntimeAdjustCredit(r.Context(), runtimeScope(service), u.ID, in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	route("POST /internal/v1/product-runtime/migrations", "migrations:write", func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
		var in billing.RuntimeImportRequest
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := b.RuntimeImportBalance(r.Context(), runtimeScope(service), in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
}
