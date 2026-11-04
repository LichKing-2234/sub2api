package service

import (
	"context"
	"errors"
	"fmt"
)

// KiroNativeAttributionAccount resolves the account that usage from the Kiro
// native thin proxy (/kiro) is attributed to.
//
// The native proxy forwards the Kiro protocol raw to kiro2cc, which owns Kiro
// account selection; sub2api never routes to one of its own accounts for this
// path. usage_logs.account_id is NOT NULL, so the row still needs an existing
// account: this helper picks a stable one from the API key's group (or a
// configured pinned account id) purely for billing/statistics attribution.
func (s *GatewayService) KiroNativeAttributionAccount(ctx context.Context, groupID *int64, pinnedAccountID int64) (*Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, errors.New("kiro native: account repository is not configured")
	}
	if pinnedAccountID > 0 {
		account, err := s.accountRepo.GetByID(ctx, pinnedAccountID)
		if err != nil {
			return nil, fmt.Errorf("kiro native: load pinned attribution account %d: %w", pinnedAccountID, err)
		}
		if account == nil {
			return nil, fmt.Errorf("kiro native: pinned attribution account %d not found", pinnedAccountID)
		}
		return account, nil
	}
	if groupID == nil {
		return nil, errors.New("kiro native: api key is not bound to a group; cannot resolve usage attribution account")
	}

	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	if err != nil {
		return nil, fmt.Errorf("kiro native: list accounts of group %d: %w", *groupID, err)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("kiro native: group %d has no schedulable account for usage attribution", *groupID)
	}

	var platform string
	if s.groupRepo != nil {
		if group, groupErr := s.resolveGroupByID(ctx, *groupID); groupErr == nil && group != nil {
			platform = group.Platform
		}
	}
	// Prefer an account on the group's own platform; mixed-scheduling groups may
	// only expose foreign-platform accounts, so fall back to any schedulable one.
	for i := range accounts {
		if platform != "" && accounts[i].Platform != platform {
			continue
		}
		if accounts[i].IsSchedulable() {
			return &accounts[i], nil
		}
	}
	for i := range accounts {
		if accounts[i].IsSchedulable() {
			return &accounts[i], nil
		}
	}
	return nil, fmt.Errorf("kiro native: group %d has no schedulable account for usage attribution", *groupID)
}
