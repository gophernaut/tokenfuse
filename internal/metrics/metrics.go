// Package metrics exposes Prometheus metrics for TokenFuse.
// Enable with --metrics-addr :9090 (or similar) on the run command.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// BreakerState reports the current state of each key's breaker.
	// 0 = closed, 1 = open, 2 = half_open
	BreakerState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "tokenfuse",
			Name:      "breaker_state",
			Help:      "Current breaker state per key (0=closed, 1=open, 2=half_open)",
		},
		[]string{"provider", "key_id", "key_name"},
	)

	// DailySpendUSD is the spend for the current calendar day (in config timezone).
	DailySpendUSD = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "tokenfuse",
			Name:      "daily_spend_usd",
			Help:      "Total spend so far today in USD",
		},
		[]string{"provider", "key_id", "key_name"},
	)

	// BurnRateUSDPerMin is the recent burn rate (derived from EWMA or last window).
	BurnRateUSDPerMin = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "tokenfuse",
			Name:      "burn_rate_usd_per_min",
			Help:      "Current estimated burn rate in USD per minute",
		},
		[]string{"provider", "key_id", "key_name"},
	)

	// TripsTotal counts how many times a breaker was tripped.
	TripsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "tokenfuse",
			Name:      "trips_total",
			Help:      "Number of times a breaker has been tripped",
		},
		[]string{"provider", "key_id", "rule"},
	)

	// PollsTotal counts successful usage polls.
	PollsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "tokenfuse",
			Name:      "polls_total",
			Help:      "Number of successful usage poll cycles",
		},
		[]string{"provider"},
	)

	// PollErrorsTotal counts poll failures.
	PollErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "tokenfuse",
			Name:      "poll_errors_total",
			Help:      "Number of failed usage polls",
		},
		[]string{"provider"},
	)

	// LastPollTimestamp is the unix timestamp of the last successful poll.
	LastPollTimestamp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "tokenfuse",
			Name:      "last_poll_timestamp_seconds",
			Help:      "Unix timestamp of the last successful poll",
		},
		[]string{"provider"},
	)
)

func init() {
	prometheus.MustRegister(
		BreakerState,
		DailySpendUSD,
		BurnRateUSDPerMin,
		TripsTotal,
		PollsTotal,
		PollErrorsTotal,
		LastPollTimestamp,
	)
}

// StartServer starts a Prometheus metrics HTTP server on the given address (e.g. ":9090").
// It runs in a background goroutine. Returns the server for graceful shutdown if needed.
func StartServer(addr string) *http.Server {
	if addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		// Ignore error here; main loop will log if needed on shutdown
		_ = srv.ListenAndServe()
	}()

	return srv
}

// UpdateBreakerState sets the gauge for a key's breaker state.
func UpdateBreakerState(provider, keyID, keyName string, state int) {
	BreakerState.WithLabelValues(provider, keyID, keyName).Set(float64(state))
}

// UpdateDailySpend sets today's spend for a key.
func UpdateDailySpend(provider, keyID, keyName string, usd float64) {
	DailySpendUSD.WithLabelValues(provider, keyID, keyName).Set(usd)
}

// UpdateBurnRate sets the current burn rate.
func UpdateBurnRate(provider, keyID, keyName string, usdPerMin float64) {
	BurnRateUSDPerMin.WithLabelValues(provider, keyID, keyName).Set(usdPerMin)
}

// IncTrip increments the trip counter.
func IncTrip(provider, keyID, rule string) {
	TripsTotal.WithLabelValues(provider, keyID, rule).Inc()
}

// IncPollSuccess records a successful poll.
func IncPollSuccess(provider string) {
	PollsTotal.WithLabelValues(provider).Inc()
	LastPollTimestamp.WithLabelValues(provider).Set(float64(time.Now().Unix()))
}

// IncPollError records a failed poll.
func IncPollError(provider string) {
	PollErrorsTotal.WithLabelValues(provider).Inc()
}
