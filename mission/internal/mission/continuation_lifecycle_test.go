package mission

import (
	"fmt"
	"strings"
	"testing"
)

const pauseContinuationAction = "resume mission before continuation"

func TestPauseResumeRestoresLatestDurableFoundryActionAcrossReload(t *testing.T) {
	store := NewStore(t.TempDir())
	contract, err := store.StartObjective(
		"supervise a bounded Atlas external issue repair workgraph",
		ObjectiveStartOptions{CorrelationID: "corr-pause-resume-foundry"},
	)
	if err != nil {
		t.Fatal(err)
	}
	const nextAction = "month2-gap-closure-reconciliation"
	if _, err := store.Update(contract.MissionID, func(record *Record) error {
		record.CurrentRoute = "ao-foundry"
		record.CurrentPhase = "atlas_workgraph_ready"
		record.ExactNextAction = nextAction
		record.Evidence.AtlasWorkgraph = &NodeCounts{
			Total: 8, Ready: 1, Completed: 7,
		}
		AppendRouteHistory(record, RouteDecision{
			Schema:          RouteSchema,
			MissionID:       record.MissionID,
			Route:           record.CurrentRoute,
			Reason:          "latest Atlas workgraph imported",
			SafeToRequest:   true,
			SafeToExecute:   false,
			SafeToPromote:   false,
			ExactNextAction: record.ExactNextAction,
			GeneratedAtUTC:  now(store.Clock),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	paused, err := Pause(store, contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != "paused" || paused.CurrentPhase != "paused" || paused.ExactNextAction != pauseContinuationAction {
		t.Fatalf("pause did not expose the bounded resume action: %+v", paused)
	}
	if paused.ReturnGate == nil || paused.ReturnGate.ExactNextAction != pauseContinuationAction ||
		paused.Reconciliation == nil || paused.Reconciliation.ExactNextAction != pauseContinuationAction {
		t.Fatalf("pause projections did not expose the bounded resume action: gate=%+v reconciliation=%+v", paused.ReturnGate, paused.Reconciliation)
	}

	reloaded := NewStore(store.Root)
	resumed, err := Resume(reloaded, contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	assertRestoredFoundryMission(t, resumed, nextAction)
	assertNoStalePauseAction(t, resumed)

	bundle, err := reloaded.LoadCheckpointBundle(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.CheckpointCount != 0 || bundle.LatestCheckpoint != nil {
		t.Fatalf("pause changed checkpoint-to-step accounting: %+v", bundle)
	}
	assertNoStalePauseAction(t, bundle)
	if len(resumed.RouteHistory) < 1 {
		t.Fatal("resume lost route history")
	}
	boundary := resumed.RouteHistory[len(resumed.RouteHistory)-1]
	if boundary.Reason != "mission pause boundary" || boundary.CorrelationID != resumed.CorrelationID ||
		boundary.Route != "ao-foundry" || boundary.ExactNextAction != nextAction {
		t.Fatalf("pause boundary did not bind the imported continuation: %+v", boundary)
	}
}

func TestRepeatedPauseResumeRestoresTheSameDurableAction(t *testing.T) {
	store := NewStore(t.TempDir())
	contract, err := store.StartObjective(
		"supervise a bounded Atlas external issue repair workgraph",
		ObjectiveStartOptions{CorrelationID: "corr-repeat-pause-resume"},
	)
	if err != nil {
		t.Fatal(err)
	}
	const nextAction = "send the next safe node to AO Foundry"
	if _, err := store.Update(contract.MissionID, func(record *Record) error {
		record.CurrentRoute = "ao-foundry"
		record.CurrentPhase = "handoff_required"
		record.ExactNextAction = nextAction
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		if _, err := Pause(store, contract.MissionID); err != nil {
			t.Fatal(err)
		}
		resumed, err := Resume(NewStore(store.Root), contract.MissionID)
		if err != nil {
			t.Fatal(err)
		}
		if resumed.Status != "active" || resumed.CurrentRoute != "ao-foundry" ||
			resumed.CurrentPhase != "routing" || resumed.ExactNextAction != nextAction {
			t.Fatalf("pause/resume cycle %d changed durable continuation: %+v", cycle+1, resumed)
		}
		assertNoStalePauseAction(t, resumed)
	}
}

func TestResumeUsesWorkflowFallbackWhenNoUsableCheckpointExists(t *testing.T) {
	store := NewStore(t.TempDir())
	contract, err := store.StartObjective(
		"supervise a bounded Atlas external issue repair workgraph",
		ObjectiveStartOptions{CorrelationID: "corr-resume-route-fallback"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(contract.MissionID, func(record *Record) error {
		record.Status = "paused"
		record.CurrentRoute = "ao-foundry"
		record.CurrentPhase = "paused"
		record.ExactNextAction = pauseContinuationAction
		record.Checkpoints = []MissionCheckpoint{}
		record.Steps = []ContinuationStep{}
		record.RouteHistory = append(record.RouteHistory, RouteDecision{
			Schema:          RouteSchema,
			MissionID:       record.MissionID,
			Route:           "ao-foundry",
			Reason:          "persisted route fallback",
			SafeToRequest:   true,
			SafeToExecute:   false,
			SafeToPromote:   false,
			ExactNextAction: "continue the imported AO Foundry node",
			GeneratedAtUTC:  now(store.Clock),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	resumed, err := Resume(NewStore(store.Root), contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "active" || resumed.CurrentRoute != contract.InitialRoute ||
		resumed.CurrentPhase != "routing" || resumed.ExactNextAction != contract.ExactNextAction {
		t.Fatalf("resume did not use deterministic workflow fallback: %+v", resumed)
	}
	assertNoStalePauseAction(t, resumed)
}

func TestResumeRejectsForeignCorrelationCheckpoint(t *testing.T) {
	store := NewStore(t.TempDir())
	contract, err := store.StartObjective(
		"supervise a bounded Atlas external issue repair workgraph",
		ObjectiveStartOptions{CorrelationID: "corr-resume-owner"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(store, contract.MissionID, ContinueOptions{MaxIterations: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(contract.MissionID, func(record *Record) error {
		record.Status = "paused"
		record.CurrentPhase = "paused"
		record.ExactNextAction = pauseContinuationAction
		record.Checkpoints[len(record.Checkpoints)-1].CorrelationID = "corr-foreign"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := Resume(NewStore(store.Root), contract.MissionID); err == nil ||
		!strings.Contains(err.Error(), "checkpoint correlation") {
		t.Fatalf("resume did not reject foreign checkpoint correlation: %v", err)
	}
}

func TestPausedCompletedMissionResumesIntoEarlyReturnDenial(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("resume a long-running Atlas mission")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(current *Record) error {
		current.Status = "done"
		current.CurrentRoute = "complete"
		current.CurrentPhase = "complete"
		current.ExactNextAction = "mission complete; read final rollup and recommended next tasks"
		current.ReturnGate = &ReturnGate{
			Schema: ReturnGateSchema, MissionID: current.MissionID,
			Status: "return_allowed", FinalResponseAllowed: true,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pause(store, record.MissionID); err != nil {
		t.Fatal(err)
	}

	resumed, err := Resume(NewStore(store.Root), record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "active" || resumed.CurrentRoute == "complete" ||
		resumed.CurrentPhase == "complete" || resumed.ReturnGate == nil ||
		resumed.ReturnGate.Status != "early_return_denied" || resumed.ReturnGate.FinalResponseAllowed {
		t.Fatalf("completed mission did not resume through the early-return gate: %+v", resumed)
	}
	assertNoStalePauseAction(t, resumed)
}

func assertRestoredFoundryMission(t *testing.T, record Record, nextAction string) {
	t.Helper()
	if record.Status != "active" || record.CurrentRoute != "ao-foundry" ||
		record.CurrentPhase != "routing" || record.ExactNextAction != nextAction {
		t.Fatalf("resume did not restore the Foundry continuation: %+v", record)
	}
	if record.Evidence.AtlasWorkgraph == nil ||
		record.Evidence.AtlasWorkgraph.Total != 8 ||
		record.Evidence.AtlasWorkgraph.Completed != 7 ||
		record.Evidence.AtlasWorkgraph.Ready != 1 ||
		record.Evidence.AtlasWorkgraph.Blocked != 0 ||
		record.Evidence.AtlasWorkgraph.Failed != 0 {
		t.Fatalf("pause/resume changed Atlas counts: %+v", record.Evidence.AtlasWorkgraph)
	}
	if record.ReturnGate == nil || record.ReturnGate.FinalResponseAllowed ||
		record.ReturnGate.ReadyNodesRemaining != 1 || record.ReturnGate.CompletedNodes != 7 {
		t.Fatalf("resume did not recompute the return gate from restored state: %+v", record.ReturnGate)
	}
}

func assertNoStalePauseAction(t *testing.T, value any) {
	t.Helper()
	if strings.Contains(fmt.Sprintf("%+v", value), pauseContinuationAction) {
		t.Fatalf("resumed surface retained stale pause action: %+v", value)
	}
}
