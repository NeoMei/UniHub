package wallet

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
)

// ProductOperations exposes only operations whose immutable posting destination
// proves a permitted product target. Historical unified operations lack product
// attribution and are intentionally visible only to the console.
func (s *Service) ProductOperations(ctx context.Context, actor, account string, products []string) ([]Operation, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	rows, e := s.pool.Query(ctx, `SELECT `+opColumns+`,command FROM wallet_operations WHERE account_id=$1 AND EXISTS (SELECT 1 FROM jsonb_array_elements(command->'postings') AS posting WHERE posting->>'destination'=ANY($2)) ORDER BY created_at DESC,id LIMIT 100`, account, targetAddresses(account, products))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Operation{}
	for rows.Next() {
		var o Operation
		var raw []byte
		if e = rows.Scan(&o.ID, &o.AccountID, &o.Kind, &o.Status, &o.TransactionID, &o.ErrorCode, &o.CreatedAt, &raw); e != nil {
			return nil, e
		}
		if operationTargetAllowed(raw, account, products) {
			out = append(out, o)
		}
	}
	return out, rows.Err()
}
func (s *Service) ProductOperation(ctx context.Context, actor, account, id string, products []string) (Operation, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Operation{}, e
	}
	var raw []byte
	var o Operation
	e := s.pool.QueryRow(ctx, `SELECT `+opColumns+`,command FROM wallet_operations WHERE account_id=$1 AND id=$2`, account, id).Scan(&o.ID, &o.AccountID, &o.Kind, &o.Status, &o.TransactionID, &o.ErrorCode, &o.CreatedAt, &raw)
	if e != nil {
		return Operation{}, dbError(e)
	}
	if !operationTargetAllowed(raw, account, products) {
		return Operation{}, accounts.ErrNotFound
	}
	return o, nil
}
func targetAddresses(account string, products []string) []string {
	out := []string{}
	for _, p := range products {
		if p != "" {
			out = append(out, Address(account, p))
		}
	}
	return out
}
func operationTargetAllowed(raw []byte, account string, products []string) bool {
	var cmd ledger.Command
	if json.Unmarshal(raw, &cmd) != nil || len(cmd.Postings) != 1 {
		return false
	}
	p := cmd.Postings[0]
	prefix := "cash:" + account + ":product:"
	if !strings.HasPrefix(p.Destination, prefix) || p.Asset != Asset {
		return false
	}
	product := strings.TrimPrefix(p.Destination, prefix)
	return product != "" && slices.Contains(products, product) && (p.Source == "world" || p.Source == Address(account, ""))
}

// BalancesForProducts never queries another product's cash address. Empty
// product denotes the same account's unified wallet and is finance-only too.
func (s *Service) BalancesForProducts(ctx context.Context, actor, account string, products []string) ([]Balance, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	all, e := s.Products(ctx)
	if e != nil {
		return nil, e
	}
	out := []Balance{}
	for _, p := range append([]Product{{ID: ""}}, all...) {
		if p.ID != "" && !slices.Contains(products, p.ID) {
			continue
		}
		n, e := s.ledger.Balance(ctx, Address(account, p.ID), Asset)
		if e != nil {
			return nil, e
		}
		out = append(out, Balance{ProductID: p.ID, Asset: Asset, Amount: strconv.FormatInt(n, 10)})
	}
	return out, nil
}
