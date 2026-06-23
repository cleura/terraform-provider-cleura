package provider

import "testing"

// TestIsRetriableStatus is a unit test: it calls our function directly with made-up
// inputs and checks the outputs. No Terraform, no API, no cluster, no quota needed.
//
// It also acts as a regression guard for the bug where the empty `case 403:` /
// `case 429:` clauses made those statuses return false (Go switch cases don't fall
// through). On the old code this test FAILS; on the fixed code it passes.
func TestIsRetriableStatus(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{403, true}, // Forbidden IP after wake-from-sleep — must retry
		{429, true}, // Too Many Requests — must retry
		{502, true},
		{503, true},
		{504, true},
		{200, false}, // success is not "retriable"
		{404, false}, // not found
		{500, false}, // intentionally not retried
	}

	for _, c := range cases {
		if got := isRetriableStatus(c.status); got != c.want {
			t.Errorf("isRetriableStatus(%d) = %v, want %v", c.status, got, c.want)
		}
	}
}
