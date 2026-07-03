// Package provider defines the core interfaces and types for usage sources
// and enforcement actions. TokenFuse is strictly out-of-band: it never
// proxies requests.
package provider

import (
	"context"
	"time"
)

// Sample is a priced usage bucket for a specific (provider, key, model, time).
// It is the unit of ingestion into the store.
type Sample struct {
	Provider       string
	KeyID          string
	KeyName        string
	Model          string
	BucketStart    time.Time
	UncachedInput  int64
	CachedInput    int64
	CacheCreation  int64
	Output         int64
	CostUSD        float64
	ActualCostUSD  float64 // from provider costs API if available (for verification)
	ProjectID      string // for OpenAI project-based enforcement
}

// Key identifies an API key for enforcement actions.
type Key struct {
	Provider  string
	ID        string
	Name      string
	ProjectID string // OpenAI project for throttling
}

// Receipt captures the result of an enforcement action for audit.
type Receipt struct {
	Success    bool
	StatusCode int
	Body       string // must be sanitized; never contains secrets
}

// UsageSource reads usage data out-of-band from a provider.
// Implementations must be read-only.
type UsageSource interface {
	// Fetch returns usage samples for the half-open interval [starting, ending).
	// The caller is responsible for pricing; sources should return raw token counts
	// when possible. Results should be grouped at the requested granularity.
	Fetch(ctx context.Context, starting, ending time.Time) ([]Sample, error)
}

// Action is the strategy for what to do when a rule fires.
// Trip is called to disable or otherwise penalize a key.
// Arm re-enables a key (with optional reduced budget semantics in half-open).
type Action interface {
	Trip(ctx context.Context, k Key) (Receipt, error)
	Arm(ctx context.Context, k Key) error
}
