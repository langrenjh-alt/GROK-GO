package app

import (
	"context"
	"errors"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/gateway"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type AccountStoreAdapter struct {
	Repository store.AccountRepository
	Management *admin.ManagementService
}

func (a AccountStoreAdapter) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	if a.Repository == nil {
		return nil, errors.New("account repository is not configured")
	}
	const pageSize = 500
	var result []domain.Account
	for offset := 0; ; offset += pageSize {
		items, err := a.Repository.ListAccounts(ctx, store.AccountFilter{Pagination: store.Pagination{Limit: pageSize, Offset: offset}})
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if len(items) < pageSize {
			return result, nil
		}
	}
}

func (a AccountStoreAdapter) Credentials(ctx context.Context, id string) (domain.Credentials, error) {
	if a.Management == nil {
		return domain.Credentials{}, errors.New("account management is not configured")
	}
	return a.Management.GetAccountCredentials(ctx, id)
}

func (a AccountStoreAdapter) UpdateAccount(ctx context.Context, account domain.Account) error {
	if a.Repository == nil {
		return errors.New("account repository is not configured")
	}
	return a.Repository.UpdateAccount(ctx, &account)
}

func (a AccountStoreAdapter) SaveOAuthRefresh(ctx context.Context, id string, update upstream.OAuthRefreshUpdate) error {
	if a.Management == nil {
		return errors.New("account management is not configured")
	}
	status := update.Status
	cooldown := update.CooldownUntil
	lastError := update.LastError
	_, err := a.Management.UpdateAccount(ctx, id, admin.UpdateAccountInput{
		Credentials: update.Credentials, Status: &status, CooldownUntil: &cooldown, LastError: &lastError,
	})
	return err
}

type ModelSourceAdapter struct {
	Repository store.ModelRepository
}

func (a ModelSourceAdapter) ListModels(ctx context.Context) ([]domain.ModelSpec, error) {
	if a.Repository == nil {
		return nil, errors.New("model repository is not configured")
	}
	enabled := true
	const pageSize = 500
	var result []domain.ModelSpec
	for offset := 0; ; offset += pageSize {
		items, err := a.Repository.ListModels(ctx, store.ModelFilter{
			Pagination: store.Pagination{Limit: pageSize, Offset: offset},
			Enabled:    &enabled,
		})
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if len(items) < pageSize {
			return result, nil
		}
	}
}

func (a ModelSourceAdapter) ResolveModel(ctx context.Context, name string) (domain.ModelSpec, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.ModelSpec{}, gateway.ErrModelNotFound
	}
	if model, err := a.Repository.GetModel(ctx, name); err == nil {
		if model.Enabled {
			return *model, nil
		}
		return domain.ModelSpec{}, gateway.ErrModelNotFound
	}
	models, err := a.ListModels(ctx)
	if err != nil {
		return domain.ModelSpec{}, err
	}
	for _, model := range models {
		if strings.EqualFold(model.ID, name) || strings.EqualFold(model.UpstreamModel, name) {
			return model, nil
		}
		for _, alias := range model.Aliases {
			if strings.EqualFold(alias, name) {
				return model, nil
			}
		}
	}
	return domain.ModelSpec{}, gateway.ErrModelNotFound
}
