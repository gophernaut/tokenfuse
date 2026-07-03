package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/angalor/tokenfuse/internal/provider"
)

// Webhook sends a JSON payload to a generic HTTP endpoint on trips.
type Webhook struct {
	url    string
	client *http.Client
}

func NewWebhook(url string) *Webhook {
	return &Webhook{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type webhookPayload struct {
	Event     string  `json:"event"`
	Provider  string  `json:"provider"`
	KeyID     string  `json:"key_id"`
	KeyName   string  `json:"key_name"`
	Rule      string  `json:"rule"`
	EstBurn   float64 `json:"est_burn_usd"`
	ArmCmd    string  `json:"arm_command"`
	Timestamp string  `json:"timestamp"`
}

func (w *Webhook) SendTripNotification(ctx context.Context, key provider.Key, rule string, estBurn float64, armCmd string) error {
	payload := webhookPayload{
		Event:     "trip",
		Provider:  key.Provider,
		KeyID:     key.ID,
		KeyName:   key.Name,
		Rule:      rule,
		EstBurn:   estBurn,
		ArmCmd:    armCmd,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	b, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook error %s: %s", resp.Status, string(body))
	}
	return nil
}

func (w *Webhook) SendWithRetry(ctx context.Context, key provider.Key, rule string, estBurn float64, armCmd string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := w.SendTripNotification(ctx, key, rule, estBurn, armCmd); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return lastErr
}
