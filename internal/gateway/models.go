package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

var ErrModelNotFound = errors.New("model not found")

type ModelSource interface {
	ListModels(context.Context) ([]domain.ModelSpec, error)
	ResolveModel(context.Context, string) (domain.ModelSpec, error)
}

type StaticModelSource struct {
	mu     sync.RWMutex
	models []domain.ModelSpec
	byName map[string]domain.ModelSpec
}

func NewStaticModelSource(models []domain.ModelSpec) *StaticModelSource {
	source := &StaticModelSource{}
	source.Replace(models)
	return source
}

func (s *StaticModelSource) Replace(models []domain.ModelSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = append([]domain.ModelSpec(nil), models...)
	s.byName = make(map[string]domain.ModelSpec)
	for _, model := range models {
		s.byName[strings.ToLower(model.ID)] = model
		for _, alias := range model.Aliases {
			s.byName[strings.ToLower(alias)] = model
		}
	}
}

func (s *StaticModelSource) ListModels(context.Context) ([]domain.ModelSpec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.ModelSpec, 0, len(s.models))
	for _, model := range s.models {
		if model.Enabled {
			result = append(result, model)
		}
	}
	return result, nil
}

func (s *StaticModelSource) ResolveModel(_ context.Context, name string) (domain.ModelSpec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	model, ok := s.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok || !model.Enabled {
		return domain.ModelSpec{}, ErrModelNotFound
	}
	return model, nil
}
