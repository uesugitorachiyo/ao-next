package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIssueRepairSupervisorPersistsEventLeaseAndCheckpointAtomically(t *testing.T) {
	store, record := issueRepairTestMission(t)
	state, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "active" || len(state.Events) != 1 || state.Checkpoint.LastEventSequence != 1 {
		t.Fatalf("unexpected supervisor state: %+v", state)
	}
	if state.Lease.Status != "active" || state.Lease.PreviousWorkerActive ||
		state.Lease.SuccessorResumeAuthorized {
		t.Fatalf("unsafe lease state: %+v", state.Lease)
	}
	if state.Checkpoint.CheckpointDigest == "" || state.Events[0].EventDigest == "" {
		t.Fatalf("missing digest binding: %+v", state)
	}
	loaded, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Evidence.IssueRepairSupervisor == nil ||
		loaded.Evidence.IssueRepairSupervisor.Checkpoint.CheckpointDigest != state.Checkpoint.CheckpointDigest {
		t.Fatalf("supervisor state was not persisted with Mission: %+v", loaded.Evidence)
	}
	if len(loaded.Checkpoints) != 1 || len(loaded.Steps) != 1 {
		t.Fatalf("existing continuation/checkpoint lifecycle was not used: %+v", loaded)
	}
	index, err := BuildMissionEventIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	search := SearchMissionEvents(index, MissionEventSearchFilters{
		MissionID: record.MissionID,
		Query:     "issue repair run_started",
	})
	if search.TotalMatches == 0 {
		t.Fatalf("supervisor event absent from Mission event index: %+v", search)
	}
}

func TestIssueRepairSupervisorExactRetryIsIdempotent(t *testing.T) {
	store, record := issueRepairTestMission(t)
	request := issueRepairRequest("run_started")
	first, err := SuperviseIssueRepair(store, record.MissionID, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SuperviseIssueRepair(store, record.MissionID, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Checkpoint.CheckpointDigest != first.Checkpoint.CheckpointDigest {
		t.Fatalf("exact retry created a duplicate write: first=%+v second=%+v", first, second)
	}
}

func TestIssueRepairSupervisorRetryRejectsChangedLeaseCheckpointOrBudget(t *testing.T) {
	store, record := issueRepairTestMission(t)
	request := issueRepairRequest("run_started")
	first, err := SuperviseIssueRepair(store, record.MissionID, request)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*IssueRepairSupervisorRequest){
		"lease expiry": func(candidate *IssueRepairSupervisorRequest) {
			candidate.LeaseExpiresAt = "2026-07-28T10:59:59Z"
		},
		"checkpoint": func(candidate *IssueRepairSupervisorRequest) {
			candidate.ExpectedCheckpointDigest = strings.Repeat("0", 64)
		},
		"budget": func(candidate *IssueRepairSupervisorRequest) {
			candidate.EventBudget++
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if _, err := SuperviseIssueRepair(store, record.MissionID, candidate); err == nil {
				t.Fatalf("changed retry %s was accepted against checkpoint %s", name, first.Checkpoint.CheckpointDigest)
			}
		})
	}
}

func TestIssueRepairSupervisorExactPostFirstEventReplayIsIdempotent(t *testing.T) {
	store, record := issueRepairTestMission(t)
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	request := issueRepairRequest("discovery_completed")
	request.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	persisted, err := SuperviseIssueRepair(store, record.MissionID, request)
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := SuperviseIssueRepair(store, record.MissionID, request)
	if err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	if !reflect.DeepEqual(replayed, persisted) {
		t.Fatalf("exact replay changed persisted state")
	}
	if len(replayed.Events) != 2 {
		t.Fatalf("exact replay event count = %d, want 2", len(replayed.Events))
	}
}

func TestIssueRepairSupervisorRejectsLeaseAndCheckpointConflicts(t *testing.T) {
	store, record := issueRepairTestMission(t)
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	conflict := issueRepairRequest("discovery_completed")
	conflict.LeaseID = "lease-other-worker"
	conflict.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, conflict); err == nil ||
		!strings.Contains(err.Error(), "lease conflict") {
		t.Fatalf("live-worker lease conflict was not rejected: %v", err)
	}
	mismatch := issueRepairRequest("discovery_completed")
	mismatch.ExpectedCheckpointDigest = strings.Repeat("0", 64)
	if _, err := SuperviseIssueRepair(store, record.MissionID, mismatch); err == nil ||
		!strings.Contains(err.Error(), "checkpoint digest mismatch") {
		t.Fatalf("checkpoint mismatch was not rejected: %v", err)
	}
}

func TestIssueRepairSupervisorFailsClosedAtBudgetExhaustion(t *testing.T) {
	store, record := issueRepairTestMission(t)
	request := issueRepairRequest("run_started")
	request.EventBudget = 1
	first, err := SuperviseIssueRepair(store, record.MissionID, request)
	if err != nil {
		t.Fatal(err)
	}
	next := issueRepairRequest("discovery_completed")
	next.EventBudget = 1
	next.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, next); err == nil ||
		!strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("budget exhaustion did not fail closed: %v", err)
	}
}

func TestIssueRepairSupervisorChainsEventsAndPreservesAuthorityBoundary(t *testing.T) {
	store, record := issueRepairTestMission(t)
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	next := issueRepairRequest("discovery_completed")
	next.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	second, err := SuperviseIssueRepair(store, record.MissionID, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 2 || second.Events[1].PreviousEventDigest == nil ||
		*second.Events[1].PreviousEventDigest != second.Events[0].EventDigest {
		t.Fatalf("event digest chain is broken: %+v", second.Events)
	}
	if second.SafeToExecute || second.ExecutesWork || second.ApprovesWork ||
		second.MutatesRepositories {
		t.Fatalf("supervisor widened authority: %+v", second)
	}
}

func TestIssueRepairSupervisorRejectsExpiredOrChangedLease(t *testing.T) {
	store, record := issueRepairTestMission(t)
	expired := issueRepairRequest("run_started")
	expired.LeaseExpiresAt = "2026-07-28T09:59:59Z"
	if _, err := SuperviseIssueRepair(store, record.MissionID, expired); err == nil ||
		!strings.Contains(err.Error(), "lease expired") {
		t.Fatalf("expired lease was not rejected: %v", err)
	}
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	changed := issueRepairRequest("discovery_completed")
	changed.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	changed.LeaseOwner = "different-owner"
	if _, err := SuperviseIssueRepair(store, record.MissionID, changed); err == nil ||
		!strings.Contains(err.Error(), "ownership mismatch") {
		t.Fatalf("changed lease ownership was not rejected: %v", err)
	}
}

func TestIssueRepairSupervisorInterruptedWriteRecoversWithoutPartialEvent(t *testing.T) {
	store, record := issueRepairTestMission(t)
	store.transactionFault = func(stage string, _ missionTransactionPaths) error {
		if stage == "before_checkpoint_replace" {
			return errors.New("injected supervisor interruption")
		}
		return nil
	}
	if _, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started")); err == nil {
		t.Fatal("interrupted supervisor write unexpectedly succeeded")
	}
	store.transactionFault = nil
	recovered, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Evidence.IssueRepairSupervisor != nil || len(recovered.Steps) != 0 {
		t.Fatalf("interrupted transaction retained partial supervisor state: %+v", recovered)
	}
}

func TestIssueRepairSupervisorRejectsTamperedPersistedChain(t *testing.T) {
	store, record := issueRepairTestMission(t)
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.Evidence.IssueRepairSupervisor.Events[0].EventDigest = strings.Repeat("f", 64)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := issueRepairRequest("discovery_completed")
	next.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, next); err == nil ||
		!strings.Contains(err.Error(), "event digest mismatch") {
		t.Fatalf("tampered event chain was not rejected: %v", err)
	}
}

func TestIssueRepairSupervisorRejectsTamperedCheckpointLease(t *testing.T) {
	store, record := issueRepairTestMission(t)
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.Evidence.IssueRepairSupervisor.Checkpoint.Lease.Owner = "tampered-owner"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := issueRepairRequest("discovery_completed")
	next.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, next); err == nil ||
		!strings.Contains(err.Error(), "checkpoint lease mismatch") {
		t.Fatalf("tampered checkpoint lease was not rejected: %v", err)
	}
}

func TestIssueRepairSupervisorRejectsHashConsistentInvalidEvent(t *testing.T) {
	store, record := issueRepairTestMission(t)
	if _, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		state := candidate.Evidence.IssueRepairSupervisor
		state.Events[0].EventType = "arbitrary_execution"
		digest, err := canonicalIssueRepairDigest(state.Events[0], "event_digest")
		if err != nil {
			return err
		}
		state.Events[0].EventDigest = digest
		checkpoint, err := buildIssueRepairCheckpoint(*state, state.Checkpoint.CreatedAt)
		if err != nil {
			return err
		}
		state.Checkpoint = checkpoint
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := issueRepairRequest("discovery_completed")
	loaded, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	next.ExpectedCheckpointDigest = loaded.Evidence.IssueRepairSupervisor.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, next); err == nil ||
		!strings.Contains(err.Error(), "event semantics") {
		t.Fatalf("hash-consistent invalid event was not rejected: %v", err)
	}
}

func TestIssueRepairSupervisorRejectsHashConsistentTerminalResurrection(t *testing.T) {
	store, record := issueRepairTestMission(t)
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	terminal := issueRepairRequest("run_completed")
	terminal.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		state := candidate.Evidence.IssueRepairSupervisor
		state.Status = "active"
		state.Lease.Status = "active"
		checkpoint, err := buildIssueRepairCheckpoint(*state, state.Checkpoint.CreatedAt)
		if err != nil {
			return err
		}
		state.Checkpoint = checkpoint
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := issueRepairRequest("checkpoint_created")
	loaded, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	next.ExpectedCheckpointDigest = loaded.Evidence.IssueRepairSupervisor.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, next); err == nil ||
		!strings.Contains(err.Error(), "terminal status and event mismatch") {
		t.Fatalf("hash-consistent terminal resurrection was not rejected: %v", err)
	}
}

func TestIssueRepairSupervisorRejectsBuriedTerminalEvent(t *testing.T) {
	store, record := issueRepairTestMission(t)
	first, err := SuperviseIssueRepair(store, record.MissionID, issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	terminal := issueRepairRequest("run_completed")
	terminal.ExpectedCheckpointDigest = first.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		state := candidate.Evidence.IssueRepairSupervisor
		state.Status = "active"
		state.Lease.Status = "active"
		request := issueRepairRequest("checkpoint_created")
		event, err := buildIssueRepairEvent(*state, request, state.Checkpoint.CreatedAt)
		if err != nil {
			return err
		}
		state.Events = append(state.Events, event)
		checkpoint, err := buildIssueRepairCheckpoint(*state, state.Checkpoint.CreatedAt)
		if err != nil {
			return err
		}
		state.Checkpoint = checkpoint
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := issueRepairRequest("discovery_completed")
	loaded, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	next.ExpectedCheckpointDigest = loaded.Evidence.IssueRepairSupervisor.Checkpoint.CheckpointDigest
	if _, err := SuperviseIssueRepair(store, record.MissionID, next); err == nil ||
		!strings.Contains(err.Error(), "lifecycle order") {
		t.Fatalf("buried terminal event was not rejected: %v", err)
	}
}

func TestIssueRepairCanonicalDigestsMatchArchitectureVectors(t *testing.T) {
	previous := strings.Repeat("9", 64)
	event := IssueRepairEvent{
		Schema:              IssueRepairEventSchema,
		RunID:               "repair-run-20260728",
		RunEnvelopeDigest:   "1b2970bc69f2be7493dc2b4783fa9a5b169f6656d88c2aefb5557d8440a4b588",
		Sequence:            2,
		PreviousEventDigest: &previous,
		EventDigest:         "9367e38c4214c9684aa6a3e200bdf2fc72a8ed5c8cc28cc9ce63654605f261aa",
		Actor:               "ao-forge",
		LeaseID:             "lease-run-20260728",
		EventType:           "discovery_completed",
		InputDigests:        []string{strings.Repeat("2", 64)},
		OutputDigests:       []string{strings.Repeat("5", 64)},
		ReasonCode:          "DISCOVERY_COMPLETE",
		Timestamp:           "2026-07-27T23:00:00Z",
	}
	eventDigest, err := canonicalIssueRepairDigest(event, "event_digest")
	if err != nil {
		t.Fatal(err)
	}
	if eventDigest != event.EventDigest {
		t.Fatalf("event canonical digest=%s want=%s", eventDigest, event.EventDigest)
	}
	checkpoint := IssueRepairCheckpoint{
		Schema:            IssueRepairCheckpointSchema,
		RunID:             event.RunID,
		RunEnvelopeDigest: event.RunEnvelopeDigest,
		LastEventSequence: 2,
		LastEventDigest:   event.EventDigest,
		StateDigest:       strings.Repeat("a", 64),
		Lease: IssueRepairLease{
			LeaseID:                   "lease-run-20260728",
			Owner:                     "ao-forge",
			Status:                    "active",
			ExpiresAt:                 "2026-07-27T23:50:00Z",
			OwnershipVerifiedAt:       "2026-07-27T23:00:00Z",
			PreviousWorkerActive:      false,
			SuccessorResumeAuthorized: false,
			AuthorizedEventActors:     []string{"ao-forge"},
		},
		CheckpointDigest: "c74b7571119710c4464be314cb1dbdb0f610d5da372d01481f695153e023f029",
		CreatedAt:        "2026-07-27T23:01:00Z",
	}
	checkpointDigest, err := canonicalIssueRepairDigest(checkpoint, "checkpoint_digest")
	if err != nil {
		t.Fatal(err)
	}
	if checkpointDigest != checkpoint.CheckpointDigest {
		t.Fatalf("checkpoint canonical digest=%s want=%s", checkpointDigest, checkpoint.CheckpointDigest)
	}
}

func TestCLIIssueRepairSupervisorAcceptsStrictBoundedRequest(t *testing.T) {
	store, record := issueRepairTestMission(t)
	requestPath := filepath.Join(t.TempDir(), "request.json")
	body, err := json.Marshal(issueRepairCLIRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--home", store.Root,
		"issue-repair", "supervise",
		"--mission", record.MissionID,
		"--request", requestPath,
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("issue-repair supervise failed: %s", stderr.String())
	}
	var state IssueRepairSupervisorState
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Schema != IssueRepairSupervisorSchema || len(state.Events) != 1 {
		t.Fatalf("unexpected CLI state: %+v", state)
	}
}

func TestCLIIssueRepairSupervisorRejectsUnknownAndOversizedInput(t *testing.T) {
	store, record := issueRepairTestMission(t)
	dir := t.TempDir()
	unknownPath := filepath.Join(dir, "unknown.json")
	body, err := json.Marshal(issueRepairRequest("run_started"))
	if err != nil {
		t.Fatal(err)
	}
	body = append(body[:len(body)-1], []byte(`,"execute":false}`)...)
	if err := os.WriteFile(unknownPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--home", store.Root, "issue-repair", "supervise",
		"--mission", record.MissionID, "--request", unknownPath, "--json",
	}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("unknown field was not rejected: code=%d stderr=%s", code, stderr.String())
	}

	oversizedPath := filepath.Join(dir, "oversized.json")
	if err := os.WriteFile(oversizedPath, bytes.Repeat([]byte("x"), issueRepairRequestLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{
		"--home", store.Root, "issue-repair", "supervise",
		"--mission", record.MissionID, "--request", oversizedPath, "--json",
	}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "exceeds") {
		t.Fatalf("oversized request was not rejected: code=%d stderr=%s", code, stderr.String())
	}
}

func issueRepairTestMission(t *testing.T) (Store, Record) {
	t.Helper()
	stamp := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := NewStore(t.TempDir())
	store.Clock = func() time.Time { return stamp }
	record, err := store.Start("supervise bounded autonomous GitHub issue repair")
	if err != nil {
		t.Fatal(err)
	}
	return store, record
}

func issueRepairRequest(eventType string) IssueRepairSupervisorRequest {
	return IssueRepairSupervisorRequest{
		RunID:             "repair-run-20260728",
		RunEnvelopeDigest: strings.Repeat("a", 64),
		Actor:             "ao-mission",
		LeaseID:           "lease-worker-20260728",
		LeaseOwner:        "ao-mission",
		LeaseExpiresAt:    "2026-07-28T11:00:00Z",
		EventType:         eventType,
		InputDigests:      []string{strings.Repeat("b", 64)},
		OutputDigests:     []string{strings.Repeat("c", 64)},
		ReasonCode:        "SUPERVISOR_TEST_EVENT",
		EventBudget:       8,
	}
}

func issueRepairCLIRequest(eventType string) IssueRepairSupervisorRequest {
	request := issueRepairRequest(eventType)
	request.LeaseExpiresAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	return request
}
