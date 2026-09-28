package graphene

import "testing"

func TestUnitValidateSquashStep(t *testing.T) {
	for _, tc := range []struct {
		name, boundary, previous string
		count                    int
		wantError                bool
	}{
		{"bottom uses history", "historical-base", "", 1, false},
		{"adjacent commits", "parent-tip", "parent-tip", 1, false},
		{"parent rewritten", "old-parent", "new-parent", 1, true},
		{"extra commit", "base", "", 2, true},
		{"empty branch", "base", "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSquashStep("topic", tc.boundary, tc.previous, tc.count); (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}
