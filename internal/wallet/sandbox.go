package wallet

import (
	"context"

	"unihub/internal/accounts"
)

// ConfigureSandboxRechargeUsers copies the allowed UniHub user IDs at startup.
// Configure it before serving requests or starting workers. An empty list denies
// new sandbox recharges; account names and finance roles do not grant this right.
func (s *Service) ConfigureSandboxRechargeUsers(userIDs []string) {
	users := make(map[string]struct{}, len(userIDs))
	for _, id := range userIDs {
		if id != "" {
			users[id] = struct{}{}
		}
	}
	s.sandboxRechargeUsers = users
}

func (s *Service) CanSandboxRecharge(actor string) bool {
	_, allowed := s.sandboxRechargeUsers[actor]
	return allowed
}

// RequireSandboxRecharge authorizes the current settlement actor independently
// of the actor persisted on a recharge order.
func (s *Service) RequireSandboxRecharge(ctx context.Context, actor, account string) error {
	if !s.CanSandboxRecharge(actor) {
		return accounts.ErrForbidden
	}
	return s.finance(ctx, s.pool, actor, account)
}
