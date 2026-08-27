package mission

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCreateSliceCheckpointAppendsEvidenceBoundS01WithoutLifecycleMutation(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	contract, err := store.StartObjective(
		"Coordinate one bounded implementation workgraph",
		ObjectiveStartOptions{CorrelationID: "cross-platform-baseline-test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	digest := writeSliceCheckpointEvidence(t, store, record, "S01", nil)
	before, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := CreateSliceCheckpoint(store, record.MissionID, SliceCheckpointOptions{
		Slice: "S01", EvidenceDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.CheckpointCount != 1 || bundle.LatestCheckpoint == nil {
		t.Fatalf("missing S01 checkpoint: %+v", bundle)
	}
	if bundle.LatestCheckpoint.Result != "slice_pass:S01:"+digest {
		t.Fatalf("wrong evidence binding: %+v", bundle.LatestCheckpoint)
	}
	if bundle.ExecutesWork || bundle.ApprovesWork || bundle.MutatesRepositories {
		t.Fatalf("slice checkpoint widened authority: %+v", bundle)
	}

	after, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	before.UpdatedAtUTC = ""
	after.UpdatedAtUTC = ""
	before.Checkpoints = nil
	after.Checkpoints = nil
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("slice checkpoint changed Mission lifecycle:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCreateSliceCheckpointReplayConflictAndOrder(t *testing.T) {
	newMission := func(t *testing.T) (Store, Record) {
		t.Helper()
		store := NewStore(t.TempDir())
		contract, err := store.StartObjective(
			"Coordinate one bounded implementation workgraph",
			ObjectiveStartOptions{CorrelationID: "slice-order-test"},
		)
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.Load(contract.MissionID)
		if err != nil {
			t.Fatal(err)
		}
		return store, record
	}

	t.Run("exact replay is idempotent", func(t *testing.T) {
		store, record := newMission(t)
		digest := writeSliceCheckpointEvidence(t, store, record, "S01", nil)
		options := SliceCheckpointOptions{Slice: "S01", EvidenceDigest: digest}
		first, err := CreateSliceCheckpoint(store, record.MissionID, options)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := CreateSliceCheckpoint(store, record.MissionID, options)
		if err != nil {
			t.Fatal(err)
		}
		if replay.CheckpointCount != first.CheckpointCount {
			t.Fatalf("exact replay appended a checkpoint: first=%d replay=%d", first.CheckpointCount, replay.CheckpointCount)
		}
	})

	t.Run("same slice with another digest conflicts", func(t *testing.T) {
		store, record := newMission(t)
		first := writeSliceCheckpointEvidence(t, store, record, "S01", nil)
		second := writeSliceCheckpointEvidence(t, store, record, "S01", func(document map[string]any) {
			document["producer_note"] = "distinct reviewed evidence"
		})
		if _, err := CreateSliceCheckpoint(store, record.MissionID, SliceCheckpointOptions{Slice: "S01", EvidenceDigest: first}); err != nil {
			t.Fatal(err)
		}
		_, err := CreateSliceCheckpoint(store, record.MissionID, SliceCheckpointOptions{Slice: "S01", EvidenceDigest: second})
		if err == nil || !strings.Contains(err.Error(), "slice S01 already checkpointed with a different evidence digest") {
			t.Fatalf("conflicting replay was not rejected: %v", err)
		}
	})

	t.Run("S02 requires S01", func(t *testing.T) {
		store, record := newMission(t)
		digest := writeSliceCheckpointEvidence(t, store, record, "S02", nil)
		_, err := CreateSliceCheckpoint(store, record.MissionID, SliceCheckpointOptions{Slice: "S02", EvidenceDigest: digest})
		if err == nil || !strings.Contains(err.Error(), "slice checkpoint is out of order") {
			t.Fatalf("out-of-order S02 was not rejected: %v", err)
		}
	})

	t.Run("S02 follows S01 but S03 cannot skip S02", func(t *testing.T) {
		store, record := newMission(t)
		s01 := writeSliceCheckpointEvidence(t, store, record, "S01", nil)
		s02 := writeSliceCheckpointEvidence(t, store, record, "S02", nil)
		s03 := writeSliceCheckpointEvidence(t, store, record, "S03", nil)
		if _, err := CreateSliceCheckpoint(store, record.MissionID, SliceCheckpointOptions{Slice: "S01", EvidenceDigest: s01}); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateSliceCheckpoint(store, record.MissionID, SliceCheckpointOptions{Slice: "S03", EvidenceDigest: s03}); err == nil || !strings.Contains(err.Error(), "slice checkpoint is out of order") {
			t.Fatalf("skipped S02 was not rejected: %v", err)
		}
		bundle, err := CreateSliceCheckpoint(store, record.MissionID, SliceCheckpointOptions{Slice: "S02", EvidenceDigest: s02})
		if err != nil {
			t.Fatal(err)
		}
		if bundle.CheckpointCount != 2 || bundle.LatestCheckpoint.Result != "slice_pass:S02:"+s02 {
			t.Fatalf("S02 checkpoint mismatch: %+v", bundle)
		}
	})
}

func TestCreateSliceCheckpointRejectsInvalidEvidenceWithoutMutation(t *testing.T) {
	newMission := func(t *testing.T) (Store, Record) {
		t.Helper()
		store := NewStore(t.TempDir())
		contract, err := store.StartObjective(
			"Coordinate one bounded implementation workgraph",
			ObjectiveStartOptions{CorrelationID: "slice-validation-test"},
		)
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.Load(contract.MissionID)
		if err != nil {
			t.Fatal(err)
		}
		return store, record
	}
	assertRejectedWithoutCheckpoint := func(t *testing.T, store Store, record Record, options SliceCheckpointOptions, want string) {
		t.Helper()
		before, err := store.Load(record.MissionID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = CreateSliceCheckpoint(store, record.MissionID, options)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("wanted rejection containing %q, got %v", want, err)
		}
		after, loadErr := store.Load(record.MissionID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("failed slice checkpoint mutated record:\nbefore=%+v\nafter=%+v", before, after)
		}
	}

	for _, test := range []struct {
		name   string
		slice  string
		digest string
		want   string
	}{
		{name: "empty slice", slice: "", digest: "sha256:" + strings.Repeat("a", 64), want: "slice must be one of S01 through S07"},
		{name: "unknown slice", slice: "S08", digest: "sha256:" + strings.Repeat("a", 64), want: "slice must be one of S01 through S07"},
		{name: "bare digest", slice: "S01", digest: strings.Repeat("a", 64), want: "evidence digest must be sha256"},
		{name: "uppercase digest", slice: "S01", digest: "sha256:" + strings.Repeat("A", 64), want: "evidence digest must be sha256"},
		{name: "short digest", slice: "S01", digest: "sha256:abcd", want: "evidence digest must be sha256"},
		{name: "missing artifact", slice: "S01", digest: "sha256:" + strings.Repeat("a", 64), want: "not retained by Mission"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, record := newMission(t)
			assertRejectedWithoutCheckpoint(t, store, record, SliceCheckpointOptions{Slice: test.slice, EvidenceDigest: test.digest}, test.want)
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "wrong correlation", mutate: func(doc map[string]any) { doc["correlation_id"] = "other-correlation" }, want: "correlation_id mismatch"},
		{name: "wrong mission", mutate: func(doc map[string]any) { doc["mission_ref"] = "mission-other" }, want: "mission_ref mismatch"},
		{name: "wrong slice", mutate: func(doc map[string]any) { doc["slice"] = "S02" }, want: "slice mismatch"},
		{name: "non-pass result", mutate: func(doc map[string]any) { doc["result"] = "fail" }, want: "result must be pass"},
		{name: "missing authority", mutate: func(doc map[string]any) { delete(doc["authority"].(map[string]any), "provider_calls") }, want: "authority missing property: provider_calls"},
		{name: "true authority", mutate: func(doc map[string]any) { doc["authority"].(map[string]any)["provider_calls"] = true }, want: "authority must remain false: provider_calls"},
		{name: "unknown authority", mutate: func(doc map[string]any) { doc["authority"].(map[string]any)["deploy_anyway"] = false }, want: "authority unknown property: deploy_anyway"},
		{name: "authority case variant", mutate: func(doc map[string]any) { doc["authority"].(map[string]any)["Provider_Calls"] = false }, want: "authority field case variant"},
		{name: "nested true authority", mutate: func(doc map[string]any) { doc["producer"] = map[string]any{"executes_work": true} }, want: "nested authority must remain false: /producer/executes_work"},
		{name: "oversized evidence", mutate: func(doc map[string]any) { doc["padding"] = strings.Repeat("x", 16*1024*1024) }, want: "slice evidence exceeds 16777216 bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, record := newMission(t)
			digest := writeSliceCheckpointEvidence(t, store, record, "S01", test.mutate)
			assertRejectedWithoutCheckpoint(t, store, record, SliceCheckpointOptions{Slice: "S01", EvidenceDigest: digest}, test.want)
		})
	}

	t.Run("mismatched content ref", func(t *testing.T) {
		store, record := newMission(t)
		digest := writeSliceCheckpointEvidence(t, store, record, "S01", nil)
		updated, err := store.Update(record.MissionID, func(candidate *Record) error {
			candidate.ArtifactRefs[0].ContentRef = filepath.Join(store.Root, "artifacts", "sha256", strings.Repeat("b", 64))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedWithoutCheckpoint(t, store, updated, SliceCheckpointOptions{Slice: "S01", EvidenceDigest: digest}, "content_ref is not the expected retained object")
	})

	t.Run("retained byte drift", func(t *testing.T) {
		store, record := newMission(t)
		digest := writeSliceCheckpointEvidence(t, store, record, "S01", nil)
		current, err := store.Load(record.MissionID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(current.ArtifactRefs[0].ContentRef, []byte(`{"tampered":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		assertRejectedWithoutCheckpoint(t, store, current, SliceCheckpointOptions{Slice: "S01", EvidenceDigest: digest}, "retained evidence digest mismatch")
	})

	t.Run("duplicate JSON key", func(t *testing.T) {
		store, record := newMission(t)
		body := []byte(`{"schema":"ao.test.v1","correlation_id":"slice-validation-test","mission_ref":"` + record.MissionID + `","slice":"S01","result":"pass","result":"fail","authority":{}}`)
		contentRef, digest, err := store.retainArtifact(body)
		if err != nil {
			t.Fatal(err)
		}
		updated, err := store.Update(record.MissionID, func(candidate *Record) error {
			candidate.ArtifactRefs = append(candidate.ArtifactRefs, ArtifactRef{Schema: ArtifactRefSchema, Ref: "duplicate.json", ContentRef: contentRef, Digest: digest})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedWithoutCheckpoint(t, store, updated, SliceCheckpointOptions{Slice: "S01", EvidenceDigest: digest}, "duplicate JSON key")
	})
}

func writeSliceCheckpointEvidence(
	t *testing.T,
	store Store,
	record Record,
	slice string,
	mutate func(map[string]any),
) string {
	t.Helper()
	document := map[string]any{
		"schema":         "ao.architecture.development-baseline-slice-evidence.v1",
		"correlation_id": record.CorrelationID,
		"mission_ref":    record.MissionID,
		"slice":          slice,
		"result":         "pass",
		"authority": map[string]any{
			"safe_to_execute":          false,
			"executes_work":            false,
			"approves_work":            false,
			"mutates_repositories":     false,
			"provider_calls":           false,
			"credential_use":           false,
			"release":                  false,
			"publication":              false,
			"deployment":               false,
			"promotion":                false,
			"compatibility_activation": false,
			"external_beta":            false,
			"rsi":                      false,
		},
	}
	if mutate != nil {
		mutate(document)
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	contentRef, digest, err := store.retainArtifact(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.ArtifactRefs = append(candidate.ArtifactRefs, ArtifactRef{
			Schema: ArtifactRefSchema, Ref: "slice-evidence.json", ContentRef: contentRef,
			Digest: digest, Kind: "correlation-evidence",
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestOperatorReadbackSchemasDeclareTerminalProjectionFields(t *testing.T) {
	for _, name := range []string{"command-status-v0.1.schema.json", "dashboard-readback-v0.1.schema.json"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", name))
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(body, &schema); err != nil {
			t.Fatal(err)
		}
		for field, want := range map[string]string{
			"source_record_status":          "string",
			"terminal_projection_status":    "string",
			"terminal_projection_read_only": "boolean",
			"effective_operator_status":     "string",
		} {
			if got := schema.Properties[field].Type; got != want {
				t.Errorf("%s property %s type = %q, want %q", name, field, got, want)
			}
		}
	}
}

func TestCheckpointCreateAppendsOneIdempotentReadOnlyCheckpoint(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	record, err := store.Start("checkpoint the current operator state")
	if err != nil {
		t.Fatal(err)
	}
	before := record
	for attempt := 0; attempt < 2; attempt++ {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"--home", home, "checkpoint", "create", "--mission", record.MissionID, "--json"}, &stdout, &stderr); code != 0 {
			t.Fatalf("checkpoint create attempt %d: code=%d stderr=%s", attempt+1, code, stderr.String())
		}
		var bundle MissionCheckpointBundle
		if err := json.Unmarshal(stdout.Bytes(), &bundle); err != nil {
			t.Fatal(err)
		}
		if bundle.CheckpointCount != 1 || bundle.LatestCheckpoint == nil || bundle.LatestCheckpoint.Result != "checkpoint_created" ||
			bundle.ExecutesWork || bundle.ApprovesWork || bundle.MutatesRepositories {
			t.Fatalf("unexpected checkpoint bundle: %+v", bundle)
		}
	}
	after, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Checkpoints) != 1 || len(after.Steps) != len(before.Steps) ||
		after.Status != before.Status || after.CurrentRoute != before.CurrentRoute ||
		after.CurrentPhase != before.CurrentPhase || after.ExactNextAction != before.ExactNextAction {
		t.Fatalf("checkpoint create advanced Mission state: before=%+v after=%+v", before, after)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--home", home, "checkpoint", "create", "--json"}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "requires --mission") {
		t.Fatalf("missing Mission identity code=%d stderr=%q", code, stderr.String())
	}
}

func TestCheckpointCreateEvidenceBoundSliceCLI(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	contract, err := store.StartObjective(
		"Coordinate one bounded implementation workgraph",
		ObjectiveStartOptions{CorrelationID: "slice-cli-test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	digest := writeSliceCheckpointEvidence(t, store, record, "S01", nil)

	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "slice without digest",
			args: []string{"--home", home, "checkpoint", "create", "--mission", record.MissionID, "--slice", "S01", "--json"},
		},
		{
			name: "digest without slice",
			args: []string{"--home", home, "checkpoint", "create", "--mission", record.MissionID, "--evidence-digest", digest, "--json"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(test.args, &stdout, &stderr); code == 0 ||
				!strings.Contains(stderr.String(), "checkpoint create requires --slice and --evidence-digest together") || stdout.Len() != 0 {
				t.Fatalf("unpaired flags accepted: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}

	args := []string{
		"--home", home, "checkpoint", "create", "--mission", record.MissionID,
		"--slice", "S01", "--evidence-digest", digest, "--json",
	}
	for attempt := 0; attempt < 2; attempt++ {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("slice checkpoint attempt %d: code=%d stderr=%s", attempt+1, code, stderr.String())
		}
		var bundle MissionCheckpointBundle
		if err := json.Unmarshal(stdout.Bytes(), &bundle); err != nil {
			t.Fatal(err)
		}
		if bundle.CheckpointCount != 1 || bundle.LatestCheckpoint == nil ||
			bundle.LatestCheckpoint.Result != "slice_pass:S01:"+digest ||
			bundle.ExecutesWork || bundle.ApprovesWork || bundle.MutatesRepositories {
			t.Fatalf("unexpected slice checkpoint bundle: %+v", bundle)
		}
	}

	var stdout, stderr bytes.Buffer
	textArgs := args[:len(args)-1]
	if code := Run(textArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("text replay: code=%d stderr=%s", code, stderr.String())
	}
	for _, line := range []string{
		"checkpoints=1", "safe_to_execute=false", "executes_work=false", "approves_work=false",
	} {
		if !strings.Contains(stdout.String(), line) {
			t.Fatalf("text readback missing %q: %s", line, stdout.String())
		}
	}
}

func TestCheckpointCreateDoesNotChangePauseSemantics(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("pause without creating a checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Pause(store, record.MissionID); err != nil {
		t.Fatal(err)
	}
	after, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Checkpoints) != 0 {
		t.Fatalf("pause created checkpoints: %+v", after.Checkpoints)
	}
}

func TestGenericMissionViewsProjectValidatedTerminalState(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	record, err := store.Start("run governed pool external beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.CurrentPhase = "lifecycle-canary"
		candidate.ExactNextAction = "continue lifecycle canary"
		candidate.GoalLease = &GoalLease{Schema: GoalLeaseSchema, MaxIterations: 1, CheckpointPolicy: "after_each_node_or_timed_interval"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	statePath := writeTerminalProjectionState(t, store, record.MissionID, func(*TerminalIndexImportReadback) {})

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "status", args: []string{"status", "--mission", record.MissionID, "--terminal-state", statePath, "--json"}},
		{name: "inspect", args: []string{"mission", "inspect", "--mission", record.MissionID, "--terminal-state", statePath, "--json"}},
		{name: "dashboard", args: []string{"mission", "dashboard", "--mission", record.MissionID, "--terminal-state", statePath, "--json"}},
		{name: "command", args: []string{"command", "status", "--mission", record.MissionID, "--terminal-state", statePath, "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--home", home}, test.args...)
			if code := Run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			text := stdout.String()
			for _, want := range []string{
				`"status": "done"`, `"current_phase": "reconciled"`, `"exact_next_action": "none"`,
				`"source_record_status": "active"`, `"terminal_projection_status": "done"`,
				`"terminal_projection_read_only": true`, `"effective_operator_status": "done"`,
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("view does not project %s: %s", want, text)
				}
			}
			if !strings.Contains(text, `"completed": 7`) && !strings.Contains(text, `"completed_nodes": 7`) {
				t.Fatalf("view does not project terminal counts: %s", text)
			}
		})
	}

	persisted, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "active" || persisted.CurrentPhase != "lifecycle-canary" {
		t.Fatalf("read-only terminal projection mutated Mission: %+v", persisted)
	}
	persistedJSON, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persistedJSON), "terminal_projection_status") {
		t.Fatalf("persisted Mission contains read-only projection fields: %s", persistedJSON)
	}
	projected, err := projectRecordWithTerminalState(persisted, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Reconciliation == nil || projected.Reconciliation.Status != "reconciled" ||
		projected.Reconciliation.AtlasReadyNodes != 0 || projected.Reconciliation.ExactNextAction != "none" {
		t.Fatalf("route reconciliation contradicts terminal state: %+v", projected.Reconciliation)
	}
	if projected.GoalLease == nil || projected.GoalLease.MaxIterations != 1 ||
		projected.GoalLease.CheckpointPolicy != "after_each_node_or_timed_interval" {
		t.Fatalf("terminal projection replaced the Mission lease policy: %+v", projected.GoalLease)
	}
}

func TestTerminalProjectionDistinguishesSourceAndEffectiveStatuses(t *testing.T) {
	tests := []struct {
		name           string
		sourceStatus   string
		edit           func(*TerminalIndexImportReadback)
		terminalStatus string
		effective      string
	}{
		{
			name: "active source plus nonterminal projection", sourceStatus: "active",
			edit: func(state *TerminalIndexImportReadback) {
				state.Status = "reconciled_fail_closed"
				state.Counts.Completed = 6
				state.Counts.Ready = 1
				state.CompletionObserved = false
				state.ReadinessPassed = false
				state.ReturnGateStatus = "early_return_denied"
				state.FinalResponseAllowed = false
				state.ExactNextAction = "continue node 7"
			},
			terminalStatus: "active", effective: "active",
		},
		{
			name: "done source plus done projection", sourceStatus: "done",
			edit: func(*TerminalIndexImportReadback) {}, terminalStatus: "done", effective: "done",
		},
		{
			name: "done source plus nonterminal projection", sourceStatus: "done",
			edit: func(state *TerminalIndexImportReadback) {
				state.Status = "reconciled_fail_closed"
				state.Counts.Completed = 6
				state.Counts.Ready = 1
				state.CompletionObserved = false
				state.ReadinessPassed = false
				state.ReturnGateStatus = "early_return_denied"
				state.FinalResponseAllowed = false
				state.ExactNextAction = "continue node 7"
			},
			terminalStatus: "active", effective: "done",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			store := NewStore(home)
			record, err := store.Start(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if test.sourceStatus == "done" {
				record, err = store.Update(record.MissionID, func(candidate *Record) error {
					candidate.Status = "done"
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			statePath := writeTerminalProjectionState(t, store, record.MissionID, test.edit)
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"--home", home, "status", "--mission", record.MissionID, "--terminal-state", statePath, "--json"}, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			for _, want := range []string{
				`"source_record_status": "` + test.sourceStatus + `"`,
				`"terminal_projection_status": "` + test.terminalStatus + `"`,
				`"terminal_projection_read_only": true`,
				`"effective_operator_status": "` + test.effective + `"`,
			} {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("projection missing %s: %s", want, stdout.String())
				}
			}
		})
	}
}

func TestDurableMissionRejectsTerminalProjectionFields(t *testing.T) {
	for _, operation := range []string{"save", "load"} {
		t.Run(operation, func(t *testing.T) {
			store := NewStore(t.TempDir())
			record, err := store.Start("keep terminal projection out of durable state")
			if err != nil {
				t.Fatal(err)
			}
			record.SourceRecordStatus = "active"
			record.TerminalProjectionStatus = "done"
			record.TerminalProjectionReadOnly = true
			record.EffectiveOperatorStatus = "done"
			if operation == "save" {
				if err := store.Save(record); err == nil || !strings.Contains(err.Error(), "projection") {
					t.Fatalf("Save error = %v, want projection rejection", err)
				}
				return
			}
			body, err := json.MarshalIndent(record, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.path(record.MissionID), append(body, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(record.MissionID); err == nil || !strings.Contains(err.Error(), "projection") {
				t.Fatalf("Load error = %v, want projection rejection", err)
			}
		})
	}
}

func TestGenericMissionViewsRejectInvalidTerminalState(t *testing.T) {
	tests := []struct {
		name string
		edit func(*TerminalIndexImportReadback)
		want string
	}{
		{name: "wrong mission", edit: func(state *TerminalIndexImportReadback) { state.MissionID = "mission-other" }, want: "mission identity"},
		{name: "stale", edit: func(state *TerminalIndexImportReadback) { state.GeneratedAtUTC = "2020-01-01T00:00:00Z" }, want: "stale"},
		{name: "unsafe", edit: func(state *TerminalIndexImportReadback) { state.ExecutesWork = true }, want: "safety"},
		{name: "contradictory", edit: func(state *TerminalIndexImportReadback) { state.Counts.Ready = 1 }, want: "contradictory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			store := NewStore(home)
			record, err := store.Start("reject bad terminal projection")
			if err != nil {
				t.Fatal(err)
			}
			statePath := writeTerminalProjectionState(t, store, record.MissionID, test.edit)
			var stdout, stderr bytes.Buffer
			code := Run([]string{"--home", home, "status", "--mission", record.MissionID, "--terminal-state", statePath, "--json"}, &stdout, &stderr)
			if code == 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("code=%d error=%q want %q", code, stderr.String(), test.want)
			}
		})
	}

	home := t.TempDir()
	store := NewStore(home)
	record, err := store.Start("reject altered terminal projection")
	if err != nil {
		t.Fatal(err)
	}
	statePath := writeTerminalProjectionState(t, store, record.MissionID, func(*TerminalIndexImportReadback) {})
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"completed": 7`), []byte(`"completed": 6`), 1)
	if err := os.WriteFile(statePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--home", home, "status", "--mission", record.MissionID, "--terminal-state", statePath, "--json"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "digest mismatch") {
		t.Fatalf("altered state code=%d error=%q", code, stderr.String())
	}
}

func writeTerminalProjectionState(t *testing.T, store Store, missionID string, edit func(*TerminalIndexImportReadback)) string {
	t.Helper()
	record, err := store.Load(missionID)
	if err != nil {
		t.Fatal(err)
	}
	state := TerminalIndexImportReadback{
		Schema: TerminalIndexImportSchema, Surface: "import", MissionID: missionID,
		IndexDigest:    "sha256:afbf4c71026c5214495eb90ccfc18eb023c9613285e17a0a14c2a022c0e00101",
		GeneratedAtUTC: record.UpdatedAtUTC, Status: "reconciled",
		Counts:             TerminalIndexCounts{Total: 7, Minimum: 7, Completed: 7},
		Lease:              TerminalIndexLease{TargetMinutes: 120, MaximumMinutes: 360, ElapsedMinutes: 115, Status: "within_window"},
		CompletionObserved: true, TimingCompliant: true, CanonicalEvidenceAgreement: true,
		ReadinessPassed: true, ReturnGateStatus: "final_response_allowed", FinalResponseAllowed: true,
		ConflictCodes: []string{}, ExactNextAction: "none", ReadOnly: true,
	}
	edit(&state)
	signTerminalIndexImport(&state)
	path := filepath.Join(t.TempDir(), "terminal-state.json")
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
