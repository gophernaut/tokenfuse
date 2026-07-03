package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/angalor/tokenfuse/internal/provider"
)

// DeactivateKey is the v1 enforcement action that calls the Admin API
// to set an API key status to "inactive".
type DeactivateKey struct {
	c *Client
}

// NewDeactivateKey returns an Action that deactivates keys via the admin API.
func NewDeactivateKey(adminKey string, hc *http.Client) *DeactivateKey {
	return &DeactivateKey{c: NewClient(adminKey, hc)}
}

func (d *DeactivateKey) Trip(ctx context.Context, k provider.Key) (provider.Receipt, error) {
	return d.setStatus(ctx, k, "inactive")
}

func (d *DeactivateKey) Arm(ctx context.Context, k provider.Key) error {
	_, err := d.setStatus(ctx, k, "active")
	return err
}

func (d *DeactivateKey) setStatus(ctx context.Context, k provider.Key, status string) (provider.Receipt, error) {
	body := map[string]string{"status": status}
	b, _ := json.Marshal(body)

	resp, err := d.c.do(ctx, http.MethodPost, "/organizations/api_keys/"+k.ID, bytes.NewReader(b))
	if err != nil {
		return provider.Receipt{Success: false}, err
	}
	defer resp.Body.Close()

	// Read limited body for receipt (never contains the admin key)
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	return provider.Receipt{
		Success:    resp.StatusCode >= 200 && resp.StatusCode < 300,
		StatusCode: resp.StatusCode,
		Body:       string(respBody),
	}, nil
}

// NotifyOnly is a no-op action used for dry-run and catch-all notify_only rules.
// It logs at the caller site instead of performing any admin call.
type NotifyOnly struct{}

func (NotifyOnly) Trip(ctx context.Context, k provider.Key) (provider.Receipt, error) {
	return provider.Receipt{Success: true, StatusCode: 0, Body: "dry-run/notify_only"}, nil
}

func (NotifyOnly) Arm(ctx context.Context, k provider.Key) error {
	return nil
}
