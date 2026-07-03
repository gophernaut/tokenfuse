// Package notify implements the Telegram bot notifier.
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

type Telegram struct {
	token  string
	chatID string
	client *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{
		token:  token,
		chatID: chatID,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// SendTripNotification sends the standard trip alert and includes the exact arm command.
func (t *Telegram) SendTripNotification(ctx context.Context, key provider.Key, rule string, estBurn float64, armCmd string) error {
	text := fmt.Sprintf(
		"🚨 *TokenFuse Trip*\n\nKey: `%s` (%s)\nRule: %s\nEst. burn: $%.2f\n\nTo re-arm:\n`%s`",
		key.Name, key.ID, rule, estBurn, armCmd,
	)

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)

	payload := map[string]interface{}{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram error %s: %s", resp.Status, string(b))
	}
	// Best effort decode ok result
	var res struct {
		OK bool `json:"ok"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if !res.OK {
		return fmt.Errorf("telegram reported not ok")
	}
	return nil
}

// SendWithRetry tries up to 3 times with backoff.
func (t *Telegram) SendWithRetry(ctx context.Context, key provider.Key, rule string, estBurn float64, armCmd string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := t.SendTripNotification(ctx, key, rule, estBurn, armCmd); err == nil {
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
