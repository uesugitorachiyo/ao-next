package mission

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportAtlasWorkgraphReactivatesCompletedMissionWithReadyNodes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("run multiple bounded Atlas waves")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(store, record.MissionID, "atlas-final-synthesis-readback", writeParentBoundAtlasFinalSynthesisReadback(t, dir, record.MissionID)); err != nil {
		t.Fatal(err)
	}

	completed, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "done" {
		t.Fatalf("precondition: final synthesis did not complete mission: %+v", completed)
	}

	workgraphPath := filepath.Join(dir, "next-wave-workgraph.json")
	workgraph := `{"schema":"ao.atlas.workgraph.v0.1","nodes":[{"id":"next-wave-node-1","status":"ready"}]}`
	if err := os.WriteFile(workgraphPath, []byte(workgraph), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(store, record.MissionID, "atlas-workgraph", workgraphPath); err != nil {
		t.Fatal(err)
	}

	updated, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "active" || updated.CurrentRoute != "ao-foundry" || updated.CurrentPhase != "atlas_workgraph_ready" {
		t.Fatalf("ready next-wave workgraph did not reactivate mission: %+v", updated)
	}
	if updated.ExactNextAction != "next-wave-node-1" {
		t.Fatalf("exact next action = %q, want next-wave-node-1", updated.ExactNextAction)
	}
	if updated.Evidence.AtlasRecommendation != nil || updated.Evidence.AtlasFinalSynthesis != nil || updated.Evidence.FoundryRollup != nil {
		t.Fatalf("new ready workgraph retained stale terminal evidence: %+v", updated.Evidence)
	}
	if updated.ReturnGate == nil || updated.ReturnGate.FinalResponseAllowed || updated.ReturnGate.ReadyNodesRemaining != 1 {
		t.Fatalf("ready next-wave workgraph did not close return gate: %+v", updated.ReturnGate)
	}
}

func TestImportAtlasWorkgraphClearsSupersededBlockersWhenReady(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("replace a terminal blocker with an executable Atlas wave")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(rec *Record) error {
		rec.Status = "blocked"
		rec.Blockers = []string{"superseded upstream verification failure"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	workgraphPath := filepath.Join(dir, "replacement-workgraph.json")
	workgraph := `{"schema":"ao.atlas.workgraph.v0.1","nodes":[{"id":"replacement-ready-node","status":"ready"}]}`
	if err := os.WriteFile(workgraphPath, []byte(workgraph), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(store, record.MissionID, "atlas-workgraph", workgraphPath); err != nil {
		t.Fatal(err)
	}

	updated, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Blockers) != 0 {
		t.Fatalf("ready workgraph retained superseded blockers: %+v", updated)
	}
	if updated.ReturnGate == nil || updated.ReturnGate.HardBlocker || updated.ReturnGate.FinalResponseAllowed {
		t.Fatalf("ready workgraph retained terminal blocker in return gate: %+v", updated.ReturnGate)
	}
}

func TestEvaluateReturnGateDeniesReadyNodesForStaleDoneRecord(t *testing.T) {
	record := Record{
		MissionID:       "mission-stale-done",
		Status:          "done",
		ExactNextAction: "next-wave-node-1",
		Evidence: EvidenceSummary{
			AtlasWorkgraph: &NodeCounts{Total: 9, Completed: 8, Ready: 1},
		},
		GoalLease: &GoalLease{MinNodes: 8},
	}

	gate := EvaluateReturnGate(record)
	if gate.FinalResponseAllowed || gate.Status != "early_return_denied" || gate.ReadyNodesRemaining != 1 {
		t.Fatalf("stale done record with ready work passed return gate: %+v", gate)
	}
}
