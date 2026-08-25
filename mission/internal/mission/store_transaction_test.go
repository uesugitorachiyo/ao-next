package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportTransactionRecoversRecordAndCheckpointAfterInterruptedPartialWrite(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	if err := store.SaveCheckpointBundle(BuildCheckpointBundle(record)); err != nil {
		t.Fatal(err)
	}
	artifactPath, chainPath := transactionTestArtifactAndChain(t, dir, record)
	beforeRecord := readFileForTransactionTest(t, store.path(record.MissionID))
	beforeCheckpoint := readFileForTransactionTest(t, store.checkpointPath(record.MissionID))

	store.transactionFault = func(stage string, paths missionTransactionPaths) error {
		if stage != "before_checkpoint_replace" {
			return nil
		}
		if err := os.WriteFile(paths.Checkpoint, []byte("partial checkpoint\n"), 0o644); err != nil {
			return err
		}
		return errors.New("injected interruption before checkpoint replacement")
	}
	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPath,
		chainPath,
	); err == nil || !strings.Contains(err.Error(), "injected interruption") {
		t.Fatalf("interrupted transaction did not fail: %v", err)
	}

	candidateRecord := readFileForTransactionTest(t, store.path(record.MissionID))
	partialCheckpoint := readFileForTransactionTest(t, store.checkpointPath(record.MissionID))
	if bytes.Equal(candidateRecord, beforeRecord) {
		t.Fatal("failure injection did not leave candidate Mission state for recovery")
	}
	if bytes.Equal(partialCheckpoint, beforeCheckpoint) {
		t.Fatal("failure injection did not leave partial checkpoint state for recovery")
	}
	if _, err := os.Stat(store.transactionJournalPath(record.MissionID)); err != nil {
		t.Fatalf("transaction journal is not durable after interruption: %v", err)
	}

	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_recovery_restore" {
			return errors.New("injected recovery interruption")
		}
		return nil
	}
	if _, err := store.Load(record.MissionID); err == nil ||
		!strings.Contains(err.Error(), "injected recovery interruption") {
		t.Fatalf("recovery failure was not reported: %v", err)
	}
	if got := readFileForTransactionTest(t, store.path(record.MissionID)); !bytes.Equal(got, candidateRecord) {
		t.Fatal("failed recovery changed Mission before restoration was allowed")
	}
	if got := readFileForTransactionTest(t, store.checkpointPath(record.MissionID)); !bytes.Equal(got, partialCheckpoint) {
		t.Fatal("failed recovery changed checkpoint before restoration was allowed")
	}

	store.transactionFault = nil
	if _, err := store.Load(record.MissionID); err != nil {
		t.Fatalf("next load did not recover interrupted transaction: %v", err)
	}
	if got := readFileForTransactionTest(t, store.path(record.MissionID)); !bytes.Equal(got, beforeRecord) {
		t.Fatalf("Mission recovery was not byte-exact:\nwant=%s\ngot=%s", beforeRecord, got)
	}
	if got := readFileForTransactionTest(t, store.checkpointPath(record.MissionID)); !bytes.Equal(got, beforeCheckpoint) {
		t.Fatalf("checkpoint recovery was not byte-exact:\nwant=%s\ngot=%s", beforeCheckpoint, got)
	}
	if _, err := os.Stat(store.transactionJournalPath(record.MissionID)); !os.IsNotExist(err) {
		t.Fatalf("recovered transaction journal remains: %v", err)
	}
}

func TestInterruptedImportRecoversAfterLifecycleUpdateWithExistingCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	if _, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1}); err != nil {
		t.Fatal(err)
	}
	paused, err := Pause(store, record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath, chainPath := transactionTestArtifactAndChain(t, dir, paused)
	beforeRecord := readFileForTransactionTest(t, store.path(record.MissionID))
	beforeCheckpoint := readFileForTransactionTest(t, store.checkpointPath(record.MissionID))

	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_checkpoint_replace" {
			return errors.New("injected interrupted import after lifecycle update")
		}
		return nil
	}
	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPath,
		chainPath,
	); err == nil || !strings.Contains(err.Error(), "injected interrupted import") {
		t.Fatalf("interrupted import did not fail at the expected boundary: %v", err)
	}

	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("lifecycle update left an unrecoverable checkpoint preimage: %v", err)
	}
	if recovered.Status != "paused" {
		t.Fatalf("recovery lost paused lifecycle state: %+v", recovered)
	}
	if got := readFileForTransactionTest(t, store.path(record.MissionID)); !bytes.Equal(got, beforeRecord) {
		t.Fatal("recovery did not restore exact post-lifecycle Mission bytes")
	}
	if got := readFileForTransactionTest(t, store.checkpointPath(record.MissionID)); !bytes.Equal(got, beforeCheckpoint) {
		t.Fatal("recovery did not restore exact post-lifecycle checkpoint bytes")
	}
}

func TestInterruptedImportRecoversAfterSaveWithExistingCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	continued, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	continued.ExactNextAction = "preserve an exact valid Save replacement"
	if err := store.Save(continued); err != nil {
		t.Fatal(err)
	}
	artifactPath, chainPath := transactionTestArtifactAndChain(t, dir, continued)
	beforeRecord := readFileForTransactionTest(t, store.path(record.MissionID))
	beforeCheckpoint := readFileForTransactionTest(t, store.checkpointPath(record.MissionID))

	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_checkpoint_replace" {
			return errors.New("injected interrupted import after Save")
		}
		return nil
	}
	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPath,
		chainPath,
	); err == nil || !strings.Contains(err.Error(), "injected interrupted import") {
		t.Fatalf("interrupted import did not fail at the expected boundary: %v", err)
	}

	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("Save left an unrecoverable checkpoint preimage: %v", err)
	}
	if recovered.ExactNextAction != continued.ExactNextAction {
		t.Fatalf("recovery lost the saved Mission state: %+v", recovered)
	}
	if got := readFileForTransactionTest(t, store.path(record.MissionID)); !bytes.Equal(got, beforeRecord) {
		t.Fatal("recovery did not restore exact saved Mission bytes")
	}
	if got := readFileForTransactionTest(t, store.checkpointPath(record.MissionID)); !bytes.Equal(got, beforeCheckpoint) {
		t.Fatal("recovery did not restore exact checkpoint bytes paired with Save")
	}
}

func TestSaveReplacementSynchronizesEventDecisionWithReplacementRecord(t *testing.T) {
	store, record := startCorrelationTestMission(t, t.TempDir())
	continued, err := Continue(store, record.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Steps) != 2 || len(continued.Checkpoints) != 2 {
		t.Fatalf("continuation did not create two iterations: %+v", continued)
	}

	replacement := continued
	replacement.Steps = append([]ContinuationStep(nil), continued.Steps[:1]...)
	replacement.Checkpoints = append([]MissionCheckpoint(nil), continued.Checkpoints[:1]...)
	replacement.CurrentRoute = replacement.Steps[0].Route
	replacement.CurrentPhase = replacement.Steps[0].Result
	replacement.ExactNextAction = replacement.Steps[0].ExactNextAction
	if err := store.Save(replacement); err != nil {
		t.Fatal(err)
	}

	decision, err := store.LoadEventLoopDecision(record.MissionID)
	if err != nil {
		t.Fatalf("load synchronized event decision: %v", err)
	}
	if decision.Iteration != replacement.Steps[0].Iteration ||
		decision.Route != replacement.Steps[0].Route ||
		decision.ExactNextAction != replacement.Steps[0].ExactNextAction {
		t.Fatalf("Save retained an event decision from the replaced record: %+v", decision)
	}

	replacement.Steps = []ContinuationStep{}
	replacement.Checkpoints = []MissionCheckpoint{}
	replacement.CurrentPhase = "routing"
	if err := store.Save(replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadEventLoopDecision(record.MissionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Save retained an event decision for a replacement without steps: %v", err)
	}
}

func TestUpdateSynchronizesEventDecisionWithMutatedSteps(t *testing.T) {
	store, record := startCorrelationTestMission(t, t.TempDir())
	if _, err := Continue(store, record.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 2}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.Steps = append([]ContinuationStep(nil), candidate.Steps[:1]...)
		candidate.Checkpoints = append([]MissionCheckpoint(nil), candidate.Checkpoints[:1]...)
		candidate.CurrentRoute = candidate.Steps[0].Route
		candidate.CurrentPhase = candidate.Steps[0].Result
		candidate.ExactNextAction = candidate.Steps[0].ExactNextAction
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.LoadEventLoopDecision(record.MissionID)
	if err != nil {
		t.Fatalf("load synchronized Update event: %v", err)
	}
	if decision.Iteration != updated.Steps[0].Iteration {
		t.Fatalf("Update retained a stale event decision: %+v", decision)
	}

	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.Steps = []ContinuationStep{}
		candidate.Checkpoints = []MissionCheckpoint{}
		candidate.CurrentPhase = "routing"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadEventLoopDecision(record.MissionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Update retained an event decision after removing all steps: %v", err)
	}
}

func TestFirstArchiveImportMaterializesCheckpointAndEventSidecars(t *testing.T) {
	source, record := startCorrelationTestMission(t, t.TempDir())
	continued, err := Continue(source, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := BuildMissionArchive(continued)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "mission-archive.json")
	writeJSONForTest(t, archivePath, archive)

	destination := NewStore(t.TempDir())
	if _, err := ImportMissionArchive(destination, archivePath); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := destination.LoadCheckpointBundle(record.MissionID)
	if err != nil {
		t.Fatalf("first archive import omitted checkpoint sidecar: %v", err)
	}
	if checkpoint.CheckpointCount != len(continued.Checkpoints) {
		t.Fatalf("imported checkpoint sidecar disagrees with archive: %+v", checkpoint)
	}
	decision, err := destination.LoadEventLoopDecision(record.MissionID)
	if err != nil {
		t.Fatalf("first archive import omitted event sidecar: %v", err)
	}
	if decision.Iteration != continued.Steps[len(continued.Steps)-1].Iteration {
		t.Fatalf("imported event sidecar disagrees with archive: %+v", decision)
	}
}

func TestFirstSaveKeepsPairedStateAfterVisibleReplacementFault(t *testing.T) {
	source, record := startCorrelationTestMission(t, t.TempDir())
	continued, err := Continue(source, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{
		"after_initial_checkpoint_replace",
		"after_initial_event_decision_replace",
		"after_initial_record_replace",
	} {
		t.Run(stage, func(t *testing.T) {
			destination := NewStore(t.TempDir())
			observed := false
			destination.transactionFault = func(got string, _ missionTransactionPaths) error {
				if got == stage {
					observed = true
					return errors.New("injected visible replacement fault")
				}
				return nil
			}
			if err := destination.Save(continued); err != nil {
				t.Fatalf("visible replacement was reported as failed: %v", err)
			}
			if !observed {
				t.Fatal("visible replacement fault was not exercised")
			}
			if _, err := destination.Load(record.MissionID); err != nil {
				t.Fatalf("visible Mission record became unreadable: %v", err)
			}
			if _, err := destination.LoadCheckpointBundle(record.MissionID); err != nil {
				t.Fatalf("visible Mission record lost checkpoint sidecar: %v", err)
			}
			if _, err := destination.LoadEventLoopDecision(record.MissionID); err != nil {
				t.Fatalf("visible Mission record lost event sidecar: %v", err)
			}
		})
	}
}

func TestStandaloneSidecarsRejectStateThatDisagreesWithMission(t *testing.T) {
	store, record := startCorrelationTestMission(t, t.TempDir())
	continued, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	checkpointPath := store.checkpointPath(record.MissionID)
	eventPath := store.eventLoopPath(record.MissionID)
	beforeCheckpoint := readFileForTransactionTest(t, checkpointPath)
	beforeEvent := readFileForTransactionTest(t, eventPath)

	badCheckpoint := BuildCheckpointBundle(continued)
	badCheckpoint.CorrelationID = "corr-foreign"
	if err := store.SaveCheckpointBundle(badCheckpoint); err == nil {
		t.Fatal("SaveCheckpointBundle accepted state from another correlation")
	}
	if got := readFileForTransactionTest(t, checkpointPath); !bytes.Equal(got, beforeCheckpoint) {
		t.Fatal("rejected checkpoint changed durable sidecar bytes")
	}

	badEvent, err := store.LoadEventLoopDecision(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	badEvent.Iteration++
	if err := store.SaveEventLoopDecision(badEvent); err == nil {
		t.Fatal("SaveEventLoopDecision accepted state that disagrees with Mission")
	}
	if got := readFileForTransactionTest(t, eventPath); !bytes.Equal(got, beforeEvent) {
		t.Fatal("rejected event decision changed durable sidecar bytes")
	}

	writeJSONForTest(t, checkpointPath, badCheckpoint)
	if _, err := store.LoadCheckpointBundle(record.MissionID); err == nil {
		t.Fatal("LoadCheckpointBundle exposed state that disagrees with Mission")
	}
	if err := os.WriteFile(eventPath, mustMarshalIndentedLineForTransactionTest(t, badEvent), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadEventLoopDecision(record.MissionID); err == nil {
		t.Fatal("LoadEventLoopDecision exposed state that disagrees with Mission")
	}
}

func TestImportTransactionCASPreservesConcurrentMissionUpdate(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	if err := store.SaveCheckpointBundle(BuildCheckpointBundle(record)); err != nil {
		t.Fatal(err)
	}
	artifactPath, chainPath := transactionTestArtifactAndChain(t, dir, record)
	beforeCheckpoint := readFileForTransactionTest(t, store.checkpointPath(record.MissionID))

	concurrent := record
	concurrent.ExactNextAction = "concurrent Mission update must survive"
	concurrent.UpdatedAtUTC = "2026-07-20T13:00:00Z"
	concurrentBytes, err := json.MarshalIndent(concurrent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	concurrentBytes = append(concurrentBytes, '\n')
	store.transactionFault = func(stage string, paths missionTransactionPaths) error {
		if stage == "before_record_cas" {
			return os.WriteFile(paths.Record, concurrentBytes, 0o644)
		}
		return nil
	}

	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPath,
		chainPath,
	); err == nil || !strings.Contains(err.Error(), "compare-and-swap") {
		t.Fatalf("concurrent update was not rejected by CAS: %v", err)
	}
	if got := readFileForTransactionTest(t, store.path(record.MissionID)); !bytes.Equal(got, concurrentBytes) {
		t.Fatal("failed import overwrote concurrent Mission update")
	}
	if got := readFileForTransactionTest(t, store.checkpointPath(record.MissionID)); !bytes.Equal(got, beforeCheckpoint) {
		t.Fatal("CAS failure changed checkpoint")
	}
	if _, err := os.Stat(store.transactionJournalPath(record.MissionID)); !os.IsNotExist(err) {
		t.Fatalf("CAS failure created a transaction journal: %v", err)
	}
}

func TestStoreListRecoversInterruptedImportBeforeReturningRecords(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	if err := store.SaveCheckpointBundle(BuildCheckpointBundle(record)); err != nil {
		t.Fatal(err)
	}
	artifactPath, chainPath := transactionTestArtifactAndChain(t, dir, record)
	beforeRecord := readFileForTransactionTest(t, store.path(record.MissionID))
	beforeCheckpoint := readFileForTransactionTest(t, store.checkpointPath(record.MissionID))
	store.transactionFault = func(stage string, paths missionTransactionPaths) error {
		if stage != "before_checkpoint_replace" {
			return nil
		}
		if err := os.WriteFile(paths.Checkpoint, []byte("partial checkpoint\n"), 0o644); err != nil {
			return err
		}
		return errors.New("injected list recovery interruption")
	}
	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPath,
		chainPath,
	); err == nil {
		t.Fatal("failure injection did not interrupt import")
	}

	records, err := store.List()
	if err != nil {
		t.Fatalf("list did not recover interrupted import: %v", err)
	}
	if len(records) != 1 || records[0].MissionID != record.MissionID ||
		len(records[0].ArtifactRefs) != len(record.ArtifactRefs) ||
		len(records[0].CorrelatedImports) != 0 {
		t.Fatalf("list exposed candidate or service-file state: %+v", records)
	}
	if got := readFileForTransactionTest(t, store.path(record.MissionID)); !bytes.Equal(got, beforeRecord) {
		t.Fatal("list recovery did not restore Mission bytes")
	}
	if got := readFileForTransactionTest(t, store.checkpointPath(record.MissionID)); !bytes.Equal(got, beforeCheckpoint) {
		t.Fatal("list recovery did not restore checkpoint bytes")
	}
}

func TestMissionStoreLockSerializesWriters(t *testing.T) {
	store := NewStore(t.TempDir())
	missionID := "mission-0123456789abcdef"
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.withMissionLock(missionID, func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.withMissionLock(missionID, func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second writer entered while first Mission lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second writer did not acquire released Mission lock")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func transactionTestArtifactAndChain(t *testing.T, dir string, record Record) (string, string) {
	t.Helper()
	artifactPath := filepath.Join(dir, "authorization.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":           "ao.blueprint.build-authorization.v0.1",
		"status":           "ready",
		"authorization_id": "authorization-transaction-recovery-001",
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "blueprint-authorization",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)
	return artifactPath, chainPath
}

func readFileForTransactionTest(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustMarshalIndentedLineForTransactionTest(t *testing.T, value any) []byte {
	t.Helper()
	body, err := marshalIndentedLine(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
