package server

import "testing"

func TestNormalizeQuery(t *testing.T) {
	cases := map[string]string{
		"project%3AAPP+watching%3Ame+is%3Aopen": "project:APP watching:me is:open",
		"project%253AAPP%2Bis%253Aopen":         "project:APP is:open",
		"100% cpu usage":                        "100% cpu usage",
		"  crash   map  ":                       "crash map",
		"label:crash":                           "label:crash",
	}
	for in, want := range cases {
		if got := normalizeQuery(in); got != want {
			t.Errorf("normalizeQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
