package mission

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildMissionLifecycleProjectionBindsMetricsAndEventIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("mission lifecycle projection")
	if err != nil {
		t.Fatal(err)
	}
	record.Steps = []ContinuationStep{{Result: "handoff_required", Route: "ao-atlas"}}
	record.Evidence.AtlasRecommendation = &AtlasRecommendationReadbackCounts{
		TotalNodes:      8,
		CompletedNodes:  3,
		ReadyNodes:      5,
		ExactNextAction: "emit the next bounded import",
	}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}

	projection, err := BuildMissionLifecycleProjection(store, record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMissionLifecycleProjection(projection); err != nil {
		t.Fatal(err)
	}
	if projection.MissionID != record.MissionID || projection.MissionStatus != "active" {
		t.Fatalf("projection lost mission identity: %+v", projection)
	}
	if projection.Metrics.HandoffSteps != 1 || projection.Metrics.CompletedNodes != 3 || projection.Metrics.ReadyNodes != 5 {
		t.Fatalf("projection lost lifecycle metrics: %+v", projection.Metrics)
	}
	if projection.EventCount == 0 || !strings.HasPrefix(projection.EventIndexDigest, "sha256:") || !strings.HasPrefix(projection.SourceRecordDigest, "sha256:") {
		t.Fatalf("projection is not digest bound: %+v", projection)
	}
	if projection.SafeToExecute || projection.ExecutesWork || projection.ApprovesWork || projection.MutatesRepositories || !projection.RSIRemainsDenied {
		t.Fatalf("projection widened authority: %+v", projection)
	}
}

func TestMissionLifecycleProjectionCLIEmitsReadback(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("mission lifecycle projection CLI")
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--home", dir, "mission", "projection", "--mission", record.MissionID, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("mission projection failed: code=%d stderr=%s", code, stderr.String())
	}
	var projection MissionLifecycleProjection
	if err := json.Unmarshal(stdout.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Schema != MissionLifecycleProjectionSchema || projection.MissionID != record.MissionID || projection.EventCount == 0 {
		t.Fatalf("unexpected projection CLI output: %+v", projection)
	}
}

func TestLifecycleProjectionContractFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "valid", "lifecycle-projection-readback.json")
	result, err := ValidateContractFile(path)
	if err != nil || result.Status != "ready" || result.Contract != MissionLifecycleProjectionSchema {
		t.Fatalf("lifecycle projection fixture should validate: result=%+v err=%v", result, err)
	}
}
