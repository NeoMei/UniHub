package httpapi

import (
	"context"
	"net/http"

	"unihub/internal/accounts"
	"unihub/internal/wallet"
)

// Both routes are console-only by construction: /admin/v1/* passes the central
// wrapper (which rejects a product origin), and /v1/accounts/{team}/member-transfers
// is deliberately absent from productRouteAllowed/productScopeAllowed, so a
// product token gets 403 before reaching the action. CSRF is enforced by the
// console Handler for every non-GET /v1/ and /admin/v1/ request.

type adminTransferBody struct {
	AccountID string `json:"account_id"`
	ProductID string `json:"product_id"`
	Amount    string `json:"amount"`
	// ActionForm always includes an operator reason; it is not persisted here.
	Reason string `json:"reason"`
}

// superAdminTransferResolve lets only a live super_admin target any account.
// The admin check runs inside the posting transaction via RequireAdminOn, so a
// revoked role cannot slip between the HTTP check and the durable command.
func (s *api) superAdminTransferResolve(actor string, body adminTransferBody) wallet.AdminTransferTarget {
	return func(ctx context.Context, q accounts.Querier) (string, string, error) {
		if s.options.PlatformAdmin == nil {
			return "", "", accounts.ErrForbidden
		}
		if e := s.options.PlatformAdmin.RequireAdminOn(ctx, q, actor, "admins:manage"); e != nil {
			return "", "", e
		}
		if body.AccountID == "" {
			return "", "", wallet.ErrInvalid
		}
		if e := q.QueryRow(ctx, `SELECT 1 FROM accounts WHERE id=$1`, body.AccountID).Scan(new(int)); e != nil {
			return "", "", accounts.ErrNotFound
		}
		return body.AccountID, body.ProductID, nil
	}
}

// memberTransferResolve checks, in the same locked transaction, that the URL
// account is a team owned by the actor and the target user is one of its
// members; the destination is that member's own personal account. The team's
// shared wallet is never the source or destination.
func (s *api) memberTransferResolve(actor, team, targetUser string, body adminTransferBody) wallet.AdminTransferTarget {
	return func(ctx context.Context, q accounts.Querier) (string, string, error) {
		if team == "" || targetUser == "" {
			return "", "", wallet.ErrInvalid
		}
		var role string
		e := q.QueryRow(ctx, `SELECT role FROM account_members WHERE account_id=$1 AND user_id=$2`, team, actor).Scan(&role)
		if e != nil {
			return "", "", accounts.ErrForbidden
		}
		if role != "owner" {
			return "", "", accounts.ErrForbidden
		}
		var kind string
		if e = q.QueryRow(ctx, `SELECT kind FROM accounts WHERE id=$1`, team).Scan(&kind); e != nil || kind != "team" {
			return "", "", accounts.ErrForbidden
		}
		var memberRole string
		if e = q.QueryRow(ctx, `SELECT role FROM account_members WHERE account_id=$1 AND user_id=$2`, team, targetUser).Scan(&memberRole); e != nil {
			return "", "", accounts.ErrForbidden
		}
		var personal string
		e = q.QueryRow(ctx, `SELECT id FROM accounts WHERE kind='personal' AND personal_user_id=$1`, targetUser).Scan(&personal)
		if e != nil || personal == "" {
			return "", "", accounts.ErrForbidden
		}
		return personal, body.ProductID, nil
	}
}

// adminTransferRoute registers the super_admin endpoint on the central wrapper.
// Called from adminRoutes with its console-only `central` wrapper, so a product
// origin is rejected before the action runs.
func (s *api) adminTransferRoute(central func(string, action)) {
	central("POST /admin/v1/transfers", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b adminTransferBody
		if e := decode(w, r, &b); e != nil {
			return e
		}
		op, e := s.wallet.AdminTransfer(r.Context(), u.ID, b.Amount, r.Header.Get("Idempotency-Key"), s.superAdminTransferResolve(u.ID, b))
		if e != nil {
			return e
		}
		return respond(w, 202, op)
	})
}

// memberTransferRoute registers the team-owner endpoint on the plain `add`.
// Console-only is enforced by the missing product-route allowlist entry and the
// explicit originProduct guard; CSRF comes from the console Handler.
func (s *api) memberTransferRoute(add func(string, action)) {
	add("POST /v1/accounts/{account}/member-transfers", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		// A product token must never reach the action even with a broad scope.
		if originProduct(r) != "" {
			return accounts.ErrForbidden
		}
		var b struct {
			UserID    string `json:"user_id"`
			ProductID string `json:"product_id"`
			Amount    string `json:"amount"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		body := adminTransferBody{ProductID: b.ProductID, Amount: b.Amount}
		op, e := s.wallet.AdminTransfer(r.Context(), u.ID, b.Amount, r.Header.Get("Idempotency-Key"), s.memberTransferResolve(u.ID, r.PathValue("account"), b.UserID, body))
		if e != nil {
			return e
		}
		return respond(w, 202, op)
	})
}
