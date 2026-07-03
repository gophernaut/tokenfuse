// Package pricing loads the embedded model prices and computes per-bucket costs.
//
// Costs are always computed locally because Anthropic's /cost_report groups by
// workspace, not by API key. We derive USD from token counts × prices.
//
// Unknown models are never priced at $0. They use the conservative default
// from config and the caller is expected to log a warning.
package pricing

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/angalor/tokenfuse/internal/config"
	"github.com/angalor/tokenfuse/internal/provider"
)

//go:embed prices.yaml
var embeddedYAML []byte

// modelPrice matches the YAML structure (per million tokens, USD).
type modelPrice struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// Pricer holds the price table and fallback for unknowns.
type Pricer struct {
	table   map[string]modelPrice
	unknown modelPrice
}

// NewPricer loads the embedded prices.yaml and prepares a pricer using the
// given conservative defaults for unknown models.
func NewPricer(unk config.UnknownModelPrice) (*Pricer, error) {
	var raw map[string]modelPrice
	if err := yaml.Unmarshal(embeddedYAML, &raw); err != nil {
		return nil, fmt.Errorf("parse embedded prices.yaml: %w", err)
	}

	// Conservative unknown pricing: use the high input price for all
	// input-side tokens (uncached + creation + reads) to avoid under-billing.
	u := modelPrice{
		Input:      unk.Input,
		Output:     unk.Output,
		CacheRead:  unk.Input, // conservative (reads are cheaper in reality)
		CacheWrite: unk.Input,
	}

	return &Pricer{
		table:   raw,
		unknown: u,
	}, nil
}

// Cost returns the USD cost for the given token counts and whether the
// unknown-model default was used.
func (p *Pricer) Cost(model string, uncached, cached, creation, output int64) (usd float64, usedDefault bool) {
	price, ok := p.table[model]
	usedDefault = !ok
	if usedDefault {
		price = p.unknown
	}

	const perM = 1_000_000.0

	usd += float64(uncached) * price.Input / perM
	usd += float64(cached) * price.CacheRead / perM
	usd += float64(creation) * price.CacheWrite / perM
	usd += float64(output) * price.Output / perM

	return usd, usedDefault
}

// PriceSample fills CostUSD on the sample and returns true if the unknown
// model default was applied (caller should warn).
func (p *Pricer) PriceSample(s *provider.Sample) bool {
	cost, used := p.Cost(s.Model, s.UncachedInput, s.CachedInput, s.CacheCreation, s.Output)
	s.CostUSD = cost
	return used
}
