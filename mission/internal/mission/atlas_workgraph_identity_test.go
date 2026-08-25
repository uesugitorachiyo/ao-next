package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAtlasWorkgraphImportRejectsCorrelatedIdentityMismatches(t *testing.T) {
	// These cases catch an import implementation that only verifies correlation_id,
	// then accepts a Workgraph for another (or no) Mission instance.
	for _, tc := range []struct {
		name           string
		missionID      string
		includeMission bool
		targetInstance string
		includeTarget  bool
		wantError      string
	}{
		{
			name:           "missing mission_id",
			includeMission: false,
			targetInstance: "mission-bound-by-test",
			includeTarget:  true,
			wantError:      "atlas-workgraph mission_id is required for correlated mission",
		},
		{
			name:           "wrong mission_id",
			missionID:      "mission-other-instance",
			includeMission: true,
			targetInstance: "mission-bound-by-test",
			includeTarget:  true,
			wantError:      "atlas-workgraph mission_id does not match mission",
		},
		{
			name:           "missing target_instance",
			includeMission: true,
			includeTarget:  false,
			wantError:      "atlas-workgraph target_instance is required for correlated mission",
		},
		{
			name:           "wrong target_instance",
			includeMission: true,
			targetInstance: "mission-other-target",
			includeTarget:  true,
			wantError:      "atlas-workgraph target_instance does not match mission",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "mission-home")
			store, record := startAtlasWorkgraphIdentityMission(t, home)
			if tc.missionID == "" {
				tc.missionID = record.MissionID
			}
			if tc.targetInstance == "mission-bound-by-test" {
				tc.targetInstance = record.MissionID
			}
			artifactPath := writeAtlasWorkgraphIdentityFixture(t, t.TempDir(), record,
				tc.missionID, tc.includeMission, tc.targetInstance, tc.includeTarget)

			beforeFiles := snapshotMissionHomeFiles(t, home)
			beforeRecord := atlasWorkgraphIdentityDurableStateOf(record)
			_, err := ImportArtifact(store, record.MissionID, "atlas-workgraph", artifactPath)
			if err == nil {
				t.Fatalf("wrong and missing identity fixtures are accepted or mutate state: expected error containing %q, got nil", tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wrong and missing identity fixtures are accepted or mutate state: error=%q, want substring %q", err, tc.wantError)
			}

			afterFiles := snapshotMissionHomeFiles(t, home)
			if !reflect.DeepEqual(afterFiles, beforeFiles) {
				t.Fatalf("rejected identity fixture changed Mission home:\nbefore=%+v\nafter=%+v", beforeFiles, afterFiles)
			}
			assertNoAtlasWorkgraphImportSideEffects(t, store, record.MissionID)
			afterRecord, err := store.Load(record.MissionID)
			if err != nil {
				t.Fatal(err)
			}
			if got := atlasWorkgraphIdentityDurableStateOf(afterRecord); !reflect.DeepEqual(got, beforeRecord) {
				t.Fatalf("rejected identity fixture changed durable Mission state:\nbefore=%+v\nafter=%+v", beforeRecord, got)
			}
		})
	}
}

func TestAtlasWorkgraphImportAcceptsMatchingCorrelatedIdentity(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mission-home")
	store, record := startAtlasWorkgraphIdentityMission(t, home)
	artifactPath := writeAtlasWorkgraphIdentityFixture(t, t.TempDir(), record,
		record.MissionID, true, record.MissionID, true)

	if _, err := ImportArtifact(store, record.MissionID, "atlas-workgraph", artifactPath); err != nil {
		t.Fatalf("matching correlated Workgraph rejected: %v", err)
	}
	updated, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Evidence.AtlasWorkgraph == nil || updated.Evidence.AtlasWorkgraph.Total != 1 || updated.Evidence.AtlasWorkgraph.Ready != 1 {
		t.Fatalf("matching correlated Workgraph was not imported: %+v", updated.Evidence.AtlasWorkgraph)
	}
}

func TestAtlasWorkgraphImportRetainsUncorrelatedIdentityCompatibility(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mission-home")
	store := NewStore(home)
	record, err := store.Start("Verify uncorrelated Atlas Workgraph compatibility")
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := writeAtlasWorkgraphIdentityFixture(t, t.TempDir(), record, "", false, "", false)

	if _, err := ImportArtifact(store, record.MissionID, "atlas-workgraph", artifactPath); err != nil {
		t.Fatalf("uncorrelated Workgraph without identity fields rejected: %v", err)
	}
}

func startAtlasWorkgraphIdentityMission(t *testing.T, home string) (Store, Record) {
	t.Helper()
	store := NewStore(home)
	contract, err := store.StartObjective(
		"Verify correlated Atlas Workgraph identity binding",
		ObjectiveStartOptions{CorrelationID: "correlation-workgraph-identity-gate"},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	return store, record
}

func writeAtlasWorkgraphIdentityFixture(
	t *testing.T,
	dir string,
	record Record,
	missionID string,
	includeMission bool,
	targetInstance string,
	includeTarget bool,
) string {
	t.Helper()
	missionField := ""
	if includeMission {
		missionField = fmt.Sprintf("  \"mission_id\": %q,\n", missionID)
	}
	targetField := ""
	if includeTarget {
		targetField = fmt.Sprintf("  \"target_instance\": %q,\n", targetInstance)
	}
	body := fmt.Sprintf(`{
  "contract_version": "ao.atlas.workgraph.v0.1",
  "id": "workgraph-identity-gate",
%s%s  "objective_digest": %q,
  "correlation_id": %q,
  "nodes": [
    {
      "id": "node-01",
      "status": "ready",
      "dependencies": [],
      "blockers": [],
      "stitch_task": false,
      "factory_task": {
        "contract_version": "ao.atlas.factory-task.v0.1",
        "id": "task-01",
        "objective": "identity gate fixture",
        "target_factory_repo": "ao-mission",
        "factory_folder": "identity-gate/node-01",
        "acceptance_criteria": ["identity binding passes"],
        "non_goals": ["no repository mutation"],
        "write_scope": ["identity-gate/node-01"],
        "required_gates": ["identity_bound"],
        "rollback_scope": ["identity-gate/node-01"],
        "verification_commands": ["go test"],
        "required_evidence": ["node-01.json"],
        "safety_limits": ["no authority expansion"],
        "authority_boundary": "bounded_test",
        "dependency_refs": [],
        "context_pack_refs": []
      }
    }
  ]
}
`, targetField, missionField, record.ObjectiveDigest, record.CorrelationID)
	path := filepath.Join(dir, "atlas-workgraph.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type atlasWorkgraphIdentityFileSnapshot struct {
	Digest string
	Size   int64
}

func snapshotMissionHomeFiles(t *testing.T, home string) map[string]atlasWorkgraphIdentityFileSnapshot {
	t.Helper()
	files := map[string]atlasWorkgraphIdentityFileSnapshot{}
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		files[filepath.ToSlash(relative)] = atlasWorkgraphIdentityFileSnapshot{
			Digest: hex.EncodeToString(digest[:]),
			Size:   int64(len(body)),
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

type atlasWorkgraphIdentityDurableState struct {
	ArtifactRefs    []ArtifactRef
	Evidence        EvidenceSummary
	CurrentRoute    string
	CurrentPhase    string
	ExactNextAction string
	RouteHistory    []RouteDecision
	Steps           []ContinuationStep
	Checkpoints     []MissionCheckpoint
	ReturnGate      *ReturnGate
	Reconciliation  *RouteReconciliation
	UpdatedAtUTC    string
}

func atlasWorkgraphIdentityDurableStateOf(record Record) atlasWorkgraphIdentityDurableState {
	return atlasWorkgraphIdentityDurableState{
		ArtifactRefs:    record.ArtifactRefs,
		Evidence:        record.Evidence,
		CurrentRoute:    record.CurrentRoute,
		CurrentPhase:    record.CurrentPhase,
		ExactNextAction: record.ExactNextAction,
		RouteHistory:    record.RouteHistory,
		Steps:           record.Steps,
		Checkpoints:     record.Checkpoints,
		ReturnGate:      record.ReturnGate,
		Reconciliation:  record.Reconciliation,
		UpdatedAtUTC:    record.UpdatedAtUTC,
	}
}

func assertNoAtlasWorkgraphImportSideEffects(t *testing.T, store Store, missionID string) {
	t.Helper()
	for _, path := range []string{
		store.transactionJournalPath(missionID),
		store.eventLoopPath(missionID),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected import left durable side-effect file %q: %v", path, err)
		}
	}
	err := filepath.WalkDir(store.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.Contains(entry.Name(), ".tmp-") {
			return fmt.Errorf("temporary file remains: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
