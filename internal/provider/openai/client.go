package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	apiBase = "https://api.openai.com/v1"
)

// Client for OpenAI Admin API (usage, costs, rate limits).
type Client struct {
	httpClient *http.Client
	adminKey   string
	baseURL    string
}

func NewClient(adminKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		httpClient: hc,
		adminKey:   adminKey,
		baseURL:    apiBase,
	}
}

// newTestClient for tests (same package).
func newTestClient(adminKey, base string, hc *http.Client) *Client {
	c := NewClient(adminKey, hc)
	c.baseURL = base
	return c
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.adminKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tokenfuse/0.1 (+https://github.com/angalor/tokenfuse)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("openai api error: %s: %s", resp.Status, string(b))
	}
	return resp, nil
}
