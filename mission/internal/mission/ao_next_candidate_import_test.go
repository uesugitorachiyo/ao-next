package mission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAONextCandidateImportIsReadOnlyDigestIdempotentAndCommandVisible(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("supervise AO Next candidate import workgraph")
	if err != nil {
		t.Fatal(err)
	}
	statusBefore := record.Status
	routeBefore := record.CurrentRoute
	phaseBefore := record.CurrentPhase
	nextBefore := record.ExactNextAction

	document := validAONextCandidateRecord()
	path := filepath.Join(t.TempDir(), "terminal.json")
	writeAONextCandidateRecord(t, path, document)
	readback, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", path)
	if err != nil {
		t.Fatal(err)
	}
	if readback.SafeToExecute || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("candidate import widened authority: %#v", readback)
	}

	imported, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Status != statusBefore || imported.CurrentRoute != routeBefore ||
		imported.CurrentPhase != phaseBefore || imported.ExactNextAction != nextBefore {
		t.Fatalf("candidate import mutated Mission workflow state: before=%#v after=%#v", record, imported)
	}
	projection := imported.Evidence.AONextCandidate
	if projection == nil || projection.Status != "passed" || projection.RunID != "run-windows-01" ||
		projection.SourceDigest != "sha256:1111111111111111111111111111111111111111111111111111111111111111" ||
		projection.RecordDigest != "sha256:2222222222222222222222222222222222222222222222222222222222222222" ||
		!projection.ReadOnly || projection.ExecutesWork || projection.ApprovesWork || projection.MutatesRepositories {
		t.Fatalf("candidate projection mismatch: %#v", projection)
	}
	if len(imported.ArtifactRefs) != 1 || imported.ArtifactRefs[0].ContentRef == "" {
		t.Fatalf("candidate bytes not retained: %#v", imported.ArtifactRefs)
	}
	command := BuildCommandStatus(imported)
	if command.AONextCandidate == nil || command.AONextCandidate.ArtifactDigest != projection.ArtifactDigest {
		t.Fatalf("Command omitted candidate projection: %#v", command)
	}

	copyPath := filepath.Join(t.TempDir(), "terminal-copy.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", copyPath); err != nil {
		t.Fatalf("exact digest reimport failed: %v", err)
	}
	reimported, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reimported.ArtifactRefs) != 1 {
		t.Fatalf("exact digest reimport duplicated artifact refs: %#v", reimported.ArtifactRefs)
	}

	document["record_digest"] = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	writeAONextCandidateRecord(t, path, document)
	if _, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", path); err == nil {
		t.Fatal("changed candidate bytes replaced durable projection")
	}
	afterDrift, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if afterDrift.Evidence.AONextCandidate.ArtifactDigest != projection.ArtifactDigest ||
		afterDrift.Status != statusBefore {
		t.Fatalf("rejected drift mutated Mission: %#v", afterDrift)
	}

	nextDocument := validAONextCandidateRecord()
	nextDocument["record_digest"] = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	nextDocument["measurement"].(map[string]any)["run_id"] = "run-windows-02"
	nextPath := filepath.Join(t.TempDir(), "terminal-next-run.json")
	writeAONextCandidateRecord(t, nextPath, nextDocument)
	if _, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", nextPath); err != nil {
		t.Fatalf("new candidate run was not retained additively: %v", err)
	}
	latest, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Evidence.AONextCandidate.RunID != "run-windows-02" || len(latest.ArtifactRefs) != 2 ||
		latest.ArtifactRefs[0].Digest != projection.ArtifactDigest {
		t.Fatalf("new candidate did not preserve prior evidence and update latest projection: %#v", latest)
	}
}

func TestAONextCandidateImportFailsClosedWithoutMissionMutation(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{name: "unknown-field", edit: func(document map[string]any) { document["executes_work"] = true }},
		{name: "wrong-variant", edit: func(document map[string]any) { document["variant"] = "N4" }},
		{name: "multiple-workers", edit: func(document map[string]any) { document["measurement"].(map[string]any)["worker_count"] = 2 }},
		{name: "dynamic-fanout", edit: func(document map[string]any) { document["measurement"].(map[string]any)["dynamic_fanout"] = true }},
		{name: "unauthorized-effect", edit: func(document map[string]any) { document["measurement"].(map[string]any)["unauthorized_effects"] = 1 }},
		{name: "incomplete-evidence", edit: func(document map[string]any) { document["measurement"].(map[string]any)["evidence_complete"] = false }},
		{name: "bad-source-digest", edit: func(document map[string]any) {
			document["measurement"].(map[string]any)["source_digest"] = "not-a-digest"
		}},
		{name: "empty-captures", edit: func(document map[string]any) { document["capture_digests"] = []any{} }},
		{name: "terminal-success-contradiction", edit: func(document map[string]any) { document["terminal_state"] = "failed" }},
		{name: "negative-interventions", edit: func(document map[string]any) {
			document["measurement"].(map[string]any)["operator_interventions"] = -1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			record, err := store.Start("supervise AO Next candidate import workgraph")
			if err != nil {
				t.Fatal(err)
			}
			document := validAONextCandidateRecord()
			test.edit(document)
			path := filepath.Join(t.TempDir(), "terminal.json")
			writeAONextCandidateRecord(t, path, document)
			if _, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", path); err == nil {
				t.Fatal("unsafe candidate import passed")
			}
			assertNoAONextCandidateMutation(t, store, record)
		})
	}

	t.Run("duplicate-key", func(t *testing.T) {
		store := NewStore(t.TempDir())
		record, err := store.Start("supervise AO Next candidate import workgraph")
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(validAONextCandidateRecord())
		if err != nil {
			t.Fatal(err)
		}
		body = []byte(strings.Replace(string(body), `"schema_version":`, `"schema_version":"ao.next.live-run-record.v1","schema_version":`, 1))
		path := filepath.Join(t.TempDir(), "duplicate.json")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", path); err == nil {
			t.Fatal("duplicate key passed")
		}
		assertNoAONextCandidateMutation(t, store, record)
	})

	t.Run("oversized", func(t *testing.T) {
		store := NewStore(t.TempDir())
		record, err := store.Start("supervise AO Next candidate import workgraph")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "oversized.json")
		if err := os.WriteFile(path, make([]byte, aoNextCandidateInputLimit+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", path); err == nil {
			t.Fatal("oversized input passed")
		}
		assertNoAONextCandidateMutation(t, store, record)
	})

	if runtime.GOOS != "windows" {
		t.Run("symlink", func(t *testing.T) {
			store := NewStore(t.TempDir())
			record, err := store.Start("supervise AO Next candidate import workgraph")
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			target := filepath.Join(directory, "target.json")
			writeAONextCandidateRecord(t, target, validAONextCandidateRecord())
			link := filepath.Join(directory, "terminal.json")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportArtifact(store, record.MissionID, "ao-next-terminal", link); err == nil {
				t.Fatal("symlink input passed")
			}
			assertNoAONextCandidateMutation(t, store, record)
		})
	}
}

func assertNoAONextCandidateMutation(t *testing.T, store Store, before Record) {
	t.Helper()
	after, err := store.Load(before.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Evidence.AONextCandidate != nil || len(after.ArtifactRefs) != 0 || after.Status != before.Status ||
		after.CurrentRoute != before.CurrentRoute || after.CurrentPhase != before.CurrentPhase ||
		after.ExactNextAction != before.ExactNextAction {
		t.Fatalf("rejected candidate mutated Mission: %#v", after)
	}
}

func validAONextCandidateRecord() map[string]any {
	return map[string]any{
		"schema_version":           "ao.next.live-run-record.v1",
		"variant":                  "N7",
		"terminal_state":           "passed",
		"capture_digests":          []any{"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"raw_capture_index_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"verifier_report_digest":   "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"git_workspace":            map[string]any{},
		"ao2_control_diagnostics":  []any{},
		"native_effect_observations": []any{
			map[string]any{"effect_id": "write-product"},
		},
		"record_digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		"measurement": map[string]any{
			"schema_version":          "ao.next.run-measurement.v2",
			"run_id":                  "run-windows-01",
			"task_id":                 "windows-journey",
			"variant":                 "N7",
			"source_digest":           "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			"operator_interventions":  0,
			"repair_attempts":         0,
			"task_success":            true,
			"unauthorized_effects":    0,
			"evidence_complete":       true,
			"evidence_digest_valid":   true,
			"cross_runtime_agreement": true,
			"worker_count":            1,
			"dynamic_fanout":          false,
			"hidden_test_exposure":    false,
		},
	}
}

func writeAONextCandidateRecord(t *testing.T, path string, document map[string]any) {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
