package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/angalor/tokenfuse/internal/provider"
)

// usageReport is the shape returned by /usage_report/messages when grouping by api_key_id + model.
type usageReport struct {
	Data []struct {
		StartingAt string `json:"starting_at"`
		Results    []struct {
			APIKeyID            string `json:"api_key_id"`
			Model               string `json:"model"`
			UncachedInputTokens int64  `json:"uncached_input_tokens"`
			CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
			OutputTokens        int64  `json:"output_tokens"`
			CacheCreation       struct {
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page,omitempty"`
}

// keyListResponse for mapping IDs to human names.
type keyListResponse struct {
	Data []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"data"`
	HasMore bool `json:"has_more"`
}

// UsageSource implements provider.UsageSource for Anthropic.
type UsageSource struct {
	c *Client
}

// NewUsageSource returns a source that can poll usage_report.
func NewUsageSource(adminKey string, hc *http.Client) *UsageSource {
	return &UsageSource{c: NewClient(adminKey, hc)}
}

// newTestUsageSource is for tests.
func newTestUsageSource(adminKey, base string, hc *http.Client) *UsageSource {
	return &UsageSource{c: newTestClient(adminKey, base, hc)}
}

// Fetch implements the UsageSource interface.
// It fetches 1-minute buckets for the [starting, ending) window grouped by
// api_key_id and model. It also does a best-effort name lookup.
func (u *UsageSource) Fetch(ctx context.Context, starting, ending time.Time) ([]provider.Sample, error) {
	keyNames, err := u.listKeyNames(ctx)
	if err != nil {
		// Non-fatal: we can still use IDs as names.
		keyNames = map[string]string{}
	}

	var all []provider.Sample
	page := ""
	for {
		samples, next, err := u.fetchPage(ctx, starting, ending, page)
		if err != nil {
			return nil, err
		}
		for _, s := range samples {
			if name, ok := keyNames[s.KeyID]; ok && name != "" {
				s.KeyName = name
			} else {
				s.KeyName = s.KeyID
			}
			all = append(all, s)
		}
		if next == "" {
			break
		}
		page = next
	}
	return all, nil
}

func (u *UsageSource) listKeyNames(ctx context.Context) (map[string]string, error) {
	resp, err := u.c.do(ctx, http.MethodGet, "/organizations/api_keys?limit=200", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var kl keyListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&kl); err != nil {
		return nil, err
	}

	m := make(map[string]string, len(kl.Data))
	for _, k := range kl.Data {
		m[k.ID] = k.Name
	}
	// TODO: handle has_more pagination for very large orgs (rare for keys).
	return m, nil
}

func (u *UsageSource) fetchPage(ctx context.Context, starting, ending time.Time, page string) ([]provider.Sample, string, error) {
	q := url.Values{}
	q.Set("starting_at", starting.UTC().Format(time.RFC3339))
	q.Set("ending_at", ending.UTC().Format(time.RFC3339))
	q.Set("bucket_width", "1m")
	q.Add("group_by[]", "api_key_id")
	q.Add("group_by[]", "model")
	if page != "" {
		q.Set("page", page)
	}

	path := "/organizations/usage_report/messages?" + q.Encode()

	resp, err := u.c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var report usageReport
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := dec.Decode(&report); err != nil {
		return nil, "", fmt.Errorf("decode usage report: %w", err)
	}

	var out []provider.Sample
	for _, bucket := range report.Data {
		for _, r := range bucket.Results {
			creation := r.CacheCreation.Ephemeral1h + r.CacheCreation.Ephemeral5m
			s := provider.Sample{
				Provider:      "anthropic",
				KeyID:         r.APIKeyID,
				Model:         r.Model,
				BucketStart:   mustParseTime(bucket.StartingAt),
				UncachedInput: r.UncachedInputTokens,
				CachedInput:   r.CacheReadInputTokens,
				CacheCreation: creation,
				Output:        r.OutputTokens,
				// CostUSD filled by caller using pricing.Pricer
			}
			out = append(out, s)
		}
	}

	return out, report.NextPage, nil
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
