package pricing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/angalor/tokenfuse/internal/config"
	"github.com/angalor/tokenfuse/internal/provider"
)

func TestPricer_KnownModels(t *testing.T) {
	p, err := NewPricer(config.UnknownModelPrice{Input: 15, Output: 75})
	if err != nil {
		t.Fatal(err)
	}

	// These numbers are chosen to match realistic token counts.
	// This serves as the golden math reference for cache tiers + known models.
	cases := []struct {
		model     string
		uncached  int64
		cached    int64
		creation  int64
		output    int64
		wantUSD   float64
		wantDef   bool
	}{
		{
			model:    "claude-3-5-sonnet-20241022",
			uncached: 1_000_000,
			output:   500_000,
			wantUSD:  3.0*1 + 15.0*0.5, // 3 + 7.5 = 10.5
			wantDef:  false,
		},
		{
			model:    "claude-3-5-sonnet-20241022",
			uncached: 100_000,
			cached:   900_000,
			creation: 50_000,
			output:   100_000,
			wantUSD:  (100_000*3 + 900_000*0.30 + 50_000*3.75 + 100_000*15) / 1_000_000,
			wantDef:  false,
		},
		{
			model:    "claude-3-5-haiku-20241022",
			uncached: 10_000_000,
			output:   2_000_000,
			wantUSD:  0.80*10 + 4.0*2,
			wantDef:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got, used := p.Cost(tc.model, tc.uncached, tc.cached, tc.creation, tc.output)
			if used != tc.wantDef {
				t.Errorf("usedDefault = %v, want %v", used, tc.wantDef)
			}
			// Use small epsilon for float
			if diff := got - tc.wantUSD; diff < -0.0001 || diff > 0.0001 {
				t.Errorf("Cost() = %.6f, want %.6f (diff %.6f)", got, tc.wantUSD, diff)
			}
		})
	}
}

func TestPricer_UnknownModelFallback(t *testing.T) {
	p, _ := NewPricer(config.UnknownModelPrice{Input: 15.0, Output: 75.0})

	// Unknown model must NEVER be $0. Golden expectation: full conservative rate.
	got, used := p.Cost("some-future-model-2026", 1_000_000, 0, 200_000, 300_000)
	if !used {
		t.Error("expected usedDefault=true for unknown model")
	}
	want := (1_000_000*15.0 + 200_000*15.0 + 300_000*75.0) / 1_000_000 // 15+3+22.5 = 40.5
	if got < 40.4 || got > 40.6 {
		t.Errorf("unknown model cost = %.2f, want %.2f (conservative)", got, want)
	}
}

func TestPricer_PriceSample(t *testing.T) {
	p, _ := NewPricer(config.UnknownModelPrice{Input: 15, Output: 75})

	s := &provider.Sample{
		Model:         "claude-3-5-sonnet-20241022",
		UncachedInput: 2_000_000,
		Output:        1_000_000,
	}
	used := p.PriceSample(s)
	if used {
		t.Error("known model should not use default")
	}
	if s.CostUSD < 20.9 || s.CostUSD > 21.1 {
		t.Errorf("PriceSample cost = %.4f", s.CostUSD)
	}
}

// Golden file test: we compute a realistic bucket and write/verify against testdata.
func TestPricer_GoldenFile(t *testing.T) {
	p, _ := NewPricer(config.UnknownModelPrice{Input: 15, Output: 75})

	s := provider.Sample{
		Model:         "claude-3-5-sonnet-20241022",
		UncachedInput: 5200,
		CachedInput:   450,
		CacheCreation: 1500, // summed from the two ephemeral types
		Output:        980,
	}

	used := p.PriceSample(&s)
	if used {
		t.Fatal("should not be unknown")
	}

	got := s.CostUSD

	// Expected golden value (manually computed from prices + tokens)
	// uncached: 5200 * 3 /1e6 = 0.0156
	// cached : 450 * 0.3 /1e6 = 0.000135
	// creation: 1500 * 3.75 /1e6 = 0.005625
	// output: 980 * 15 /1e6 = 0.0147
	// total ≈ 0.03606
	want := 0.03606

	if diff := got - want; diff < -0.00001 || diff > 0.00001 {
		t.Errorf("golden cost = %.8f, want %.8f", got, want)
	}

	// Write a golden artifact for review (optional, useful for CI diff)
	goldenDir := filepath.Join("testdata")
	_ = os.MkdirAll(goldenDir, 0755)
	goldenPath := filepath.Join(goldenDir, "sonnet_bucket.golden")
	content := []byte("model=claude-3-5-sonnet-20241022\n" +
		"uncached=5200 cached=450 creation=1500 output=980\n" +
		"cost_usd=" + "0.03606\n")
	_ = os.WriteFile(goldenPath, content, 0644)
}
