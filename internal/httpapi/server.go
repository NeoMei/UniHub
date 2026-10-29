// Package httpapi exposes the account and wallet API. All business endpoints
// require a verified access token; payment notifications use a separate signature.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/identityapi"
	"unihub/internal/ledger"
	"unihub/internal/modelaccess"
	"unihub/internal/platformadmin"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/wallet"
)

type Options struct {
	PlatformAdmin    *platformadmin.Store
	ProductRuntime   map[string]RuntimeService
	ProductAuth      *productauth.Service
	Sandbox          bool
	Merchant         string
	SigningKey       []byte
	Billing          *billing.Service
	ServiceToken     string
	ModelAccess      *modelaccess.Service
	OperatorVerifier auth.Verifier
	ProductVerifiers map[string]auth.Verifier
	ProductAccess    map[string]products.Client
	ProductUsers     *productusers.Store
	Identity         ProfileService
}
type ProfileService interface {
	Profile(context.Context, string) (identityapi.Profile, error)
	UpdateDisplayName(context.Context, string, string) (identityapi.Profile, error)
	ChangePassword(context.Context, string, string, string) error
	SessionAllowed(context.Context, string) bool
	MfaStatus(context.Context, string) (mfaStatus, error)
	MfaEnroll(context.Context, string) (mfaSetup, mfaStatus, error)
	MfaConfirm(context.Context, string, string, string, string) (mfaStatus, error)
	MfaDisable(context.Context, string, string, string) (mfaStatus, error)
}
type api struct {
	verifier auth.Verifier
	accounts *accounts.Store
	wallet   *wallet.Service
	options  Options
}
type action func(http.ResponseWriter, *http.Request, accounts.User) error

func New(v auth.Verifier, a *accounts.Store, w *wallet.Service, o Options) http.Handler {
	if o.ProductUsers != nil {
		if o.Billing != nil {
			o.Billing.ConfigureProductUsers(o.ProductUsers)
		}
		if o.ModelAccess != nil {
			o.ModelAccess.ConfigureProductUsers(o.ProductUsers)
		}
	}
	s := &api{v, a, w, o}
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"status": "ok", "service": "unihub"})
	})
	add := func(pattern string, f action) { m.HandleFunc(pattern, s.authenticated(f)) }
	s.productUserRoutes(add)
	s.productRuntimeRoutes(m)
	s.productRuntimeRefundRoutes(m)
	s.productAuthRoutes(m)
	s.adminRoutes(add)
	s.platformInvitationRoutes(add)
	s.invitationQueryRoutes(add)
	if o.Identity != nil {
		s.profileRoutes(add)
	}
	if o.Billing != nil {
		s.billingRoutes(m, add)
	}
	if o.Billing != nil && o.ModelAccess != nil {
		s.modelRoutes(m, add)
	}
	add("GET /v1/me", func(w http.ResponseWriter, r *http.Request, u accounts.User) error { return respond(w, 200, u) })
	add("GET /v1/accounts", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.accounts.ListAccounts(r.Context(), u.ID)
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	add("POST /v1/accounts", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Name string `json:"name"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if len(b.Name) > 100 {
			return wallet.ErrInvalid
		}
		a, e := s.accounts.CreateTeam(r.Context(), u.ID, b.Name)
		if e != nil {
			return e
		}
		return respond(w, 201, a)
	})
	add("GET /v1/accounts/{account}/members", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.accounts.ListMembers(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	add("POST /v1/accounts/{account}/invitations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		a, e := s.accounts.Invite(r.Context(), u.ID, r.PathValue("account"), b.UserID, b.Role)
		if e != nil {
			return e
		}
		return respond(w, 201, a)
	})
	add("DELETE /v1/accounts/{account}/invitations/{invitation}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := s.accounts.RevokeInvitation(r.Context(), u.ID, r.PathValue("account"), r.PathValue("invitation")); e != nil {
			return e
		}
		w.WriteHeader(204)
		return nil
	})
	add("POST /v1/invitations/{invitation}/accept", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.accounts.AcceptInvitation(r.Context(), u.ID, r.PathValue("invitation"))
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	add("PATCH /v1/accounts/{account}/members/{member}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Role string `json:"role"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if e := s.accounts.ChangeRole(r.Context(), u.ID, r.PathValue("account"), r.PathValue("member"), b.Role); e != nil {
			return e
		}
		w.WriteHeader(204)
		return nil
	})
	add("DELETE /v1/accounts/{account}/members/{member}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := s.accounts.RemoveMember(r.Context(), u.ID, r.PathValue("account"), r.PathValue("member")); e != nil {
			return e
		}
		w.WriteHeader(204)
		return nil
	})
	add("GET /v1/products", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.wallet.Products(r.Context())
		if e != nil {
			return e
		}
		a = filterProducts(r, a, func(p wallet.Product) string { return p.ID })
		if s.options.Billing == nil {
			return respond(w, 200, a)
		}
		out := make([]map[string]any, 0, len(a))
		for _, product := range a {
			profile, e := s.options.Billing.Profile(r.Context(), product.ID)
			if e != nil {
				return e
			}
			out = append(out, map[string]any{"id": product.ID, "name": product.Name, "billing_profile": profile})
		}
		return respond(w, 200, out)
	})
	add("GET /v1/accounts/{account}/wallets", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var a []wallet.Balance
		var e error
		if originProduct(r) != "" {
			a, e = s.wallet.BalancesForProducts(r.Context(), u.ID, r.PathValue("account"), billingProducts(r))
		} else {
			a, e = s.wallet.Balances(r.Context(), u.ID, r.PathValue("account"))
		}
		if e != nil {
			return e
		}
		if originProduct(r) != "" {
			out := []wallet.Balance{}
			for _, x := range a {
				if x.ProductID == "" || allowedBillingProduct(r, x.ProductID) {
					out = append(out, x)
				}
			}
			a = out
		}
		return respond(w, 200, a)
	})
	add("POST /v1/accounts/{account}/recharges", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if !o.Sandbox {
			return paymentUnavailable
		}
		var b struct {
			ProductID string `json:"product_id"`
			Amount    string `json:"amount"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if e := requireBillingProduct(r, b.ProductID); e != nil {
			return e
		}
		a, e := s.wallet.CreateRecharge(r.Context(), u.ID, r.PathValue("account"), b.ProductID, b.Amount, r.Header.Get("Idempotency-Key"), o.Merchant)
		if e != nil {
			return e
		}
		return respond(w, 201, a)
	})
	add("GET /v1/accounts/{account}/recharges/{order}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.wallet.GetOrder(r.Context(), u.ID, r.PathValue("account"), r.PathValue("order"))
		if e != nil {
			return e
		}
		if e := requireBillingProduct(r, a.ProductID); e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	add("POST /v1/accounts/{account}/transfers", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			ProductID string `json:"product_id"`
			Amount    string `json:"amount"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if e := requireBillingProduct(r, b.ProductID); e != nil {
			return e
		}
		a, e := s.wallet.Transfer(r.Context(), u.ID, r.PathValue("account"), b.ProductID, b.Amount, r.Header.Get("Idempotency-Key"))
		if e != nil {
			return e
		}
		return respond(w, 202, a)
	})
	add("GET /v1/accounts/{account}/operations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var a []wallet.Operation
		var e error
		if originProduct(r) != "" {
			a, e = s.wallet.ProductOperations(r.Context(), u.ID, r.PathValue("account"), billingProducts(r))
		} else {
			a, e = s.wallet.Operations(r.Context(), u.ID, r.PathValue("account"))
		}
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	add("GET /v1/accounts/{account}/operations/{operation}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var a wallet.Operation
		var e error
		if originProduct(r) != "" {
			a, e = s.wallet.ProductOperation(r.Context(), u.ID, r.PathValue("account"), r.PathValue("operation"), billingProducts(r))
		} else {
			a, e = s.wallet.GetOperation(r.Context(), u.ID, r.PathValue("account"), r.PathValue("operation"))
		}
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	add("GET /v1/accounts/{account}/audit", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.wallet.Audit(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	if o.Sandbox {
		m.HandleFunc("POST /webhooks/sandbox", s.webhook)
		add("POST /v1/accounts/{account}/recharges/{order}/sandbox-settle", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			order, e := s.wallet.GetOrder(r.Context(), u.ID, r.PathValue("account"), r.PathValue("order"))
			if e != nil {
				return e
			}
			if e := requireBillingProduct(r, order.ProductID); e != nil {
				return e
			}
			receipt := wallet.Receipt{Merchant: o.Merchant, OrderID: order.ID, TransactionID: "sandbox:" + order.ID, Amount: order.Amount, Currency: order.Currency, Status: "SUCCEEDED"}
			body, _ := json.Marshal(receipt)
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			op, e := s.receive(r.Context(), body, ts, wallet.SignReceipt(o.SigningKey, ts, body))
			if e != nil {
				return e
			}
			return respond(w, 202, op)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}

var paymentUnavailable = errors.New("payments unavailable")

func (s *api) authenticated(next action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Authorization")) > 1 {
			fail(w, 401, "unauthorized")
			return
		}
		h := r.Header.Get("Authorization")
		c, browser := auth.BrowserClaims(r.Context())
		var e error
		product := ""
		if h != "" || !browser {
			if !strings.HasPrefix(h, "Bearer ") || len(h) > 16384 {
				fail(w, 401, "unauthorized")
				return
			}
			c, e = s.verifier.Verify(r.Context(), strings.TrimPrefix(h, "Bearer "))
			if e != nil {
				for id, verifier := range s.options.ProductVerifiers {
					c, e = verifier.Verify(r.Context(), strings.TrimPrefix(h, "Bearer "))
					if e == nil {
						product = id
						break
					}
				}
			}
		}
		if e != nil {
			fail(w, 401, "unauthorized")
			return
		}
		if product != "" {
			policy, explicit := s.options.ProductAccess[product]
			if explicit && (policy.ProductID == "" || policy.ClientID != product) {
				fail(w, 403, "forbidden")
				return
			}
			if (explicit && !productScopeAllowed(r.Pattern, policy)) || (!explicit && !productRouteAllowed(r.Pattern)) {
				fail(w, 403, "forbidden")
				return
			}
			if explicit {
				if len(r.Header.Values("Origin")) > 1 || (r.Header.Get("Origin") != "" && !policy.AllowsOrigin(r.Header.Get("Origin"))) {
					fail(w, 403, "forbidden")
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), productPolicyKey{}, policy))
				product = policy.ProductID
			} else if r.Header.Get("Origin") != "" {
				fail(w, 403, "forbidden")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), productIdentityKey{}, product))
		}
		u, e := s.accounts.EnsureIdentity(r.Context(), accounts.Identity{Issuer: c.Issuer, Subject: c.Subject, Name: c.Name, Email: c.Email})
		if e != nil {
			writeError(w, e)
			return
		}
		if s.options.PlatformAdmin != nil {
			if e = s.options.PlatformAdmin.CredentialAllowed(r.Context(), u.ID, c.IssuedAt); e != nil {
				writeError(w, e)
				return
			}
		}
		if s.options.ProductUsers != nil {
			if e = s.options.ProductUsers.CheckActive(r.Context(), u.ID); e != nil {
				writeError(w, e)
				return
			}
		}
		// Manifest-managed access:read clients opted into product membership.
		// Recheck it on every business route so direct bearer calls cannot
		// bypass a product block through the account/finance API. Legacy clients
		// without this contract retain their established scope policy.
		if s.options.ProductUsers != nil {
			if policy, ok := productPolicy(r); ok && slices.Contains(policy.Scopes, "access:read") && s.options.ProductUsers.Managed(policy.ProductID, productusers.Instance(policy.InstanceID)) {
				if _, e = s.options.ProductUsers.Resolve(r.Context(), u, policy.ProductID, productusers.Instance(policy.InstanceID)); e != nil {
					writeError(w, e)
					return
				}
			}
		}
		r = withAccessExpiry(r, c)
		if browser && h == "" {
			if gate, ok := s.options.Identity.(interface {
				BrowserSessionAllowed(context.Context, string) bool
			}); ok && !gate.BrowserSessionAllowed(r.Context(), u.ID) {
				fail(w, 401, "unauthorized")
				return
			}
		}
		if h != "" && s.options.Identity != nil && !s.options.Identity.SessionAllowed(r.Context(), strings.TrimPrefix(h, "Bearer ")) {
			fail(w, 401, "unauthorized")
			return
		}
		if e = next(w, r, u); e != nil {
			writeError(w, e)
		}
	}
}
func respond(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	_ = respond(w, status, map[string]string{"error": code})
}
func writeError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, errRuntimeActor):
		fail(w, 401, "actor_proof_required")
	case errors.Is(e, accounts.ErrForbidden):
		fail(w, 403, "forbidden")
	case errors.Is(e, modelaccess.ErrCredential):
		fail(w, 403, "invalid_model_credential")
	case errors.Is(e, accounts.ErrNotFound):
		fail(w, 404, "not_found")
	case errors.Is(e, accounts.ErrConflict) || errors.Is(e, wallet.ErrConflict):
		fail(w, 409, "conflict")
	case errors.Is(e, accounts.ErrInvalid) || errors.Is(e, wallet.ErrInvalid):
		fail(w, 400, "invalid_request")
	case errors.Is(e, paymentUnavailable):
		fail(w, 503, "payment_not_configured")
	case errors.Is(e, billing.ErrInvalid):
		fail(w, 400, "invalid_request")
	case errors.Is(e, billing.ErrConflict):
		fail(w, 409, "conflict")
	case errors.Is(e, billing.ErrPending) || errors.Is(e, wallet.ErrPending):
		fail(w, 409, "operation_pending")
	case errors.Is(e, billing.ErrInsufficientCredits):
		fail(w, 402, "insufficient_credits")
	case errors.Is(e, ledger.ErrInsufficientFunds):
		fail(w, 402, "insufficient_funds")
	case errors.Is(e, billing.ErrUnsupported):
		fail(w, 409, "unsupported_plan_change")
	case errors.Is(e, billing.ErrManualReview):
		fail(w, 409, "refund_requires_review")
	default:
		fail(w, 503, "service_unavailable")
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return wallet.ErrInvalid
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return wallet.ErrInvalid
	}
	return nil
}
func (s *api) receive(ctx context.Context, body []byte, ts, sig string) (wallet.Operation, error) {
	if e := wallet.VerifyReceipt(s.options.SigningKey, ts, sig, body, time.Now()); e != nil {
		return wallet.Operation{}, e
	}
	var r wallet.Receipt
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if e := d.Decode(&r); e != nil {
		return wallet.Operation{}, wallet.ErrInvalid
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return wallet.Operation{}, wallet.ErrInvalid
	}
	if r.Merchant != s.options.Merchant {
		return wallet.Operation{}, wallet.ErrInvalid
	}
	return s.wallet.Receive(ctx, r)
}
func (s *api) webhook(w http.ResponseWriter, r *http.Request) {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if e != nil {
		fail(w, 400, "invalid_request")
		return
	}
	if e = wallet.VerifyReceipt(s.options.SigningKey, r.Header.Get("X-UniHub-Timestamp"), r.Header.Get("X-UniHub-Signature"), b, time.Now()); e != nil {
		fail(w, 401, "invalid_signature")
		return
	}
	op, e := s.receive(r.Context(), b, r.Header.Get("X-UniHub-Timestamp"), r.Header.Get("X-UniHub-Signature"))
	if e != nil {
		writeError(w, e)
		return
	}
	_ = respond(w, 202, op)
}
