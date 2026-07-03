# TokenFuse

> Your agent looped at 2:03 a.m. The breaker tripped at 2:07. Damage: $8.61 — not $412.

TokenFuse is an open-source (MIT), self-hosted circuit breaker for AI API spend.

It is **strictly out-of-band**: it polls provider usage APIs (read-only) and acts through admin APIs. It never sits in your request path. If TokenFuse crashes or is misconfigured, your production agents keep running.

## Supported Providers
- Anthropic (Admin API usage + deactivate)
- OpenAI (Admin API usage/completions + costs + project throttling)

## Non-goals (v1)
- No request proxying
- No automatic re-arm (manual `arm` CLI only)

## Quick start

```bash
cp tokenfuse.example.yaml tokenfuse.yaml
# export the four required variables
export ANTHROPIC_ADMIN_KEY=sk-ant-admin-...
export TG_TOKEN=...
export TG_CHAT=...

go run ./cmd/tokenfuse version
go run ./cmd/tokenfuse run --dry-run --config tokenfuse.yaml
```

See `tokenfuse.example.yaml` for the full contract.

## How it works (high level)

1. Every `poll_interval`, fetch a sliding `lookback` window of 1-minute buckets grouped by `api_key_id` + model.
2. Price every bucket locally using `prices.yaml` (or the conservative `unknown_model_price_per_mtok` default).
3. Upsert into SQLite (idempotent).
4. Run detectors per key:
   - Static budget (daily / monthly in configured timezone)
   - EWMA anomaly (`burn > multiplier × baseline` sustained for `sustain` minutes)
5. On trip: persist `breaker_state` → execute `Action` (e.g. `deactivate`) → notify.
6. `arm` is the only way to re-enable a key (explicit, with optional reduced budget).

## Important caveats

- **Latency, not a force field.** Usage data has a few minutes of lag. This is a seatbelt, not a hard stop.
- **Admin keys are radioactive.** They are read only from the env vars named in config. They never appear in logs, DB, or panics (see tests).
- **Fail safe on money.** Unknown models are priced at the conservative default, never $0.
- **--dry-run is first class.** Use it for the first week.

## CLI

```
tokenfuse run [--dry-run] [--config tokenfuse.yaml] [--metrics-addr :9090]
tokenfuse status [--watch] [--interval 5s]
tokenfuse trip <key-id-or-name>
tokenfuse arm <key-id-or-name> [--budget N]
tokenfuse export --month 2026-06 [--format csv|json] [--db tokenfuse.db]
tokenfuse events --since 2026-06-01 [--kind trip] [--format table|json|csv]
tokenfuse simulate [--days 7] [--config ...]
tokenfuse validate [--config ...]
tokenfuse version
```

- `run --dry-run`: full pipeline simulation (logs WOULD TRIP, no actions).
- `status --watch`: live updating table with burn rates (TTY colors for state).
- `events`: query append-only audit log (great for export/audit).
- `simulate`: replay historical samples to test config changes.
- `validate`: check config + envs without running.
- Metrics: Prometheus endpoint when `--metrics-addr` is set (breaker_state, daily_spend_usd, burn_rate, trips_total, etc.).

`status` output is intended to be screenshot-worthy (aligned columns + TTY colors).

## Configuration

See `tokenfuse.example.yaml` and `internal/config/config.go`.

Key features:
- Multiple providers (anthropic + openai).
- Per-key overrides: `anomaly: { multiplier: 15, sustain: 2m }`.
- Optional webhook: `notify.webhook.url`.
- Timezone-aware budgets, conservative unknown model pricing.
- Validation fails fast on missing envs or missing catch-all `*` rule.

Full example in `tokenfuse.example.yaml`.

## Metrics

Run with `--metrics-addr :9090` to expose `/metrics` for Prometheus/Grafana.
Useful gauges: `tokenfuse_breaker_state`, `tokenfuse_daily_spend_usd`, `tokenfuse_burn_rate_usd_per_min`, counters for trips and polls.

## Configuration Reference (key parts)

```yaml
providers:
  anthropic: { admin_key_env: ANTHROPIC_ADMIN_KEY }
  openai:    { admin_key_env: OPENAI_ADMIN_KEY }  # optional

poll_interval: 60s
lookback: 15m
timezone: Europe/Warsaw

defaults:
  anomaly: { multiplier: 20, sustain: 3m, warmup: 24h, half_life: 6h }
  unknown_model_price_per_mtok: { input: 15.0, output: 75.0 }

keys:
  - match: "trading-agent"
    daily_budget: 10.00
    on_trip: deactivate
    anomaly: { multiplier: 15 }   # per-key override
  - match: "*"
    monthly_budget: 200.00
    on_trip: notify_only

notify:
  telegram: { token_env: TG_TOKEN, chat_id_env: TG_CHAT }
  webhook:  { url: "https://hooks.example.com/..." }  # optional
```

## Threat model (honest)

**TokenFuse can stop:**
- Runaway agent loops
- Keys suddenly burning 20× baseline (sustained)

**TokenFuse cannot stop:**
- Keys never configured
- Spend in the minutes before next poll
- Indirect usage (Bedrock, etc.)
- Post-trip calls (it only prevents future ones)

This is a **seatbelt**, not a force field. Latency is minutes.

## Development & Testing

```bash
go test ./...          # all tests use httptest, no real network
go run ./cmd/tokenfuse validate
go run ./cmd/tokenfuse simulate --days 1
go run ./cmd/tokenfuse run --dry-run --metrics-addr :9090
```

## Systemd Example

```ini
[Unit]
Description=TokenFuse AI spend breaker
After=network.target

[Service]
ExecStart=/usr/local/bin/tokenfuse run --config /etc/tokenfuse.yaml
Restart=always
User=tokenfuse
EnvironmentFile=/etc/tokenfuse.env

[Install]
WantedBy=multi-user.target
```

## License

MIT

## Threat model (honest)

**TokenFuse can stop:**
- Runaway agent loops that keep calling the same expensive model
- Keys that suddenly burn 20× their historical rate

**TokenFuse cannot stop:**
- A key you never configured
- Spend that happens in the 5–15 minutes before the next poll + detection
- Keys used via AWS Bedrock / Vertex / other indirect paths (unless you add sources)
- Anything after the breaker has already tripped (it only prevents further calls)

## Development

```bash
go test ./...
go run ./cmd/tokenfuse run --dry-run
```

## License

MIT
