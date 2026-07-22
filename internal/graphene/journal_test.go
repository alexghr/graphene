package graphene

import (
	"strings"
	"testing"
)

func TestOperationJournalValidate(t *testing.T) {
	t.Parallel()
	journal, err := newOperationJournal("sync", "/repo/.git", "main", strings.Repeat("a", 40), nil)
	if err != nil {
		t.Fatal(err)
	}
	journal.Refs["refs/heads/main"] = JournalRef{
		Original: RefValue{Exists: true, OID: strings.Repeat("a", 40)},
		Expected: RefValue{Exists: true, OID: strings.Repeat("b", 40)},
	}
	if err := journal.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationJournalRejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	journal, err := newOperationJournal("sync", "/repo/.git", "main", strings.Repeat("a", 40), nil)
	if err != nil {
		t.Fatal(err)
	}
	journal.Version++
	if err := journal.validate(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("validate() = %v, want unsupported version", err)
	}
}

func TestRefDriftIsSortedAndTracksAbsence(t *testing.T) {
	t.Parallel()
	expected := map[string]RefValue{
		"refs/heads/z": {Exists: true, OID: "old-z"},
		"refs/heads/a": {},
	}
	actual := map[string]RefValue{
		"refs/heads/z": {},
		"refs/heads/a": {Exists: true, OID: "new-a"},
	}
	drift := refDrift(expected, actual)
	if len(drift) != 2 || drift[0].Ref != "refs/heads/a" || drift[1].Ref != "refs/heads/z" {
		t.Fatalf("refDrift() = %#v", drift)
	}
	got := formatRefDrift(drift)
	want := "  refs/heads/a: expected absent, found new-a\n  refs/heads/z: expected old-z, found absent"
	if got != want {
		t.Fatalf("formatRefDrift() = %q, want %q", got, want)
	}
}
