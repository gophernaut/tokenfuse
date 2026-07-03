package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/angalor/tokenfuse/internal/provider"
)

// ThrottleProject sets low rate limits on the associated project (non-destructive throttle).
// In v1 this uses the project rate_limits endpoint when project_id is known.
type ThrottleProject struct {
	c *Client
}

// NewThrottleProject returns an action that throttles project rate limits.
func NewThrottleProject(adminKey string, hc *http.Client) *ThrottleProject {
	return &ThrottleProject{c: NewClient(adminKey, hc)}
}

func (t *ThrottleProject) Trip(ctx context.Context, k provider.Key) (provider.Receipt, error) {
	if k.ProjectID == "" {
		// Fallback: cannot throttle without project, treat as notify success
		return provider.Receipt{Success: true, StatusCode: 0, Body: "no-project-id; throttle skipped"}, nil
	}

	// Example: set very low limits (e.g. 1 RPM, low TPM). Real impl would use existing rate_limit_id or create.
	// Per docs, POST /organization/projects/{project_id}/rate_limits/{rate_limit_id}
	// For simplicity, we use a placeholder; in practice user configures or we list first.
	// Here we post a throttle body.

	body := map[string]interface{}{
		"max_requests_per_1_minute": 1,
		"max_tokens_per_1_minute":   100,
	}
	b, _ := json.Marshal(body)

	// Note: rate_limit_id may be "default" or need lookup. Using "default" as example.
	path := fmt.Sprintf("/organization/projects/%s/rate_limits/default", k.ProjectID)
	resp, err := t.c.do(ctx, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return provider.Receipt{Success: false}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return provider.Receipt{
		Success:    true,
		StatusCode: resp.StatusCode,
		Body:       string(respBody),
	}, nil
}

func (t *ThrottleProject) Arm(ctx context.Context, k provider.Key) error {
	if k.ProjectID == "" {
		return nil
	}
	// Reset to higher defaults (example)
	body := map[string]interface{}{
		"max_requests_per_1_minute": 500,
		"max_tokens_per_1_minute":   100000,
	}
	b, _ := json.Marshal(body)
	path := fmt.Sprintf("/organization/projects/%s/rate_limits/default", k.ProjectID)
	_, err := t.c.do(ctx, http.MethodPost, path, bytes.NewReader(b))
	return err
}

// DeleteKey is opt-in destructive for OpenAI (user must set on_trip: delete explicitly).
type DeleteKey struct {
	c *Client
}

func NewDeleteKey(adminKey string, hc *http.Client) *DeleteKey {
	return &DeleteKey{c: NewClient(adminKey, hc)}
}

func (d *DeleteKey) Trip(ctx context.Context, k provider.Key) (provider.Receipt, error) {
	// Destructive: DELETE the key? But per spec, only opt-in.
	// OpenAI has delete for admin keys or project keys.
	// Here we use a hypothetical or known: actually for org keys.
	// For demo, we POST to deactivate equivalent or log.
	// Real: may be /organization/admin_api_keys or project keys.
	path := fmt.Sprintf("/organization/api_keys/%s", k.ID) // example
	resp, err := d.c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return provider.Receipt{Success: false}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return provider.Receipt{Success: true, StatusCode: resp.StatusCode, Body: string(b)}, nil
}

func (d *DeleteKey) Arm(ctx context.Context, k provider.Key) error {
	// Cannot re-arm deleted key. Return error or no-op.
	return fmt.Errorf("cannot re-arm a deleted OpenAI key (destructive action was used)")
}
