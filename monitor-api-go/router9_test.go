package main

import "testing"

func TestAggregateRouter9(t *testing.T) {
	// Two days, mixed status and models, zero-token rows alongside tokenised
	// ones — the shape the real dashboard returns.
	reqs := []router9Req{
		{Model: "big-pickle", Timestamp: "2026-08-05T09:09:04Z", Status: "success", Tokens: struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		}{Prompt: 1000, Completion: 50}},
		{Model: "big-pickle", Timestamp: "2026-08-05T16:00:00+07:00", Status: "error"},
		{Model: "claude-sonnet-4.5", Timestamp: "2026-08-04T23:59:59Z", Status: "success"},
	}

	u := aggregateRouter9(reqs)

	if u.TotalRequests != 3 || u.Success != 2 || u.Errors != 1 {
		t.Fatalf("totals = %+v, want 3/2/1", u)
	}
	if u.PromptTokens != 1000 || u.CompletionTokens != 50 {
		t.Fatalf("tokens = %d/%d, want 1000/50", u.PromptTokens, u.CompletionTokens)
	}
	// The second request is 09:09 UTC = 16:09 WIB, same day as the first; the
	// third is 23:59 UTC on the 4th, which is 06:59 WIB on the 5th. Bucketing
	// in WIB must merge the two August 5 requests into one day.
	if len(u.Days) != 1 {
		t.Fatalf("days = %+v, want 1 WIB day (both timestamps fall on Aug 5 WIB)", u.Days)
	}
	if d := u.Days[0]; d.Requests != 3 || d.Success != 2 || d.Errors != 1 {
		t.Errorf("day = %+v, want 3 requests 2 ok 1 err", d)
	}
	if len(u.Models) != 2 {
		t.Fatalf("models = %+v, want 2", u.Models)
	}
	// Sorted by request count desc: big-pickle (2) before claude-sonnet (1).
	if u.Models[0].Model != "big-pickle" {
		t.Errorf("models[0] = %q, want big-pickle", u.Models[0].Model)
	}
}
