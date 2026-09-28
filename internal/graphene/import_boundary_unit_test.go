package graphene

import (
	"strings"
	"testing"
)

func TestUnitSelectImportBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []boundaryEvidence
		want       string
		wantError  string
	}{
		{"one feature commit", []boundaryEvidence{{"parent", 1}}, "parent", ""},
		{"multiple feature commits", []boundaryEvidence{{"parent", 3}}, "parent", ""},
		{"stale local base", []boundaryEvidence{{"local", 35}, {"upstream", 2}}, "upstream", ""},
		{"local base ahead", []boundaryEvidence{{"local", 2}, {"upstream", 5}}, "local", ""},
		{"duplicate evidence", []boundaryEvidence{{"parent", 2}, {"parent", 2}}, "parent", ""},
		{"already on base", []boundaryEvidence{{"older", 2}, {"head", 0}}, "head", ""},
		{"closer evidence resolves old tie", []boundaryEvidence{{"older", 5}, {"other", 5}, {"parent", 2}}, "parent", ""},
		{"unrelated histories", nil, "", "cannot find"},
		{"ambiguous closest boundary", []boundaryEvidence{{"one", 2}, {"other", 2}}, "", "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectImportBoundary("main", tc.candidates)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("boundary = %q, error = %v; want %q", got, err, tc.wantError)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("boundary = %q, error = %v; want %q", got, err, tc.want)
			}
		})
	}
}
