package graphene

import (
	"strings"
	"testing"
)

func TestUnitSelectLegacyBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []boundaryEvidence
		want       string
		wantError  string
	}{
		{"local parent", []boundaryEvidence{{"parent", 1}}, "parent", ""},
		{"stale local base", []boundaryEvidence{{"local", 32}, {"upstream", 1}}, "upstream", ""},
		{"rewritten base", []boundaryEvidence{{"common", 4}, {"fork", 1}}, "fork", ""},
		{"duplicate evidence", []boundaryEvidence{{"parent", 1}, {"parent", 1}}, "parent", ""},
		{"missing history", nil, "", "cannot determine"},
		{"extra commits", []boundaryEvidence{{"parent", 2}}, "", "cannot determine"},
		{"no branch commit", []boundaryEvidence{{"tip", 0}}, "", "cannot determine"},
		{"conflicting evidence", []boundaryEvidence{{"one", 1}, {"other", 1}}, "", "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectLegacyBoundary("stack/one", tc.candidates)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || !strings.Contains(err.Error(), "stack/one") {
					t.Fatalf("boundary = %q, error = %v; want %q identifying stack/one", got, err, tc.wantError)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("boundary = %q, error = %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestUnitBoundaryRequiresFullCommitID(t *testing.T) {
	for _, id := range []string{strings.Repeat("a", 40), strings.Repeat("0123456789abcdef", 4)} {
		if !isFullCommitID(id) {
			t.Errorf("rejected full commit ID %q", id)
		}
	}
	for _, id := range []string{"", "main", "HEAD^", "--all", "abcdef1", strings.Repeat("x", 40), strings.Repeat("a", 41)} {
		if isFullCommitID(id) {
			t.Errorf("accepted non-commit ID %q", id)
		}
	}
}
