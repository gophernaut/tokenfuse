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

// Slack posts to incoming webhook URL (from env SLACK_WEBHOOK_URL or similar).
type Slack struct {
	webhookURL string
	client     *http.Client
}

func NewSlack(webhookURL string) *Slack {
	return &Slack{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *Slack) SendTripNotification(ctx context.Context, key provider.Key, rule string, estBurn float64, armCmd string) error {
	text := fmt.Sprintf("🚨 TokenFuse Trip\nKey: %s (%s)\nRule: %s\nEst burn: $%.2f\nRe-arm: %s",
		key.Name, key.ID, rule, estBurn, armCmd)

	payload := map[string]string{"text": text}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack error %s: %s", resp.Status, string(b))
	}
	return nil
}

func (s *Slack) SendWithRetry(ctx context.Context, key provider.Key, rule string, estBurn float64, armCmd string) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		if err := s.SendTripNotification(ctx, key, rule, estBurn, armCmd); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(i+1) * 1500 * time.Millisecond)
	}
	return lastErr
}
