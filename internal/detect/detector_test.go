package detect

import (
	"testing"
	"time"

	"github.com/angalor/tokenfuse/internal/config"
	"github.com/angalor/tokenfuse/internal/store"
)

func TestDetector_BudgetAndAnomaly(t *testing.T) {
	tz, _ := time.LoadLocation("UTC")
	baseAnomaly := config.AnomalyDefaults{
		Multiplier: 20,
		Sustain:    config.Duration(3 * time.Minute),
		Warmup:     config.Duration(24 * time.Hour),
		HalfLife:   config.Duration(6 * time.Hour),
	}

	now := time.Now().UTC()
	lowSamples := []store.SampleCost{
		{BucketStart: now.Add(-10 * time.Minute), CostUSD: 0.1},
		{BucketStart: now.Add(-9 * time.Minute), CostUSD: 0.1},
	}


	for _, tt := range []struct {
		name          string
		samples       []store.SampleCost
		dailySpend    float64
		monthlySpend  float64
		dailyBudget   *float64
		monthlyBudget *float64
		anomaly       *config.AnomalyDefaults
		wantTrip      bool
		wantRule      string
	}{
		{
			name:         "no trip",
			samples:      lowSamples,
			dailySpend:   1.0,
			monthlySpend: 10.0,
			wantTrip:     false,
		},
		{
			name:        "daily budget trip",
			samples:     lowSamples,
			dailySpend:  15.0,
			dailyBudget: floatPtr(10.0),
			wantTrip:    true,
			wantRule:    "daily_budget",
		},
		{
			name:     "monthly budget trip",
			samples:  lowSamples,
			monthlySpend: 150.0,
			monthlyBudget: floatPtr(100.0),
			wantTrip: true,
			wantRule: "monthly_budget",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// fresh detector per case
			d := NewDetector(baseAnomaly, tz)
			var res RuleResult
			res = d.Evaluate("k1", tt.samples, tt.dailySpend, tt.monthlySpend, tt.dailyBudget, tt.monthlyBudget, now, tt.anomaly)
			if res.ShouldTrip != tt.wantTrip {
				t.Errorf("ShouldTrip = %v, want %v", res.ShouldTrip, tt.wantTrip)
			}
			if tt.wantRule != "" && res.Rule != tt.wantRule {
				t.Errorf("Rule = %s, want %s", res.Rule, tt.wantRule)
			}
		})
	}
}

func floatPtr(f float64) *float64 { return &f }
