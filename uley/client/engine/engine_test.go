package engine

import "testing"

func TestStripThinking(t *testing.T) {
	cases := map[string]string{
		"<think>\n\n</think>\n\nHello": "Hello",
		"<think>hmm</think> Hi":        "Hi",
		"  plain  ":                    "plain",
		"<think>unfinished":            "<think>unfinished",
	}
	for in, want := range cases {
		if got := stripThinking(in); got != want {
			t.Errorf("stripThinking(%q)=%q want %q", in, got, want)
		}
	}
}
