package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeClaude55WebSearchRequestBody(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		body       string
		wantChange bool
		wantThink  bool
		wantChoice bool
	}{
		{
			name:       "opus 5.5 Claude Code side-query is normalized",
			model:      "claude-opus-5-5",
			body:       `{"model":"claude-opus-5-5","thinking":{"type":"disabled"},"tool_choice":{"type":"tool","name":"web_search"},"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
			wantChange: true,
		},
		{
			name:       "sonnet 5.5 Claude Code side-query is normalized",
			model:      "claude-sonnet-5-5",
			body:       `{"model":"claude-sonnet-5-5","thinking":{"type":"disabled"},"tool_choice":{"type":"tool","name":"web_search"},"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
			wantChange: true,
		},
		{
			name:       "opus 5.5 adaptive request keeps thinking and auto tool choice",
			model:      "claude-opus-5-5",
			body:       `{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"tool_choice":{"type":"auto"}}`,
			wantChange: false,
			wantThink:  true,
			wantChoice: true,
		},
		{
			name:       "opus 5 (non-5.5 generation) is untouched",
			model:      "claude-opus-5",
			body:       `{"model":"claude-opus-5","thinking":{"type":"disabled"},"tool_choice":{"type":"tool","name":"web_search"}}`,
			wantChange: false,
			wantThink:  true,
			wantChoice: true,
		},
		{
			name:       "haiku 5.5 is left to its own validation rules",
			model:      "claude-haiku-5-5",
			body:       `{"model":"claude-haiku-5-5","thinking":{"type":"disabled"},"tool_choice":{"type":"tool","name":"web_search"}}`,
			wantChange: false,
			wantThink:  true,
			wantChoice: true,
		},
		{
			name:       "empty request is a no-op",
			model:      "claude-opus-5-5",
			body:       `{"model":"claude-opus-5-5"}`,
			wantChange: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := normalizeClaude55WebSearchRequestBody([]byte(tc.body), tc.model)
			if changed != tc.wantChange {
				t.Fatalf("changed = %v, want %v", changed, tc.wantChange)
			}
			if got := gjson.GetBytes(out, "thinking").Exists(); got != tc.wantThink {
				t.Fatalf("thinking exists = %v, want %v (body=%s)", got, tc.wantThink, out)
			}
			if got := gjson.GetBytes(out, "tool_choice").Exists(); got != tc.wantChoice {
				t.Fatalf("tool_choice exists = %v, want %v (body=%s)", got, tc.wantChoice, out)
			}
		})
	}
}
