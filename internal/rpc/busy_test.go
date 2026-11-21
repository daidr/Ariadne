package rpc

import "testing"

func TestBusyAndQuotaTextClassification(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		busy  bool
		quota bool
	}{
		{
			name:  "antigravity individual quota reached",
			text:  "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h17m22s.",
			busy:  false,
			quota: true,
		},
		{
			name:  "team quota reached",
			text:  "Team quota reached. Resets in 45m.",
			busy:  false,
			quota: true,
		},
		{
			name:  "capacity and quota reset",
			text:  "You have exhausted your capacity on this model. Your quota will reset after 2h.",
			busy:  true,
			quota: true,
		},
		{
			name:  "429 with quota reached",
			text:  "Error 429: Individual quota reached. Resets in 1h17m22s.",
			busy:  true,
			quota: true,
		},
		{
			name:  "usage limit reached",
			text:  "Usage limit reached. Resets in 30m.",
			busy:  false,
			quota: true,
		},
		{
			name:  "insufficient quota code",
			text:  "insufficient_quota",
			busy:  false,
			quota: true,
		},
		{
			name:  "transient rate limit",
			text:  "rate limit exceeded",
			busy:  true,
			quota: false,
		},
		{
			name:  "too many requests",
			text:  "too many requests",
			busy:  true,
			quota: false,
		},
		{
			name:  "retrying 429 notice",
			text:  "API error (attempt 1): Error 429. Retrying.",
			busy:  true,
			quota: false,
		},
		{
			name:  "bad request",
			text:  "generating and executing: Error 400, Message: synthetic Gemini request rejected",
			busy:  false,
			quota: false,
		},
		{
			name:  "size limit is not quota",
			text:  "model response exceeds size limit",
			busy:  false,
			quota: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BusyText(tc.text); got != tc.busy {
				t.Fatalf("BusyText(%q) = %v, want %v", tc.text, got, tc.busy)
			}
			if got := QuotaText(tc.text); got != tc.quota {
				t.Fatalf("QuotaText(%q) = %v, want %v", tc.text, got, tc.quota)
			}
		})
	}
}
