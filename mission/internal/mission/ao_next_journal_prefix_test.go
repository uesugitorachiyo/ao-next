package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const journalTestZeroDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
const journalTestOneDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestAONextJournalProjectionIsPureAndComplete(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"prepared", "prepared"},
		{"provider_intent_recorded", "provider_intent_recorded"},
		{"provider_captured", "provider_captured"},
		{"provider_outcome_unknown", "provider_outcome_unknown"},
		{"effect_outcome_unknown", "effect_outcome_unknown"},
		{"effects_pending", "effects_pending"},
		{"verifying", "verifying"},
		{"passed", "passed"},
		{"failed", "failed"},
		{"stopped_denied", "stopped"},
		{"stopped_interrupted", "stopped"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := validAONextJournalPrefix(t, test.name)
			prefix, err := parseAONextJournalPrefix(body)
			if err != nil {
				t.Fatalf("parse %s: %v", test.name, err)
			}
			before := journalTestJSON(t, prefix)
			for attempt := 0; attempt < 2; attempt++ {
				status, err := projectAONextJournalPrefix(prefix)
				if err != nil || status != test.want {
					t.Fatalf("project %s: status=%q err=%v", test.name, status, err)
				}
			}
			if after := journalTestJSON(t, prefix); !bytes.Equal(before, after) {
				t.Fatalf("projection mutated prefix: before=%s after=%s", before, after)
			}
		})
	}
}

func TestAONextJournalImportIsReadOnlyAndDigestIdempotent(t *testing.T) {
	directory := t.TempDir()
	externalDirectory := journalTestResolvedTempDir(t)
	store := NewStore(filepath.Join(directory, "mission home"))
	contract, err := store.StartObjective("supervise AO Next journal prefix", ObjectiveStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	record, err = Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	before := record
	beforeRecord := journalTestReadOptional(t, store.path(record.MissionID))
	beforeCheckpoint := journalTestReadOptional(t, store.checkpointPath(record.MissionID))
	beforeEvent := journalTestReadOptional(t, store.eventLoopPath(record.MissionID))
	store.Clock = func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }

	body := validAONextJournalPrefix(t, "passed")
	path := filepath.Join(externalDirectory, "Engine exports", "prefix 日本語.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	readback, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, path)
	if err != nil {
		t.Fatal(err)
	}
	if readback.SafeToExecute || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("journal import widened authority: %#v", readback)
	}

	imported, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	assertJournalLifecycleUnchanged(t, before, imported)
	afterRecord := journalTestReadOptional(t, store.path(record.MissionID))
	if imported.UpdatedAtUTC != before.UpdatedAtUTC {
		t.Fatalf("journal import changed updated_at_utc: before=%q after=%q", before.UpdatedAtUTC, imported.UpdatedAtUTC)
	}
	if !journalTestRecordDiffIsOnlyImport(t, beforeRecord, afterRecord) {
		t.Fatal("journal import changed record fields outside projection and artifact_refs")
	}
	if afterCheckpoint := journalTestReadOptional(t, store.checkpointPath(record.MissionID)); !bytes.Equal(beforeCheckpoint, afterCheckpoint) {
		t.Fatal("read-only import rewrote checkpoint bytes")
	}
	projection := imported.Evidence.AONextJournalProjection
	if projection == nil || projection.Schema != "ao.mission.ao-next-journal-projection.v1" ||
		projection.RunID != "run-stage1-export" || projection.Status != "passed" ||
		projection.PrefixDigest == "" || projection.ArtifactDigest != readback.Artifact.Digest ||
		readback.Artifact.Ref != path || readback.Artifact.ContentRef == "" || !projection.ReadOnly || projection.ExecutesWork ||
		projection.ApprovesWork || projection.MutatesRepositories {
		t.Fatalf("journal projection mismatch: %#v", projection)
	}
	if len(imported.ArtifactRefs) != len(before.ArtifactRefs)+1 {
		t.Fatalf("journal artifact reference count=%d want=%d", len(imported.ArtifactRefs), len(before.ArtifactRefs)+1)
	}
	retained, err := os.ReadFile(readback.Artifact.ContentRef)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, body) {
		t.Fatal("journal import did not retain exact bytes")
	}
	if command := BuildCommandStatus(imported); command.AONextJournalProjection == nil ||
		command.AONextJournalProjection.ArtifactDigest != projection.ArtifactDigest {
		t.Fatalf("Command omitted journal projection: %#v", command)
	}
	if afterEvent := journalTestReadOptional(t, store.eventLoopPath(record.MissionID)); !bytes.Equal(beforeEvent, afterEvent) {
		t.Fatal("read-only import changed the durable event decision")
	}

	copyPath := filepath.Join(externalDirectory, "second locator.json")
	if err := os.WriteFile(copyPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	stateBeforeReimport := journalTestTreeSnapshot(t, store.Root)
	if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, copyPath); err != nil {
		t.Fatalf("exact digest reimport failed: %v", err)
	}
	stateAfterReimport := journalTestTreeSnapshot(t, store.Root)
	if !reflect.DeepEqual(stateBeforeReimport, stateAfterReimport) {
		t.Fatal("exact digest reimport changed Mission state or duplicated retained evidence")
	}
	reimported, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if reimported.ArtifactRefs[len(reimported.ArtifactRefs)-1].Ref != path || len(reimported.ArtifactRefs) != len(imported.ArtifactRefs) {
		t.Fatalf("exact digest reimport replaced provenance or duplicated refs: %#v", reimported)
	}
}

func TestAONextJournalConcurrentConflictRetainsOnlyWinner(t *testing.T) {
	directory := t.TempDir()
	store := NewStore(filepath.Join(directory, "home"))
	record, err := store.Start("concurrent AO Next journal imports")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(journalTestResolvedTempDir(t), "prepared.json"),
		filepath.Join(journalTestResolvedTempDir(t), "intent.json"),
	}
	for index, body := range [][]byte{validAONextJournalPrefix(t, "prepared"), validAONextJournalPrefix(t, "provider_intent_recorded")} {
		if err := os.WriteFile(paths[index], body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	originalHook := beforeAONextJournalImportLock
	defer func() { beforeAONextJournalImportLock = originalHook }()
	var mutex sync.Mutex
	arrived := 0
	ready := make(chan struct{})
	release := make(chan struct{})
	beforeAONextJournalImportLock = func() {
		mutex.Lock()
		arrived++
		if arrived == 2 {
			close(ready)
		}
		mutex.Unlock()
		<-release
	}

	type result struct {
		readback ImportReadback
		err      error
	}
	results := make(chan result, 2)
	for _, path := range paths {
		go func() {
			readback, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, path)
			results <- result{readback: readback, err: err}
		}()
	}
	<-ready
	close(release)

	var winner ImportReadback
	succeeded := 0
	failed := 0
	for range paths {
		result := <-results
		if result.err == nil {
			succeeded++
			winner = result.readback
		} else if strings.Contains(result.err.Error(), "already bound to a different digest") {
			failed++
		} else {
			t.Fatalf("unexpected concurrent import error: %v", result.err)
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("concurrent results: succeeded=%d failed=%d", succeeded, failed)
	}
	objects := journalTestArtifactObjects(t, store.Root)
	if len(objects) != 1 || objects[0] != strings.TrimPrefix(winner.Artifact.Digest, "sha256:") {
		t.Fatalf("retained objects=%v winner=%s", objects, winner.Artifact.Digest)
	}
}

func TestAONextJournalRecordOnlyTransactionRecoversWithoutSidecarDrift(t *testing.T) {
	directory := t.TempDir()
	store := NewStore(filepath.Join(directory, "home"))
	record, err := store.Start("recover AO Next journal record-only transaction")
	if err != nil {
		t.Fatal(err)
	}
	record, err = Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	beforeRecord := journalTestReadOptional(t, store.path(record.MissionID))
	beforeCheckpoint := journalTestReadOptional(t, store.checkpointPath(record.MissionID))
	beforeEvent := journalTestReadOptional(t, store.eventLoopPath(record.MissionID))
	path := filepath.Join(journalTestResolvedTempDir(t), "prefix.json")
	if err := os.WriteFile(path, validAONextJournalPrefix(t, "prepared"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_journal_commit" {
			return fmt.Errorf("injected record-only interruption")
		}
		return nil
	}
	if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, path); err == nil ||
		!strings.Contains(err.Error(), "injected record-only interruption") {
		t.Fatalf("interrupted import error=%v", err)
	}
	store.transactionFault = nil
	if _, err := store.Load(record.MissionID); err != nil {
		t.Fatal(err)
	}
	if after := journalTestReadOptional(t, store.path(record.MissionID)); !bytes.Equal(beforeRecord, after) {
		t.Fatal("recovery did not restore exact record bytes")
	}
	if after := journalTestReadOptional(t, store.checkpointPath(record.MissionID)); !bytes.Equal(beforeCheckpoint, after) {
		t.Fatal("recovery changed checkpoint bytes")
	}
	if after := journalTestReadOptional(t, store.eventLoopPath(record.MissionID)); !bytes.Equal(beforeEvent, after) {
		t.Fatal("recovery changed event-decision bytes")
	}
}

func TestAONextJournalImportFailuresDoNotMutateMission(t *testing.T) {
	fixtures := []string{
		"invalid-digest-drift.json",
		"invalid-duplicate-key.json",
		"invalid-identity-drift.json",
		"invalid-sequence-gap.json",
		"invalid-terminal-contradiction.json",
		"invalid-unknown-field.json",
	}
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			assertRejectedJournalImportDoesNotMutate(t, readJournalPrefixFixture(t, name))
		})
	}
	t.Run("oversized", func(t *testing.T) {
		assertRejectedJournalImportDoesNotMutate(t, make([]byte, aoNextJournalPrefixInputLimit+1))
	})
	t.Run("directory", func(t *testing.T) {
		directory := t.TempDir()
		store := NewStore(filepath.Join(directory, "home"))
		record, err := store.Start("reject journal directory")
		if err != nil {
			t.Fatal(err)
		}
		before := journalTestTreeSnapshot(t, store.Root)
		if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, directory); err == nil {
			t.Fatal("journal directory unexpectedly imported")
		}
		if after := journalTestTreeSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
			t.Fatal("directory rejection mutated Mission")
		}
	})
	t.Run("changed-run-conflict-before-retention", func(t *testing.T) {
		directory := t.TempDir()
		externalDirectory := journalTestResolvedTempDir(t)
		store := NewStore(filepath.Join(directory, "home"))
		record, err := store.Start("reject changed journal run")
		if err != nil {
			t.Fatal(err)
		}
		first := validAONextJournalPrefix(t, "prepared")
		firstPath := filepath.Join(externalDirectory, "first.json")
		if err := os.WriteFile(firstPath, first, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, firstPath); err != nil {
			t.Fatal(err)
		}
		changed := validAONextJournalPrefix(t, "provider_intent_recorded")
		changedPath := filepath.Join(externalDirectory, "changed.json")
		if err := os.WriteFile(changedPath, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		changedDigest := digestBytes(changed)
		changedObject := filepath.Join(store.Root, retainedArtifactDirectory, strings.TrimPrefix(changedDigest, "sha256:"))
		before := journalTestTreeSnapshot(t, store.Root)
		if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, changedPath); err == nil {
			t.Fatal("changed digest for imported run unexpectedly passed")
		}
		after := journalTestTreeSnapshot(t, store.Root)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("changed-run conflict mutated Mission record, sidecars, or artifact directory")
		}
		if _, err := os.Lstat(changedObject); !os.IsNotExist(err) {
			t.Fatalf("changed-run conflict left orphan object: %v", err)
		}
	})
}

func TestAONextJournalImportPathBoundary(t *testing.T) {
	body := validAONextJournalPrefix(t, "prepared")
	assertPathRejected := func(t *testing.T, path string) {
		t.Helper()
		store := NewStore(filepath.Join(t.TempDir(), "Mission state"))
		record, err := store.Start("journal path boundary")
		if err != nil {
			t.Fatal(err)
		}
		before := journalTestTreeSnapshot(t, store.Root)
		if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, path); err == nil {
			t.Fatalf("unsafe path %q unexpectedly imported", path)
		}
		if after := journalTestTreeSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
			t.Fatalf("unsafe path %q mutated Mission", path)
		}
	}
	assertPathRejected(t, "relative-prefix.json")
	t.Run("inside-state-root", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "Mission state"))
		record, err := store.Start("journal inside root")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.Root, "prefix.json")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		before := journalTestTreeSnapshot(t, store.Root)
		if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, path); err == nil {
			t.Fatal("path inside Mission root unexpectedly imported")
		}
		if after := journalTestTreeSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
			t.Fatal("inside-root rejection mutated Mission")
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("symlinked-ancestor", func(t *testing.T) {
			directory := journalTestResolvedTempDir(t)
			realDirectory := filepath.Join(directory, "real")
			if err := os.Mkdir(realDirectory, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(realDirectory, "prefix.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(directory, "linked")
			if err := os.Symlink(realDirectory, link); err != nil {
				t.Fatal(err)
			}
			assertPathRejected(t, filepath.Join(link, "prefix.json"))
		})
	}
	t.Run("clean-external-locator", func(t *testing.T) {
		directory := filepath.Join(journalTestResolvedTempDir(t), "Engine exports with spaces", "日本語")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "prefix.json")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(filepath.Join(t.TempDir(), "Mission state"))
		record, err := store.Start("journal external path")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, path); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.Load(record.MissionID)
		if err != nil {
			t.Fatal(err)
		}
		if got := loaded.ArtifactRefs[len(loaded.ArtifactRefs)-1].Ref; got != path {
			t.Fatalf("original locator=%q want=%q", got, path)
		}
	})
}

func TestAONextJournalPrefixRejectsExactJSONClasses(t *testing.T) {
	valid := validAONextJournalPrefix(t, "prepared")
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"duplicate-key", []byte(strings.Replace(string(valid), `"schema_version":`, `"schema_version":"ao.next.execution-journal-prefix.v1","schema_version":`, 1)), `duplicate key "schema_version"`},
		{"casing", []byte(strings.Replace(string(valid), `"schema_version":`, `"Schema_Version":`, 1)), `contract field "Schema_Version" must use exact lowercase spelling "schema_version"`},
		{"trailing", append(append([]byte(nil), valid...), []byte("\n{}")...), "trailing JSON is not allowed"},
		{"integer-type", []byte(strings.Replace(string(valid), `"worker_count":1`, `"worker_count":"1"`, 1)), `field "worker_count" must be uint32`},
		{"unknown-field", []byte(strings.Replace(string(valid), "{", `{"extra":false,`, 1)), `contains unknown field "extra"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseAONextJournalPrefix(test.body); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestAONextJournalPrefixRejectsNestedNativeEffectDrift(t *testing.T) {
	document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
	terminal := document["terminal_record"].(map[string]any)
	terminal["native_effect_observations"] = []any{map[string]any{
		"effect_id": "effect-1", "output_digest": journalTestZeroDigest,
		"status": json.Number("0"), "stdout": []any{}, "stderr": []any{}, "extra": false,
	}}
	journalTestRefreshTerminalPrefix(t, document)
	if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil ||
		!strings.Contains(err.Error(), `contains unknown field "extra"`) {
		t.Fatalf("nested native effect drift error=%v", err)
	}
}

func TestAONextJournalTerminalMatchesProducerSchemaAndVerifier(t *testing.T) {
	t.Run("measurement-origin-enum", func(t *testing.T) {
		document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
		journalTestMeasurement(t, document)["measurement_origin"] = "invented"
		journalTestRefreshTerminalPrefix(t, document)
		if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil ||
			!strings.Contains(err.Error(), "measurement_origin") {
			t.Fatalf("invalid measurement origin error=%v", err)
		}
	})

	t.Run("u32-overflow", func(t *testing.T) {
		document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
		journalTestMeasurement(t, document)["worker_turns"] = json.Number("4294967296")
		journalTestRefreshTerminalPrefix(t, document)
		if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil ||
			!strings.Contains(err.Error(), "worker_turns") {
			t.Fatalf("u32 overflow error=%v", err)
		}
	})

	t.Run("u64-maximum", func(t *testing.T) {
		document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
		journalTestMeasurement(t, document)["wall_clock_ms"] = json.Number("18446744073709551615")
		journalTestRefreshTerminalPrefix(t, document)
		if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err != nil {
			t.Fatalf("producer-valid u64 maximum rejected: %v", err)
		}
	})

	t.Run("native-effect-int32-overflow", func(t *testing.T) {
		document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
		terminal := document["terminal_record"].(map[string]any)
		terminal["native_effect_observations"] = []any{map[string]any{
			"effect_id": "effect-1", "output_digest": journalTestZeroDigest,
			"status": json.Number("2147483648"), "stdout": []any{}, "stderr": []any{},
		}}
		journalTestRefreshTerminalPrefix(t, document)
		if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil ||
			!strings.Contains(err.Error(), "status") {
			t.Fatalf("native effect int32 overflow error=%v", err)
		}
	})

	t.Run("verifier-digest-mismatch", func(t *testing.T) {
		document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
		terminal := document["terminal_record"].(map[string]any)
		terminal["verifier_report_digest"] = journalTestZeroDigest
		journalTestRefreshTerminalPrefix(t, document)
		if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil ||
			!strings.Contains(err.Error(), "verifier") {
			t.Fatalf("verifier mismatch error=%v", err)
		}
	})
}

func TestAONextJournalTokenRowNullableContract(t *testing.T) {
	counters := []string{"input_tokens", "cached_input_tokens", "reasoning_tokens", "output_tokens"}
	for _, nullable := range append(append([]string(nil), counters...), "all") {
		t.Run("accept-null-"+nullable, func(t *testing.T) {
			document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
			tokens := journalTestTokens(t, document)
			if nullable == "all" {
				for _, field := range counters {
					tokens[field] = nil
				}
			} else {
				tokens[nullable] = nil
			}
			journalTestRefreshTerminalPrefix(t, document)
			if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err != nil {
				t.Fatalf("nullable token row rejected: %v", err)
			}
		})
	}
	for _, field := range append(append([]string(nil), counters...), "reported_total_tokens") {
		t.Run("missing-"+field, func(t *testing.T) {
			document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
			delete(journalTestTokens(t, document), field)
			if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("field %q is required", field)) {
				t.Fatalf("missing field error=%v", err)
			}
		})
	}
	invalids := []struct {
		name  string
		value any
	}{{"string", "7"}, {"fractional", json.Number("7.5")}, {"object", map[string]any{}}, {"boolean", true}}
	for _, field := range counters {
		for _, invalid := range invalids {
			t.Run("reject-"+field+"-"+invalid.name, func(t *testing.T) {
				document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
				journalTestTokens(t, document)[field] = invalid.value
				if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil {
					t.Fatal("invalid nullable counter passed")
				}
			})
		}
	}
	for _, invalid := range append(invalids, struct {
		name  string
		value any
	}{"null", nil}) {
		t.Run("reject-reported-total-"+invalid.name, func(t *testing.T) {
			document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
			journalTestTokens(t, document)["reported_total_tokens"] = invalid.value
			if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil {
				t.Fatal("invalid reported total passed")
			}
		})
	}
	if _, err := parseAONextJournalPrefix(validAONextJournalPrefix(t, "passed")); err != nil {
		t.Fatalf("11+7+5+13=36 rejected: %v", err)
	}
	document := journalTestDocument(t, validAONextJournalPrefix(t, "passed"))
	journalTestTokens(t, document)["reported_total_tokens"] = json.Number("35")
	journalTestRefreshTerminalPrefix(t, document)
	if _, err := parseAONextJournalPrefix(journalTestJSON(t, document)); err == nil || err.Error() != "AO Next journal token reported total 35 differs from calculated 36" {
		t.Fatalf("total mismatch error=%v", err)
	}
}

func assertRejectedJournalImportDoesNotMutate(t *testing.T, body []byte) {
	t.Helper()
	directory := t.TempDir()
	store := NewStore(filepath.Join(directory, "home"))
	record, err := store.Start("reject invalid AO Next journal")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(journalTestResolvedTempDir(t), "prefix.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	before := journalTestTreeSnapshot(t, store.Root)
	if _, err := ImportArtifact(store, record.MissionID, aoNextJournalPrefixImportKind, path); err == nil {
		t.Fatal("invalid journal prefix unexpectedly imported")
	}
	if after := journalTestTreeSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected journal prefix mutated Mission")
	}
}

func assertJournalLifecycleUnchanged(t *testing.T, before, after Record) {
	t.Helper()
	if before.Status != after.Status || before.CurrentRoute != after.CurrentRoute || before.CurrentPhase != after.CurrentPhase ||
		before.ExactNextAction != after.ExactNextAction || !reflect.DeepEqual(before.Blockers, after.Blockers) ||
		!reflect.DeepEqual(before.Steps, after.Steps) || !reflect.DeepEqual(before.RouteHistory, after.RouteHistory) ||
		!reflect.DeepEqual(before.WorkflowContract, after.WorkflowContract) ||
		!reflect.DeepEqual(before.Evidence.AONextCandidate, after.Evidence.AONextCandidate) {
		t.Fatalf("journal import changed durable Mission lifecycle: before=%#v after=%#v", before, after)
	}
}

func validAONextJournalPrefix(t *testing.T, state string) []byte {
	t.Helper()
	document := journalTestDocument(t, readJournalPrefixFixture(t, "valid-prepared.json"))
	events := make([]any, 0, 12)
	add := func(kind map[string]any) {
		events = append(events, map[string]any{"schema_version": "ao.next.journal-event.v1", "sequence": json.Number(fmt.Sprint(len(events))), "kind": kind})
	}
	provider := func() {
		add(map[string]any{"kind": "provider_request_intent", "prepared_run_digest": journalTestZeroDigest, "execution_authority_digest": journalTestOneDigest})
	}
	captured := func() {
		provider()
		add(map[string]any{"kind": "provider_process_started", "invocation_digest": journalTestZeroDigest})
		add(map[string]any{"kind": "provider_output_retained", "raw_capture_digest": journalTestZeroDigest})
		add(map[string]any{"kind": "provider_capture_index_published", "index_digest": journalTestOneDigest})
		add(map[string]any{"kind": "provider_capture_verified", "index_digest": journalTestOneDigest})
		add(map[string]any{"kind": "adapter_turn_normalized", "turn_digest": journalTestOneDigest})
	}
	effects := func(complete bool) {
		captured()
		add(map[string]any{"kind": "effect_intent", "effect_id": "effect-1", "effect_digest": journalTestOneDigest})
		if complete {
			add(map[string]any{"kind": "effect_completed", "observation": map[string]any{"effect_id": "effect-1", "output_digest": journalTestZeroDigest, "status": json.Number("0"), "stdout": []any{}, "stderr": []any{}}})
		}
	}
	verify := func() {
		effects(true)
		add(map[string]any{"kind": "verification_started", "attempt": json.Number("0")})
	}
	terminalState := ""
	switch state {
	case "prepared":
	case "provider_intent_recorded":
		provider()
	case "provider_outcome_unknown":
		provider()
		add(map[string]any{"kind": "provider_process_started", "invocation_digest": journalTestZeroDigest})
	case "provider_captured":
		captured()
	case "effect_outcome_unknown":
		effects(false)
	case "effects_pending":
		effects(true)
	case "verifying":
		verify()
	case "passed", "failed":
		verify()
		terminalState = state
	case "stopped_denied":
		verify()
		terminalState = "denied"
	case "stopped_interrupted":
		verify()
		terminalState = "interrupted"
	default:
		t.Fatalf("unsupported journal test state %q", state)
	}
	document["events"] = events
	document["last_sequence"] = nil
	if len(events) > 0 {
		document["last_sequence"] = json.Number(fmt.Sprint(len(events) - 1))
	}
	document["terminal_digest"] = nil
	document["terminal_record"] = nil
	if terminalState != "" {
		passed := journalTestDocument(t, readJournalPrefixFixture(t, "valid-passed.json"))
		terminal := passed["terminal_record"].(map[string]any)
		terminal["terminal_state"] = terminalState
		terminal["measurement"].(map[string]any)["task_success"] = terminalState == "passed"
		journalTestRefreshRecordDigest(t, terminal)
		terminalDigest := journalTestDigest(t, terminal)
		add(map[string]any{"kind": "verifier_recorded", "report_digest": journalTestOneDigest})
		add(map[string]any{"kind": "terminal_published", "record_digest": terminalDigest})
		document["events"] = events
		document["last_sequence"] = json.Number(fmt.Sprint(len(events) - 1))
		document["terminal_digest"] = terminalDigest
		document["terminal_record"] = terminal
	}
	journalTestRefreshPrefix(t, document)
	return journalTestJSON(t, document)
}

func journalTestRefreshTerminalPrefix(t *testing.T, document map[string]any) {
	t.Helper()
	terminal := document["terminal_record"].(map[string]any)
	journalTestRefreshRecordDigest(t, terminal)
	terminalDigest := journalTestDigest(t, terminal)
	document["terminal_digest"] = terminalDigest
	events := document["events"].([]any)
	events[len(events)-1].(map[string]any)["kind"].(map[string]any)["record_digest"] = terminalDigest
	journalTestRefreshPrefix(t, document)
}

func journalTestRefreshRecordDigest(t *testing.T, terminal map[string]any) {
	t.Helper()
	measurement := terminal["measurement"].(map[string]any)
	semanticMeasurement := make(map[string]any, len(measurement)-2)
	for key, value := range measurement {
		if key != "wall_clock_ms" && key != "model_wait_ms" {
			semanticMeasurement[key] = value
		}
	}
	terminal["record_digest"] = journalTestDigest(t, []any{terminal["variant"], terminal["terminal_state"], semanticMeasurement, terminal["capture_digests"], terminal["raw_capture_index_digest"], terminal["verifier_report_digest"], terminal["n7_execution_authority_digest"], terminal["git_workspace"], terminal["ao2_control_diagnostics"], terminal["native_effect_observations"]})
}

func journalTestRefreshPrefix(t *testing.T, document map[string]any) {
	t.Helper()
	document["events_digest"] = journalTestDigest(t, document["events"])
	document["prefix_digest"] = journalTestDigest(t, []any{document["schema_version"], document["run_id"], document["request_digest"], document["journal_identity"], document["worker_count"], document["dynamic_fanout"], document["first_sequence"], document["last_sequence"], document["preceding_prefix_digest"], document["events_digest"], document["events"], document["terminal_digest"], document["terminal_record"], document["safe_to_execute"], document["executes_work"], document["approves_work"], document["mutates_repositories"], document["grants_provider_access"], document["publishes_artifacts"], document["releases"], document["deploys"], document["advances_authority"]})
}

func journalTestTokens(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	terminal, ok := document["terminal_record"].(map[string]any)
	if !ok {
		t.Fatal("terminal record is not an object")
	}
	measurement, ok := terminal["measurement"].(map[string]any)
	if !ok {
		t.Fatal("measurement is not an object")
	}
	tokens, ok := measurement["tokens"].(map[string]any)
	if !ok {
		t.Fatal("tokens is not an object")
	}
	return tokens
}

func journalTestMeasurement(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	terminal, ok := document["terminal_record"].(map[string]any)
	if !ok {
		t.Fatal("terminal record is not an object")
	}
	measurement, ok := terminal["measurement"].(map[string]any)
	if !ok {
		t.Fatal("measurement is not an object")
	}
	return measurement
}

func readJournalPrefixFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "tests", "fixtures", "mission-migration", "journal-prefix", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func journalTestDocument(t *testing.T, body []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func journalTestDigest(t *testing.T, value any) string {
	t.Helper()
	return digestBytes(journalTestJSON(t, value))
}

func journalTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}

func journalTestReadOptional(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return body
}

func journalTestResolvedTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func journalTestRecordDiffIsOnlyImport(t *testing.T, beforeBody, afterBody []byte) bool {
	t.Helper()
	before := journalTestDocument(t, beforeBody)
	after := journalTestDocument(t, afterBody)
	after["artifact_refs"] = before["artifact_refs"]
	beforeEvidence, _ := before["evidence"].(map[string]any)
	afterEvidence, _ := after["evidence"].(map[string]any)
	delete(afterEvidence, "ao_next_journal_projection")
	if beforeEvidence == nil {
		beforeEvidence = map[string]any{}
	}
	if afterEvidence == nil {
		afterEvidence = map[string]any{}
	}
	before["evidence"] = beforeEvidence
	after["evidence"] = afterEvidence
	return reflect.DeepEqual(before, after)
}

func journalTestArtifactObjects(t *testing.T, root string) []string {
	t.Helper()
	directory := filepath.Join(root, retainedArtifactDirectory)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	objects := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			objects = append(objects, entry.Name())
		}
	}
	sort.Strings(objects)
	return objects
}

type journalTestSnapshotEntry struct {
	Path   string
	Mode   fs.FileMode
	Size   int64
	Digest string
}

func journalTestTreeSnapshot(t *testing.T, root string) []journalTestSnapshotEntry {
	t.Helper()
	entries := []journalTestSnapshotEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot := journalTestSnapshotEntry{Path: filepath.ToSlash(relative), Mode: info.Mode(), Size: info.Size()}
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.Digest = digestBytes(body)
		}
		entries = append(entries, snapshot)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}
