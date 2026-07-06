// cmd/tokenfuse/main.go is the entry point for the TokenFuse binary.
//
// Subcommands (v1):
//   tokenfuse run [--dry-run] [--config tokenfuse.yaml]
//   tokenfuse status
//   tokenfuse trip <key>
//   tokenfuse arm <key> [--budget N]
//   tokenfuse export --month 2026-06 [--format csv]
//   tokenfuse version
//
// Design principles:
// - --dry-run is first-class (full pipeline, no side effects on admin APIs)
// - Admin keys are only read from the environment variables named in config
// - All breaker transitions are persisted before any Action is executed
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"runtime/debug"
	"time"

	"github.com/angalor/tokenfuse/internal/breaker"
	"github.com/angalor/tokenfuse/internal/config"
	"github.com/angalor/tokenfuse/internal/detect"
	"github.com/angalor/tokenfuse/internal/metrics"
	"github.com/angalor/tokenfuse/internal/notify"
	"github.com/angalor/tokenfuse/internal/pricing"
	"github.com/angalor/tokenfuse/internal/provider"
	"github.com/angalor/tokenfuse/internal/provider/anthropic"
	"github.com/angalor/tokenfuse/internal/provider/openai"
	"github.com/angalor/tokenfuse/internal/store"
)

const (
	version = "0.1.0-dev"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "version", "-v", "--version":
		printVersion()
	case "run":
		runCmd(args)
	case "status":
		statusCmd(args)
	case "trip":
		tripCmd(args)
	case "arm":
		armCmd(args)
	case "export":
		exportCmd(args)
	case "simulate":
		simulateCmd(args)
	case "validate":
		validateCmd(args)
	case "events":
		eventsCmd(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `TokenFuse — self-hosted AI API spend circuit breaker (out-of-band)

Usage:
  tokenfuse run [--dry-run] [--config path] [--metrics-addr :9090]
  tokenfuse status [--watch]
  tokenfuse trip <key-id-or-name>
  tokenfuse arm <key-id-or-name> [--budget N]
  tokenfuse export --month YYYY-MM [--format csv|json]
  tokenfuse events --since 2026-06-01 [--format json|csv|table]
  tokenfuse simulate [--days 7]
  tokenfuse validate
  tokenfuse version

Environment:
  All sensitive values are read exclusively from variables named in the config file.
  See tokenfuse.example.yaml.
`)
}

func printVersion() {
	fmt.Printf("tokenfuse %s\n", version)
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		fmt.Printf("  module: %s\n", info.Main.Path)
	}
	fmt.Println("  https://github.com/angalor/tokenfuse")
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "tokenfuse.yaml", "path to config file")
	dryRun := fs.Bool("dry-run", false, "simulate all trips (log only, do not call admin APIs)")
	dbPath := fs.String("db", "tokenfuse.db", "sqlite database path")
	metricsAddr := fs.String("metrics-addr", "", "address to serve Prometheus metrics, e.g. :9090 (disabled by default)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if *dryRun {
		logger = logger.With("dry_run", true)
	}

	logger.Info("starting",
		"dry_run", *dryRun,
		"poll_interval", cfg.PollInterval.Duration(),
		"lookback", cfg.Lookback.Duration(),
		"tz", cfg.Timezone,
		"keys_configured", len(cfg.Keys),
	)

	db, err := store.Open(*dbPath)
	if err != nil {
		logger.Error("open db failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	metricsSrv := metrics.StartServer(*metricsAddr)
	if metricsSrv != nil {
		logger.Info("prometheus metrics enabled", "addr", *metricsAddr)
		defer func() {
			// Best effort shutdown
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = metricsSrv.Shutdown(shutdownCtx)
		}()
	}

	pricer, err := pricing.NewPricer(cfg.Defaults.UnknownModelPricePerMtok)
	if err != nil {
		logger.Error("pricing init failed", "err", err)
		os.Exit(1)
	}

	// Build action per rule (support deactivate/notify_only/throttle/delete)
	actions := buildActions(cfg, *dryRun, logger)

	// Telegram (required in current config)
	tgToken := config.MustEnv(cfg.Notify.Telegram.TokenEnv)
	tgChat := config.MustEnv(cfg.Notify.Telegram.ChatIDEnv)
	notifier := notify.NewTelegram(tgToken, tgChat)

	// Optional generic webhook
	if cfg.Notify.Webhook.URL != "" {
		webhook := notify.NewWebhook(cfg.Notify.Webhook.URL)
		// For simplicity we can wrap or call both; here we just log that it's configured.
		// In a fuller impl we would call multiple notifiers.
		_ = webhook // TODO: call in trip path alongside telegram
		logger.Info("webhook notifier configured", "url", cfg.Notify.Webhook.URL)
	}

	tz, _ := time.LoadLocation(cfg.Timezone)
	det := detect.NewDetector(cfg.Defaults.Anomaly, tz)
	bman := breaker.NewManager(db)

	// Sources
	var anthropicSrc *anthropic.UsageSource
	if cfg.Providers.Anthropic.AdminKeyEnv != "" {
		ak := config.MustEnv(cfg.Providers.Anthropic.AdminKeyEnv)
		anthropicSrc = anthropic.NewUsageSource(ak, nil)
	}
	var openaiSrc *openai.UsageSource
	if cfg.Providers.OpenAI.AdminKeyEnv != "" {
		ok := config.MustEnv(cfg.Providers.OpenAI.AdminKeyEnv)
		openaiSrc = openai.NewUsageSource(ok, nil)  // note: import openai below
	}

	// Boot reconcile for known providers
	if anthropicSrc != nil {
		if err := bman.ReconcileOnBoot(context.Background(), "anthropic", actions["*"]); err != nil {
			logger.Warn("reconcile on boot had issue", "err", err)
		}
	}

	// Main poll + detect loop
	ticker := time.NewTicker(cfg.PollInterval.Duration())
	defer ticker.Stop()

	// Do one immediate tick
	runTick(context.Background(), cfg, anthropicSrc, openaiSrc, pricer, db, det, bman, actions, notifier, logger, tz)

	for {
		select {
		case <-ticker.C:
			// Add jitter (0 to 25% of poll interval) to be nice to provider APIs
			jitter := time.Duration(rand.Float64() * float64(cfg.PollInterval.Duration()) * 0.25)
			time.Sleep(jitter)

			runTick(context.Background(), cfg, anthropicSrc, openaiSrc, pricer, db, det, bman, actions, notifier, logger, tz)
		}
	}
}

// buildActions creates the enforcement strategy per configured key rule.
// Supports "*" catch-all and exact ID/name match (simple for v1).
// on_trip values: deactivate, notify_only (anthropic), throttle, delete (openai opt-in)
func buildActions(cfg *config.Config, dryRun bool, logger *slog.Logger) map[string]provider.Action {
	acts := make(map[string]provider.Action)

	// Anthropic bases
	var baseDeact provider.Action = anthropic.NewDeactivateKey("", nil) // key injected via env in constructor? wait, fixed below if needed
	_ = baseDeact // reconstructed per provider in real, simplified here

	for _, kr := range cfg.Keys {
		var act provider.Action
		switch kr.OnTrip {
		case config.OnTripDeactivate:
			if cfg.Providers.Anthropic.AdminKeyEnv != "" {
				act = anthropic.NewDeactivateKey(config.MustEnv(cfg.Providers.Anthropic.AdminKeyEnv), nil)
			} else if cfg.Providers.OpenAI.AdminKeyEnv != "" {
				// fallback
				act = openai.NewThrottleProject(config.MustEnv(cfg.Providers.OpenAI.AdminKeyEnv), nil)
			} else {
				act = anthropic.NotifyOnly{}
			}
		case "throttle":
			if cfg.Providers.OpenAI.AdminKeyEnv != "" {
				act = openai.NewThrottleProject(config.MustEnv(cfg.Providers.OpenAI.AdminKeyEnv), nil)
			} else {
				act = anthropic.NotifyOnly{}
			}
		case "delete":
			if cfg.Providers.OpenAI.AdminKeyEnv != "" {
				act = openai.NewDeleteKey(config.MustEnv(cfg.Providers.OpenAI.AdminKeyEnv), nil)
			} else {
				act = anthropic.NotifyOnly{}
			}
		default:
			act = anthropic.NotifyOnly{}
		}

		if dryRun {
			act = &dryRunAction{inner: act, logger: logger}
		}
		acts[kr.Match] = act
	}
	return acts
}

// dryRunAction wraps any Action and only logs instead of acting.
type dryRunAction struct {
	inner  provider.Action
	logger *slog.Logger
}

func (d *dryRunAction) Trip(ctx context.Context, k provider.Key) (provider.Receipt, error) {
	d.logger.Info("WOULD TRIP",
		"key_id", k.ID,
		"key_name", k.Name,
		"est_burn", "see event",
	)
	return provider.Receipt{Success: true, StatusCode: 0, Body: "dry-run"}, nil
}

func (d *dryRunAction) Arm(ctx context.Context, k provider.Key) error {
	d.logger.Info("WOULD ARM", "key_id", k.ID, "key_name", k.Name)
	return nil
}

// resolveAction finds the best matching action for a key (exact then "*").
func resolveAction(keyID, keyName string, actions map[string]provider.Action) provider.Action {
	if a, ok := actions[keyID]; ok {
		return a
	}
	if a, ok := actions[keyName]; ok {
		return a
	}
	if a, ok := actions["*"]; ok {
		return a
	}
	return anthropic.NotifyOnly{}
}

func runTick(
	ctx context.Context,
	cfg *config.Config,
	anthropicSrc *anthropic.UsageSource,
	openaiSrc *openai.UsageSource,
	pricer *pricing.Pricer,
	db *store.DB,
	det *detect.Detector,
	bman *breaker.Manager,
	actions map[string]provider.Action,
	notifier *notify.Telegram,
	logger *slog.Logger,
	tz *time.Location,
) {
	end := time.Now()
	start := end.Add(-cfg.Lookback.Duration())

	logger.Debug("tick start", "window_start", start, "window_end", end)

	var allSamples []provider.Sample
	providersPolled := []string{}

	if anthropicSrc != nil {
		s, err := anthropicSrc.Fetch(ctx, start, end)
		if err != nil {
			logger.Error("anthropic poll error", "err", err)
			_ = db.AppendEvent(ctx, store.Event{TS: time.Now(), Kind: "poll_error", DetailJSON: fmt.Sprintf(`{"provider":"anthropic","error":%q}`, err.Error())})
		} else {
			allSamples = append(allSamples, s...)
			providersPolled = append(providersPolled, "anthropic")
		}
	}
	if openaiSrc != nil {
		s, err := openaiSrc.Fetch(ctx, start, end)
		if err != nil {
			logger.Error("openai poll error", "err", err)
			_ = db.AppendEvent(ctx, store.Event{TS: time.Now(), Kind: "poll_error", DetailJSON: fmt.Sprintf(`{"provider":"openai","error":%q}`, err.Error())})
		} else {
			allSamples = append(allSamples, s...)
			providersPolled = append(providersPolled, "openai")

			// Fetch actual costs as cross-check
			if actualCosts, cerr := openaiSrc.FetchCosts(ctx, start, end); cerr == nil {
				for i := range s {
					if ac, ok := actualCosts[s[i].KeyID]; ok {
						s[i].ActualCostUSD = ac
						// Optionally override CostUSD with actual for this provider
						// s[i].CostUSD = ac
					}
				}
				// re-append? since we appended before, update in allSamples too
				for i := range allSamples {
					if allSamples[i].Provider == "openai" {
						if ac, ok := actualCosts[allSamples[i].KeyID]; ok {
							allSamples[i].ActualCostUSD = ac
						}
					}
				}
			}
		}
	}

	unknowns := 0
	for i := range allSamples {
		if pricer.PriceSample(&allSamples[i]) {
			unknowns++
			logger.Warn("unknown model priced conservatively",
				"model", allSamples[i].Model, "key_id", allSamples[i].KeyID, "provider", allSamples[i].Provider,
			)
		}
		if err := db.UpsertSample(ctx, allSamples[i]); err != nil {
			logger.Error("upsert failed", "err", err)
		}
	}
	logger.Info("polled", "samples", len(allSamples), "unknowns", unknowns, "providers", providersPolled)

	// Update Prometheus metrics
	for _, p := range providersPolled {
		metrics.IncPollSuccess(p)
	}

	// Group samples by provider+key
	type keyKey struct{ Prov, Key string }
	byKey := map[keyKey][]store.SampleCost{}
	keyNames := map[keyKey]string{}
	keyProjects := map[keyKey]string{}
	for _, s := range allSamples {
		kk := keyKey{s.Provider, s.KeyID}
		byKey[kk] = append(byKey[kk], store.SampleCost{BucketStart: s.BucketStart, CostUSD: s.CostUSD})
		keyNames[kk] = s.KeyName
		keyProjects[kk] = s.ProjectID
	}

	now := time.Now()
	dayStart := startOfDay(now, tz)
	monthStart := startOfMonth(now, tz)

	for kk, costs := range byKey {
		prov := kk.Prov
		keyID := kk.Key
		keyName := keyNames[kk]
		if keyName == "" {
			keyName = keyID
		}

		daily, _ := db.GetKeySpendSince(ctx, prov, keyID, dayStart)
		monthly, _ := db.GetKeySpendSince(ctx, prov, keyID, monthStart)

		dailyB, monthlyB := findBudgetsForKey(cfg, keyID, keyName)
		_ = resolveRule(cfg, keyID, keyName)

		anOverride := getAnomalyOverride(cfg, keyID, keyName)
		res := det.Evaluate(keyID, costs, daily, monthly, dailyB, monthlyB, now, anOverride)

		if res.ShouldTrip {
			k := provider.Key{Provider: prov, ID: keyID, Name: keyName, ProjectID: keyProjects[kk]}

			if err := bman.Trip(ctx, prov, keyID, res.Rule, res.EstBurnUSD); err != nil {
				logger.Error("failed to persist trip", "err", err)
				continue
			}

			_ = db.AppendEvent(ctx, store.Event{
				TS:         now,
				Provider:   prov,
				KeyID:      keyID,
				Kind:       "trip",
				DetailJSON: fmt.Sprintf(`{"rule":%q,"est_burn_usd":%f}`, res.Rule, res.EstBurnUSD),
			})

			act := resolveAction(keyID, keyName, actions)
			receipt, actErr := act.Trip(ctx, k)

			_ = db.AppendEvent(ctx, store.Event{
				TS:         now,
				Provider:   prov,
				KeyID:      keyID,
				Kind:       "trip_action_result",
				DetailJSON: fmt.Sprintf(`{"success":%v,"status":%d}`, receipt.Success, receipt.StatusCode),
			})

			if actErr != nil {
				logger.Error("action error", "err", actErr)
			}

			armCmd := fmt.Sprintf("tokenfuse arm %s", keyID)
			if notifyErr := notifier.SendWithRetry(ctx, k, res.Rule, res.EstBurnUSD, armCmd); notifyErr != nil {
				logger.Error("notify failed after retries", "err", notifyErr)
				_ = db.AppendEvent(ctx, store.Event{TS: now, Provider: prov, KeyID: keyID, Kind: "notify_result", DetailJSON: `{"success":false}`})
			} else {
				_ = db.AppendEvent(ctx, store.Event{TS: now, Provider: prov, KeyID: keyID, Kind: "notify_result", DetailJSON: `{"success":true}`})
			}

			// Generic webhook if configured
			if cfg.Notify.Webhook.URL != "" {
				wh := notify.NewWebhook(cfg.Notify.Webhook.URL)
				_ = wh.SendWithRetry(ctx, k, res.Rule, res.EstBurnUSD, armCmd)
			}

			logger.Warn("breaker tripped", "provider", prov, "key", keyName, "rule", res.Rule, "est", res.EstBurnUSD)

			metrics.IncTrip(prov, keyID, res.Rule)
		}

		// Always update current gauges for this key (after possible trip)
		currentState, _ := bman.GetState(ctx, prov, keyID)
		stateVal := 0
		switch currentState {
		case breaker.Open:
			stateVal = 1
		case breaker.HalfOpen:
			stateVal = 2
		}
		metrics.UpdateBreakerState(prov, keyID, keyName, stateVal)
		metrics.UpdateDailySpend(prov, keyID, keyName, daily)
		// Rough burn rate from latest sample or average of window
		var recentRate float64
		if len(costs) > 0 {
			recentRate = costs[len(costs)-1].CostUSD // 1m bucket → $/min
		}
		metrics.UpdateBurnRate(prov, keyID, keyName, recentRate)
	}
}

func findBudgetsForKey(cfg *config.Config, keyID, keyName string) (daily, monthly *float64) {
	for _, kr := range cfg.Keys {
		if kr.Match == keyID || kr.Match == keyName || kr.Match == "*" {
			if kr.DailyBudget != nil {
				daily = kr.DailyBudget
			}
			if kr.MonthlyBudget != nil {
				monthly = kr.MonthlyBudget
			}
			if kr.Match != "*" {
				break
			}
		}
	}
	return
}

func resolveRule(cfg *config.Config, keyID, keyName string) string {
	for _, kr := range cfg.Keys {
		if kr.Match == keyID || kr.Match == keyName || kr.Match == "*" {
			return string(kr.OnTrip)
		}
	}
	return "notify_only"
}

func getAnomalyOverride(cfg *config.Config, keyID, keyName string) *config.AnomalyDefaults {
	for _, kr := range cfg.Keys {
		if kr.Match == keyID || kr.Match == keyName || kr.Match == "*" {
			if kr.Anomaly != nil {
				return kr.Anomaly
			}
			// if * , continue to see if more specific, but for simplicity return nil to use global
			if kr.Match != "*" {
				return nil
			}
		}
	}
	return nil
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func startOfMonth(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	y, m, _ := local.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, loc)
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dbPath := fs.String("db", "tokenfuse.db", "sqlite database path")
	watch := fs.Bool("watch", false, "continuously refresh status (press Ctrl-C to exit)")
	interval := fs.Duration("interval", 5*time.Second, "refresh interval for --watch")
	fs.Parse(args)

	runOnce := func() {
		db, err := store.Open(*dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nfailed to open db: %v\n", err)
			return
		}
		defer db.Close()

		rows, err := db.GetStatusSummary(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nstatus query failed: %v\n", err)
			return
		}

		today := time.Now().UTC().Truncate(24 * time.Hour)

		tty := isTTY()
		green := ""
		amber := ""
		red := ""
		reset := ""
		if tty {
			green = "\033[32m"
			amber = "\033[33m"
			red = "\033[31m"
			reset = "\033[0m"
		}

		if *watch {
			fmt.Print("\033[H\033[2J") // clear screen
		}

		fmt.Printf("TokenFuse Status  %s   (interval: %s)\n", time.Now().Format(time.RFC3339), *interval)
		fmt.Println("KEY / NAME                     TODAY $   BURN $/min   LAST UPDATE           STATE")
		fmt.Println("-----------------------------------------------------------------------------------")

		for _, r := range rows {
			daily, _ := db.GetKeySpendSince(context.Background(), "anthropic", r.KeyID, today)

			// approximate burn from last cost (1m sample)
			burn := r.LastCostUSD

			stateColor := ""
			switch r.Breaker {
			case "open":
				stateColor = red
			case "half_open":
				stateColor = amber
			default:
				stateColor = green
			}

			name := r.KeyName
			if len(name) > 26 {
				name = name[:23] + "..."
			}

			fmt.Printf("%-28s %8.2f   %8.2f   %s   %s%s%s\n",
				name, daily, burn,
				r.LastBucket.Format("2006-01-02 15:04"),
				stateColor, r.Breaker, reset,
			)
		}

		if len(rows) == 0 {
			fmt.Println("(no data — start with `tokenfuse run --dry-run`)")
		}
	}

	if *watch {
		t := time.NewTicker(*interval)
		defer t.Stop()
		runOnce()
		for range t.C {
			runOnce()
		}
	} else {
		runOnce()
	}
}

func tripCmd(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: tokenfuse trip <key-id-or-name>")
		os.Exit(2)
	}
	keyArg := args[0]

	// Minimal: load default config + db
	cfg, err := config.Load("tokenfuse.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed (use --config or default): %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open("tokenfuse.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "db open failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	adminKey := config.MustEnv(cfg.Providers.Anthropic.AdminKeyEnv)
	act := anthropic.NewDeactivateKey(adminKey, nil)
	bman := breaker.NewManager(db)

	k := provider.Key{Provider: "anthropic", ID: keyArg, Name: keyArg}
	if err := bman.Trip(context.Background(), "anthropic", keyArg, "manual", 0); err != nil {
		fmt.Fprintf(os.Stderr, "persist trip failed: %v\n", err)
		os.Exit(1)
	}
	if _, err := act.Trip(context.Background(), k); err != nil {
		fmt.Fprintf(os.Stderr, "action failed: %v\n", err)
	}

	fmt.Printf("MANUAL TRIP executed for key=%s\n", keyArg)
}

func armCmd(args []string) {
	fs := flag.NewFlagSet("arm", flag.ExitOnError)
	budget := fs.Float64("budget", 0, "optional reduced budget for first hour after arm")
	fs.Parse(args)

	if len(fs.Args()) < 1 {
		fmt.Fprintln(os.Stderr, "usage: tokenfuse arm <key-id-or-name> [--budget N]")
		os.Exit(2)
	}
	keyArg := fs.Args()[0]

	cfg, err := config.Load("tokenfuse.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open("tokenfuse.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "db open failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	adminKey := config.MustEnv(cfg.Providers.Anthropic.AdminKeyEnv)
	act := anthropic.NewDeactivateKey(adminKey, nil)
	bman := breaker.NewManager(db)

	k := provider.Key{Provider: "anthropic", ID: keyArg, Name: keyArg}
	if err := bman.Arm(context.Background(), "anthropic", keyArg); err != nil {
		fmt.Fprintf(os.Stderr, "persist arm failed: %v\n", err)
		os.Exit(1)
	}
	if err := act.Arm(context.Background(), k); err != nil {
		fmt.Fprintf(os.Stderr, "action arm failed: %v\n", err)
	}

	if *budget > 0 {
		fmt.Printf("MANUAL ARM executed for key=%s (reduced budget hint: %.2f for 1h)\n", keyArg, *budget)
	} else {
		fmt.Printf("MANUAL ARM executed for key=%s\n", keyArg)
	}
}

func exportCmd(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	month := fs.String("month", "", "YYYY-MM")
	format := fs.String("format", "csv", "csv|json")
	dbPath := fs.String("db", "tokenfuse.db", "sqlite path")
	fs.Parse(args)

	if *month == "" {
		fmt.Fprintln(os.Stderr, "--month YYYY-MM is required")
		os.Exit(2)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.GetMonthlyExport(context.Background(), *month)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export query: %v\n", err)
		os.Exit(1)
	}

	switch *format {
	case "json":
		// simple print
		fmt.Println("[")
		for i, r := range rows {
			comma := ","
			if i == len(rows)-1 {
				comma = ""
			}
			fmt.Printf(`  {"provider":%q,"key_id":%q,"key_name":%q,"model":%q,"cost_usd":%f,"input_tokens":%d,"output_tokens":%d}%s`+"\n",
				r.Provider, r.KeyID, r.KeyName, r.Model, r.TotalCost, r.TotalInput, r.TotalOutput, comma)
		}
		fmt.Println("]")
	case "csv":
		fmt.Println("provider,key_id,key_name,model,cost_usd,input_tokens,output_tokens")
		for _, r := range rows {
			fmt.Printf("%s,%s,%s,%s,%.4f,%d,%d\n",
				r.Provider, r.KeyID, r.KeyName, r.Model, r.TotalCost, r.TotalInput, r.TotalOutput)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown format %s\n", *format)
		os.Exit(2)
	}
}

func simulateCmd(args []string) {
	fs := flag.NewFlagSet("simulate", flag.ExitOnError)
	cfgPath := fs.String("config", "tokenfuse.yaml", "config file")
	dbPath := fs.String("db", "tokenfuse.db", "database")
	days := fs.Int("days", 3, "replay last N days of historical samples")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	tz, _ := time.LoadLocation(cfg.Timezone)
	det := detect.NewDetector(cfg.Defaults.Anomaly, tz)

	since := time.Now().Add(-time.Duration(*days) * 24 * time.Hour)

	fmt.Printf("=== Simulation: replaying last %d day(s) ===\n", *days)

	samples, _ := db.GetKeySamplesSince(context.Background(), "anthropic", "", since)

	groups := map[string][]store.SampleCost{}
	for _, s := range samples {
		groups["simulated"] = append(groups["simulated"], s)
	}

	tripCount := 0
	for k, data := range groups {
		res := det.Evaluate(k, data, 0, 0, nil, nil, time.Now(), nil)
		if res.ShouldTrip {
			tripCount++
			fmt.Printf("  WOULD TRIP key=%s rule=%s est=%.2f\n", k, res.Rule, res.EstBurnUSD)
		}
	}
	fmt.Printf("\nHypothetical trips: %d\n", tripCount)
	fmt.Println("Tune your config and re-run to test scenarios.")
}

func validateCmd(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	cfgPath := fs.String("config", "tokenfuse.yaml", "path to config")
	fs.Parse(args)

	_, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Config invalid: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Config is valid")
}

func eventsCmd(args []string) {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	dbPath := fs.String("db", "tokenfuse.db", "sqlite database path")
	sinceStr := fs.String("since", "", "start time (RFC3339 or YYYY-MM-DD)")
	untilStr := fs.String("until", "", "end time (RFC3339 or YYYY-MM-DD)")
	provider := fs.String("provider", "", "filter by provider (anthropic, openai)")
	kind := fs.String("kind", "", "filter by kind (trip, poll_error, etc.)")
	limit := fs.Int("limit", 100, "max events to return")
	format := fs.String("format", "table", "table|json|csv")
	fs.Parse(args)

	if *sinceStr == "" {
		*sinceStr = time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	}

	since, err := parseTime(*sinceStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --since: %v\n", err)
		os.Exit(1)
	}

	var until time.Time
	if *untilStr != "" {
		until, err = parseTime(*untilStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --until: %v\n", err)
			os.Exit(1)
		}
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	events, err := db.GetEvents(context.Background(), since, *provider, *kind, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query failed: %v\n", err)
		os.Exit(1)
	}

	// Simple filter for until if provided
	if !until.IsZero() {
		filtered := events[:0]
		for _, e := range events {
			if e.TS.Before(until) || e.TS.Equal(until) {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(events)
	case "csv":
		fmt.Println("id,ts,provider,key_id,kind,detail_json")
		for _, e := range events {
			fmt.Printf("%d,%s,%s,%s,%s,%q\n", e.ID, e.TS.Format(time.RFC3339), e.Provider, e.KeyID, e.Kind, e.DetailJSON)
		}
	default:
		for _, e := range events {
			fmt.Printf("%s | %s | %s/%s | %s | %s\n",
				e.TS.Format(time.RFC3339),
				e.Kind,
				e.Provider,
				e.KeyID,
				e.DetailJSON,
				"",
			)
		}
	}
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("use RFC3339 or YYYY-MM-DD")
}
