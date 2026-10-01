package graphene

import (
	"bytes"
	"encoding/json"
	"testing"
)

func assertJSONFields(t *testing.T, data []byte, want map[string]string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for name, value := range want {
		var compact bytes.Buffer
		if err := json.Compact(&compact, fields[name]); err != nil {
			t.Fatalf("missing or invalid JSON field %q in %s", name, data)
		}
		if got := compact.String(); got != value {
			t.Errorf("JSON field %q = %s, want %s", name, got, value)
		}
	}
	return fields
}

func TestUnitGraphJSONFieldNames(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	graph := graphOutput{
		SchemaVersion: outputSchemaVersion, CurrentBranch: "stack/one",
		Branches: []branchOutput{{Name: "stack/one", Parent: "main", Commit: "abc", Tracked: true}},
		Pending: publicPending(&Pending{
			Operation: "amend", Branch: "stack/one", ReturnBranch: "stack/one",
			Queue: []RebaseOp{{Top: "stack/two", Onto: "stack/one", Upstream: "def"}},
		}),
	}
	if err := writeJSON(&out, graph); err != nil {
		t.Fatal(err)
	}
	fields := assertJSONFields(t, out.Bytes(), map[string]string{
		"schema_version": "1", "current_branch": `"stack/one"`,
	})
	var branches []json.RawMessage
	if err := json.Unmarshal(fields["branches"], &branches); err != nil || len(branches) != 1 {
		t.Fatalf("branches = %s, error = %v", fields["branches"], err)
	}
	assertJSONFields(t, branches[0], map[string]string{
		"name": `"stack/one"`, "parent": `"main"`, "commit": `"abc"`, "tracked": "true",
	})
	pending := assertJSONFields(t, fields["pending"], map[string]string{
		"operation": `"amend"`, "branch": `"stack/one"`, "return_branch": `"stack/one"`, "phase": `""`,
	})
	var rebases []json.RawMessage
	if err := json.Unmarshal(pending["rebases"], &rebases); err != nil || len(rebases) != 1 {
		t.Fatalf("rebases = %s, error = %v", pending["rebases"], err)
	}
	assertJSONFields(t, rebases[0], map[string]string{
		"branch": `"stack/two"`, "onto": `"stack/one"`, "upstream": `"def"`,
	})

	out.Reset()
	if err := writeJSON(&out, graphOutput{SchemaVersion: outputSchemaVersion, Branches: []branchOutput{}}); err != nil {
		t.Fatal(err)
	}
	assertJSONFields(t, out.Bytes(), map[string]string{
		"schema_version": "1", "current_branch": `""`, "branches": "[]", "pending": "null",
	})
}

func TestUnitPendingJSONEmptyRebasesAndPhase(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"", "conflict"} {
		p := &Pending{Operation: "split", Branch: "stack/one", ReturnBranch: "stack/two"}
		if phase != "" {
			p.Recovery = &recoveryState{Phase: phase}
		}
		var out bytes.Buffer
		if err := writeJSON(&out, publicPending(p)); err != nil {
			t.Fatal(err)
		}
		assertJSONFields(t, out.Bytes(), map[string]string{
			"operation": `"split"`, "branch": `"stack/one"`, "return_branch": `"stack/two"`,
			"phase": `"` + phase + `"`, "rebases": "[]",
		})
	}
}

func TestUnitPushPlanJSONFieldNames(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	app := &App{stdout: &out}
	if err := app.writePushPlanJSON(pushPlan{
		Remote: "origin", Scope: "current branch only (untracked)",
		ForceWithLease: true, DryRun: true, Branches: []string{"topic"},
	}, "topic"); err != nil {
		t.Fatal(err)
	}
	assertJSONFields(t, out.Bytes(), map[string]string{
		"schema_version": "1", "current_branch": `"topic"`, "remote": `"origin"`,
		"scope": `"current branch only (untracked)"`, "atomic": "true",
		"force_with_lease": "true", "dry_run": "true", "branches": `["topic"]`,
	})
}
