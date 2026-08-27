package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestTerminalIndexCLIExposesDurableReadbackSurfaces(t *testing.T) {
	root, indexPath := writeMissionTerminalFixture(t, terminalFixtureOptions{})
	statePath := filepath.Join(t.TempDir(), "state.json")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"terminal-index", "import", "--root", root, "--index", indexPath, "--state", statePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("import code=%d stderr=%s", code, stderr.String())
	}
	var views []TerminalIndexImportReadback
	stateDigests := map[string]bool{}
	for _, surface := range []string{"inspect", "checkpoint", "event-index", "command-readback"} {
		stdout.Reset()
		stderr.Reset()
		if code := Run([]string{"terminal-index", surface, "--state", statePath}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s code=%d stderr=%s", surface, code, stderr.String())
		}
		var readback TerminalIndexImportReadback
		if err := json.Unmarshal(stdout.Bytes(), &readback); err != nil {
			t.Fatal(err)
		}
		if readback.Surface != surface || readback.IndexDigest == "" || !readback.FinalResponseAllowed {
			t.Fatalf("%s readback changed reconciliation: %+v", surface, readback)
		}
		if readback.TerminalProjectionStatus != "done" || !readback.TerminalProjectionReadOnly {
			t.Fatalf("%s readback hides terminal projection semantics: %+v", surface, readback)
		}
		unsigned := readback
		unsigned.StateDigest = ""
		body, err := json.Marshal(unsigned)
		if err != nil {
			t.Fatal(err)
		}
		if readback.StateDigest != digestBytes(body) {
			t.Fatalf("%s state digest is not surface-bound", surface)
		}
		stateDigests[readback.StateDigest] = true
		views = append(views, readback)
	}
	if len(stateDigests) != 4 {
		t.Fatalf("state digests must be distinct by surface: %v", stateDigests)
	}
	if err := ValidateTerminalSurfaceAgreement(views); err != nil {
		t.Fatalf("canonical terminal views disagree: %v", err)
	}

	mismatched := append([]TerminalIndexImportReadback(nil), views...)
	mismatched[3].Counts.Completed--
	signTerminalIndexImport(&mismatched[3])
	if err := ValidateTerminalSurfaceAgreement(mismatched); err == nil ||
		!strings.Contains(err.Error(), "canonical payload mismatch") {
		t.Fatalf("equal index digest excused canonical mismatch: %v", err)
	}
}

func TestImportTerminalIndexAcceptsValidEvidenceAndIsIdempotent(t *testing.T) {
	root, indexPath := writeMissionTerminalFixture(t, terminalFixtureOptions{})
	statePath := filepath.Join(t.TempDir(), "state.json")
	first, err := ImportTerminalIndex(root, indexPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ImportTerminalIndex(root, indexPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if first.IndexDigest != second.IndexDigest || !first.ReadinessPassed || !first.FinalResponseAllowed {
		t.Fatalf("unexpected import readback: %+v", first)
	}
	restarted, err := LoadTerminalIndexImport(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.IndexDigest != first.IndexDigest || restarted.ConflictCodes == nil {
		t.Fatalf("restart changed state: %+v", restarted)
	}
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var altered map[string]any
	if err := json.Unmarshal(body, &altered); err != nil {
		t.Fatal(err)
	}
	altered["final_response_allowed"] = false
	body, _ = json.Marshal(altered)
	if err := os.WriteFile(statePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTerminalIndexImport(statePath); err == nil || !strings.Contains(err.Error(), "state digest mismatch") {
		t.Fatalf("error = %v, want durable state digest rejection", err)
	}
}

func TestLoadTerminalIndexImportMigratesSerializedLegacyState(t *testing.T) {
	const legacy = `{
  "schema": "ao.mission.terminal-index-import.v1",
  "surface": "import",
  "mission_id": "mission-adec9975c8b052bf",
  "index_digest": "sha256:7e4e58a685bce28fe22c4b68e7ff906f739becddb455d5d19bf84e9c754ab122",
  "state_digest": "sha256:554b2532fedc34fb35cc2a17faba9c83450c2c36e46beda104421d1bd3e45f29",
  "generated_at_utc": "2026-08-07T14:21:49Z",
  "status": "reconciled",
  "counts": {"total": 4, "minimum": 4, "completed": 4, "ready": 0, "blocked": 0, "failed": 0},
  "lease": {"minimum_minutes": 0, "target_minutes": 120, "maximum_minutes": 180, "elapsed_minutes": 73, "status": "within_window"},
  "completion_observed": true,
  "timing_compliant": true,
  "canonical_evidence_agreement": true,
  "readiness_passed": true,
  "return_gate_status": "final_response_allowed",
  "final_response_allowed": true,
  "conflict_codes": [],
  "exact_next_action": "none",
  "read_only": true,
  "safe_to_execute": false,
  "executes_work": false,
  "approves_work": false,
  "mutates_repositories": false,
  "calls_providers": false,
  "publishes": false,
  "releases": false,
  "deploys": false,
  "advances_authority": false
}`
	path := filepath.Join(t.TempDir(), "legacy-state.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	readback, err := LoadTerminalIndexImport(path)
	if err != nil {
		t.Fatal(err)
	}
	if readback.TerminalProjectionStatus != "done" || !readback.TerminalProjectionReadOnly {
		t.Fatalf("legacy state was not projected in memory: %+v", readback)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "terminal_projection_status") {
		t.Fatalf("legacy state was rewritten: %s", body)
	}
}

func TestImportTerminalIndexAcceptsCanonicalizedFresh60Closure(t *testing.T) {
	root, indexPath := writeMissionTerminalFixture(t, terminalFixtureOptions{
		terminalNextAction: fresh60CompletedNextAction,
	})

	readback, err := ImportTerminalIndex(root, indexPath, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if readback.ExactNextAction != "none" || !readback.FinalResponseAllowed || len(readback.ConflictCodes) != 0 {
		t.Fatalf("unexpected canonical closure readback: %+v", readback)
	}
}

func TestImportTerminalIndexPreservesExplicitZeroMinimumMinutes(t *testing.T) {
	root, indexPath := writeMissionTerminalFixture(t, terminalFixtureOptions{zeroMinimumMinutes: true})

	readback, err := ImportTerminalIndex(root, indexPath, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if readback.Counts.Minimum != 40 || readback.Lease.MinimumMinutes != 0 ||
		!readback.ReadinessPassed || !readback.FinalResponseAllowed {
		t.Fatalf("explicit zero-minute lease changed during import: %+v", readback)
	}
}

func TestImportTerminalIndexRejectsInvalidOrContradictoryEvidence(t *testing.T) {
	tests := []struct {
		name    string
		options terminalFixtureOptions
		want    string
	}{
		{name: "artifact digest", options: terminalFixtureOptions{alterArtifactDigest: true}, want: "artifact digest mismatch"},
		{name: "index digest", options: terminalFixtureOptions{alterIndexDigest: true}, want: "index digest mismatch"},
		{name: "wrong contract", options: terminalFixtureOptions{wrongContract: true}, want: "contract_version"},
		{name: "wrong identity", options: terminalFixtureOptions{wrongMission: true}, want: "mission identity mismatch"},
		{name: "stale root claims current", options: terminalFixtureOptions{rootCurrent: true}, want: "artifact state"},
		{name: "non monotonic source", options: terminalFixtureOptions{nonMonotonic: true}, want: "non-monotonic"},
		{name: "semantic forgery", options: terminalFixtureOptions{semanticForgery: true}, want: "lease status"},
		{name: "unsafe authority", options: terminalFixtureOptions{unsafe: true}, want: "safety boundary"},
		{name: "ready final", options: terminalFixtureOptions{ready: 1}, want: "final response"},
		{name: "malformed", options: terminalFixtureOptions{malformed: true}, want: "invalid JSON"},
		{name: "duplicate", options: terminalFixtureOptions{duplicate: true}, want: "duplicate JSON key"},
		{name: "oversized", options: terminalFixtureOptions{oversized: true}, want: "size limit"},
		{name: "traversal", options: terminalFixtureOptions{traversal: true}, want: "unsafe artifact path"},
		{name: "symlink", options: terminalFixtureOptions{symlink: true}, want: "regular file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, indexPath := writeMissionTerminalFixture(t, test.options)
			_, err := ImportTerminalIndex(root, indexPath, filepath.Join(t.TempDir(), "state.json"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestImportTerminalIndexRejectsConflictingSecondImport(t *testing.T) {
	root, firstPath := writeMissionTerminalFixture(t, terminalFixtureOptions{})
	statePath := filepath.Join(t.TempDir(), "state.json")
	if _, err := ImportTerminalIndex(root, firstPath, statePath); err != nil {
		t.Fatal(err)
	}
	_, secondPath := writeMissionTerminalFixtureAtRoot(t, root, terminalFixtureOptions{generatedAt: "2026-07-28T13:00:00Z"})
	if _, err := ImportTerminalIndex(root, secondPath, statePath); err == nil || !strings.Contains(err.Error(), "conflicting terminal index import") {
		t.Fatalf("error = %v, want conflicting import rejection", err)
	}
	restarted, err := LoadTerminalIndexImport(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.GeneratedAtUTC != "2026-07-28T12:00:00Z" {
		t.Fatalf("conflict corrupted durable state: %+v", restarted)
	}
}

func TestImportTerminalIndexPreservesFailClosedTiming(t *testing.T) {
	tests := []struct {
		name        string
		elapsed     int
		sourceLabel string
		wantStatus  string
		wantCode    string
	}{
		{name: "below minimum", elapsed: 90, sourceLabel: "minimum_not_met", wantStatus: "minimum_not_met", wantCode: "lease_minimum_not_met"},
		{name: "above maximum", elapsed: 181, sourceLabel: "maximum_exceeded", wantStatus: "maximum_exceeded", wantCode: "lease_maximum_exceeded"},
		{name: "mislabeled maximum", elapsed: 181, sourceLabel: "minimum_minutes_met", wantStatus: "maximum_exceeded", wantCode: "terminal_lease_status_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, indexPath := writeFailClosedTimingFixture(t, test.elapsed, test.sourceLabel)
			readback, err := ImportTerminalIndex(root, indexPath, filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if readback.Lease.Status != test.wantStatus || readback.ReadinessPassed || readback.FinalResponseAllowed ||
				!containsTerminalString(readback.ConflictCodes, test.wantCode) {
				t.Fatalf("timing was not preserved fail closed: %+v", readback)
			}
		})
	}
}

func TestBuildHistoricalMissionTerminalIndexFailsClosed(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "production-readiness", "ao-mission-doubled-wave-v01")
	index, err := BuildHistoricalMissionTerminalIndex(root, "2026-07-28T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTerminalIndex(root, index); err != nil {
		t.Fatal(err)
	}
	if index.Counts.Completed != 26 || index.Counts.Total != 60 || index.FinalResponseAllowed || index.ReadinessPassed {
		t.Fatalf("historical state was misclassified: %+v", index)
	}
	for _, code := range []string{"canonical_terminal_missing", "duration_state_stale", "historical_snapshot_not_live", "timing_unresolved"} {
		if !containsTerminalString(index.ConflictCodes, code) {
			t.Fatalf("missing conflict %q in %v", code, index.ConflictCodes)
		}
	}
}

func writeFailClosedTimingFixture(t *testing.T, elapsed int, sourceLabel string) (string, string) {
	t.Helper()
	root, indexPath := writeMissionTerminalFixture(t, terminalFixtureOptions{})
	terminalBody := []byte(fmt.Sprintf(`{"schema":"terminal.v1","mission_id":"fixture-wave","completed_nodes":40,"ready_nodes":0,"blocked_nodes":0,"failed_nodes":0,"elapsed_minutes":%d,"lease_time_status":"%s","final_response_allowed":true,"exact_next_action":"none","executes_work":false,"approves_work":false,"mutates_repositories":false,"calls_providers":false,"publishes":false,"releases":false,"deploys":false,"advances_authority":false}`, elapsed, sourceLabel))
	if err := os.WriteFile(filepath.Join(root, "terminal.json"), terminalBody, 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := loadCanonicalTerminalIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	for artifactIndex := range index.Artifacts {
		if index.Artifacts[artifactIndex].Role == "terminal" {
			index.Artifacts[artifactIndex].SHA256 = digestBytes(terminalBody)
		}
	}
	index.Lease.ElapsedMinutes = elapsed
	index.ConflictCodes = []string{"terminal_final_response_allowed_despite_violation"}
	switch {
	case elapsed < index.Lease.MinimumMinutes:
		index.Lease.Status = "minimum_not_met"
		index.ConflictCodes = append(index.ConflictCodes, "lease_minimum_not_met")
	case elapsed > index.Lease.MaximumMinutes:
		index.Lease.Status = "maximum_exceeded"
		index.ConflictCodes = append(index.ConflictCodes, "lease_maximum_exceeded")
	}
	if sourceLabel != index.Lease.Status {
		index.ConflictCodes = append(index.ConflictCodes, "terminal_lease_status_mismatch")
	}
	sort.Strings(index.ConflictCodes)
	index.ConflictSummaries = nil
	for _, code := range index.ConflictCodes {
		index.ConflictSummaries = append(index.ConflictSummaries, strings.ReplaceAll(code, "_", " "))
	}
	index.ReadinessPassed = false
	index.FinalResponseAllowed = false
	index.ReturnGateStatus = "final_response_denied"
	index.ExactNextAction = "Review the canonical conflict codes and produce a fresh governed terminal observation."
	signMissionTerminalIndex(&index)
	body, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, indexPath
}

type terminalFixtureOptions struct {
	generatedAt         string
	terminalNextAction  string
	zeroMinimumMinutes  bool
	ready               int
	alterArtifactDigest bool
	alterIndexDigest    bool
	wrongContract       bool
	wrongMission        bool
	rootCurrent         bool
	nonMonotonic        bool
	semanticForgery     bool
	unsafe              bool
	malformed           bool
	duplicate           bool
	oversized           bool
	traversal           bool
	symlink             bool
}

func writeMissionTerminalFixture(t *testing.T, options terminalFixtureOptions) (string, string) {
	t.Helper()
	return writeMissionTerminalFixtureAtRoot(t, t.TempDir(), options)
}

func writeMissionTerminalFixtureAtRoot(t *testing.T, root string, options terminalFixtureOptions) (string, string) {
	t.Helper()
	missionID := "fixture-wave"
	terminalMission := missionID
	if options.wrongMission {
		terminalMission = "other-wave"
	}
	completed := 40 - options.ready
	if options.nonMonotonic {
		completed = 39
	}
	terminalNextAction := defaultTerminalString(options.terminalNextAction, "none")
	minimumMinutes := 120
	if options.zeroMinimumMinutes {
		minimumMinutes = 0
	}
	leaseBody := []byte(fmt.Sprintf(`{"schema":"lease.v1","mission_id":"fixture-wave","minimum_nodes":40,"minimum_minutes":%d,"target_minutes":150,"maximum_minutes":180}`, minimumMinutes))
	rootCompleted := 0
	rootReady := 40
	if options.nonMonotonic {
		rootCompleted = 40
		rootReady = 0
	}
	rootBody := []byte(fmt.Sprintf(`{"schema":"root.v1","mission_id":"fixture-wave","completed_nodes":%d,"ready_nodes":%d,"blocked_nodes":0,"failed_nodes":0}`, rootCompleted, rootReady))
	terminalBody := []byte(fmt.Sprintf(`{"schema":"terminal.v1","mission_id":"%s","completed_nodes":%d,"ready_nodes":%d,"blocked_nodes":0,"failed_nodes":0,"elapsed_minutes":150,"lease_time_status":"within_window","final_response_allowed":true,"exact_next_action":%q,"executes_work":false,"approves_work":false,"mutates_repositories":false,"calls_providers":false,"publishes":false,"releases":false,"deploys":false,"advances_authority":false}`, terminalMission, completed, options.ready, terminalNextAction))
	if options.malformed {
		terminalBody = []byte("{")
	}
	if options.duplicate {
		terminalBody = []byte(`{"mission_id":"fixture-wave","mission_id":"fixture-wave"}`)
	}
	if options.oversized {
		terminalBody = []byte(`{"padding":"` + strings.Repeat("x", terminalIndexMaxFileBytes) + `"}`)
	}
	artifacts := []struct {
		role, state, name string
		body              []byte
	}{
		{"lease", "lease_authority", "lease.json", leaseBody},
		{"root", "initial_snapshot", "root.json", rootBody},
		{"terminal", "terminal_candidate", "terminal.json", terminalBody},
	}
	index := CanonicalTerminalIndex{
		ContractVersion:            "ao.canonical-terminal-index.v1",
		SchemaDigest:               digestBytes([]byte("ao.canonical-terminal-index.v1")),
		MissionID:                  missionID,
		EvidenceRoot:               ".",
		GeneratedAtUTC:             defaultTerminalString(options.generatedAt, "2026-07-28T12:00:00Z"),
		TerminalReference:          "terminal.json",
		Counts:                     TerminalIndexCounts{Total: 40, Minimum: 40, Completed: completed, Ready: options.ready},
		Lease:                      TerminalIndexLease{MinimumMinutes: minimumMinutes, TargetMinutes: 150, MaximumMinutes: 180, ElapsedMinutes: 150, Status: "within_window"},
		CompletionObserved:         completed >= 40,
		CanonicalEvidenceAgreement: true,
		ReadinessPassed:            options.ready == 0,
		ReturnGateStatus:           "final_response_allowed",
		FinalResponseAllowed:       true,
		ConflictCodes:              []string{},
		ConflictSummaries:          []string{"none"},
		ExactNextAction:            "none",
		SafetyBoundaries:           TerminalIndexSafety{},
	}
	if options.wrongContract {
		index.ContractVersion = "ao.canonical-terminal-index.v0"
		index.SchemaDigest = digestBytes([]byte(index.ContractVersion))
	}
	if options.semanticForgery {
		index.Lease.Status = "maximum_exceeded"
	}
	if options.unsafe {
		index.SafetyBoundaries.MutatesRepositories = true
	}
	for sequence, artifact := range artifacts {
		path := filepath.Join(root, artifact.name)
		if err := os.WriteFile(path, artifact.body, 0o600); err != nil {
			t.Fatal(err)
		}
		relative := artifact.name
		if options.traversal && artifact.role == "terminal" {
			relative = "../terminal.json"
		}
		if options.symlink && artifact.role == "terminal" {
			target := filepath.Join(root, "terminal-target.json")
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			createTestSymlink(t, target, path)
		}
		digest := digestBytes(artifact.body)
		if options.alterArtifactDigest && artifact.role == "terminal" {
			digest = "sha256:" + strings.Repeat("0", 64)
		}
		var schemaDoc map[string]any
		_ = json.Unmarshal(artifact.body, &schemaDoc)
		schema, _ := schemaDoc["schema"].(string)
		state := artifact.state
		if options.rootCurrent && artifact.role == "root" {
			state = "current"
		}
		index.Artifacts = append(index.Artifacts, TerminalIndexArtifact{
			Role: artifact.role, Sequence: sequence, Path: relative, Schema: schema, SHA256: digest, State: state,
		})
		if sequence > 0 {
			index.Lineage = append(index.Lineage, TerminalIndexLineage{FromSequence: sequence - 1, ToSequence: sequence, Relation: "precedes"})
		}
	}
	signMissionTerminalIndex(&index)
	if options.alterIndexDigest {
		index.Digest = "sha256:" + strings.Repeat("f", 64)
	}
	indexPath := filepath.Join(root, "index-"+strings.ReplaceAll(index.GeneratedAtUTC, ":", "")+".json")
	body, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, indexPath
}

func signMissionTerminalIndex(index *CanonicalTerminalIndex) {
	index.Digest = ""
	body, _ := json.Marshal(*index)
	index.Digest = digestBytes(body)
}

func defaultTerminalString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func TestTerminalNoActionAcceptsFresh60ClosureOnly(t *testing.T) {
	const closure = "Fresh 60-node Mission-to-Atlas soak complete; no further execution is authorized."

	if !terminalNoAction(closure) {
		t.Fatalf("expected exact fresh 60-node closure to be treated as no action")
	}

	for _, action := range []string{
		"Run " + closure,
		closure + " Then execute another soak.",
	} {
		if terminalNoAction(action) {
			t.Fatalf("expected executable variant to remain actionable: %q", action)
		}
	}
}
