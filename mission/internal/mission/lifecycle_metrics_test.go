package mission

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestBuildMissionLifecycleMetricsSeparatesHandoffsFromCompletedNodes(t *testing.T) {
	record := Record{
		MissionID: "mission-metrics-test",
		Steps: []ContinuationStep{
			{Result: "handoff_required"},
			{Result: "handoff_required"},
			{Result: "handoff_required"},
		},
		Evidence: EvidenceSummary{
			AtlasRecommendation: &AtlasRecommendationReadbackCounts{
				TotalNodes:     10,
				CompletedNodes: 4,
				ReadyNodes:     6,
			},
		},
	}

	metrics := BuildMissionLifecycleMetrics(record)
	if err := ValidateMissionLifecycleMetrics(metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.HandoffSteps != 3 || metrics.CompletedNodes != 4 || metrics.EvidenceCompletedNodes != 4 || metrics.ReadyNodes != 6 {
		t.Fatalf("metrics conflated handoffs and evidence completion: %#v", metrics)
	}
	if metrics.HandoffStepsCountAsCompletedNodes {
		t.Fatal("handoff steps must not count as completed nodes")
	}
}

func TestMissionMetricsCLIEmitsAuditableReadback(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("mission metrics CLI")
	if err != nil {
		t.Fatal(err)
	}
	record.Steps = []ContinuationStep{{Result: "handoff_required"}}
	record.Evidence.AtlasRecommendation = &AtlasRecommendationReadbackCounts{TotalNodes: 2, CompletedNodes: 1, ReadyNodes: 1}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run([]string{"--home", dir, "mission", "metrics", "--mission", record.MissionID, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("mission metrics failed: code=%d stderr=%s", code, stderr.String())
	}
	var metrics MissionLifecycleMetrics
	if err := json.Unmarshal(stdout.Bytes(), &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.HandoffSteps != 1 || metrics.CompletedNodes != 1 || metrics.FinalResponseAllowed {
		t.Fatalf("unexpected CLI metrics: %#v", metrics)
	}
}

func TestMissionMetricsCLIClearsTerminalNextAction(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("terminal mission metrics CLI")
	if err != nil {
		t.Fatal(err)
	}
	record.Status = "done"
	record.CurrentRoute = "complete"
	record.CurrentPhase = "complete"
	record.ExactNextAction = "mission complete; read final rollup and recommended next tasks"
	record.Evidence.AtlasRecommendation = &AtlasRecommendationReadbackCounts{
		Status:               "completed",
		TotalNodes:           10,
		CompletedNodes:       10,
		ReadyNodes:           0,
		FinalResponseAllowed: true,
		ReturnGateStatus:     "final_response_allowed",
	}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run([]string{"--home", dir, "mission", "metrics", "--mission", record.MissionID, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("terminal mission metrics failed: code=%d stderr=%s", code, stderr.String())
	}
	var metrics MissionLifecycleMetrics
	if err := json.Unmarshal(stdout.Bytes(), &metrics); err != nil {
		t.Fatal(err)
	}
	if !metrics.FinalResponseAllowed || metrics.ReturnGateStatus != "return_allowed" || metrics.ExactNextAction != "" {
		t.Fatalf("terminal metrics retained an actionable continuation: %#v", metrics)
	}
}

func TestLifecycleMetricsContractFixtureValidates(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "valid", "lifecycle-metrics-readback.json")
	result, err := ValidateContractFile(path)
	if err != nil || result.Status != "ready" || result.Contract != MissionLifecycleMetricsSchema {
		t.Fatalf("lifecycle metrics fixture should validate: result=%+v err=%v", result, err)
	}
}
