// Package config loads and validates tokenfuse.yaml (and the example).
// Validation is strict: missing required environment variables cause startup
// failure. Unknown models are handled at pricing time with a conservative default.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a yaml-friendly wrapper around time.Duration that accepts
// Go duration strings such as "60s", "15m", "24h".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

// OnTrip is the action to take when a budget/anomaly rule fires for a key.
type OnTrip string

const (
	OnTripDeactivate OnTrip = "deactivate"
	OnTripNotifyOnly OnTrip = "notify_only"
)

// KeyRule matches a key (by name glob or exact ID) and defines its budget + action.
type KeyRule struct {
	Match         string            `yaml:"match"`
	DailyBudget   *float64          `yaml:"daily_budget,omitempty"`
	MonthlyBudget *float64          `yaml:"monthly_budget,omitempty"`
	OnTrip        OnTrip            `yaml:"on_trip"`
	Anomaly       *AnomalyDefaults  `yaml:"anomaly,omitempty"` // optional per-key override
}

// Providers holds per-provider admin credentials (by env var name only).
type Providers struct {
	Anthropic struct {
		AdminKeyEnv string `yaml:"admin_key_env"`
	} `yaml:"anthropic"`
	OpenAI struct {
		AdminKeyEnv string `yaml:"admin_key_env"`
	} `yaml:"openai"`
}

// AnomalyDefaults configures the EWMA anomaly detector.
type AnomalyDefaults struct {
	Multiplier float64  `yaml:"multiplier"`
	Sustain    Duration `yaml:"sustain"`
	Warmup     Duration `yaml:"warmup"`
	HalfLife   Duration `yaml:"half_life"` // default 6h if unset
}

// UnknownModelPrice is the conservative fallback price per million tokens
// when a model ID is not present in prices.yaml.
type UnknownModelPrice struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
	// CacheRead and CacheWrite may be added later; v1 uses input/output.
}

// Defaults holds global detector and pricing defaults.
type Defaults struct {
	Anomaly               AnomalyDefaults   `yaml:"anomaly"`
	UnknownModelPricePerMtok UnknownModelPrice `yaml:"unknown_model_price_per_mtok"`
}

// Notify holds notification channel configuration (env var names only).
type Notify struct {
	Telegram struct {
		TokenEnv  string `yaml:"token_env"`
		ChatIDEnv string `yaml:"chat_id_env"`
	} `yaml:"telegram"`
	Webhook struct {
		URL string `yaml:"url"` // direct URL or use env via loader if needed
	} `yaml:"webhook"`
}

// Config is the root configuration.
type Config struct {
	Providers    Providers `yaml:"providers"`
	PollInterval Duration  `yaml:"poll_interval"`
	Lookback     Duration  `yaml:"lookback"`
	Timezone     string    `yaml:"timezone"`
	Defaults     Defaults  `yaml:"defaults"`
	Keys         []KeyRule `yaml:"keys"`
	Notify       Notify    `yaml:"notify"`
}

// Load reads a YAML file and returns a validated Config.
// It does NOT require provider keys to be present in the environment yet
// (that is done at use time), but any *_env referenced must exist.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := c.setDefaults(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) setDefaults() error {
	if c.PollInterval == 0 {
		c.PollInterval = Duration(60 * time.Second)
	}
	if c.Lookback == 0 {
		c.Lookback = Duration(15 * time.Minute)
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if c.Defaults.Anomaly.Multiplier == 0 {
		c.Defaults.Anomaly.Multiplier = 20
	}
	if c.Defaults.Anomaly.Sustain == 0 {
		c.Defaults.Anomaly.Sustain = Duration(3 * time.Minute)
	}
	if c.Defaults.Anomaly.Warmup == 0 {
		c.Defaults.Anomaly.Warmup = Duration(24 * time.Hour)
	}
	if c.Defaults.Anomaly.HalfLife == 0 {
		c.Defaults.Anomaly.HalfLife = Duration(6 * time.Hour)
	}
	if c.Defaults.UnknownModelPricePerMtok.Input == 0 {
		c.Defaults.UnknownModelPricePerMtok.Input = 15.0
	}
	if c.Defaults.UnknownModelPricePerMtok.Output == 0 {
		c.Defaults.UnknownModelPricePerMtok.Output = 75.0
	}

	// Fill defaults for per-key anomaly overrides if partially specified
	for i := range c.Keys {
		if c.Keys[i].Anomaly != nil {
			a := c.Keys[i].Anomaly
			if a.Multiplier == 0 {
				a.Multiplier = c.Defaults.Anomaly.Multiplier
			}
			if a.Sustain == 0 {
				a.Sustain = c.Defaults.Anomaly.Sustain
			}
			if a.Warmup == 0 {
				a.Warmup = c.Defaults.Anomaly.Warmup
			}
			if a.HalfLife == 0 {
				a.HalfLife = c.Defaults.Anomaly.HalfLife
			}
		}
	}
	return nil
}

// Validate performs structural and environment checks.
// It refuses to start if referenced environment variables are unset
// or if the keys section is invalid.
func (c *Config) Validate() error {
	var errs []error

	// Timezone must be loadable
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		errs = append(errs, fmt.Errorf("invalid timezone %q: %w", c.Timezone, err))
	}

	// Poll/ lookback sanity
	if c.PollInterval.Duration() < time.Second {
		errs = append(errs, errors.New("poll_interval must be >= 1s"))
	}
	if c.Lookback.Duration() < time.Minute {
		errs = append(errs, errors.New("lookback must be >= 1m"))
	}

	// Providers: at least anthropic or openai
	hasAnthropic := c.Providers.Anthropic.AdminKeyEnv != ""
	hasOpenAI := c.Providers.OpenAI.AdminKeyEnv != ""
	if !hasAnthropic && !hasOpenAI {
		errs = append(errs, errors.New("at least one provider (anthropic or openai) admin_key_env is required"))
	}
	if hasAnthropic && os.Getenv(c.Providers.Anthropic.AdminKeyEnv) == "" {
		errs = append(errs, fmt.Errorf("environment variable %s (providers.anthropic.admin_key_env) is not set", c.Providers.Anthropic.AdminKeyEnv))
	}
	if hasOpenAI && os.Getenv(c.Providers.OpenAI.AdminKeyEnv) == "" {
		errs = append(errs, fmt.Errorf("environment variable %s (providers.openai.admin_key_env) is not set", c.Providers.OpenAI.AdminKeyEnv))
	}

	// Keys
	if len(c.Keys) == 0 {
		errs = append(errs, errors.New("at least one key rule is required"))
	}
	hasCatchAll := false
	for i, kr := range c.Keys {
		if kr.Match == "" {
			errs = append(errs, fmt.Errorf("keys[%d]: match is required", i))
		}
		if kr.Match == "*" {
			hasCatchAll = true
		}
		if kr.OnTrip != OnTripDeactivate && kr.OnTrip != OnTripNotifyOnly {
			errs = append(errs, fmt.Errorf("keys[%d] (%s): on_trip must be %q or %q", i, kr.Match, OnTripDeactivate, OnTripNotifyOnly))
		}
		if kr.DailyBudget != nil && *kr.DailyBudget < 0 {
			errs = append(errs, fmt.Errorf("keys[%d]: daily_budget must be >= 0", i))
		}
		if kr.MonthlyBudget != nil && *kr.MonthlyBudget < 0 {
			errs = append(errs, fmt.Errorf("keys[%d]: monthly_budget must be >= 0", i))
		}
		if kr.Anomaly != nil {
			if kr.Anomaly.Multiplier <= 0 {
				errs = append(errs, fmt.Errorf("keys[%d]: anomaly.multiplier must be > 0", i))
			}
			if kr.Anomaly.Sustain.Duration() <= 0 {
				errs = append(errs, fmt.Errorf("keys[%d]: anomaly.sustain must be > 0", i))
			}
		}
	}
	if !hasCatchAll {
		// Per spec: "a keys list that leaves any discovered key without at least a notify rule"
		// For startup we require an explicit catch-all for safety in v1.
		errs = append(errs, errors.New(`keys section must contain a catch-all match: "*" (required for safety)`))
	}

	// Notify (telegram required for v1)
	if c.Notify.Telegram.TokenEnv == "" || c.Notify.Telegram.ChatIDEnv == "" {
		errs = append(errs, errors.New("notify.telegram.token_env and chat_id_env are required"))
	} else {
		if os.Getenv(c.Notify.Telegram.TokenEnv) == "" {
			errs = append(errs, fmt.Errorf("environment variable %s (notify.telegram.token_env) is not set", c.Notify.Telegram.TokenEnv))
		}
		if os.Getenv(c.Notify.Telegram.ChatIDEnv) == "" {
			errs = append(errs, fmt.Errorf("environment variable %s (notify.telegram.chat_id_env) is not set", c.Notify.Telegram.ChatIDEnv))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// MustEnv returns the value of name or panics. Only use after Validate.
func MustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		panic("config: required env var " + name + " is empty after validation")
	}
	return v
}
