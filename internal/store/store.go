// Package store provides the SQLite persistence layer for TokenFuse.
// It uses modernc.org/sqlite (pure Go, no CGO) so cross-compilation remains trivial.
//
// All writes are done with idempotency where possible (samples use composite PK).
// breaker_state is the source of truth for whether a key is currently disabled.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/angalor/tokenfuse/internal/provider"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps *sql.DB with TokenFuse-specific helpers.
type DB struct {
	*sql.DB
}

// Open opens (or creates) the database at path and runs migrations.
func Open(path string) (*DB, error) {
	// Use WAL mode and reasonable busy timeout for a long-running poller.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	// Good defaults for a single-writer poller
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &DB{DB: db}
	if err := s.Migrate(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Migrate applies any pending embedded migrations.
func (db *DB) Migrate() error {
	// Ensure migration tracking table
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	);`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // lexical order: 001_..., 002_...

	for _, name := range files {
		// Extract version from leading digits, e.g. "001_init.sql" → 1
		var version int
		_, _ = fmt.Sscanf(name, "%d", &version)
		if version == 0 {
			continue
		}

		var applied int
		err := db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version = ?`, version).Scan(&applied)
		if err == nil {
			continue // already applied
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check migration %d: %w", version, err)
		}

		data, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(data)); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// UpsertSample inserts or replaces a usage sample. The composite primary key
// makes repeated polls for the same bucket idempotent.
func (db *DB) UpsertSample(ctx context.Context, s provider.Sample) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO samples (
			provider, key_id, key_name, model, bucket_start,
			uncached_input, cached_input, cache_creation, output, cost_usd, actual_cost_usd, project_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, key_id, model, bucket_start) DO UPDATE SET
			key_name = excluded.key_name,
			uncached_input = excluded.uncached_input,
			cached_input = excluded.cached_input,
			cache_creation = excluded.cache_creation,
			output = excluded.output,
			cost_usd = excluded.cost_usd,
			actual_cost_usd = excluded.actual_cost_usd,
			project_id = excluded.project_id
	`, s.Provider, s.KeyID, s.KeyName, s.Model, s.BucketStart.Format(time.RFC3339),
		s.UncachedInput, s.CachedInput, s.CacheCreation, s.Output, s.CostUSD, s.ActualCostUSD, s.ProjectID)
	return err
}

// GetBreakerState returns the current breaker record for a key, or nil if none.
func (db *DB) GetBreakerState(ctx context.Context, provider, keyID string) (*BreakerState, error) {
	row := db.QueryRowContext(ctx, `
		SELECT provider, key_id, state, tripped_at, rule, est_burn_usd
		FROM breaker_state
		WHERE provider = ? AND key_id = ?
	`, provider, keyID)

	var bs BreakerState
	var trippedAt sql.NullString
	var est sql.NullFloat64
	if err := row.Scan(&bs.Provider, &bs.KeyID, &bs.State, &trippedAt, &bs.Rule, &est); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if trippedAt.Valid {
		t, _ := time.Parse(time.RFC3339, trippedAt.String)
		bs.TrippedAt = &t
	}
	if est.Valid {
		bs.EstBurnUSD = est.Float64
	}
	return &bs, nil
}

// SetBreakerState persists a breaker transition. Must be called before executing the Action.
func (db *DB) SetBreakerState(ctx context.Context, bs BreakerState) error {
	var tripped interface{}
	if bs.TrippedAt != nil {
		tripped = bs.TrippedAt.UTC().Format(time.RFC3339)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO breaker_state (provider, key_id, state, tripped_at, rule, est_burn_usd)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, key_id) DO UPDATE SET
			state = excluded.state,
			tripped_at = excluded.tripped_at,
			rule = excluded.rule,
			est_burn_usd = excluded.est_burn_usd
	`, bs.Provider, bs.KeyID, bs.State, tripped, bs.Rule, bs.EstBurnUSD)
	return err
}

// BreakerState represents persisted breaker information.
type BreakerState struct {
	Provider   string
	KeyID      string
	State      string // "closed", "open", "half_open"
	TrippedAt  *time.Time
	Rule       string
	EstBurnUSD float64
}

// AppendEvent records an audit event. Events are append-only.
func (db *DB) AppendEvent(ctx context.Context, e Event) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO events (ts, provider, key_id, kind, detail_json)
		VALUES (?, ?, ?, ?, ?)
	`, e.TS.UTC().Format(time.RFC3339), e.Provider, e.KeyID, e.Kind, e.DetailJSON)
	return err
}

// Event is an audit row.
type Event struct {
	ID         int64
	TS         time.Time
	Provider   string
	KeyID      string
	Kind       string // poll_error, warn, trip, trip_action_result, arm, notify_result, ...
	DetailJSON string
}

// KeyStatusRow is a summary row for the status command (Phase 2+).
type KeyStatusRow struct {
	KeyID       string
	KeyName     string
	TodaySpend  float64
	LastBucket  time.Time
	LastCostUSD float64
	Breaker     string // closed / open / half_open
}

// GetStatusSummary returns a simple view of keys seen in samples + their breaker state.
// For Phase 2 this is basic (sum of all historical cost for the key as proxy for "today").
func (db *DB) GetStatusSummary(ctx context.Context) ([]KeyStatusRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT 
			s.key_id,
			COALESCE(s.key_name, s.key_id),
			COALESCE(SUM(s.cost_usd), 0),
			MAX(s.bucket_start),
			COALESCE(bs.state, 'closed')
		FROM samples s
		LEFT JOIN breaker_state bs ON bs.provider = s.provider AND bs.key_id = s.key_id
		GROUP BY s.provider, s.key_id
		ORDER BY MAX(s.bucket_start) DESC
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KeyStatusRow
	for rows.Next() {
		var r KeyStatusRow
		var lastStr string
		var lastCost sql.NullFloat64
		if err := rows.Scan(&r.KeyID, &r.KeyName, &r.TodaySpend, &lastStr, &r.Breaker); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, lastStr); err == nil {
			r.LastBucket = t
		}
		if lastCost.Valid {
			r.LastCostUSD = lastCost.Float64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetKeySpendSince returns the total cost_usd for samples of (provider, key) with bucket_start >= since.
func (db *DB) GetKeySpendSince(ctx context.Context, provider, keyID string, since time.Time) (float64, error) {
	var total sql.NullFloat64
	err := db.QueryRowContext(ctx, `
		SELECT SUM(cost_usd) FROM samples
		WHERE provider = ? AND key_id = ? AND bucket_start >= ?
	`, provider, keyID, since.Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

// SampleCost is a minimal row for detector calculations.
type SampleCost struct {
	BucketStart time.Time
	CostUSD     float64
}

// GetKeySamplesSince returns ordered samples (by bucket) for a key since a time, for EWMA/budget calc.
func (db *DB) GetKeySamplesSince(ctx context.Context, provider, keyID string, since time.Time) ([]SampleCost, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT bucket_start, cost_usd FROM samples
		WHERE provider = ? AND key_id = ? AND bucket_start >= ?
		ORDER BY bucket_start ASC
	`, provider, keyID, since.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SampleCost
	for rows.Next() {
		var bs string
		var cost float64
		if err := rows.Scan(&bs, &cost); err != nil {
			return nil, err
		}
		t, _ := time.Parse(time.RFC3339, bs)
		out = append(out, SampleCost{BucketStart: t, CostUSD: cost})
	}
	return out, rows.Err()
}

// GetOpenBreakers returns all keys currently in non-closed state.
func (db *DB) GetOpenBreakers(ctx context.Context) ([]BreakerState, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT provider, key_id, state, tripped_at, rule, est_burn_usd
		FROM breaker_state
		WHERE state != 'closed'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BreakerState
	for rows.Next() {
		var bs BreakerState
		var trippedAt sql.NullString
		var est sql.NullFloat64
		if err := rows.Scan(&bs.Provider, &bs.KeyID, &bs.State, &trippedAt, &bs.Rule, &est); err != nil {
			return nil, err
		}
		if trippedAt.Valid {
			t, _ := time.Parse(time.RFC3339, trippedAt.String)
			bs.TrippedAt = &t
		}
		if est.Valid {
			bs.EstBurnUSD = est.Float64
		}
		out = append(out, bs)
	}
	return out, rows.Err()
}

// ExportRow for monthly rollup export.
type ExportRow struct {
	Provider    string
	KeyID       string
	KeyName     string
	Model       string
	TotalCost   float64
	TotalInput  int64
	TotalOutput int64
}

// GetMonthlyExport returns aggregated costs for a month (YYYY-MM prefix on bucket_start).
func (db *DB) GetMonthlyExport(ctx context.Context, yearMonth string) ([]ExportRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT provider, key_id, COALESCE(key_name, key_id), model,
		       SUM(cost_usd), SUM(uncached_input + cached_input + cache_creation), SUM(output)
		FROM samples
		WHERE bucket_start LIKE ?
		GROUP BY provider, key_id, model
		ORDER BY provider, key_id, model
	`, yearMonth+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ExportRow
	for rows.Next() {
		var r ExportRow
		var cost float64
		var inp, outp int64
		if err := rows.Scan(&r.Provider, &r.KeyID, &r.KeyName, &r.Model, &cost, &inp, &outp); err != nil {
			return nil, err
		}
		r.TotalCost = cost
		r.TotalInput = inp
		r.TotalOutput = outp
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetEvents queries the append-only events table for audit/export use cases.
func (db *DB) GetEvents(ctx context.Context, since time.Time, provider, kind string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}

	query := `
		SELECT id, ts, provider, key_id, kind, detail_json
		FROM events
		WHERE ts >= ?
	`
	args := []interface{}{since.Format(time.RFC3339)}

	if provider != "" {
		query += " AND provider = ?"
		args = append(args, provider)
	}
	if kind != "" {
		query += " AND kind = ?"
		args = append(args, kind)
	}
	query += " ORDER BY ts DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var tsStr string
		if err := rows.Scan(&e.ID, &tsStr, &e.Provider, &e.KeyID, &e.Kind, &e.DetailJSON); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			e.TS = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
