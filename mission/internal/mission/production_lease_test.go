package mission

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCLIContinueExplicitZeroMinimumMinutesPersistsZero(t *testing.T) {
	dir := t.TempDir()
	record := startMissionThroughCLI(t, dir)

	continued := continueMissionThroughCLI(t, dir, record.MissionID,
		"--min-nodes", "8",
		"--min-minutes", "0",
		"--max-minutes", "180",
	)
	if continued.GoalLease == nil || continued.GoalLease.MinMinutes != 0 {
		t.Fatalf("explicit zero minimum was not preserved: %+v", continued.GoalLease)
	}
}

func TestCLIContinueExplicitZeroMinimumMinutesUpdatesExistingLease(t *testing.T) {
	dir := t.TempDir()
	record := startMissionThroughCLI(t, dir)

	continued := continueMissionThroughCLI(t, dir, record.MissionID,
		"--min-nodes", "8",
		"--min-minutes", "120",
		"--max-minutes", "180",
	)
	if continued.GoalLease == nil || continued.GoalLease.MinMinutes != 120 {
		t.Fatalf("historical minimum was not established: %+v", continued.GoalLease)
	}
	continued = continueMissionThroughCLI(t, dir, record.MissionID,
		"--min-nodes", "8",
		"--max-minutes", "180",
	)
	if continued.GoalLease == nil || continued.GoalLease.MinMinutes != 120 {
		t.Fatalf("omitted minimum should preserve historical value: %+v", continued.GoalLease)
	}

	continued = continueMissionThroughCLI(t, dir, record.MissionID,
		"--min-nodes", "8",
		"--min-minutes", "0",
		"--max-minutes", "180",
	)
	if continued.GoalLease == nil || continued.GoalLease.MinMinutes != 0 {
		t.Fatalf("explicit zero did not replace the existing minimum: %+v", continued.GoalLease)
	}
}

func TestCLIContinueRejectsNegativeMinimumMinutes(t *testing.T) {
	dir := t.TempDir()
	record := startMissionThroughCLI(t, dir)
	var out, errOut bytes.Buffer
	code := Run([]string{
		"--home", dir,
		"continue",
		"--mission", record.MissionID,
		"--min-minutes", "-1",
	}, &out, &errOut)
	if code == 0 {
		t.Fatalf("negative minimum unexpectedly succeeded: %s", out.String())
	}
	if !strings.Contains(errOut.String(), "min-minutes must be zero or greater") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestContinueLowersExistingMinimumToExactImportedWorkgraphTotal(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("close an exact nine-node workgraph")
	if err != nil {
		t.Fatal(err)
	}
	record.GoalLease = &GoalLease{
		Schema:           GoalLeaseSchema,
		MinNodes:         10,
		MinMinutes:       0,
		MaxMinutes:       180,
		MaxIterations:    10,
		ReturnOnlyWhen:   defaultReturnOnlyWhen,
		CheckpointPolicy: defaultCheckpointPolicy,
	}
	record.Evidence.AtlasWorkgraph = &NodeCounts{Total: 9, Completed: 9}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}

	continued, err := Continue(store, record.MissionID, ContinueOptions{MinNodes: 9})
	if err != nil {
		t.Fatal(err)
	}
	if continued.GoalLease == nil || continued.GoalLease.MinNodes != 9 {
		t.Fatalf("exact imported workgraph total did not lower lease: %+v", continued.GoalLease)
	}
}

func TestContinueRejectsLowerMinimumWithoutExactImportedWorkgraphTotal(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("reject unbound lease reduction")
	if err != nil {
		t.Fatal(err)
	}
	record.GoalLease = &GoalLease{
		Schema:           GoalLeaseSchema,
		MinNodes:         10,
		MinMinutes:       0,
		MaxMinutes:       180,
		MaxIterations:    10,
		ReturnOnlyWhen:   defaultReturnOnlyWhen,
		CheckpointPolicy: defaultCheckpointPolicy,
	}
	record.Evidence.AtlasWorkgraph = &NodeCounts{Total: 9, Completed: 9}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}

	if _, err := Continue(store, record.MissionID, ContinueOptions{MinNodes: 8}); err == nil || !strings.Contains(err.Error(), "must equal imported Atlas workgraph total") {
		t.Fatalf("unsafe lease reduction unexpectedly succeeded: %v", err)
	}
}

func TestContinueRejectsNegativeMinimumMinutes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("reject negative production lease")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(store, record.MissionID, ContinueOptions{MinMinutes: -1}); err == nil || !strings.Contains(err.Error(), "min-minutes must be zero or greater") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMissionDoctorAcceptsZeroMinimumMinutes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("doctor accepts useful-work lease")
	if err != nil {
		t.Fatal(err)
	}
	record.GoalLease = &GoalLease{
		Schema:           GoalLeaseSchema,
		MinNodes:         8,
		MinMinutes:       0,
		MaxMinutes:       180,
		MaxIterations:    8,
		ReturnOnlyWhen:   defaultReturnOnlyWhen,
		CheckpointPolicy: defaultCheckpointPolicy,
	}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}

	readback := BuildMissionDoctorReadback(store)
	if readback.Status == "blocked" || readback.LeaseHealthStatus == "invalid" {
		t.Fatalf("zero-minimum lease should be healthy: %+v", readback)
	}
}

func startMissionThroughCLI(t *testing.T, dir string) Record {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "production lease regression"}, &out, &errOut); code != 0 {
		t.Fatalf("start failed: %s", errOut.String())
	}
	var record Record
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func continueMissionThroughCLI(t *testing.T, dir, missionID string, flags ...string) Record {
	t.Helper()
	args := []string{"--home", dir, "continue", "--mission", missionID, "--max-iterations", "1"}
	args = append(args, flags...)
	var out, errOut bytes.Buffer
	if code := Run(args, &out, &errOut); code != 0 {
		t.Fatalf("continue failed: %s", errOut.String())
	}
	var record Record
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	return record
}
