package mission

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContinuePreservesImportedWorkflowRouteForLegacyMission(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("supervise a bounded Atlas workgraph")
	if err != nil {
		t.Fatal(err)
	}

	authorizationPath := filepath.Join(store.Root, "build-authorization.json")
	if err := os.WriteFile(authorizationPath, []byte(`{
  "schema": "ao.blueprint.build-authorization.v0.1",
  "project_id": "month-1",
  "status": "ready",
  "approved_by_user": true,
  "blocking_assumptions": [],
  "next_allowed_action": "ao-atlas"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(store, record.MissionID, "blueprint-authorization", authorizationPath); err != nil {
		t.Fatal(err)
	}

	workgraphPath := filepath.Join(store.Root, "workgraph.json")
	if err := os.WriteFile(workgraphPath, []byte(`{
  "contract_version": "ao.atlas.workgraph.v0.1",
  "id": "month-1-workgraph",
  "target_instance": "ao-stack",
  "nodes": [{"id": "node-1", "status": "ready", "dependencies": [], "blockers": []}]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(store, record.MissionID, "atlas-workgraph", workgraphPath); err != nil {
		t.Fatal(err)
	}

	continued, err := Continue(store, record.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	if continued.CurrentRoute != "ao-foundry" || continued.ExactNextAction != "node-1" {
		t.Fatalf("continuation discarded imported route: route=%s next=%q", continued.CurrentRoute, continued.ExactNextAction)
	}
	if len(continued.Steps) != 1 || continued.Steps[0].Route != "ao-foundry" {
		t.Fatalf("continuation step did not preserve imported route: %+v", continued.Steps)
	}
}
