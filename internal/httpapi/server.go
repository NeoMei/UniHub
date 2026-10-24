// Package httpapi exposes the account and wallet API. All business endpoints
// require a verified access token; payment notifications use a separate signature.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/ledger"
	"unihub/internal/modelaccess"
	"unihub/internal/wallet"
)

type Options struct {
	Sandbox          bool
	Merchant         string
	SigningKey       []byte
	Billing          *billing.Service
	ServiceToken     string
	ModelAccess      *modelaccess.Service
	OperatorVerifier auth.Verifier
	ProductVerifiers map[string]auth.Verifier
}
type api struct {
	verifier auth.Verifier
	accounts *accounts.Store
	wallet   *wallet.Service
	options  Options
}
type action func(http.ResponseWriter, *http.Request, accounts.User) error

func New(v auth.Verifier, a *accounts.Store, w *wallet.Service, o Options) http.Handler {
	s := &api{v, a, w, o}
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"status": "ok", "service": "unihub"})
	})
	add := func(pattern string, f action) { m.HandleFunc(pattern, s.authenticated(f)) }
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
		return respond(w, 200, a)
	})
	add("GET /v1/accounts/{account}/wallets", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.wallet.Balances(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
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
		a, e := s.wallet.Transfer(r.Context(), u.ID, r.PathValue("account"), b.ProductID, b.Amount, r.Header.Get("Idempotency-Key"))
		if e != nil {
			return e
		}
		return respond(w, 202, a)
	})
	add("GET /v1/accounts/{account}/operations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.wallet.Operations(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		return respond(w, 200, a)
	})
	add("GET /v1/accounts/{account}/operations/{operation}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		a, e := s.wallet.GetOperation(r.Context(), u.ID, r.PathValue("account"), r.PathValue("operation"))
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
			if !productRouteAllowed(r.Pattern) {
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
