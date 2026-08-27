package mission

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestContinuePersistsEachIterationAsRecordCheckpointTransaction(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("persist each continuation iteration")
	if err != nil {
		t.Fatal(err)
	}

	var checkpointReplacements atomic.Int32
	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage != "before_checkpoint_replace" {
			return nil
		}
		if checkpointReplacements.Add(1) == 2 {
			return errors.New("interrupt second continuation transaction")
		}
		return nil
	}
	if _, err := Continue(store, record.MissionID, ContinueOptions{
		UntilDone:     true,
		MaxIterations: 3,
		MinNodes:      10,
	}); err == nil || !strings.Contains(err.Error(), "interrupt second continuation transaction") {
		t.Fatalf("Continue did not stop at the injected second-iteration interruption: %v", err)
	}

	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("recover interrupted continuation: %v", err)
	}
	bundle, err := store.LoadCheckpointBundle(record.MissionID)
	if err != nil {
		t.Fatalf("load recovered continuation checkpoint: %v", err)
	}
	if len(recovered.Steps) != 1 || len(recovered.Checkpoints) != 1 {
		t.Fatalf("first durable iteration was not preserved: steps=%d checkpoints=%d", len(recovered.Steps), len(recovered.Checkpoints))
	}
	if bundle.CheckpointCount != 1 || bundle.LatestCheckpoint == nil ||
		bundle.LatestCheckpoint.Iteration != recovered.Steps[0].Iteration {
		t.Fatalf("checkpoint does not match the first durable iteration: %+v", bundle)
	}
}

func TestConcurrentContinueCannotOverwriteNewerCheckpoint(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("serialize concurrent continuation transactions")
	if err != nil {
		t.Fatal(err)
	}

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var blockFirst sync.Once
	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_record_cas" {
			blockFirst.Do(func() {
				close(firstEntered)
				<-releaseFirst
			})
		}
		return nil
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
		firstDone <- err
	}()
	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Continue did not enter its record/checkpoint transaction")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second Continue was not serialized behind the first: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}

	store.transactionFault = nil
	finalRecord, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.LoadCheckpointBundle(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalRecord.Steps) != 2 || len(finalRecord.Checkpoints) != 2 {
		t.Fatalf("concurrent continuations lost an iteration: steps=%d checkpoints=%d", len(finalRecord.Steps), len(finalRecord.Checkpoints))
	}
	if bundle.CheckpointCount != 2 || bundle.LatestCheckpoint == nil ||
		bundle.LatestCheckpoint.Iteration != finalRecord.Steps[1].Iteration {
		t.Fatalf("checkpoint was stale after concurrent continuations: %+v", bundle)
	}
}

func TestFailedContinuationDoesNotLeaveFutureEventLoopDecision(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("rollback event-loop decision with continuation")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Steps) != 1 {
		t.Fatalf("first continuation did not commit: %+v", first)
	}
	firstDecision, err := store.LoadEventLoopDecision(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if firstDecision.Iteration != 1 {
		t.Fatalf("unexpected first event-loop decision: %+v", firstDecision)
	}

	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_journal_commit" {
			return errors.New("interrupt second continuation after event decision")
		}
		return nil
	}
	if _, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1}); err == nil {
		t.Fatal("second continuation was not interrupted")
	}
	journalBody, err := os.ReadFile(store.transactionJournalPath(record.MissionID))
	if err != nil {
		t.Fatal(err)
	}
	var journal missionTransactionJournal
	if err := json.Unmarshal(journalBody, &journal); err != nil {
		t.Fatal(err)
	}
	if !journal.EventDecisionTransactional ||
		!journal.BeforeEventDecisionExists ||
		len(journal.BeforeEventDecision) == 0 {
		t.Fatalf("prepared journal did not preserve the prior event decision: %+v", journal)
	}
	futureDecision := readFileForTransactionTest(t, store.eventLoopPath(record.MissionID))
	if digestBytes(futureDecision) != journal.CandidateEventDecisionDigest {
		t.Fatal("prepared journal did not bind the atomically written candidate event decision")
	}

	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("recover interrupted continuation: %v", err)
	}
	decision, err := store.LoadEventLoopDecision(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Steps) != 1 {
		t.Fatalf("record did not roll back to first continuation: %+v", recovered)
	}
	if decision.Iteration != 1 || decision != firstDecision {
		t.Fatalf("future event-loop decision survived rollback:\nfirst=%+v\nafter=%+v", firstDecision, decision)
	}
}

func TestFailedFirstContinuationRemovesRolledBackEventLoopDecision(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("remove rolled-back first event-loop decision")
	if err != nil {
		t.Fatal(err)
	}
	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_journal_commit" {
			return errors.New("interrupt first continuation after event decision")
		}
		return nil
	}
	if _, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1}); err == nil {
		t.Fatal("first continuation was not interrupted")
	}
	journalBody, err := os.ReadFile(store.transactionJournalPath(record.MissionID))
	if err != nil {
		t.Fatal(err)
	}
	var journal missionTransactionJournal
	if err := json.Unmarshal(journalBody, &journal); err != nil {
		t.Fatal(err)
	}
	if !journal.EventDecisionTransactional || journal.BeforeEventDecisionExists {
		t.Fatalf("prepared first-continuation journal has wrong event preimage state: %+v", journal)
	}

	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("recover interrupted first continuation: %v", err)
	}
	if len(recovered.Steps) != 0 {
		t.Fatalf("first continuation record did not roll back: %+v", recovered)
	}
	if _, err := store.LoadEventLoopDecision(record.MissionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back first event-loop decision remains: %v", err)
	}
}
