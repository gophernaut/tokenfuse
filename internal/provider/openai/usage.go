package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/angalor/tokenfuse/internal/provider"
)

// completionsUsageResponse for /organization/usage/completions
type completionsUsageResponse struct {
	Object  string `json:"object"`
	Data    []struct {
		StartTime int64 `json:"start_time"`
		Results   []struct {
			ProjectID           string `json:"project_id"`
			APIKeyID            string `json:"api_key_id"`
			Model               string `json:"model"`
			PromptTokens        int64  `json:"prompt_tokens"`
			CompletionTokens    int64  `json:"completion_tokens"`
			TotalTokens         int64  `json:"total_tokens"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

// UsageSource for OpenAI.
type UsageSource struct {
	c *Client
}

func NewUsageSource(adminKey string, hc *http.Client) *UsageSource {
	return &UsageSource{c: NewClient(adminKey, hc)}
}

func (u *UsageSource) Fetch(ctx context.Context, starting, ending time.Time) ([]provider.Sample, error) {
	var all []provider.Sample

	startUnix := starting.Unix()
	// OpenAI uses start_time as unix, and we can loop with page

	page := ""
	for {
		samples, next, err := u.fetchPage(ctx, startUnix, page)
		if err != nil {
			return nil, err
		}
		all = append(all, samples...)
		if next == "" {
			break
		}
		page = next
		// safety
		if len(all) > 10000 {
			break
		}
	}
	return all, nil
}

func (u *UsageSource) fetchPage(ctx context.Context, startTime int64, page string) ([]provider.Sample, string, error) {
	q := url.Values{}
	q.Set("start_time", strconv.FormatInt(startTime, 10))
	q.Set("limit", "100")
	if page != "" {
		q.Set("page", page) // or next_page per some reports
	}
	// group by for per key model
	q.Add("group_by[]", "api_key_id")
	q.Add("group_by[]", "model")
	q.Add("group_by[]", "project_id")

	path := "/organization/usage/completions?" + q.Encode()

	resp, err := u.c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var report completionsUsageResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&report); err != nil {
		return nil, "", fmt.Errorf("decode openai usage: %w", err)
	}

	var out []provider.Sample
	for _, bucket := range report.Data {
		for _, r := range bucket.Results {
			// Use prompt as uncached input approx, completion as output
			s := provider.Sample{
				Provider:      "openai",
				KeyID:         r.APIKeyID,
				KeyName:       r.APIKeyID, // name lookup not implemented for openai v1
				Model:         r.Model,
				BucketStart:   time.Unix(bucket.StartTime, 0).UTC(),
				UncachedInput: r.PromptTokens,
				CachedInput:   0,
				CacheCreation: 0,
				Output:        r.CompletionTokens,
				ProjectID:     r.ProjectID,
				// Cost filled by pricer
			}
			out = append(out, s)
		}
	}

	next := ""
	if report.HasMore {
		next = report.NextPage
	}
	return out, next, nil
}

// costsResponse for /organization/costs
type costsResponse struct {
	Object  string `json:"object"`
	Data    []struct {
		StartTime int64 `json:"start_time"`
		Results   []struct {
			ProjectID string  `json:"project_id"`
			APIKeyID  string  `json:"api_key_id"`
			Amount    float64 `json:"amount"` // USD
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

// FetchCosts returns actual costs for the period (aggregated, often daily).
// Returns map of keyID -> total actual cost for the window.
func (u *UsageSource) FetchCosts(ctx context.Context, starting, ending time.Time) (map[string]float64, error) {
	startUnix := starting.Unix()
	costs := make(map[string]float64)

	page := ""
	for {
		pageCosts, next, err := u.fetchCostsPage(ctx, startUnix, page)
		if err != nil {
			return nil, err
		}
		for k, v := range pageCosts {
			costs[k] += v
		}
		if next == "" {
			break
		}
		page = next
	}
	return costs, nil
}

func (u *UsageSource) fetchCostsPage(ctx context.Context, startTime int64, page string) (map[string]float64, string, error) {
	q := url.Values{}
	q.Set("start_time", strconv.FormatInt(startTime, 10))
	q.Set("limit", "100")
	if page != "" {
		q.Set("page", page)
	}
	q.Add("group_by[]", "api_key_id")

	path := "/organization/costs?" + q.Encode()

	resp, err := u.c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var report costsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&report); err != nil {
		return nil, "", fmt.Errorf("decode openai costs: %w", err)
	}

	costs := make(map[string]float64)
	for _, bucket := range report.Data {
		for _, r := range bucket.Results {
			if r.APIKeyID != "" {
				costs[r.APIKeyID] += r.Amount
			}
		}
	}

	next := ""
	if report.HasMore {
		next = report.NextPage
	}
	return costs, next, nil
}
