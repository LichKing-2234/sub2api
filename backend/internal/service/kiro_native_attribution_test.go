package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type kiroNativeAttributionAccountRepoStub struct {
	AccountRepository
	byGroup []Account
	byID    map[int64]*Account
	listErr error
	getErr  error
}

func (s *kiroNativeAttributionAccountRepoStub) ListSchedulableByGroupID(_ context.Context, _ int64) ([]Account, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]Account(nil), s.byGroup...), nil
}

func (s *kiroNativeAttributionAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.byID[id], nil
}

type kiroNativeAttributionGroupRepoStub struct {
	GroupRepository
	group *Group
}

func (s *kiroNativeAttributionGroupRepoStub) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if s.group != nil && s.group.ID == id {
		return s.group, nil
	}
	return nil, errors.New("group not found")
}

func newKiroNativeAttributionTestService(accountRepo AccountRepository, groupRepo GroupRepository) *GatewayService {
	return NewGatewayService(
		accountRepo, groupRepo,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func kiroNativeSchedulableAccount(id int64, platform string) Account {
	return Account{
		ID:          id,
		Platform:    platform,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
	}
}

func TestKiroNativeAttributionAccountPinned(t *testing.T) {
	pinned := kiroNativeSchedulableAccount(42, PlatformOpenAI)
	pinned.Status = StatusDisabled // pinned accounts are used even when not schedulable
	svc := newKiroNativeAttributionTestService(&kiroNativeAttributionAccountRepoStub{
		byID: map[int64]*Account{42: &pinned},
	}, nil)
	groupID := int64(7)

	account, err := svc.KiroNativeAttributionAccount(context.Background(), &groupID, 42)

	require.NoError(t, err)
	require.Equal(t, int64(42), account.ID)
}

func TestKiroNativeAttributionAccountPrefersGroupPlatform(t *testing.T) {
	foreign := kiroNativeSchedulableAccount(1, PlatformOpenAI)
	preferred := kiroNativeSchedulableAccount(2, PlatformAnthropic)
	svc := newKiroNativeAttributionTestService(&kiroNativeAttributionAccountRepoStub{
		byGroup: []Account{foreign, preferred},
	}, &kiroNativeAttributionGroupRepoStub{group: &Group{ID: 7, Platform: PlatformAnthropic}})
	groupID := int64(7)

	account, err := svc.KiroNativeAttributionAccount(context.Background(), &groupID, 0)

	require.NoError(t, err)
	require.Equal(t, int64(2), account.ID)
}

func TestKiroNativeAttributionAccountFallsBackToAnySchedulable(t *testing.T) {
	foreign := kiroNativeSchedulableAccount(3, PlatformOpenAI)
	svc := newKiroNativeAttributionTestService(&kiroNativeAttributionAccountRepoStub{
		byGroup: []Account{foreign},
	}, &kiroNativeAttributionGroupRepoStub{group: &Group{ID: 7, Platform: PlatformAnthropic}})
	groupID := int64(7)

	account, err := svc.KiroNativeAttributionAccount(context.Background(), &groupID, 0)

	require.NoError(t, err)
	require.Equal(t, int64(3), account.ID)
}

func TestKiroNativeAttributionAccountErrors(t *testing.T) {
	t.Run("no group", func(t *testing.T) {
		svc := newKiroNativeAttributionTestService(&kiroNativeAttributionAccountRepoStub{}, nil)
		_, err := svc.KiroNativeAttributionAccount(context.Background(), nil, 0)
		require.Error(t, err)
	})

	t.Run("no schedulable accounts", func(t *testing.T) {
		svc := newKiroNativeAttributionTestService(&kiroNativeAttributionAccountRepoStub{
			byGroup: []Account{},
		}, nil)
		groupID := int64(7)
		_, err := svc.KiroNativeAttributionAccount(context.Background(), &groupID, 0)
		require.Error(t, err)
	})

	t.Run("all accounts unschedulable", func(t *testing.T) {
		disabled := kiroNativeSchedulableAccount(4, PlatformAnthropic)
		disabled.Schedulable = false
		svc := newKiroNativeAttributionTestService(&kiroNativeAttributionAccountRepoStub{
			byGroup: []Account{disabled},
		}, nil)
		groupID := int64(7)
		_, err := svc.KiroNativeAttributionAccount(context.Background(), &groupID, 0)
		require.Error(t, err)
	})
}
