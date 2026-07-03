package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad_ValidExample(t *testing.T) {
	// Prepare required envs that Validate checks
	t.Setenv("ANTHROPIC_ADMIN_KEY", "sk-ant-admin-test-123")
	t.Setenv("TG_TOKEN", "123456:ABC-DEF")
	t.Setenv("TG_CHAT", "-1001234567890")

	path := filepath.Join("..", "..", "tokenfuse.example.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("tokenfuse.example.yaml not present yet")
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(example) failed: %v", err)
	}

	if cfg.PollInterval.Duration() != 60*time.Second {
		t.Errorf("expected poll_interval 60s, got %s", cfg.PollInterval.Duration())
	}
	if cfg.Lookback.Duration() != 15*time.Minute {
		t.Errorf("expected lookback 15m, got %s", cfg.Lookback.Duration())
	}
	if cfg.Timezone != "Europe/Warsaw" {
		t.Errorf("expected Europe/Warsaw, got %s", cfg.Timezone)
	}
	if len(cfg.Keys) != 2 {
		t.Errorf("expected 2 key rules, got %d", len(cfg.Keys))
	}
	if cfg.Defaults.Anomaly.Multiplier != 20 {
		t.Error("expected default anomaly.multiplier 20")
	}
	// Check per-key anomaly override from updated example
	foundOverride := false
	for _, k := range cfg.Keys {
		if k.Match == "trading-agent" && k.Anomaly != nil && k.Anomaly.Multiplier == 15 {
			foundOverride = true
		}
	}
	if !foundOverride {
		t.Error("expected per-key anomaly override for trading-agent")
	}
}

func TestValidate_MissingEnv(t *testing.T) {
	// Do not set the required envs
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "bad.yaml")
	content := `providers:
  anthropic:
    admin_key_env: ANTHROPIC_ADMIN_KEY
poll_interval: 60s
lookback: 15m
timezone: UTC
defaults:
  anomaly: { multiplier: 20, sustain: 3m, warmup: 24h }
  unknown_model_price_per_mtok: { input: 15, output: 75 }
keys:
  - match: "*"
    monthly_budget: 100
    on_trip: notify_only
notify:
  telegram: { token_env: TG_TOKEN, chat_id_env: TG_CHAT }
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing env vars")
	}
	if !contains(err.Error(), "ANTHROPIC_ADMIN_KEY") {
		t.Errorf("error should mention missing ANTHROPIC_ADMIN_KEY, got: %v", err)
	}
}

func TestValidate_RequiresCatchAll(t *testing.T) {
	t.Setenv("ANTHROPIC_ADMIN_KEY", "sk-ant-admin-test")
	t.Setenv("TG_TOKEN", "tok")
	t.Setenv("TG_CHAT", "chat")

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "no-catch.yaml")
	content := `providers:
  anthropic:
    admin_key_env: ANTHROPIC_ADMIN_KEY
poll_interval: 60s
lookback: 15m
timezone: UTC
defaults:
  anomaly: { multiplier: 20, sustain: 3m, warmup: 24h }
  unknown_model_price_per_mtok: { input: 15, output: 75 }
keys:
  - match: "specific-key"
    daily_budget: 5
    on_trip: deactivate
notify:
  telegram: { token_env: TG_TOKEN, chat_id_env: TG_CHAT }
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil || !contains(err.Error(), "catch-all") {
		t.Fatalf("expected catch-all requirement error, got: %v", err)
	}
}

func TestValidate_InvalidOnTrip(t *testing.T) {
	t.Setenv("ANTHROPIC_ADMIN_KEY", "sk-ant-admin-test")
	t.Setenv("TG_TOKEN", "tok")
	t.Setenv("TG_CHAT", "chat")

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "bad-action.yaml")
	content := `providers:
  anthropic:
    admin_key_env: ANTHROPIC_ADMIN_KEY
poll_interval: 60s
lookback: 15m
timezone: UTC
defaults:
  anomaly: { multiplier: 20, sustain: 3m, warmup: 24h }
  unknown_model_price_per_mtok: { input: 15, output: 75 }
keys:
  - match: "*"
    monthly_budget: 100
    on_trip: delete_key
notify:
  telegram: { token_env: TG_TOKEN, chat_id_env: TG_CHAT }
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err == nil || !contains(err.Error(), "on_trip must be") {
		t.Fatalf("expected on_trip validation error, got: %v", err)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func TestValidate_OpenAIProvider(t *testing.T) {
	t.Setenv("OPENAI_ADMIN_KEY", "sk-admin-test")
	t.Setenv("TG_TOKEN", "tok")
	t.Setenv("TG_CHAT", "chat")

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "openai.yaml")
	content := `providers:
  openai:
    admin_key_env: OPENAI_ADMIN_KEY
poll_interval: 60s
lookback: 15m
timezone: UTC
defaults:
  anomaly: { multiplier: 20, sustain: 3m, warmup: 24h, half_life: 6h }
  unknown_model_price_per_mtok: { input: 15, output: 75 }
keys:
  - match: "*"
    monthly_budget: 100
    on_trip: notify_only
notify:
  telegram: { token_env: TG_TOKEN, chat_id_env: TG_CHAT }
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("expected valid openai config: %v", err)
	}
	if cfg.Providers.OpenAI.AdminKeyEnv == "" {
		t.Error("openai provider not loaded")
	}
}

func TestValidate_PerKeyAnomaly(t *testing.T) {
	t.Setenv("ANTHROPIC_ADMIN_KEY", "sk-ant-admin-test")
	t.Setenv("TG_TOKEN", "tok")
	t.Setenv("TG_CHAT", "chat")

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "perkey.yaml")
	content := `providers:
  anthropic:
    admin_key_env: ANTHROPIC_ADMIN_KEY
poll_interval: 60s
lookback: 15m
timezone: UTC
defaults:
  anomaly: { multiplier: 20, sustain: 3m, warmup: 24h, half_life: 6h }
  unknown_model_price_per_mtok: { input: 15, output: 75 }
keys:
  - match: "special"
    daily_budget: 5
    on_trip: deactivate
    anomaly: { multiplier: 5, sustain: 1m, warmup: 1h, half_life: 1h }
  - match: "*"
    monthly_budget: 100
    on_trip: notify_only
notify:
  telegram: { token_env: TG_TOKEN, chat_id_env: TG_CHAT }
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("per-key anomaly config failed: %v", err)
	}
	if cfg.Keys[0].Anomaly == nil || cfg.Keys[0].Anomaly.Multiplier != 5 {
		t.Error("per-key anomaly override not parsed")
	}
}

func TestValidate_WebhookOptional(t *testing.T) {
	t.Setenv("ANTHROPIC_ADMIN_KEY", "sk-ant-admin-test")
	t.Setenv("TG_TOKEN", "tok")
	t.Setenv("TG_CHAT", "chat")

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "webhook.yaml")
	content := `providers:
  anthropic:
    admin_key_env: ANTHROPIC_ADMIN_KEY
poll_interval: 60s
lookback: 15m
timezone: UTC
defaults:
  anomaly: { multiplier: 20, sustain: 3m, warmup: 24h, half_life: 6h }
  unknown_model_price_per_mtok: { input: 15, output: 75 }
keys:
  - match: "*"
    monthly_budget: 100
    on_trip: notify_only
notify:
  telegram: { token_env: TG_TOKEN, chat_id_env: TG_CHAT }
  webhook: { url: "https://example.com/hook" }
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("webhook should be optional: %v", err)
	}
}
