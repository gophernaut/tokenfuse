// Package detect implements budget and anomaly (EWMA) rules for tripping breakers.
// It operates on already-priced samples from the store.
package detect

import (
	"time"

	"github.com/angalor/tokenfuse/internal/config"
	"github.com/angalor/tokenfuse/internal/store"
)

// RuleResult describes why (or if) a trip should occur.
type RuleResult struct {
	ShouldTrip bool
	Rule       string // "daily_budget", "monthly_budget", "anomaly"
	EstBurnUSD float64
}

// Detector holds config and per-key state for running rules.
type Detector struct {
	Anomaly config.AnomalyDefaults
	TZ      *time.Location

	// in-memory EWMA state per key (keyID -> state)
	ewmas map[string]*ewmaState
}

type ewmaState struct {
	ewma               float64 // $/min baseline
	lastUpdate         time.Time
	consecutiveMinutes int // how many recent minutes the violation held
}

func NewDetector(anomaly config.AnomalyDefaults, tz *time.Location) *Detector {
	if tz == nil {
		tz = time.UTC
	}
	return &Detector{
		Anomaly: anomaly,
		TZ:      tz,
		ewmas:   make(map[string]*ewmaState),
	}
}

// Evaluate runs all applicable rules for a key against its recent samples.
// It returns the first firing rule (or zero value).
// It also updates internal EWMA state.
// dailySpend and monthlySpend should be full calendar sums (queried separately by caller).
// anomalyOverride allows per-key settings from config.
func (d *Detector) Evaluate(keyID string, samples []store.SampleCost, dailySpend, monthlySpend float64, dailyBudget, monthlyBudget *float64, now time.Time, anomalyOverride *config.AnomalyDefaults) RuleResult {
	if len(samples) == 0 {
		return RuleResult{}
	}

	// Budget checks (use pre-queried full day/month)
	if dailyBudget != nil && dailySpend > *dailyBudget {
		latest := samples[len(samples)-1]
		return RuleResult{ShouldTrip: true, Rule: "daily_budget", EstBurnUSD: latest.CostUSD}
	}
	if monthlyBudget != nil && monthlySpend > *monthlyBudget {
		latest := samples[len(samples)-1]
		return RuleResult{ShouldTrip: true, Rule: "monthly_budget", EstBurnUSD: latest.CostUSD}
	}

	// Anomaly logic (use override if provided)
	res := d.evaluateAnomaly(keyID, samples, now, anomalyOverride)
	if res.ShouldTrip {
		return res
	}
	return RuleResult{}
}

// evaluateAnomaly updates EWMA and checks for sustained violation.
func (d *Detector) evaluateAnomaly(keyID string, samples []store.SampleCost, now time.Time, anomalyOverride *config.AnomalyDefaults) RuleResult {
	if len(samples) == 0 {
		return RuleResult{}
	}

	state, ok := d.ewmas[keyID]
	if !ok {
		state = &ewmaState{}
		d.ewmas[keyID] = state
	}

	an := d.Anomaly
	if anomalyOverride != nil {
		an = *anomalyOverride
	}

	// Sort assumed by caller (ascending time). Compute per-bucket rate ($/min)
	// Since buckets are 1m, cost_usd of a bucket ≈ $/min for that minute.
	// We process newest sample(s) for update.

	// Seed or update using latest samples
	alpha := 0.0
	halfLifeMin := an.HalfLife.Duration().Minutes()
	if halfLifeMin > 0 {
		alpha = 0.693147 / halfLifeMin // ln2 / half_life_minutes
	}
	if alpha > 1 {
		alpha = 1
	}
	if alpha <= 0 {
		alpha = 0.01 // fallback
	}

	// Use the most recent sample's cost as instantaneous rate
	latest := samples[len(samples)-1]
	instantRate := latest.CostUSD // for 1m bucket, this is $/min

	if state.lastUpdate.IsZero() || len(samples) == 1 {
		// bootstrap with average of available
		var sum float64
		for _, s := range samples {
			sum += s.CostUSD
		}
		state.ewma = sum / float64(len(samples))
	}

	state.lastUpdate = now

	// Check violation against *current* baseline (before incorporating this sample into baseline)
	mult := an.Multiplier
	if mult <= 0 {
		mult = 20
	}
	violates := instantRate > mult*state.ewma

	// Always update baseline *after* check (incorporate current observation)
	state.ewma = alpha*instantRate + (1-alpha)*state.ewma

	if violates {
		state.consecutiveMinutes++
	} else {
		state.consecutiveMinutes = 0
	}

	sustainMin := int(an.Sustain.Duration().Minutes())
	if sustainMin <= 0 {
		sustainMin = 3
	}

	if state.consecutiveMinutes >= sustainMin {
		return RuleResult{
			ShouldTrip: true,
			Rule:       "anomaly",
			EstBurnUSD: instantRate, // or ewma * mult
		}
	}
	return RuleResult{}
}

// Note: Budget checks are performed in the runner using full day/month queries from store,
// because the lookback window passed to Evaluate may not cover the full day.
// The detector here focuses on the EWMA anomaly part + provides the struct.
