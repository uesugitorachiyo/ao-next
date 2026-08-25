package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTransactionRecoveryRejectsMalformedJournalAndPreimagesBeforeRestore(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture transactionValidationFixture) []byte
	}{
		{
			name: "duplicate field",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				return bytes.Replace(fixture.validJournal, []byte(`"mission_id":`), []byte(`"mission_id":"duplicate", "mission_id":`), 1)
			},
		},
		{
			name: "unknown field",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				return bytes.Replace(fixture.validJournal, []byte(`{`), []byte(`{"unexpected":true,`), 1)
			},
		},
		{
			name: "missing boolean",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				return removeTransactionJournalField(t, fixture.validJournal, "before_checkpoint_exists")
			},
		},
		{
			name: "null boolean",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				return replaceTransactionJournalField(t, fixture.validJournal, "before_checkpoint_exists", nil)
			},
		},
		{
			name: "checkpoint preimage present when boolean is false",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				journal := replaceTransactionJournalField(
					t,
					fixture.validJournal,
					"before_checkpoint_exists",
					false,
				)
				return replaceTransactionJournalField(
					t,
					journal,
					"before_checkpoint",
					[]byte{},
				)
			},
		},
		{
			name: "cross Mission journal",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				return replaceTransactionJournalField(t, fixture.validJournal, "mission_id", "mission-fedcba9876543210")
			},
		},
		{
			name: "duplicate record preimage field",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				record := bytes.Replace(fixture.beforeRecord, []byte(`"schema":`), []byte(`"schema":"duplicate", "schema":`), 1)
				return replaceTransactionJournalField(t, fixture.validJournal, "before_record", record)
			},
		},
		{
			name: "unknown record preimage field",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				record := bytes.Replace(fixture.beforeRecord, []byte(`{`), []byte(`{"unexpected":true,`), 1)
				return replaceTransactionJournalField(t, fixture.validJournal, "before_record", record)
			},
		},
		{
			name: "cross Mission record preimage",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				var foreign Record
				if err := json.Unmarshal(fixture.beforeRecord, &foreign); err != nil {
					t.Fatal(err)
				}
				foreign.MissionID = "mission-fedcba9876543210"
				body, err := marshalIndentedLine(foreign)
				if err != nil {
					t.Fatal(err)
				}
				return replaceTransactionJournalField(t, fixture.validJournal, "before_record", body)
			},
		},
		{
			name: "malformed record preimage",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				return replaceTransactionJournalField(t, fixture.validJournal, "before_record", []byte(`{"schema":"wrong"}`))
			},
		},
		{
			name: "cross Mission checkpoint preimage",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				var checkpoint map[string]any
				if err := json.Unmarshal(fixture.beforeCheckpoint, &checkpoint); err != nil {
					t.Fatal(err)
				}
				checkpoint["mission_id"] = "mission-fedcba9876543210"
				body, err := json.Marshal(checkpoint)
				if err != nil {
					t.Fatal(err)
				}
				return replaceTransactionJournalField(t, fixture.validJournal, "before_checkpoint", body)
			},
		},
		{
			name: "missing checkpoint preimage field",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				checkpoint := removeJSONField(t, fixture.beforeCheckpoint, "status")
				return replaceTransactionJournalField(t, fixture.validJournal, "before_checkpoint", checkpoint)
			},
		},
		{
			name: "null checkpoint preimage field",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				checkpoint := replaceJSONField(t, fixture.beforeCheckpoint, "safe_to_execute", nil)
				return replaceTransactionJournalField(t, fixture.validJournal, "before_checkpoint", checkpoint)
			},
		},
		{
			name: "malformed checkpoint preimage",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				return replaceTransactionJournalField(t, fixture.validJournal, "before_checkpoint", []byte(`not-json`))
			},
		},
		{
			name: "checkpoint count without latest checkpoint",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				record, checkpoint := checkpointPreimageWithLatestRecordCheckpoint(t, fixture)
				checkpoint = removeJSONField(t, checkpoint, "latest_checkpoint")
				return installPairedTransactionPreimages(t, fixture, record, checkpoint)
			},
		},
		{
			name: "checkpoint latest sequence disagrees with count",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				record, checkpoint := checkpointPreimageWithLatestRecordCheckpoint(t, fixture)
				var document map[string]any
				if err := json.Unmarshal(checkpoint, &document); err != nil {
					t.Fatal(err)
				}
				latest := document["latest_checkpoint"].(map[string]any)
				latest["sequence"] = 2
				body, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return installPairedTransactionPreimages(t, fixture, record, body)
			},
		},
		{
			name: "checkpoint latest iteration disagrees with paired record",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				record, checkpoint := checkpointPreimageWithLatestRecordCheckpoint(t, fixture)
				var document map[string]any
				if err := json.Unmarshal(checkpoint, &document); err != nil {
					t.Fatal(err)
				}
				latest := document["latest_checkpoint"].(map[string]any)
				latest["iteration"] = 99
				body, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return installPairedTransactionPreimages(t, fixture, record, body)
			},
		},
		{
			name: "checkpoint latest identity disagrees with paired record",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				record, checkpoint := checkpointPreimageWithLatestRecordCheckpoint(t, fixture)
				var document map[string]any
				if err := json.Unmarshal(checkpoint, &document); err != nil {
					t.Fatal(err)
				}
				latest := document["latest_checkpoint"].(map[string]any)
				latest["mission_id"] = "mission-fedcba9876543210"
				body, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return installPairedTransactionPreimages(t, fixture, record, body)
			},
		},
		{
			name: "checkpoint correlation disagrees with paired record",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				checkpoint := replaceJSONField(t, fixture.beforeCheckpoint, "correlation_id", "corr-foreign")
				return replaceTransactionJournalField(t, fixture.validJournal, "before_checkpoint", checkpoint)
			},
		},
		{
			name: "checkpoint return gate disagrees with paired record",
			mutate: func(t *testing.T, fixture transactionValidationFixture) []byte {
				t.Helper()
				var document map[string]any
				if err := json.Unmarshal(fixture.beforeCheckpoint, &document); err != nil {
					t.Fatal(err)
				}
				gate := document["return_gate"].(map[string]any)
				gate["final_response_allowed"] = !gate["final_response_allowed"].(bool)
				body, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return replaceTransactionJournalField(t, fixture.validJournal, "before_checkpoint", body)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTransactionValidationFixture(t)
			journal := tt.mutate(t, fixture)
			expectedRecord := readFileForTransactionTest(t, fixture.paths.Record)
			expectedCheckpoint := readFileForTransactionTest(t, fixture.paths.Checkpoint)
			if err := os.WriteFile(fixture.paths.Journal, journal, 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.store.Load(fixture.record.MissionID); err == nil {
				t.Fatal("invalid transaction recovery input was accepted")
			}
			if got := readFileForTransactionTest(t, fixture.paths.Record); !bytes.Equal(got, expectedRecord) {
				t.Fatal("invalid journal changed the Mission record")
			}
			if got := readFileForTransactionTest(t, fixture.paths.Checkpoint); !bytes.Equal(got, expectedCheckpoint) {
				t.Fatal("invalid journal changed the checkpoint")
			}
			if got := readFileForTransactionTest(t, fixture.paths.Journal); !bytes.Equal(got, journal) {
				t.Fatal("invalid journal was changed or removed")
			}
		})
	}
}

func TestCommittedTransactionCleanupFailureReturnsSuccessAndNeverRollsBack(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("committed cleanup is not transaction failure")
	if err != nil {
		t.Fatal(err)
	}
	beforeRecord := readFileForTransactionTest(t, store.path(record.MissionID))
	cleanupAttempted := false
	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "committed_journal_cleanup" {
			cleanupAttempted = true
			return errors.New("injected cleanup directory sync failure")
		}
		return nil
	}

	continued, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatalf("committed transaction reported cleanup failure: %v", err)
	}
	if !cleanupAttempted {
		t.Fatal("committed journal cleanup fault was not exercised")
	}
	if len(continued.Steps) != 1 {
		t.Fatalf("candidate continuation was not returned: %+v", continued)
	}
	candidateRecord := readFileForTransactionTest(t, store.path(record.MissionID))
	if bytes.Equal(candidateRecord, beforeRecord) {
		t.Fatal("committed candidate record was not persisted")
	}
	if _, err := os.Stat(store.transactionJournalPath(record.MissionID)); err != nil {
		t.Fatalf("committed marker was not retained after cleanup failure: %v", err)
	}

	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("recover committed marker: %v", err)
	}
	if len(recovered.Steps) != 1 {
		t.Fatalf("committed transaction was rolled back: %+v", recovered)
	}
	if got := readFileForTransactionTest(t, store.path(record.MissionID)); !bytes.Equal(got, candidateRecord) {
		t.Fatal("committed recovery changed candidate record bytes")
	}
	if _, err := os.Stat(store.transactionJournalPath(record.MissionID)); !os.IsNotExist(err) {
		t.Fatalf("committed marker was not cleaned up: %v", err)
	}
}

func TestCommittedMarkerPostReplacementSyncFailureReturnsSuccess(t *testing.T) {
	if !committedJournalReplacementNeedsDirectorySync {
		t.Skip("committed marker replacement has no directory-sync phase on this target")
	}
	store := NewStore(t.TempDir())
	record, err := store.Start("post replacement committed marker sync")
	if err != nil {
		t.Fatal(err)
	}
	faultObserved := false
	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "after_committed_journal_replace" {
			faultObserved = true
			return errors.New("injected post-replacement directory sync failure")
		}
		return nil
	}

	continued, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatalf("visible committed-marker replacement was reported as failure: %v", err)
	}
	if !faultObserved {
		t.Fatal("exact post-replacement fault was not exercised")
	}
	if len(continued.Steps) != 1 {
		t.Fatalf("committed continuation was not returned: %+v", continued)
	}
	if _, err := os.Stat(store.transactionJournalPath(record.MissionID)); err != nil {
		t.Fatalf("committed marker was not retained after ambiguous sync: %v", err)
	}

	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("recover visible committed marker: %v", err)
	}
	if len(recovered.Steps) != 1 {
		t.Fatalf("visible committed transaction was rolled back: %+v", recovered)
	}
}

func TestRecoveryAcceptsLegacyJournalWithImplicitCandidateEventDecision(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("recover legacy event journal")
	if err != nil {
		t.Fatal(err)
	}
	record, err = Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	paths := store.transactionPaths(record.MissionID)
	recordBody := readFileForTransactionTest(t, paths.Record)
	checkpointBody := readFileForTransactionTest(t, paths.Checkpoint)
	eventBody := readFileForTransactionTest(t, paths.EventDecision)
	legacyJournal := map[string]any{
		"schema":                          missionTransactionJournalSchema,
		"state":                           missionTransactionStateCommitted,
		"mission_id":                      record.MissionID,
		"before_record":                   recordBody,
		"before_checkpoint":               checkpointBody,
		"before_checkpoint_exists":        true,
		"candidate_record_digest":         digestBytes(recordBody),
		"candidate_checkpoint_digest":     digestBytes(checkpointBody),
		"event_decision_transactional":    true,
		"before_event_decision":           eventBody,
		"before_event_decision_exists":    true,
		"candidate_event_decision_digest": digestBytes(eventBody),
	}
	writeJSONForTest(t, paths.Journal, legacyJournal)

	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("legacy v0.1 journal was not recoverable: %v", err)
	}
	if len(recovered.Steps) != 1 {
		t.Fatalf("legacy committed recovery changed Mission state: %+v", recovered)
	}
	if _, err := os.Stat(paths.Journal); !os.IsNotExist(err) {
		t.Fatalf("legacy committed journal was not cleaned up: %v", err)
	}
}

func TestMissionLockCleansOnlyExactTransactionTempFiles(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("clean exact orphan transaction temp files")
	if err != nil {
		t.Fatal(err)
	}
	paths := store.transactionPaths(record.MissionID)
	missionsDir := filepath.Dir(paths.Record)
	orphans := []string{
		filepath.Join(missionsDir, "."+filepath.Base(paths.Record)+".tmp-orphan"),
		filepath.Join(missionsDir, "."+filepath.Base(paths.Checkpoint)+".tmp-orphan"),
		filepath.Join(missionsDir, "."+filepath.Base(paths.Journal)+".tmp-orphan"),
	}
	preserved := []string{
		filepath.Join(missionsDir, "."+filepath.Base(paths.Record)+".tmp"),
		filepath.Join(missionsDir, "."+filepath.Base(paths.Record)+".tmpish-unrelated"),
		filepath.Join(missionsDir, ".unrelated.json.tmp-orphan"),
	}
	for _, path := range append(append([]string{}, orphans...), preserved...) {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := store.Load(record.MissionID); err != nil {
		t.Fatal(err)
	}
	for _, path := range orphans {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("exact orphan temp remains at %s: %v", path, err)
		}
	}
	for _, path := range preserved {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated file was removed at %s: %v", path, err)
		}
	}
}

type transactionValidationFixture struct {
	store            Store
	record           Record
	paths            missionTransactionPaths
	beforeRecord     []byte
	beforeCheckpoint []byte
	validJournal     []byte
}

func newTransactionValidationFixture(t *testing.T) transactionValidationFixture {
	t.Helper()
	store := NewStore(t.TempDir())
	record, err := store.Start("strict transaction recovery validation")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCheckpointBundle(BuildCheckpointBundle(record)); err != nil {
		t.Fatal(err)
	}
	paths := store.transactionPaths(record.MissionID)
	beforeRecord := readFileForTransactionTest(t, paths.Record)
	beforeCheckpoint := readFileForTransactionTest(t, paths.Checkpoint)
	journal := map[string]any{
		"schema":                      missionTransactionJournalSchema,
		"state":                       "prepared",
		"mission_id":                  record.MissionID,
		"before_record":               beforeRecord,
		"before_checkpoint":           beforeCheckpoint,
		"before_checkpoint_exists":    true,
		"candidate_record_digest":     digestBytes(beforeRecord),
		"candidate_checkpoint_digest": digestBytes(beforeCheckpoint),
	}
	validJournal, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	return transactionValidationFixture{
		store:            store,
		record:           record,
		paths:            paths,
		beforeRecord:     beforeRecord,
		beforeCheckpoint: beforeCheckpoint,
		validJournal:     validJournal,
	}
}

func checkpointPreimageWithLatestRecordCheckpoint(
	t *testing.T,
	fixture transactionValidationFixture,
) ([]byte, []byte) {
	t.Helper()
	var record Record
	if err := json.Unmarshal(fixture.beforeRecord, &record); err != nil {
		t.Fatal(err)
	}
	step := ContinuationStep{
		Schema:          StepSchema,
		MissionID:       record.MissionID,
		CorrelationID:   record.CorrelationID,
		Iteration:       1,
		Route:           record.CurrentRoute,
		Result:          "handoff_required",
		ExactNextAction: record.ExactNextAction,
		GeneratedAtUTC:  record.UpdatedAtUTC,
	}
	record.Steps = append(record.Steps, step)
	record.Checkpoints = append(record.Checkpoints, MissionCheckpoint{
		Schema:          MissionCheckpointSchema,
		MissionID:       record.MissionID,
		CorrelationID:   record.CorrelationID,
		Sequence:        1,
		Iteration:       1,
		Route:           record.CurrentRoute,
		Phase:           record.CurrentPhase,
		Result:          "handoff_required",
		ExactNextAction: record.ExactNextAction,
		ResumeCommand:   "ao-mission continue --mission " + record.MissionID,
		GeneratedAtUTC:  record.UpdatedAtUTC,
	})
	recordBody, err := marshalIndentedLine(record)
	if err != nil {
		t.Fatal(err)
	}
	fixture.beforeRecord = recordBody
	checkpointBody, err := marshalIndentedLine(BuildCheckpointBundle(record))
	if err != nil {
		t.Fatal(err)
	}
	return recordBody, checkpointBody
}

func installPairedTransactionPreimages(
	t *testing.T,
	fixture transactionValidationFixture,
	record []byte,
	checkpoint []byte,
) []byte {
	t.Helper()
	if err := os.WriteFile(fixture.paths.Record, record, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.paths.Checkpoint, checkpoint, 0o644); err != nil {
		t.Fatal(err)
	}
	journal := replaceTransactionJournalField(t, fixture.validJournal, "before_record", record)
	return replaceTransactionJournalField(t, journal, "before_checkpoint", checkpoint)
}

func replaceTransactionJournalField(t *testing.T, body []byte, field string, value any) []byte {
	t.Helper()
	return replaceJSONField(t, body, field, value)
}

func replaceJSONField(t *testing.T, body []byte, field string, value any) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	document[field] = value
	replaced, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return replaced
}

func removeTransactionJournalField(t *testing.T, body []byte, field string) []byte {
	t.Helper()
	return removeJSONField(t, body, field)
}

func removeJSONField(t *testing.T, body []byte, field string) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, field)
	replaced, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return replaced
}
