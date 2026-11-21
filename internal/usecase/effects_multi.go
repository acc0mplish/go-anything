package usecase

import "github.com/go-anything/go-anything/internal/domain"

// MultiSource combines several effect sources into one. Each case's effects are the webhook calls
// and the row changes together, in that order, so both are compared.
type MultiSource []EffectSource

// Drain collects from every source in turn.
func (m MultiSource) Drain() []domain.Effect {
	var out []domain.Effect
	for _, s := range m {
		if s != nil {
			out = append(out, s.Drain()...)
		}
	}
	return out
}
