package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/angalor/tokenfuse/internal/provider"
)

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify tables exist by doing a simple query
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('samples','breaker_state','events')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("expected 3 core tables, saw %d", count)
	}

	// Verify schema_migrations recorded
	var migCount int
	db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migCount)
	if migCount == 0 {
		t.Error("expected at least one migration recorded")
	}
}

func TestUpsertSample_Idempotent(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(filepath.Join(dir, "test.db"))
	defer db.Close()

	ctx := context.Background()
	s := provider.Sample{
		Provider:      "anthropic",
		KeyID:         "key_123",
		KeyName:       "test-key",
		Model:         "claude-3-5-sonnet",
		BucketStart:   time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		UncachedInput: 1000,
		Output:          200,
		CostUSD:         0.0123,
	}

	if err := db.UpsertSample(ctx, s); err != nil {
		t.Fatal(err)
	}
	// Second upsert with updated numbers should replace
	s.Output = 250
	s.CostUSD = 0.015
	if err := db.UpsertSample(ctx, s); err != nil {
		t.Fatal(err)
	}

	// Verify latest value
	var out int64
	var cost float64
	err := db.QueryRowContext(ctx, `
		SELECT output, cost_usd FROM samples
		WHERE provider=? AND key_id=? AND model=? AND bucket_start=?
	`, "anthropic", "key_123", "claude-3-5-sonnet", s.BucketStart.Format(time.RFC3339)).Scan(&out, &cost)
	if err != nil {
		t.Fatal(err)
	}
	if out != 250 || cost != 0.015 {
		t.Errorf("upsert did not replace: got output=%d cost=%f", out, cost)
	}
}

func TestBreakerState_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(filepath.Join(dir, "test.db"))
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	bs := BreakerState{
		Provider:   "anthropic",
		KeyID:      "key_abc",
		State:      "open",
		TrippedAt:  &now,
		Rule:       "daily_budget",
		EstBurnUSD: 8.61,
	}

	if err := db.SetBreakerState(ctx, bs); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetBreakerState(ctx, "anthropic", "key_abc")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.State != "open" || got.EstBurnUSD != 8.61 {
		t.Fatalf("roundtrip failed: %+v", got)
	}
}

func TestAppendEvent(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(filepath.Join(dir, "test.db"))
	defer db.Close()

	ctx := context.Background()
	e := Event{
		TS:         time.Now(),
		Provider:   "anthropic",
		KeyID:      "k1",
		Kind:       "trip",
		DetailJSON: `{"rule":"daily_budget","est":8.61}`,
	}
	if err := db.AppendEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
}

func TestGetEvents(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(filepath.Join(dir, "test.db"))
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	_ = db.AppendEvent(ctx, Event{TS: now.Add(-2 * time.Hour), Provider: "anthropic", KeyID: "k1", Kind: "trip", DetailJSON: `{"rule":"budget"}`})
	_ = db.AppendEvent(ctx, Event{TS: now, Provider: "openai", KeyID: "k2", Kind: "poll_error", DetailJSON: `{}`})

	events, err := db.GetEvents(ctx, now.Add(-3*time.Hour), "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}

	// Filter by provider
	events, _ = db.GetEvents(ctx, now.Add(-3*time.Hour), "anthropic", "", 10)
	if len(events) != 1 || events[0].Provider != "anthropic" {
		t.Error("provider filter failed")
	}
}

func TestUpsertSample_ActualCostAndProject(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(filepath.Join(dir, "test.db"))
	defer db.Close()

	ctx := context.Background()
	s := provider.Sample{
		Provider:      "openai",
		KeyID:         "key_oai",
		KeyName:       "oai-key",
		Model:         "gpt-4o",
		BucketStart:   time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		UncachedInput: 1000,
		Output:          200,
		CostUSD:         0.0123,
		ActualCostUSD:   0.0115,
		ProjectID:       "proj_abc",
	}

	if err := db.UpsertSample(ctx, s); err != nil {
		t.Fatal(err)
	}

	var actual, cost float64
	var proj string
	err := db.QueryRowContext(ctx, `
		SELECT actual_cost_usd, cost_usd, project_id FROM samples
		WHERE provider=? AND key_id=?
	`, "openai", "key_oai").Scan(&actual, &cost, &proj)
	if err != nil {
		t.Fatal(err)
	}
	if actual != 0.0115 || cost != 0.0123 || proj != "proj_abc" {
		t.Errorf("actual/project not stored: actual=%f cost=%f proj=%s", actual, cost, proj)
	}
}
