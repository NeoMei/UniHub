package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/wallet"
)

const productWalletHeader = "X-UniHub-Product-Credit-Wallet"
const legacyNetUpgradeHeader = "X-UniHub-Legacy-Net-Upgrade"

func legacyNetUpgradeRequested(r *http.Request) bool {
	values := r.Header.Values(legacyNetUpgradeHeader)
	return originProduct(r) == "superwagie" && productWalletRequested(r) && len(values) == 1 && values[0] == "1"
}

func requireLegacyNetUpgrade(r *http.Request, q billing.Quote) error {
	if q.ProductCreditUpgrade != nil && q.ProductCreditUpgrade.AppliedCreditAmount != "" &&
		(q.Offer.ProductID != originProduct(r) || !legacyNetUpgradeRequested(r)) {
		return accounts.ErrForbidden
	}
	return nil
}

func productWalletRequested(r *http.Request) bool {
	policy, ok := productPolicy(r)
	values := r.Header.Values(productWalletHeader)
	return ok && policy.ClientID != "" && policy.ProductID == originProduct(r) &&
		policy.ProductID != "" && policy.ProductID != "llmrouter" &&
		len(values) == 1 && values[0] == billing.ProductCreditScopeV1
}

func sharedWalletProduct(r *http.Request, product string) bool {
	return product != "" && product != "llmrouter" && allowedBillingProduct(r, product)
}

func (s *api) requireProductWallet(r *http.Request) error {
	if !productWalletRequested(r) {
		return accounts.ErrForbidden
	}
	policy, _ := productPolicy(r)
	if !s.hasProductModelRuntime(policy.ProductID, policy.InstanceID) {
		return accounts.ErrForbidden
	}
	ready, err := s.options.Billing.NativeCreditsReadiness(r.Context(), originProduct(r))
	if err != nil {
		return err
	}
	if !ready.Ready || ready.CreditScope != billing.ProductCreditScopeV1 {
		return accounts.ErrForbidden
	}
	return nil
}

type productWalletRenewal struct {
	Enabled   bool   `json:"enabled"`
	Version   int64  `json:"version"`
	OfferID   string `json:"offer_id,omitempty"`
	OfferName string `json:"offer_name,omitempty"`
	Price     string `json:"price,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
	Threshold string `json:"threshold,omitempty"`
}

type productWalletProduct struct {
	ProductID           string               `json:"product_id"`
	Name                string               `json:"name"`
	SubscriptionRenewal productWalletRenewal `json:"subscription_renewal"`
	AddonRenewal        productWalletRenewal `json:"addon_renewal"`
	Orders              []map[string]any     `json:"orders"`
	AddonOffers         []map[string]string  `json:"addon_offers"`
}

func (s *api) readProductWallet(w http.ResponseWriter, r *http.Request, u accounts.User) error {
	if err := s.requireProductWallet(r); err != nil {
		return err
	}
	ctx, account := r.Context(), r.PathValue("account")
	balances, err := s.wallet.BalancesForProducts(ctx, u.ID, account, nil)
	if err != nil {
		return err
	}
	if len(balances) != 1 || balances[0].ProductID != "" || balances[0].Asset != wallet.Asset {
		return billing.ErrConflict
	}
	products, err := s.wallet.Products(ctx)
	if err != nil {
		return err
	}
	allowed := []string{}
	for _, product := range products {
		if sharedWalletProduct(r, product.ID) {
			allowed = append(allowed, product.ID)
		}
	}
	b := s.options.Billing
	orders, err := b.ProductOrders(ctx, u.ID, account, allowed)
	if err != nil {
		return err
	}
	subscriptions, err := b.Subscriptions(ctx, u.ID, account)
	if err != nil {
		return err
	}
	settings, err := b.Settings(ctx, u.ID, account)
	if err != nil {
		return err
	}
	addonSettings, err := b.ProductCreditAutoAddonPolicies(ctx, u.ID, account)
	if err != nil {
		return err
	}
	offers, err := b.Catalog(ctx)
	if err != nil {
		return err
	}

	byProduct := map[string]bool{originProduct(r): true}
	for _, subscription := range subscriptions {
		byProduct[subscription.ProductID] = true
	}
	for _, order := range orders {
		byProduct[order.ProductID] = true
	}
	for _, setting := range settings {
		if setting.Purpose == "renewal" {
			byProduct[setting.ProductID] = true
		}
	}
	for _, setting := range addonSettings {
		byProduct[setting.ProductID] = true
	}
	out := []productWalletProduct{}
	for _, product := range products {
		if !byProduct[product.ID] || !sharedWalletProduct(r, product.ID) {
			continue
		}
		item := productWalletProduct{
			ProductID: product.ID, Name: product.Name,
			Orders: []map[string]any{}, AddonOffers: []map[string]string{},
		}
		for _, order := range orders {
			if order.ProductID == item.ProductID {
				row := map[string]any{
					"id": order.ID, "offer_name": order.Offer.Name, "amount": order.Amount,
					"payment_status": order.PaymentStatus, "fulfillment_status": order.FulfillmentStatus,
					"refund_status": order.RefundStatus, "created_at": order.CreatedAt,
				}
				if order.UpgradeReturn != nil && order.UpgradeReturn.Amount != "" {
					row["upgrade_refund_amount"] = order.UpgradeReturn.Amount
				}
				item.Orders = append(item.Orders, row)
			}
		}
		var renewal billing.Authorization
		for _, setting := range settings {
			if setting.ProductID == item.ProductID && setting.Purpose == "renewal" {
				renewal = setting
				item.SubscriptionRenewal.Version = setting.Version
				break
			}
		}
		for _, subscription := range subscriptions {
			if subscription.ProductID == item.ProductID && subscription.Offer.CreditScope == billing.ProductCreditScopeV1 {
				item.SubscriptionRenewal.OfferName = subscription.Offer.Name
				item.SubscriptionRenewal.Price = subscription.Offer.Price
				item.SubscriptionRenewal.Enabled = renewalCoversOffer(renewal, subscription.Offer.Price, time.Now())
				break
			}
		}
		for _, setting := range addonSettings {
			if setting.ProductID == item.ProductID {
				item.AddonRenewal = productWalletRenewal{
					Enabled: setting.Enabled, Version: setting.Version, OfferID: setting.OfferID,
					Trigger: setting.Trigger, Threshold: setting.Threshold,
				}
			}
		}
		for _, offer := range offers {
			if offer.ProductID == item.ProductID && offer.Kind == "addon" && offer.CreditScope == billing.ProductCreditScopeV1 {
				item.AddonOffers = append(item.AddonOffers, map[string]string{"id": offer.ID, "name": offer.Name, "price": offer.Price})
			}
		}
		out = append(out, item)
	}
	policy, _ := productPolicy(r)
	return respond(w, http.StatusOK, map[string]any{
		"version": billing.ProductCreditScopeV1, "account_id": account,
		"cash":     map[string]string{"asset": wallet.Asset, "available": balances[0].Amount},
		"products": out, "can_manage": policy.HasScope("settings:write"),
	})
}

func renewalCoversOffer(renewal billing.Authorization, price string, now time.Time) bool {
	amount, ae := strconv.ParseInt(price, 10, 64)
	limit, le := strconv.ParseInt(renewal.MaxPerOperation, 10, 64)
	total, te := strconv.ParseInt(renewal.MaxTotal, 10, 64)
	used, ue := strconv.ParseInt(renewal.Used, 10, 64)
	return renewal.Enabled && now.Before(renewal.ExpiresAt) && ae == nil && le == nil && te == nil && ue == nil &&
		amount > 0 && limit >= amount && used >= 0 && total >= used && total-used >= amount
}

func (s *api) readProductCredits(w http.ResponseWriter, r *http.Request, u accounts.User) error {
	if err := s.requireProductWallet(r); err != nil {
		return err
	}
	view, err := s.options.Billing.ProductCreditWallet(r.Context(), u.ID, r.PathValue("account"), originProduct(r))
	if err != nil {
		return err
	}
	batches := []map[string]any{}
	for _, batch := range view.Batches {
		kind := "subscription"
		if batch.Kind == "addon" {
			kind = "addon"
		}
		batches = append(batches, map[string]any{
			"id": batch.ID, "account_id": batch.AccountID, "asset": billing.CreditAsset,
			"kind": kind, "source_scope": "product", "product_id": view.ProductID,
			"original": batch.Original, "available": batch.Available, "reserved": batch.Reserved,
			"consumed": batch.Consumed, "expired": batch.Expired, "withdrawn": batch.Withdrawn,
			"starts_at": batch.StartsAt, "expires_at": batch.ExpiresAt,
			"created_at": batch.CreatedAt, "frozen": batch.Frozen,
		})
	}
	projection := map[string]any{
		"product_id": view.ProductID, "credit_scope": view.CreditScope,
		"available": view.Available, "reserved": view.Reserved,
		"active_membership": view.ActiveMembership,
		"batches":           batches,
	}
	if view.MembershipPaidThrough != nil {
		projection["membership_paid_through"] = view.MembershipPaidThrough
	}
	return respond(w, http.StatusOK, projection)
}

func (s *api) setProductWalletRenewal(w http.ResponseWriter, r *http.Request, u accounts.User) error {
	if err := s.requireProductWallet(r); err != nil {
		return err
	}
	product, account := r.PathValue("product"), r.PathValue("account")
	if !sharedWalletProduct(r, product) {
		return accounts.ErrForbidden
	}
	var body struct {
		billing.AutoAddonPolicyRequest
		Kind string `json:"kind"`
	}
	if err := decode(w, r, &body); err != nil {
		return err
	}
	b := s.options.Billing
	switch body.Kind {
	case "subscription":
		if body.OfferID != "" || body.Trigger != "" || body.Threshold != "" {
			return billing.ErrInvalid
		}
		settings := billing.Authorization{ProductID: product, Purpose: "renewal", Enabled: body.Enabled, Version: body.Version}
		if body.Enabled {
			subscriptions, err := b.Subscriptions(r.Context(), u.ID, account)
			if err != nil {
				return err
			}
			for _, subscription := range subscriptions {
				if subscription.ProductID == product && subscription.Offer.CreditScope == billing.ProductCreditScopeV1 {
					settings.MaxPerOperation = subscription.Offer.Price
					break
				}
			}
			if settings.MaxPerOperation == "" {
				return billing.ErrSubscriptionRequired
			}
			settings.MaxTotal = strconv.FormatInt(math.MaxInt64, 10)
			settings.ExpiresAt = time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
		}
		if _, err := b.SetAuthorization(r.Context(), u.ID, account, settings); err != nil {
			return err
		}
	case "addon":
		if _, err := b.SetProductCreditAutoAddon(r.Context(), u.ID, account, product, body.AutoAddonPolicyRequest); err != nil {
			return err
		}
	default:
		return billing.ErrInvalid
	}
	return s.readProductWallet(w, r, u)
}

func (s *api) productCreditRoutes(add func(string, action)) {
	add("GET /v1/accounts/{account}/product-wallet", s.readProductWallet)
	add("GET /v1/accounts/{account}/product-credits", s.readProductCredits)
	add("PUT /v1/accounts/{account}/product-wallet/{product}/renewal", s.setProductWalletRenewal)
}
