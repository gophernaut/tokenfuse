package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var sampleOpenAIUsage = `{
  "object": "page",
  "data": [{
    "start_time": 1751400000,
    "results": [{
      "project_id": "proj_123",
      "api_key_id": "key_openai_abc",
      "model": "gpt-4o",
      "prompt_tokens": 1200,
      "completion_tokens": 800,
      "total_tokens": 2000
    }]
  }],
  "has_more": false
}`

var sampleOpenAICosts = `{
  "object": "page",
  "data": [{
    "start_time": 1751400000,
    "results": [{
      "project_id": "proj_123",
      "api_key_id": "key_openai_abc",
      "amount": 0.45
    }]
  }],
  "has_more": false
}`

func TestOpenAIUsage_Fetch_Httptest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleOpenAIUsage))
	}))
	defer ts.Close()

	// Use internal test client override
	c := newTestClient("sk-admin-test", ts.URL, nil)
	u := &UsageSource{c: c}

	ctx := context.Background()
	start := time.Unix(1751400000, 0)
	_, err := u.Fetch(ctx, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenAICosts_Fetch_Httptest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleOpenAICosts))
	}))
	defer ts.Close()

	c := newTestClient("sk-admin-test", ts.URL, nil)
	u := &UsageSource{c: c}

	ctx := context.Background()
	start := time.Unix(1751400000, 0)
	costs, err := u.FetchCosts(ctx, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if costs["key_openai_abc"] != 0.45 {
		t.Errorf("expected cost 0.45, got %f", costs["key_openai_abc"])
	}
}
