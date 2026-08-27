package mission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMissionLifecycleAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	r, err := s.Start("build a long-running atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	if r.CurrentRoute != "ao-atlas" {
		t.Fatalf("route=%s", r.CurrentRoute)
	}
	r, err = Continue(s, r.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Steps) != 10 {
		t.Fatalf("steps=%d", len(r.Steps))
	}
	if r.GoalLease == nil || r.GoalLease.MinNodes != 10 || r.GoalLease.ReturnOnlyWhen == "" {
		t.Fatalf("missing long-run lease: %+v", r.GoalLease)
	}
	if len(r.Checkpoints) != len(r.Steps) {
		t.Fatalf("checkpoints=%d steps=%d", len(r.Checkpoints), len(r.Steps))
	}
	snap := Snapshot(r)
	if snap.SafeToExecute {
		t.Fatal("snapshot must not be safe to execute")
	}
	if snap.ExecutesWork || snap.ApprovesWork || snap.ProviderCalls {
		t.Fatal("authority boundary widened")
	}
}

func TestContinuePersistsEventLoopDecision(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build a long-running atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 3}); err != nil {
		t.Fatal(err)
	}
	decision, err := s.LoadEventLoopDecision(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Schema != EventLoopDecisionSchema || decision.MissionID != rec.MissionID {
		t.Fatalf("bad event loop decision: %+v", decision)
	}
	if decision.Status != "handoff_required" || decision.Route != "ao-atlas" || decision.Iteration != 3 {
		t.Fatalf("unexpected event loop decision: %+v", decision)
	}
	if decision.ExecutesWork || decision.ApprovesWork || decision.MutatesRepositories {
		t.Fatalf("event loop widened authority: %+v", decision)
	}
}

func TestContinueUntilDoneDoesNotStopAfterOneHandoff(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("supervise long-running atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Steps) != 4 {
		t.Fatalf("until-done stopped early after %d steps", len(continued.Steps))
	}
	if continued.ReturnGate == nil || continued.ReturnGate.FinalResponseAllowed {
		t.Fatalf("premature final return should be denied: %+v", continued.ReturnGate)
	}
}

func TestCLIContinueUntilDonePersistsMultipleHandoffs(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "supervise long-running atlas workgraph mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{
		"--home", dir,
		"continue",
		"--mission", rec.MissionID,
		"--until-done",
		"--max-iterations", "5",
		"--min-nodes", "15",
		"--min-minutes", "120",
		"--max-minutes", "180",
		"--return-only-when", "mission_done_or_true_hard_blocker_or_no_ready_work_and_no_exact_next_action",
		"--checkpoint-policy", "after_each_node_or_timed_interval",
	}, &out, &errb); code != 0 {
		t.Fatalf("continue: %s", errb.String())
	}
	var continued Record
	if err := json.Unmarshal(out.Bytes(), &continued); err != nil {
		t.Fatal(err)
	}
	if len(continued.Steps) != 5 || len(continued.Checkpoints) != 5 {
		t.Fatalf("until-done CLI stopped early or skipped checkpoints: steps=%d checkpoints=%d", len(continued.Steps), len(continued.Checkpoints))
	}
	if continued.GoalLease == nil ||
		continued.GoalLease.MinNodes != 15 ||
		continued.GoalLease.MinMinutes != 120 ||
		continued.GoalLease.MaxMinutes != 180 ||
		continued.GoalLease.MaxIterations != 5 ||
		continued.GoalLease.CheckpointPolicy != "after_each_node_or_timed_interval" {
		t.Fatalf("CLI lease did not preserve long-run contract: %+v", continued.GoalLease)
	}
	if continued.ReturnGate == nil || continued.ReturnGate.FinalResponseAllowed {
		t.Fatalf("CLI should deny final response while lease and ready work remain: %+v", continued.ReturnGate)
	}
	store := NewStore(dir)
	decision, err := store.LoadEventLoopDecision(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Iteration != 5 || decision.Status != "handoff_required" || decision.Route != "ao-atlas" {
		t.Fatalf("event loop decision should record fifth handoff, got %+v", decision)
	}
	bundle, err := store.LoadCheckpointBundle(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.CheckpointCount != 5 || bundle.ReturnGate == nil || bundle.ReturnGate.FinalResponseAllowed {
		t.Fatalf("checkpoint bundle should bind five handoffs and early-return denial: %+v", bundle)
	}
}

func TestReturnGateDoesNotCountHandoffStepsAsCompletedNodes(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("handoff accounting regression")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 5, MinNodes: 15})
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Steps) != 5 {
		t.Fatalf("handoff steps=%d, want 5", len(continued.Steps))
	}
	if continued.ReturnGate == nil || continued.ReturnGate.CompletedNodes != 0 {
		t.Fatalf("handoffs must not count as completed nodes: %+v", continued.ReturnGate)
	}
}

func TestContinueWritesCheckpointBundleAndDoctorSupervisorHealth(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("supervise checkpointed atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 3})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := s.LoadCheckpointBundle(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Schema != CheckpointBundleSchema || bundle.CheckpointCount != 3 || bundle.ExecutesWork || bundle.ApprovesWork || bundle.MutatesRepositories {
		t.Fatalf("bad checkpoint bundle: %+v", bundle)
	}
	if bundle.ReturnGate == nil || bundle.ReturnGate.FinalResponseAllowed {
		t.Fatalf("checkpoint bundle should carry early-return denial: %+v", bundle.ReturnGate)
	}
	doctor := BuildMissionDoctorReadback(s)
	if doctor.Schema != "ao.mission.doctor-readback.v0.1" || doctor.LeaseCount != 1 || doctor.FreshCheckpoints != 1 || doctor.EarlyReturnRisks != 1 {
		t.Fatalf("doctor missing supervisor health: %+v continued=%+v", doctor, continued)
	}
	for _, want := range []string{"lease_health_checked", "checkpoint_freshness_checked", "stale_route_reconciliation_checked", "early_return_risk_checked"} {
		if !stringSliceContains(doctor.Checks, want) {
			t.Fatalf("doctor missing check %q: %+v", want, doctor)
		}
	}
}

func TestResumeReevaluatesReturnGateAfterDoneState(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("resume a long-running Atlas mission")
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = "done"
	rec.CurrentRoute = "complete"
	rec.CurrentPhase = "complete"
	rec.ExactNextAction = "mission complete; read final rollup and recommended next tasks"
	rec.ReturnGate = &ReturnGate{Schema: ReturnGateSchema, MissionID: rec.MissionID, Status: "return_allowed", FinalResponseAllowed: true}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(s, rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "active" || resumed.ReturnGate == nil || resumed.ReturnGate.FinalResponseAllowed {
		t.Fatalf("resumed mission retained a terminal return gate: %+v", resumed)
	}
	if resumed.ReturnGate.Status != "early_return_denied" || !strings.HasPrefix(resumed.ReturnGate.ExactNextAction, "continue mission:") {
		t.Fatalf("resumed mission did not record a continuation denial: %+v", resumed.ReturnGate)
	}
}

func TestDoctorReadbackReportsDetailedLongRunRisks(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("doctor detailed long-run risk mission")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	continued.CurrentRoute = "ao-mission"
	continued.Reconciliation = &RouteReconciliation{
		Schema:          RouteReconciliationSchema,
		MissionID:       rec.MissionID,
		Status:          "stale_route_detected",
		CurrentRoute:    "ao-mission",
		LatestRoute:     "ao-atlas",
		ExactNextAction: "refresh route decision before final response",
	}
	if err := s.Save(continued); err != nil {
		t.Fatal(err)
	}

	doctor := BuildMissionDoctorReadback(s)
	if doctor.LeaseHealthStatus != "healthy" ||
		doctor.CheckpointFreshnessStatus != "fresh" ||
		doctor.StaleRouteDecisionStatus != "stale_route_detected" ||
		doctor.EarlyReturnRiskStatus != "risk_detected" ||
		doctor.ExactNextAction != "refresh route decision before final response" {
		t.Fatalf("doctor missing detailed long-run statuses: %+v", doctor)
	}
	if len(doctor.RiskMissions) < 2 {
		t.Fatalf("doctor should include stale route and early-return risk records: %+v", doctor)
	}
	if !doctorHasRiskKind(doctor, "stale_route") || !doctorHasRiskKind(doctor, "early_return") {
		t.Fatalf("doctor risk records missing expected kinds: %+v", doctor.RiskMissions)
	}
	if doctor.SafeToExecute || doctor.ExecutesWork || doctor.ApprovesWork || doctor.MutatesRepositories {
		t.Fatalf("doctor widened authority: %+v", doctor)
	}
}

func doctorHasRiskKind(readback MissionDoctorReadback, kind string) bool {
	for _, risk := range readback.RiskMissions {
		if risk.Kind == kind {
			return true
		}
	}
	return false
}

func TestSchedulerFailsClosedWhenCronMissing(t *testing.T) {
	old := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", old) })
	os.Setenv("PATH", t.TempDir())
	rb := ScheduleReadback("mission-x", "1m", true)
	if rb.Status != "blocked" {
		t.Fatalf("status=%s", rb.Status)
	}
	if !strings.Contains(rb.Reason, "missing") {
		t.Fatalf("reason=%s", rb.Reason)
	}
}

func TestSchedulerReadbackImportRecordsWakeupOnlyEvidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("schedule long-running workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scheduler-readback.json")
	body := `{"schema":"ao.mission.scheduler-readback.v0.1","mission_id":"` + rec.MissionID + `","status":"ready","scheduler":"codex-cron","event_loop":true,"reason":"fixture wakeup only","generated_at_utc":"2026-07-03T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "scheduler-readback", path); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Evidence.SchedulerReadback == nil || updated.Evidence.SchedulerReadback.Scheduler != "codex-cron" {
		t.Fatalf("scheduler evidence missing: %+v", updated.Evidence.SchedulerReadback)
	}
	if updated.Evidence.SchedulerReadback.ExecutesWork || updated.CurrentPhase != "scheduler_readback_recorded" {
		t.Fatalf("scheduler import widened authority or wrong phase: %+v", updated)
	}
}

func TestSchedulerReadbackImportRejectsExecutionAuthority(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("schedule long-running workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scheduler-readback.json")
	body := `{"schema":"ao.mission.scheduler-readback.v0.1","mission_id":"` + rec.MissionID + `","status":"ready","scheduler":"codex-cron","event_loop":true,"executes_work":true,"reason":"unsafe fixture","generated_at_utc":"2026-07-03T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "scheduler-readback", path); err == nil || !strings.Contains(err.Error(), "executes_work") {
		t.Fatalf("expected scheduler authority rejection, got %v", err)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Evidence.SchedulerReadback != nil {
		t.Fatalf("unsafe scheduler readback was recorded: %+v", updated.Evidence.SchedulerReadback)
	}
}

func TestRouterSendsConcreteBatchSupervisionToAtlasNotBlueprint(t *testing.T) {
	decision := DecideRoute("mission-route", "supervise twenty bounded implementation and evidence nodes", nil)
	if decision.Route != "ao-atlas" {
		t.Fatalf("concrete long-run batch should route to Atlas, got %+v", decision)
	}
	underspecified := DecideRoute("mission-route", "figure out", nil)
	if underspecified.Route != "ao-blueprint" {
		t.Fatalf("underspecified objective should still route to Blueprint, got %+v", underspecified)
	}
}

func TestArchitectureRouteContextCompatibilityVector(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "valid", "architecture-route-context-compatibility-vector.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		SchemaVersion string `json:"schema_version"`
		Edge          string `json:"edge"`
		Producer      struct {
			Repository string `json:"repository"`
		} `json:"producer"`
		Consumer struct {
			Repository     string `json:"repository"`
			ExpectedSchema string `json:"expected_schema"`
		} `json:"consumer"`
		SourceOfTruth struct {
			Status                         string `json:"status"`
			AO2Version                     string `json:"ao2_version"`
			ControlPlaneVersion            string `json:"control_plane_version"`
			CompatibilityMatrixStatus      string `json:"compatibility_matrix_status"`
			FullStackCompatibilityComplete bool   `json:"full_stack_compatibility_complete"`
			ExternalBetaLaunched           bool   `json:"external_beta_launched"`
			PromotionGranted               bool   `json:"promotion_granted"`
			ProviderPilot                  bool   `json:"provider_pilot"`
			RSIRemainsDenied               bool   `json:"rsi_remains_denied"`
		} `json:"source_of_truth"`
		RouteContext struct {
			MissionID              string `json:"mission_id"`
			Objective              string `json:"objective"`
			ExpectedRoute          string `json:"expected_route"`
			ExpectedReasonContains string `json:"expected_reason_contains"`
			ExactNextAction        string `json:"exact_next_action"`
			SafeToRequest          bool   `json:"safe_to_request"`
			SafeToExecute          bool   `json:"safe_to_execute"`
			SafeToPromote          bool   `json:"safe_to_promote"`
		} `json:"route_context"`
		Boundaries struct {
			ReleaseOrPublish      bool `json:"release_or_publish"`
			CreatesTag            bool `json:"creates_tag"`
			UploadsAssets         bool `json:"uploads_assets"`
			Deploys               bool `json:"deploys"`
			ContactsExternalUsers bool `json:"contacts_external_users"`
			ProviderPilot         bool `json:"provider_pilot"`
			PromotionGranted      bool `json:"promotion_granted"`
			RSIRemainsDenied      bool `json:"rsi_remains_denied"`
			ExecutesWork          bool `json:"executes_work"`
			ApprovesWork          bool `json:"approves_work"`
			MutatesRepositories   bool `json:"mutates_repositories"`
		} `json:"boundaries"`
	}
	if err := json.Unmarshal(body, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.SchemaVersion != "ao.compatibility.architecture-route-context-vector.v1" ||
		vector.Edge != "ao-architecture.source_of_truth -> ao-mission.route_context" ||
		vector.Producer.Repository != "ao-architecture" ||
		vector.Consumer.Repository != "ao-mission" ||
		vector.Consumer.ExpectedSchema != RouteSchema {
		t.Fatalf("bad Architecture route/context vector identity: %+v", vector)
	}
	if vector.SourceOfTruth.Status != "current_public_release_pair" ||
		vector.SourceOfTruth.AO2Version != "v0.5.1" ||
		vector.SourceOfTruth.ControlPlaneVersion != "v0.1.16" ||
		vector.SourceOfTruth.CompatibilityMatrixStatus != "proposed" ||
		vector.SourceOfTruth.FullStackCompatibilityComplete ||
		vector.SourceOfTruth.ExternalBetaLaunched ||
		vector.SourceOfTruth.PromotionGranted ||
		vector.SourceOfTruth.ProviderPilot ||
		!vector.SourceOfTruth.RSIRemainsDenied {
		t.Fatalf("bad Architecture source-of-truth fields: %+v", vector.SourceOfTruth)
	}
	if vector.Boundaries.ReleaseOrPublish ||
		vector.Boundaries.CreatesTag ||
		vector.Boundaries.UploadsAssets ||
		vector.Boundaries.Deploys ||
		vector.Boundaries.ContactsExternalUsers ||
		vector.Boundaries.ProviderPilot ||
		vector.Boundaries.PromotionGranted ||
		vector.Boundaries.ExecutesWork ||
		vector.Boundaries.ApprovesWork ||
		vector.Boundaries.MutatesRepositories ||
		!vector.Boundaries.RSIRemainsDenied {
		t.Fatalf("Architecture route/context vector widened authority: %+v", vector.Boundaries)
	}
	decision := DecideRoute(vector.RouteContext.MissionID, vector.RouteContext.Objective, nil)
	if decision.Schema != RouteSchema ||
		decision.Route != vector.RouteContext.ExpectedRoute ||
		!strings.Contains(decision.Reason, vector.RouteContext.ExpectedReasonContains) ||
		decision.ExactNextAction != vector.RouteContext.ExactNextAction ||
		decision.SafeToRequest != vector.RouteContext.SafeToRequest ||
		decision.SafeToExecute != vector.RouteContext.SafeToExecute ||
		decision.SafeToPromote != vector.RouteContext.SafeToPromote {
		t.Fatalf("Mission route/context did not consume Architecture vector: decision=%+v vector=%+v", decision, vector.RouteContext)
	}
}

func TestTelegramIntentOnly(t *testing.T) {
	rb := HandleTelegramCommand(TelegramCommand{ChatID: "1001", Command: "/continue", Role: "admin"}, map[string]string{"1001": "admin"})
	if rb.Status != "intent_recorded" || rb.MutationAuthority {
		t.Fatalf("bad readback: %+v", rb)
	}
	denied := HandleTelegramCommand(TelegramCommand{ChatID: "9", Command: "/continue", Role: "admin"}, map[string]string{})
	if denied.Status != "denied" {
		t.Fatalf("want denied")
	}
}
func TestA2AAgentCardIntentOnly(t *testing.T) {
	card := AgentCard()
	if card.MutationAuthority {
		t.Fatal("a2a must not grant mutation authority")
	}
	task := A2ATaskFor("mission.start")
	if task.MutationAuthority || task.Status != "intent_recorded" {
		t.Fatalf("bad task %+v", task)
	}
}
func TestCLIStartStatusNext(t *testing.T) {
	dir := t.TempDir()
	old := os.Getenv("AO_MISSION_HOME")
	t.Cleanup(func() { os.Setenv("AO_MISSION_HOME", old) })
	os.Setenv("AO_MISSION_HOME", dir)
	var out, errb bytes.Buffer
	if code := Run([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("init: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"start", "small objective"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"next", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("next: %s", errb.String())
	}
	var d RouteDecision
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.SafeToExecute {
		t.Fatal("next must not be safe to execute")
	}
	if _, err := os.Stat(filepath.Join(dir, "missions", rec.MissionID+".json")); err != nil {
		t.Fatal(err)
	}
}
func TestPublicSafeTextRejectsSecrets(t *testing.T) {
	if ValidatePublicSafeText("tok"+"en: abc") == nil {
		t.Fatal("expected token rejection")
	}
	if err := ValidatePublicSafeText("safe fixture with redacted token example"); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalHomeAndFinalRollup(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "atlas workgraph objective"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "final", "rollup", "--mission", rec.MissionID}, &out, &errb); code != 0 {
		t.Fatalf("rollup: %s", errb.String())
	}
	var rollup FinalRollup
	if err := json.Unmarshal(out.Bytes(), &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup.SafeToExecute || rollup.ExecutesWork {
		t.Fatal("final rollup widened authority")
	}
}

func TestValidateContractAndImports(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build atlas workgraph")
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"schema":"ao.blueprint.build-authorization.v0.1","project_id":"demo","status":"ready","next_allowed_action":"ao-atlas"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateContractFile(filepath.Join("..", "..", "examples", "valid", "mission-record.json")); err != nil {
		t.Fatal(err)
	}
	rb, err := ImportArtifact(s, rec.MissionID, "blueprint-authorization", authPath)
	if err != nil {
		t.Fatal(err)
	}
	if rb.SafeToExecute || rb.Kind != "blueprint-authorization" {
		t.Fatalf("bad readback: %+v", rb)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.ArtifactRefs) != 1 {
		t.Fatalf("artifact refs=%d", len(updated.ArtifactRefs))
	}
}

func TestContractValidationRejectsSchemaTypeMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-record.json")
	body := `{"schema":"ao.mission.record.v0.1","mission_id":42,"objective_digest":"sha256:abc","status":"active","created_at_utc":"2026-07-03T00:00:00Z","current_route":"ao-blueprint"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateContractFile(path)
	if err == nil {
		t.Fatal("expected schema type mismatch")
	}
	if result.Status != "blocked" || !strings.Contains(strings.Join(result.Blockers, ";"), "mission_id") {
		t.Fatalf("unexpected validation result: %+v", result)
	}
}

func TestSupervisorV03ContractsValidate(t *testing.T) {
	dir := t.TempDir()
	fixtures := map[string]any{
		"goal-lease.json": GoalLease{
			Schema:           GoalLeaseSchema,
			MinNodes:         10,
			MinMinutes:       120,
			MaxMinutes:       180,
			MaxIterations:    20,
			ReturnOnlyWhen:   defaultReturnOnlyWhen,
			CheckpointPolicy: defaultCheckpointPolicy,
		},
		"checkpoint.json": MissionCheckpoint{
			Schema:          MissionCheckpointSchema,
			MissionID:       "mission-contract",
			Sequence:        1,
			Iteration:       1,
			Route:           "ao-atlas",
			Phase:           "handoff_required",
			Result:          "handoff_required",
			ExactNextAction: "send objective to AO Atlas for workgraph sequencing",
			ResumeCommand:   "ao-mission continue --mission mission-contract --until-done --max-iterations 10",
			GeneratedAtUTC:  "2026-07-04T00:00:00Z",
		},
		"return-gate.json": ReturnGate{
			Schema:               ReturnGateSchema,
			MissionID:            "mission-contract",
			Status:               "early_return_denied",
			FinalResponseAllowed: false,
			Reason:               "lease minimum unmet",
			CompletedNodes:       1,
			MinNodes:             10,
			ReadyNodesRemaining:  1,
			HardBlocker:          false,
			ExactNextAction:      "continue mission",
			GeneratedAtUTC:       "2026-07-04T00:00:00Z",
		},
		"route-reconciliation.json": RouteReconciliation{
			Schema:                RouteReconciliationSchema,
			MissionID:             "mission-contract",
			Status:                "ready",
			CurrentRoute:          "ao-atlas",
			LatestRoute:           "ao-atlas",
			AtlasReadyNodes:       1,
			CommandReadbackBound:  false,
			PromoterReadbackBound: false,
			ExactNextAction:       "continue from latest exact next action",
			GeneratedAtUTC:        "2026-07-04T00:00:00Z",
		},
		"checkpoint-bundle.json": MissionCheckpointBundle{
			Schema:              CheckpointBundleSchema,
			MissionID:           "mission-contract",
			Status:              "ready",
			CheckpointCount:     1,
			ResumePrompt:        "ao-mission continue --mission mission-contract --until-done --max-iterations 10",
			SafeToExecute:       false,
			ExecutesWork:        false,
			ApprovesWork:        false,
			MutatesRepositories: false,
			GeneratedAtUTC:      "2026-07-04T00:00:00Z",
		},
	}
	for name, fixture := range fixtures {
		path := filepath.Join(dir, name)
		writeJSONForTest(t, path, fixture)
		if result, err := ValidateContractFile(path); err != nil {
			t.Fatalf("%s failed contract validation: %v %+v", name, err, result)
		}
	}
}

func TestMissionListInspectCommandStatusAndArtifactManifest(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "first objective"}, &out, &errb); code != 0 {
		t.Fatalf("start first: %s", errb.String())
	}
	var first Record
	if err := json.Unmarshal(out.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "start", "second objective"}, &out, &errb); code != 0 {
		t.Fatalf("start second: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "list", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission list: %s", errb.String())
	}
	var list []Record
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("mission list len=%d", len(list))
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "list", "--route", first.CurrentRoute, "--status", first.Status, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission list filters: %s", errb.String())
	}
	var filtered []Record
	if err := json.Unmarshal(out.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered mission list len=%d", len(filtered))
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "list", "--route", "complete", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission list empty filter: %s", errb.String())
	}
	filtered = nil
	if err := json.Unmarshal(out.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 0 {
		t.Fatalf("empty filtered mission list len=%d", len(filtered))
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "inspect", "--mission", first.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission inspect: %s", errb.String())
	}
	var inspected Record
	if err := json.Unmarshal(out.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected.MissionID != first.MissionID {
		t.Fatalf("inspect mission=%s", inspected.MissionID)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "command", "status", "--mission", first.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("command status: %s", errb.String())
	}
	var status CommandStatus
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.MissionID != first.MissionID || status.ExecutesWork || status.ApprovesWork || status.MutatesRepositories {
		t.Fatalf("bad command status: %+v", status)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "artifacts", "manifest", "--mission", first.MissionID}, &out, &errb); code != 0 {
		t.Fatalf("artifact manifest: %s", errb.String())
	}
	var manifest ArtifactManifest
	if err := json.Unmarshal(out.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestDigest == "" || manifest.Signature == "" || manifest.ExecutesWork || manifest.ApprovesWork {
		t.Fatalf("bad artifact manifest: %+v", manifest)
	}
}

func TestImportAtlasWorkgraphCountsAndFoundryFinalRollupCompletion(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build atlas workgraph")
	if err != nil {
		t.Fatal(err)
	}
	workgraphPath := filepath.Join(dir, "workgraph.json")
	workgraph := `{"schema":"ao.atlas.workgraph.v0.1","nodes":[{"node_id":"n1","status":"ready"},{"node_id":"n2","status":"blocked"},{"node_id":"n3","status":"completed"}]}`
	if err := os.WriteFile(workgraphPath, []byte(workgraph), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-workgraph", workgraphPath); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Evidence.AtlasWorkgraph == nil || updated.Evidence.AtlasWorkgraph.Total != 3 || updated.Evidence.AtlasWorkgraph.Ready != 1 {
		t.Fatalf("bad workgraph counts: %+v", updated.Evidence.AtlasWorkgraph)
	}
	rollupPath := filepath.Join(dir, "foundry-final-rollup.json")
	rollup := `{"schema":"ao.foundry.final-rollup.v0.1","status":"completed","completed_nodes":3,"total_nodes":3}`
	if err := os.WriteFile(rollupPath, []byte(rollup), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "foundry-final-rollup", rollupPath); err != nil {
		t.Fatal(err)
	}
	done, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.CurrentPhase != "complete" {
		t.Fatalf("mission not complete: %+v", done)
	}
	if done.Evidence.FoundryRollup == nil || done.Evidence.FoundryRollup.CompletedNodes != 3 {
		t.Fatalf("bad rollup evidence: %+v", done.Evidence.FoundryRollup)
	}
}

func TestImportAtlasRecommendationReadbackCompletesMission(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("import completed Atlas recommendation wave")
	if err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback.json")
	readback := `{
		"schema":"ao.atlas.recommendation-readback.v0.1",
		"status":"completed",
		"total_nodes":40,
		"completed_nodes":40,
		"ready_nodes":0,
		"checkpoint_count":40,
		"elapsed_minutes":491,
		"min_minutes_met":true,
		"lease_time_status":"minimum_minutes_met",
		"return_gate_status":"final_response_allowed",
		"final_response_allowed":true,
		"safe_to_execute":false,
		"executes_work":false,
		"approves_work":false,
		"mutates_repositories":false,
		"provider_calls":false,
		"release_or_publish":false,
		"credential_use":false,
		"direct_main_mutation":false,
		"concurrent_mutation":false,
		"exact_next_action":"Finalize AO Atlas long-run wave with Promoter, Command, and public-safety readbacks."
	}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	importReadback, err := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath)
	if err != nil {
		t.Fatal(err)
	}
	if importReadback.SafeToExecute || importReadback.ExecutesWork || importReadback.ApprovesWork {
		t.Fatalf("import widened authority: %+v", importReadback)
	}
	done, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.CurrentRoute != "complete" || done.CurrentPhase != "complete" {
		t.Fatalf("completed Atlas readback should close mission: %+v", done)
	}
	if done.Evidence.AtlasRecommendation == nil || done.Evidence.AtlasRecommendation.CompletedNodes != 40 {
		t.Fatalf("Atlas recommendation evidence missing: %+v", done.Evidence.AtlasRecommendation)
	}
	if done.ReturnGate == nil || !done.ReturnGate.FinalResponseAllowed || done.ReturnGate.ReadyNodesRemaining != 0 {
		t.Fatalf("terminal return gate not allowed: %+v", done.ReturnGate)
	}
}

func TestCLIImportsAtlasRecommendationReadback(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "import completed Atlas recommendation wave"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":491,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"final_response_allowed","final_response_allowed":true,"safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"exact_next_action":"Finalize AO Atlas long-run wave."}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "import", "atlas-recommendation-readback", "--mission", rec.MissionID, "--path", readbackPath}, &out, &errb); code != 0 {
		t.Fatalf("import: %s", errb.String())
	}
	var importReadback ImportReadback
	if err := json.Unmarshal(out.Bytes(), &importReadback); err != nil {
		t.Fatal(err)
	}
	if importReadback.Kind != "atlas-recommendation-readback" || importReadback.ExecutesWork || importReadback.ApprovesWork {
		t.Fatalf("bad import readback: %+v", importReadback)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "inspect", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("inspect: %s", errb.String())
	}
	var done Record
	if err := json.Unmarshal(out.Bytes(), &done); err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.Evidence.AtlasRecommendation == nil || done.Evidence.AtlasRecommendation.TotalNodes != 40 {
		t.Fatalf("CLI import did not complete mission: %+v", done)
	}
}

func TestImportAtlasRecommendationReadbackDeniesFinalWhenLeaseMinimumUnmet(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("import short Atlas recommendation wave")
	if err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback-short.json")
	readback := `{
		"schema":"ao.atlas.recommendation-readback.v0.1",
		"status":"completed",
		"total_nodes":40,
		"completed_nodes":40,
		"ready_nodes":0,
		"checkpoint_count":40,
		"elapsed_minutes":22,
		"min_minutes_met":false,
		"lease_time_status":"minimum_minutes_unmet",
		"return_gate_status":"blocked_minimum_minutes_unmet",
		"final_response_allowed":false,
		"safe_to_execute":false,
		"executes_work":false,
		"approves_work":false,
		"mutates_repositories":false,
		"provider_calls":false,
		"release_or_publish":false,
		"credential_use":false,
		"direct_main_mutation":false,
		"concurrent_mutation":false,
		"exact_next_action":"Continue AO Atlas wave until the minimum lease duration is met."
	}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath); err != nil {
		t.Fatal(err)
	}
	continued, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Status == "done" || continued.CurrentRoute != "ao-atlas" {
		t.Fatalf("short Atlas readback should keep Mission routed to Atlas: %+v", continued)
	}
	if continued.ReturnGate == nil || continued.ReturnGate.FinalResponseAllowed {
		t.Fatalf("short Atlas readback should deny final response: %+v", continued.ReturnGate)
	}
	if !strings.Contains(continued.ReturnGate.Reason, "blocked_minimum_minutes_unmet") {
		t.Fatalf("return gate should carry Atlas lease blocker, got %+v", continued.ReturnGate)
	}
	if !strings.Contains(continued.ReturnGate.ExactNextAction, "minimum lease") {
		t.Fatalf("return gate should preserve Atlas lease continuation action, got %+v", continued.ReturnGate)
	}
}

func TestImportAtlasRecommendationReadbackDoesNotCloseMismatchedMission(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("reject child wave readback for parent mission")
	if err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback-child.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","mission_id":"mission-child-wave","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":144,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"final_response_allowed","final_response_allowed":true,"safe_to_execute":false,"schedules_work":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"claims_authority_advance":false}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status == "done" || unchanged.CurrentRoute == "complete" || len(unchanged.ArtifactRefs) != 1 {
		t.Fatalf("mismatched readback incorrectly closed parent mission: %+v", unchanged)
	}
	if !strings.Contains(unchanged.ExactNextAction, "reconcile") {
		t.Fatalf("mismatched readback did not require parent reconciliation: %+v", unchanged)
	}
}

func TestImportAtlasRecommendationReadbackRejectsAuthorityAdvanceClaim(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("reject unsafe Atlas recommendation readback")
	if err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback-authority.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":491,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"final_response_allowed","final_response_allowed":true,"claims_authority_advance":true,"safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	err = func() error {
		_, importErr := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath)
		return importErr
	}()
	if err == nil || !strings.Contains(err.Error(), "claims_authority_advance") {
		t.Fatalf("expected authority-advance rejection, got %v", err)
	}
	unchanged, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Evidence.AtlasRecommendation != nil || unchanged.Status == "done" {
		t.Fatalf("unsafe Atlas recommendation readback was recorded: %+v", unchanged)
	}
}

func TestImportAtlasRecommendationReadbackDeniedAndBlockedBecomeExactBlockers(t *testing.T) {
	for _, tc := range []struct {
		status string
		reason string
	}{
		{status: "denied", reason: "missing stop-gate evidence for node-17"},
		{status: "blocked", reason: "Command readback disagreement"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			dir := t.TempDir()
			s := NewStore(dir)
			rec, err := s.Start("import terminal Atlas blocker")
			if err != nil {
				t.Fatal(err)
			}
			readbackPath := filepath.Join(dir, "recommendation-readback-"+tc.status+".json")
			readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"` + tc.status + `","total_nodes":40,"completed_nodes":39,"ready_nodes":0,"checkpoint_count":39,"elapsed_minutes":141,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"` + tc.status + `","final_response_allowed":true,"blocker":"` + tc.reason + `","safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"claims_authority_advance":false,"rsi_remains_denied":true,"exact_next_action":"Repair exact blocker through AO Atlas before final response."}`
			if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath); err != nil {
				t.Fatal(err)
			}
			blocked, err := s.Load(rec.MissionID)
			if err != nil {
				t.Fatal(err)
			}
			if blocked.Status != "blocked" || blocked.CurrentRoute != "ao-atlas" {
				t.Fatalf("terminal Atlas %s should block Mission: %+v", tc.status, blocked)
			}
			if !strings.Contains(strings.Join(blocked.Blockers, ";"), tc.reason) {
				t.Fatalf("missing exact blocker %q in %+v", tc.reason, blocked.Blockers)
			}
			if blocked.ReturnGate == nil || !blocked.ReturnGate.HardBlocker || !strings.Contains(blocked.ReturnGate.Reason, "terminal hard blocker") {
				t.Fatalf("terminal Atlas blocker should set hard-blocker gate: %+v", blocked.ReturnGate)
			}
		})
	}
}

func TestPromotedFoundryRollupClosesMission(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build atlas workgraph for promoted rollup")
	if err != nil {
		t.Fatal(err)
	}
	rollupPath := filepath.Join(dir, "foundry-final-rollup.json")
	rollup := `{"schema":"ao.foundry.final-rollup.v0.1","status":"promoted","completed_nodes":2,"total_nodes":2}`
	if err := os.WriteFile(rollupPath, []byte(rollup), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "foundry-final-rollup", rollupPath); err != nil {
		t.Fatal(err)
	}
	done, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.CurrentRoute != "complete" || done.Evidence.FoundryRollup.Status != "promoted" {
		t.Fatalf("promoted rollup should close mission: %+v", done)
	}
}

func TestCLIImportPromotedFoundryRollupClosesMission(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "build atlas workgraph for promoted rollup"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	workgraphPath := filepath.Join(dir, "atlas-workgraph.json")
	workgraph := `{"schema":"ao.atlas.workgraph.v0.1","nodes":[{"id":"node-1","status":"completed"},{"id":"node-2","status":"completed"}]}`
	if err := os.WriteFile(workgraphPath, []byte(workgraph), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", dir, "import", "atlas-workgraph", "--mission", rec.MissionID, "--path", workgraphPath}, &out, &errb); code != 0 {
		t.Fatalf("import atlas workgraph: %s", errb.String())
	}
	rollupPath := filepath.Join(dir, "foundry-final-rollup.json")
	rollup := `{"schema":"ao.foundry.final-rollup.v0.1","status":"promoted","completed_nodes":2,"total_nodes":2}`
	if err := os.WriteFile(rollupPath, []byte(rollup), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", dir, "import", "foundry-final-rollup", "--mission", rec.MissionID, "--path", rollupPath}, &out, &errb); code != 0 {
		t.Fatalf("import promoted rollup: %s", errb.String())
	}
	var imported ImportReadback
	if err := json.Unmarshal(out.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Kind != "foundry-final-rollup" || imported.ExactNextAction != "mission complete; read final rollup and recommended next tasks" || imported.ExecutesWork || imported.ApprovesWork {
		t.Fatalf("bad promoted rollup import readback: %+v", imported)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", dir, "mission", "inspect", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission inspect: %s", errb.String())
	}
	var done Record
	if err := json.Unmarshal(out.Bytes(), &done); err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.CurrentRoute != "complete" || done.CurrentPhase != "complete" {
		t.Fatalf("promoted rollup should close mission through CLI import: %+v", done)
	}
	if done.Evidence.FoundryRollup == nil || done.Evidence.FoundryRollup.Status != "promoted" || done.Evidence.FoundryRollup.CompletedNodes != 2 || done.Evidence.FoundryRollup.TotalNodes != 2 {
		t.Fatalf("promoted rollup evidence not normalized: %+v", done.Evidence.FoundryRollup)
	}
	if done.ReturnGate == nil || !done.ReturnGate.FinalResponseAllowed {
		t.Fatalf("promoted rollup closure should allow final response: %+v", done.ReturnGate)
	}
	bundle, err := NewStore(dir).LoadCheckpointBundle(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ReturnGate == nil || !bundle.ReturnGate.FinalResponseAllowed {
		t.Fatalf("checkpoint bundle should bind promoted closure: %+v", bundle.ReturnGate)
	}
}

func TestDeniedFoundryRollupBlocksMissionWithExactNextAction(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build atlas workgraph for denied rollup")
	if err != nil {
		t.Fatal(err)
	}
	rollupPath := filepath.Join(dir, "foundry-final-rollup.json")
	rollup := `{"schema":"ao.foundry.final-rollup.v0.1","status":"denied","completed_nodes":1,"total_nodes":2}`
	if err := os.WriteFile(rollupPath, []byte(rollup), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "foundry-final-rollup", rollupPath); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != "blocked" || !strings.Contains(blocked.ExactNextAction, "Foundry rollup denied") {
		t.Fatalf("denied rollup should block with exact next action: %+v", blocked)
	}
}

func TestFoundryTerminalStateBindingFixtureCoversClosureAndBlockers(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "foundry-terminal-state-binding.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema string `json:"schema"`
		States []struct {
			Status          string `json:"status"`
			CompletedNodes  int    `json:"completed_nodes"`
			TotalNodes      int    `json:"total_nodes"`
			ExpectedMission string `json:"expected_mission_status"`
			ExpectedRoute   string `json:"expected_route"`
			ExpectedPhase   string `json:"expected_phase"`
			ExactBlocker    string `json:"exact_blocker,omitempty"`
		} `json:"states"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != "ao.foundry.terminal-state-binding.v0.1" || len(fixture.States) != 4 {
		t.Fatalf("unexpected fixture coverage: %+v", fixture)
	}
	for _, state := range fixture.States {
		t.Run(state.Status, func(t *testing.T) {
			dir := t.TempDir()
			s := NewStore(dir)
			rec, err := s.Start("bind Foundry terminal state " + state.Status)
			if err != nil {
				t.Fatal(err)
			}
			rollupPath := filepath.Join(dir, "foundry-final-rollup.json")
			rollup := map[string]any{
				"schema":          "ao.foundry.final-rollup.v0.1",
				"status":          state.Status,
				"completed_nodes": state.CompletedNodes,
				"total_nodes":     state.TotalNodes,
				"safe_to_execute": false,
				"executes_work":   false,
				"approves_work":   false,
			}
			rollupBody, err := json.Marshal(rollup)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rollupPath, rollupBody, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportArtifact(s, rec.MissionID, "foundry-final-rollup", rollupPath); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load(rec.MissionID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != state.ExpectedMission || got.CurrentRoute != state.ExpectedRoute || got.CurrentPhase != state.ExpectedPhase {
				t.Fatalf("terminal state mismatch: got status=%s route=%s phase=%s want status=%s route=%s phase=%s", got.Status, got.CurrentRoute, got.CurrentPhase, state.ExpectedMission, state.ExpectedRoute, state.ExpectedPhase)
			}
			if state.ExactBlocker != "" && !strings.Contains(strings.Join(got.Blockers, ";"), state.ExactBlocker) {
				t.Fatalf("missing exact blocker %q in %+v", state.ExactBlocker, got.Blockers)
			}
		})
	}
}

func TestCommandCompactTimelineFixtureCoversAtlasAndReconciliationEvents(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "command-compact-timeline-readback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema              string         `json:"schema"`
		Status              string         `json:"status"`
		Compact             bool           `json:"compact"`
		IncludesKinds       []string       `json:"includes_event_kinds"`
		RecentEvents        []MissionEvent `json:"recent_events"`
		SafeToExecute       bool           `json:"safe_to_execute"`
		ExecutesWork        bool           `json:"executes_work"`
		ApprovesWork        bool           `json:"approves_work"`
		MutatesRepositories bool           `json:"mutates_repositories"`
		RSIRemainsDenied    bool           `json:"rsi_remains_denied"`
		ExactNextAction     string         `json:"exact_next_action"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != "ao.command.compact-timeline-readback.v0.1" || fixture.Status != "ready" || !fixture.Compact {
		t.Fatalf("unexpected command timeline fixture header: %+v", fixture)
	}
	for _, want := range []string{"atlas_recommendation", "final_reconciliation"} {
		foundKind := false
		for _, kind := range fixture.IncludesKinds {
			if kind == want {
				foundKind = true
			}
		}
		if !foundKind {
			t.Fatalf("fixture missing included kind %q: %+v", want, fixture.IncludesKinds)
		}
		foundEvent := false
		for _, event := range fixture.RecentEvents {
			if event.Kind == want {
				foundEvent = true
				if event.Status == "" || event.Summary == "" {
					t.Fatalf("event %q missing status or summary: %+v", want, event)
				}
			}
		}
		if !foundEvent {
			t.Fatalf("fixture missing recent event %q: %+v", want, fixture.RecentEvents)
		}
	}
	if fixture.SafeToExecute || fixture.ExecutesWork || fixture.ApprovesWork || fixture.MutatesRepositories {
		t.Fatalf("command timeline fixture widened authority: %+v", fixture)
	}
	if !fixture.RSIRemainsDenied || !strings.Contains(fixture.ExactNextAction, "continue node 19") {
		t.Fatalf("fixture should preserve RSI denial and exact next action: %+v", fixture)
	}
}

func TestDoctorCommandCompactRiskFixtureBindsEarlyReturnRisk(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "doctor-command-compact-early-return-risk.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema  string `json:"schema"`
		Status  string `json:"status"`
		Mission string `json:"mission"`
		Doctor  struct {
			Schema                string              `json:"schema"`
			Status                string              `json:"status"`
			EarlyReturnRisks      int                 `json:"early_return_risks"`
			EarlyReturnRiskStatus string              `json:"early_return_risk_status"`
			RiskMissions          []MissionDoctorRisk `json:"risk_missions"`
			ExactNextAction       string              `json:"exact_next_action"`
			SafeToExecute         bool                `json:"safe_to_execute"`
			ExecutesWork          bool                `json:"executes_work"`
			ApprovesWork          bool                `json:"approves_work"`
			MutatesRepositories   bool                `json:"mutates_repositories"`
		} `json:"doctor"`
		CommandCompact struct {
			Schema              string         `json:"schema"`
			Status              string         `json:"status"`
			Compact             bool           `json:"compact"`
			IncludesEventKinds  []string       `json:"includes_event_kinds"`
			RecentEvents        []MissionEvent `json:"recent_events"`
			SafeToExecute       bool           `json:"safe_to_execute"`
			ExecutesWork        bool           `json:"executes_work"`
			ApprovesWork        bool           `json:"approves_work"`
			MutatesRepositories bool           `json:"mutates_repositories"`
		} `json:"command_compact"`
		Binding struct {
			DoctorRiskKind           string `json:"doctor_risk_kind"`
			CommandEventKind         string `json:"command_event_kind"`
			ExactNextActionBound     bool   `json:"exact_next_action_bound"`
			FinalResponseAllowed     bool   `json:"final_response_allowed"`
			FinalResponseDenialBound bool   `json:"final_response_denial_bound"`
			CommandCompactRiskBound  bool   `json:"command_compact_risk_bound"`
		} `json:"binding"`
		SafeToExecute       bool `json:"safe_to_execute"`
		ExecutesWork        bool `json:"executes_work"`
		ApprovesWork        bool `json:"approves_work"`
		MutatesRepositories bool `json:"mutates_repositories"`
		RSIRemainsDenied    bool `json:"rsi_remains_denied"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != "ao.mission.doctor-command-compact-early-return-risk.v0.1" || fixture.Status != "risk_detected" || fixture.Mission != "ao-mission-doubled-wave-v01" {
		t.Fatalf("bad fixture header: %+v", fixture)
	}
	if fixture.Doctor.Schema != "ao.mission.doctor-readback.v0.1" || fixture.Doctor.EarlyReturnRisks < 1 || fixture.Doctor.EarlyReturnRiskStatus != "risk_detected" {
		t.Fatalf("doctor risk not bound: %+v", fixture.Doctor)
	}
	if len(fixture.Doctor.RiskMissions) == 0 || fixture.Doctor.RiskMissions[0].Kind != "early_return" || !strings.Contains(fixture.Doctor.RiskMissions[0].ExactNextAction, "Continue node-23") {
		t.Fatalf("doctor exact next action missing early-return risk: %+v", fixture.Doctor.RiskMissions)
	}
	if fixture.CommandCompact.Schema != "ao.command.compact-timeline-readback.v0.1" || !fixture.CommandCompact.Compact || !stringSliceContains(fixture.CommandCompact.IncludesEventKinds, "doctor_risk") {
		t.Fatalf("command compact risk event not bound: %+v", fixture.CommandCompact)
	}
	if len(fixture.CommandCompact.RecentEvents) == 0 || fixture.CommandCompact.RecentEvents[0].Kind != "doctor_risk" || !strings.Contains(fixture.CommandCompact.RecentEvents[0].Summary, "early_return") || !strings.Contains(fixture.CommandCompact.RecentEvents[0].Summary, "final_response_allowed=false") {
		t.Fatalf("command compact event missing early-return denial: %+v", fixture.CommandCompact.RecentEvents)
	}
	if fixture.Binding.DoctorRiskKind != "early_return" ||
		fixture.Binding.CommandEventKind != "doctor_risk" ||
		!fixture.Binding.ExactNextActionBound ||
		fixture.Binding.FinalResponseAllowed ||
		!fixture.Binding.FinalResponseDenialBound ||
		!fixture.Binding.CommandCompactRiskBound {
		t.Fatalf("bad doctor-command binding: %+v", fixture.Binding)
	}
	if fixture.SafeToExecute || fixture.ExecutesWork || fixture.ApprovesWork || fixture.MutatesRepositories || !fixture.RSIRemainsDenied {
		t.Fatalf("fixture widened authority or failed RSI denial: %+v", fixture)
	}
	if fixture.Doctor.SafeToExecute || fixture.Doctor.ExecutesWork || fixture.Doctor.ApprovesWork || fixture.Doctor.MutatesRepositories ||
		fixture.CommandCompact.SafeToExecute || fixture.CommandCompact.ExecutesWork || fixture.CommandCompact.ApprovesWork || fixture.CommandCompact.MutatesRepositories {
		t.Fatalf("nested readback widened authority: %+v", fixture)
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestFinalReconciliationEventSearchFixturePreservesReadbackShape(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "final-reconciliation-event-search-readback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var readback MissionEventSearchReadback
	if err := json.Unmarshal(body, &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.event-search-readback.v0.1" || readback.Status != "ready" || readback.Kind != "final_reconciliation" {
		t.Fatalf("unexpected event-search readback header: %+v", readback)
	}
	if readback.TotalMatches != 1 || len(readback.Events) != 1 {
		t.Fatalf("event-search fixture should contain exactly one final reconciliation event: %+v", readback)
	}
	event := readback.Events[0]
	if event.Kind != "final_reconciliation" || event.Status != "ready" || !strings.Contains(event.Summary, "artifacts_agree=true") || !strings.Contains(event.Summary, "rsi_remains_denied=true") {
		t.Fatalf("bad final reconciliation event fixture: %+v", event)
	}
	if readback.SafeToExecute || readback.ExecutesWork || readback.ApprovesWork || readback.MutatesRepositories {
		t.Fatalf("event-search fixture widened authority: %+v", readback)
	}
}

func TestEventIndexSearchesSupervisorEvidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("index supervisor checkpoint and rollup evidence")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 2}); err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	preTerminalResults := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: rec.MissionID, Query: "early_return_denied"})
	if preTerminalResults.TotalMatches == 0 {
		t.Fatalf("event index did not expose early-return risk before terminal rollup: %+v", index)
	}
	rollupPath := filepath.Join(dir, "foundry-final-rollup.json")
	rollup := `{"schema":"ao.foundry.final-rollup.v0.1","status":"blocked","completed_nodes":1,"total_nodes":2}`
	if err := os.WriteFile(rollupPath, []byte(rollup), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "foundry-final-rollup", rollupPath); err != nil {
		t.Fatal(err)
	}
	index, err = BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"checkpoint", "foundry rollup", "blocked"} {
		results := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: rec.MissionID, Query: query})
		if results.TotalMatches == 0 {
			t.Fatalf("event index did not find %q: %+v", query, index)
		}
		if results.ExecutesWork || results.ApprovesWork || results.MutatesRepositories {
			t.Fatalf("event search widened authority: %+v", results)
		}
	}
}

func TestEventIndexSearchesRouteNodePRCIRollupAndBlockerEvidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("event index evidence aliases")
	if err != nil {
		t.Fatal(err)
	}
	rec.ArtifactRefs = append(rec.ArtifactRefs,
		ArtifactRef{Schema: ArtifactRefSchema, Kind: "node-gate", Ref: "docs/evidence/node-10/node-gate.json", Digest: "sha256:node"},
		ArtifactRef{Schema: ArtifactRefSchema, Kind: "pull-request", Ref: "https://github.com/owner/repo/pull/54", Digest: "sha256:pr"},
		ArtifactRef{Schema: ArtifactRefSchema, Kind: "ci-check", Ref: "https://github.com/owner/repo/actions/runs/123", Digest: "sha256:ci"},
	)
	rec.Evidence.FoundryRollup = &FoundryRollupCounts{Status: "blocked", CompletedNodes: 8, TotalNodes: 10}
	rec.Blockers = []string{"missing CI run-link evidence"}
	rec.ReturnGate = &ReturnGate{
		Schema:               ReturnGateSchema,
		MissionID:            rec.MissionID,
		Status:               "early_return_denied",
		FinalResponseAllowed: false,
		Reason:               "ready nodes remain",
		ReadyNodesRemaining:  2,
		HardBlocker:          false,
		ExactNextAction:      "continue route, PR, CI, rollup, and blocker evidence indexing",
		GeneratedAtUTC:       "2026-07-04T00:00:00Z",
	}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"route_evidence", "node_evidence", "pr_evidence", "ci_evidence", "rollup_evidence", "blocker_evidence"} {
		results := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: rec.MissionID, Kind: kind})
		if results.TotalMatches == 0 {
			t.Fatalf("missing %s event in index: %+v", kind, index)
		}
	}
	prSearch := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: rec.MissionID, Kind: "pr_evidence", Query: "pull/54"})
	if prSearch.TotalMatches != 1 {
		t.Fatalf("PR evidence search did not bind pull request link: %+v", prSearch)
	}
	ciSearch := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: rec.MissionID, Kind: "ci_evidence", Query: "actions/runs/123"})
	if ciSearch.TotalMatches != 1 {
		t.Fatalf("CI evidence search did not bind CI run link: %+v", ciSearch)
	}
}

func TestEventEvidenceAliasSearchReadbacksFixtureCoversAllAliases(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "event-evidence-alias-search-readbacks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema     string   `json:"schema"`
		Status     string   `json:"status"`
		Mission    string   `json:"mission"`
		EventKinds []string `json:"event_kinds"`
		Searches   []struct {
			Kind                string `json:"kind"`
			Query               string `json:"query"`
			TotalMatches        int    `json:"total_matches"`
			SafeToExecute       bool   `json:"safe_to_execute"`
			ExecutesWork        bool   `json:"executes_work"`
			ApprovesWork        bool   `json:"approves_work"`
			MutatesRepositories bool   `json:"mutates_repositories"`
		} `json:"searches"`
		SafeToExecute       bool `json:"safe_to_execute"`
		ExecutesWork        bool `json:"executes_work"`
		ApprovesWork        bool `json:"approves_work"`
		MutatesRepositories bool `json:"mutates_repositories"`
		RSIRemainsDenied    bool `json:"rsi_remains_denied"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != "ao.mission.event-evidence-alias-search-readbacks.v0.1" || fixture.Status != "passed" || fixture.Mission != "ao-mission-doubled-wave-v01" {
		t.Fatalf("bad fixture header: %+v", fixture)
	}
	expected := []string{"route_evidence", "node_evidence", "pr_evidence", "ci_evidence", "rollup_evidence", "blocker_evidence"}
	for _, kind := range expected {
		if !stringSliceContains(fixture.EventKinds, kind) {
			t.Fatalf("fixture missing event kind %s: %+v", kind, fixture.EventKinds)
		}
		found := false
		for _, search := range fixture.Searches {
			if search.Kind != kind {
				continue
			}
			found = true
			if search.TotalMatches < 1 || search.SafeToExecute || search.ExecutesWork || search.ApprovesWork || search.MutatesRepositories {
				t.Fatalf("bad %s search fixture: %+v", kind, search)
			}
		}
		if !found {
			t.Fatalf("fixture missing search readback for %s", kind)
		}
	}
	if fixture.SafeToExecute || fixture.ExecutesWork || fixture.ApprovesWork || fixture.MutatesRepositories || !fixture.RSIRemainsDenied {
		t.Fatalf("fixture widened authority or failed RSI denial: %+v", fixture)
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestMissionSQLiteMigrationDryRunFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "mission-sqlite-migration-dry-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema               string `json:"schema"`
		Status               string `json:"status"`
		Mission              string `json:"mission"`
		DryRunOnly           bool   `json:"dry_run_only"`
		SourceStoreKind      string `json:"source_store_kind"`
		TargetStoreKind      string `json:"target_store_kind"`
		RecordsScanned       int    `json:"records_scanned"`
		RecordsPlanned       int    `json:"records_planned"`
		RecordsWritten       int    `json:"records_written"`
		SQLiteFileCreated    bool   `json:"sqlite_file_created"`
		SourceMutated        bool   `json:"source_mutated"`
		MigrationStarted     bool   `json:"migration_started"`
		ProviderCalls        bool   `json:"provider_calls"`
		CredentialUse        bool   `json:"credential_use"`
		ReleaseOrPublish     bool   `json:"release_or_publish"`
		DirectMainMutation   bool   `json:"direct_main_mutation"`
		SafeToExecute        bool   `json:"safe_to_execute"`
		ExecutesWork         bool   `json:"executes_work"`
		ApprovesWork         bool   `json:"approves_work"`
		MutatesRepositories  bool   `json:"mutates_repositories"`
		RSIRemainsDenied     bool   `json:"rsi_remains_denied"`
		ExactNextAction      string `json:"exact_next_action"`
		PlanDigest           string `json:"plan_digest"`
		RollbackReceiptReady bool   `json:"rollback_receipt_ready"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != "ao.mission.sqlite-migration-dry-run.v0.1" || fixture.Status != "ready" || fixture.Mission != "ao-stack-month6-recommendations" {
		t.Fatalf("bad SQLite migration fixture header: %+v", fixture)
	}
	if !fixture.DryRunOnly || fixture.SourceStoreKind != "json_ledger" || fixture.TargetStoreKind != "sqlite" {
		t.Fatalf("fixture should describe a JSON ledger to SQLite dry-run: %+v", fixture)
	}
	if fixture.RecordsScanned < 1 || fixture.RecordsPlanned != fixture.RecordsScanned || fixture.RecordsWritten != 0 {
		t.Fatalf("fixture should plan records without writing them: %+v", fixture)
	}
	if fixture.SQLiteFileCreated || fixture.SourceMutated || fixture.MigrationStarted || fixture.ProviderCalls || fixture.CredentialUse || fixture.ReleaseOrPublish || fixture.DirectMainMutation {
		t.Fatalf("fixture performed a forbidden side effect: %+v", fixture)
	}
	if fixture.SafeToExecute || fixture.ExecutesWork || fixture.ApprovesWork || fixture.MutatesRepositories || !fixture.RSIRemainsDenied {
		t.Fatalf("fixture widened authority or failed RSI denial: %+v", fixture)
	}
	if !fixture.RollbackReceiptReady || !strings.HasPrefix(fixture.PlanDigest, "sha256:") || !strings.Contains(fixture.ExactNextAction, "review SQLite migration plan") {
		t.Fatalf("fixture missing rollback/digest/next-action binding: %+v", fixture)
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestEventIndexSearchesAtlasRecommendationReadbackEvidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("index Atlas recommendation readback evidence")
	if err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback-short.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":22,"min_minutes_met":false,"lease_time_status":"minimum_minutes_unmet","return_gate_status":"blocked_minimum_minutes_unmet","final_response_allowed":false,"safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"claims_authority_advance":false,"exact_next_action":"Continue AO Atlas wave until the minimum lease duration is met."}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath); err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	results := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: rec.MissionID, Kind: "atlas_recommendation", Query: "blocked_minimum_minutes_unmet"})
	if results.TotalMatches != 1 {
		t.Fatalf("expected one Atlas recommendation event, got %+v", results)
	}
	event := results.Events[0]
	if event.Status != "completed" || !strings.Contains(event.Summary, "elapsed_minutes=22") || !strings.Contains(event.Summary, "ready_nodes=0") {
		t.Fatalf("Atlas recommendation event missing terminal details: %+v", event)
	}
	if results.ExecutesWork || results.ApprovesWork || results.MutatesRepositories {
		t.Fatalf("event search widened authority: %+v", results)
	}
}

func TestEventIndexSearchesFinalReconciliationEvidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("index final reconciliation evidence")
	if err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":491,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"final_response_allowed","final_response_allowed":true,"safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"claims_authority_advance":false}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath); err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	results := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: rec.MissionID, Kind: "final_reconciliation", Query: "artifacts_agree=true"})
	if results.TotalMatches != 1 {
		t.Fatalf("expected one final reconciliation event, got %+v", results)
	}
	if results.Events[0].Status != "ready" || !strings.Contains(results.Events[0].Summary, "rsi_remains_denied=true") {
		t.Fatalf("bad final reconciliation event: %+v", results.Events[0])
	}
}

func TestCommandStatusIncludesAtlasRecommendationSummary(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("command status over Atlas recommendation readback")
	if err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":491,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"final_response_allowed","final_response_allowed":true,"safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"claims_authority_advance":false}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-recommendation-readback", readbackPath); err != nil {
		t.Fatal(err)
	}
	done, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	status := BuildCommandStatus(done)
	if status.AtlasRecommendation == nil {
		t.Fatalf("command status missing Atlas recommendation summary: %+v", status)
	}
	if status.AtlasRecommendation.CompletedNodes != 40 || status.AtlasRecommendation.ReadyNodes != 0 || status.AtlasRecommendation.ReturnGateStatus != "final_response_allowed" || !status.AtlasRecommendation.FinalResponseAllowed {
		t.Fatalf("bad Atlas recommendation summary: %+v", status.AtlasRecommendation)
	}
	if status.ExecutesWork || status.ApprovesWork || status.MutatesRepositories {
		t.Fatalf("command status widened authority: %+v", status)
	}
}

func TestCommandStatusBindsLongRunLeaseAndCheckpointFreshness(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("command status long-run lease checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := Continue(s, rec.MissionID, ContinueOptions{
		UntilDone:        true,
		MaxIterations:    2,
		MinNodes:         15,
		MinMinutes:       120,
		MaxMinutes:       180,
		ReturnOnlyWhen:   defaultReturnOnlyWhen,
		CheckpointPolicy: defaultCheckpointPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := BuildCommandStatus(continued)
	if status.GoalLease == nil ||
		status.GoalLease.MinNodes != 15 ||
		status.GoalLease.MinMinutes != 120 ||
		status.GoalLease.MaxMinutes != 180 ||
		status.GoalLease.CheckpointPolicy != defaultCheckpointPolicy {
		t.Fatalf("command status missing long-run lease: %+v", status)
	}
	if status.CheckpointCount != 2 || status.CheckpointFreshnessStatus != "fresh" || status.ReturnGateStatus != "early_return_denied" {
		t.Fatalf("command status missing checkpoint freshness or return gate: %+v", status)
	}
	if status.ExecutesWork || status.ApprovesWork || status.MutatesRepositories {
		t.Fatalf("command status widened authority: %+v", status)
	}
}

func TestCommandStatusLeaseCheckpointFixtureValidatesReadback(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "command-status-lease-checkpoint-readback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var status CommandStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if status.Schema != "ao.command.mission-status.v0.1" ||
		status.GoalLease == nil ||
		status.GoalLease.MinNodes != 15 ||
		status.GoalLease.MinMinutes != 120 ||
		status.GoalLease.MaxMinutes != 180 ||
		status.GoalLease.CheckpointPolicy != defaultCheckpointPolicy ||
		status.CheckpointCount != 2 ||
		status.CheckpointFreshnessStatus != "fresh" ||
		status.ReturnGateStatus != "early_return_denied" {
		t.Fatalf("bad command status lease/checkpoint fixture: %+v", status)
	}
	if !status.ReadOnly || status.SafeToExecute || status.ExecutesWork || status.ApprovesWork || status.MutatesRepositories {
		t.Fatalf("fixture widened command authority: %+v", status)
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestCLICommandStatusTextIncludesAtlasRecommendationSummary(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "command status text Atlas recommendation"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":491,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"final_response_allowed","final_response_allowed":true,"safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"claims_authority_advance":false}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "import", "atlas-recommendation-readback", "--mission", rec.MissionID, "--path", readbackPath}, &out, &errb); code != 0 {
		t.Fatalf("import: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "command", "status", "--mission", rec.MissionID}, &out, &errb); code != 0 {
		t.Fatalf("command status: %s", errb.String())
	}
	text := out.String()
	for _, want := range []string{"atlas_recommendation=completed", "completed_nodes=40", "ready_nodes=0", "final_response_allowed=true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("command status text missing %q: %s", want, text)
		}
	}
}

func TestImportAtlasFinalSynthesisReadbackClosesStaleRoute(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "stale Atlas final synthesis route"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	parentBoundReadbackPath := writeParentBoundAtlasFinalSynthesisReadback(t, dir, rec.MissionID)
	if code := Run([]string{
		"--home", dir, "import", "atlas-final-synthesis-readback",
		"--mission", rec.MissionID,
		"--path", parentBoundReadbackPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("import: %s", errb.String())
	}
	done, err := NewStore(dir).Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.CurrentRoute != "complete" || done.CurrentPhase != "complete" {
		t.Fatalf("final synthesis import did not close stale route: %+v", done)
	}
	if done.ExactNextAction != "use next-wave-recommended-prompt.md for the next 30-node AO Atlas wave" {
		t.Fatalf("terminal final synthesis exact next action was not preserved: %q", done.ExactNextAction)
	}
	if done.Evidence.AtlasFinalSynthesis == nil ||
		done.Evidence.AtlasFinalSynthesis.CommandReadback != "ready" ||
		done.Evidence.AtlasFinalSynthesis.PromoterStatus != "no_promotion_requested" ||
		!done.Evidence.AtlasFinalSynthesis.BranchCleanupBound {
		t.Fatalf("final synthesis evidence not bound: %+v", done.Evidence.AtlasFinalSynthesis)
	}
	if done.Evidence.AtlasRecommendation == nil ||
		done.Evidence.AtlasRecommendation.TotalNodes != 26 ||
		done.Evidence.AtlasRecommendation.CompletedNodes != 26 ||
		done.Evidence.AtlasRecommendation.ReadyNodes != 0 ||
		done.Evidence.AtlasRecommendation.ReturnGateStatus != "final_response_allowed" ||
		!done.Evidence.AtlasRecommendation.FinalResponseAllowed ||
		!done.Evidence.AtlasRecommendation.RSIRemainsDenied {
		t.Fatalf("final synthesis did not populate Atlas recommendation counts: %+v", done.Evidence.AtlasRecommendation)
	}
	if done.Reconciliation == nil ||
		done.Reconciliation.Status != "ready" ||
		done.Reconciliation.CurrentRoute != "complete" ||
		done.Reconciliation.LatestRoute != "complete" ||
		done.Reconciliation.AtlasReadyNodes != 0 ||
		!done.Reconciliation.CommandReadbackBound ||
		!done.Reconciliation.PromoterReadbackBound {
		t.Fatalf("route/readback reconciliation not closed: %+v", done.Reconciliation)
	}
	if done.ReturnGate == nil || !done.ReturnGate.FinalResponseAllowed {
		t.Fatalf("return gate should allow terminal imported final synthesis: %+v", done.ReturnGate)
	}
}

func TestImportAtlasFinalSynthesisReadbackDoesNotCloseForeignMission(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "foreign Atlas final synthesis route"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{
		"--home", dir, "import", "atlas-final-synthesis-readback",
		"--mission", rec.MissionID,
		"--path", filepath.Join("..", "..", "examples", "valid", "atlas-final-synthesis-readback.json"),
	}, &out, &errb); code != 0 {
		t.Fatalf("import: %s", errb.String())
	}
	imported, err := NewStore(dir).Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Status == "done" || imported.CurrentRoute != "ao-atlas" || imported.CurrentPhase != "atlas_final_synthesis_readback_recorded" {
		t.Fatalf("foreign final synthesis should not close parent mission: %+v", imported)
	}
	if imported.ExactNextAction != "reconcile parent-bound Atlas final synthesis readback before closing mission" {
		t.Fatalf("foreign final synthesis did not request parent-bound reconciliation: %q", imported.ExactNextAction)
	}
	if imported.ReturnGate == nil || imported.ReturnGate.FinalResponseAllowed {
		t.Fatalf("foreign final synthesis should keep final response denied: %+v", imported.ReturnGate)
	}
}

func writeParentBoundAtlasFinalSynthesisReadback(t *testing.T, dir, missionID string) string {
	t.Helper()
	readbackBody, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "atlas-final-synthesis-readback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var readback map[string]any
	if err := json.Unmarshal(readbackBody, &readback); err != nil {
		t.Fatal(err)
	}
	readback["mission_id"] = missionID
	parentBoundReadbackPath := filepath.Join(dir, "atlas-final-synthesis-readback.json")
	readbackBody, err = json.Marshal(readback)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parentBoundReadbackPath, readbackBody, 0o644); err != nil {
		t.Fatal(err)
	}
	return parentBoundReadbackPath
}

func TestImportAtlasFinalSynthesisReadbackBindsCorrelatedMission(t *testing.T) {
	tests := []struct {
		name          string
		correlationID *string
		wantError     string
	}{
		{name: "matching", correlationID: stringPointer("corr-final-synthesis-001")},
		{name: "missing", wantError: "correlation_id is required for correlated mission"},
		{name: "mismatched", correlationID: stringPointer("corr-final-synthesis-other"), wantError: "correlation_id does not match mission"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewStore(dir)
			contract, err := store.StartObjective("correlated final synthesis import", ObjectiveStartOptions{
				CorrelationID: "corr-final-synthesis-001",
			})
			if err != nil {
				t.Fatal(err)
			}
			path := writeParentBoundAtlasFinalSynthesisReadback(t, dir, contract.MissionID)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(body, &document); err != nil {
				t.Fatal(err)
			}
			if tt.correlationID != nil {
				document["correlation_id"] = *tt.correlationID
			}
			body, err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err = ImportArtifact(store, contract.MissionID, "atlas-final-synthesis-readback", path)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("matching correlation was rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("got error %v, want %q", err, tt.wantError)
			}
		})
	}

	dir := t.TempDir()
	store := NewStore(dir)
	record, err := store.Start("legacy final synthesis import")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(store, record.MissionID, "atlas-final-synthesis-readback", writeParentBoundAtlasFinalSynthesisReadback(t, dir, record.MissionID)); err != nil {
		t.Fatalf("legacy uncorrelated import regressed: %v", err)
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestImportAtlasFinalSynthesisReadbackRejectsReadyNodeDrift(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("reject final synthesis drift")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "atlas-final-synthesis-readback.json")
	if err := os.WriteFile(path, []byte(`{
		"contract_version":"ao.atlas.ao-mission-final-synthesis-readback.v0.1",
		"mission_id":"ao-mission-drift",
		"status":"completed",
		"source_digest":"sha256:a5f018dd6e64f1975e63b344822989d1d8d779e1c17df2a931a6aac8ed352c44",
		"total_nodes":26,
		"completed_nodes":25,
		"ready_nodes":1,
		"blocked_nodes":0,
		"minimum_nodes":25,
		"target_minutes":120,
		"max_minutes":180,
		"return_gate_status":"final_response_allowed",
		"final_response_allowed":true,
		"final_response_reason":"bad drift",
		"atlas_workgraph_status":"completed",
		"foundry_rollup":"readback-only wave; no promotion requested",
		"promoter_status":"no_promotion_requested",
		"command_readback":"ready",
		"event_search_bound":true,
		"branch_cleanup_bound":true,
		"exact_next_action":"should not close",
		"feature_depth_next_tasks":["one","two","three","four","five","six","seven","eight","nine","ten"],
		"rsi_remains_denied":true,
		"promotion_claimed":false,
		"claims_authority_advance":false,
		"safe_to_execute":false,
		"schedules_work":false,
		"executes_work":false,
		"approves_work":false,
		"mutates_repositories":false
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ImportArtifact(s, rec.MissionID, "atlas-final-synthesis-readback", path)
	if err == nil || !strings.Contains(err.Error(), "final response cannot be allowed while ready nodes remain") {
		t.Fatalf("expected ready-node final synthesis rejection, got %v", err)
	}
}

func TestImportArtifactWritesDurableCheckpointResumeBundle(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("durable checkpoint after import")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-final-synthesis-readback", writeParentBoundAtlasFinalSynthesisReadback(t, dir, rec.MissionID)); err != nil {
		t.Fatal(err)
	}
	bundle, err := s.LoadCheckpointBundle(rec.MissionID)
	if err != nil {
		t.Fatalf("checkpoint bundle should be written after import: %v", err)
	}
	if bundle.Schema != CheckpointBundleSchema ||
		bundle.MissionID != rec.MissionID ||
		bundle.Status != "ready" ||
		bundle.ReturnGate == nil ||
		!bundle.ReturnGate.FinalResponseAllowed ||
		!strings.Contains(bundle.ResumePrompt, "ao-mission continue --mission") ||
		bundle.SafeToExecute ||
		bundle.ExecutesWork ||
		bundle.ApprovesWork ||
		bundle.MutatesRepositories {
		t.Fatalf("bad import checkpoint bundle: %+v", bundle)
	}
}

func TestCLICheckpointInspectReplaysAtlasImportCheckpointBundle(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "checkpoint replay after Atlas readback import"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{
		"--home", dir,
		"import", "atlas-final-synthesis-readback",
		"--mission", rec.MissionID,
		"--path", writeParentBoundAtlasFinalSynthesisReadback(t, dir, rec.MissionID),
	}, &out, &errb); code != 0 {
		t.Fatalf("import: %s", errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", dir, "checkpoint", "inspect", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("checkpoint inspect: %s", errb.String())
	}
	var bundle MissionCheckpointBundle
	if err := json.Unmarshal(out.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Schema != CheckpointBundleSchema ||
		bundle.MissionID != rec.MissionID ||
		bundle.Status != "ready" ||
		bundle.ReturnGate == nil ||
		!bundle.ReturnGate.FinalResponseAllowed ||
		!strings.Contains(bundle.ResumePrompt, "ao-mission continue --mission "+rec.MissionID) ||
		bundle.SafeToExecute ||
		bundle.ExecutesWork ||
		bundle.ApprovesWork ||
		bundle.MutatesRepositories {
		t.Fatalf("checkpoint inspect should replay readback-only import bundle: %+v", bundle)
	}
}

func TestAtlasContinuationPromptPacketBindsRollupReadinessAndEventIndex(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("atlas prompt packet")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "atlas-final-synthesis-readback", writeParentBoundAtlasFinalSynthesisReadback(t, dir, rec.MissionID)); err != nil {
		t.Fatal(err)
	}
	done, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := BuildAtlasContinuationPromptPacket(done, index)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Schema != "ao.mission.atlas-continuation-prompt-packet.v0.1" ||
		packet.MissionID != rec.MissionID ||
		packet.Status != "ready" ||
		packet.EventIndexDigest != index.IndexDigest ||
		!strings.HasPrefix(packet.FinalRollupDigest, "sha256:") ||
		packet.ReadyNodesRemaining != 0 ||
		!packet.FinalResponseAllowed ||
		!strings.Contains(packet.Prompt, "AO Atlas") ||
		!strings.Contains(packet.Prompt, "event_index_digest="+index.IndexDigest) ||
		!strings.Contains(packet.Prompt, "Do not produce a final response if ready_nodes > 0 or exact_next_action remains.") ||
		len(packet.FeatureDepthRecommendations) < 10 ||
		packet.SafeToExecute ||
		packet.ExecutesWork ||
		packet.ApprovesWork ||
		packet.MutatesRepositories {
		t.Fatalf("bad Atlas continuation prompt packet: %+v", packet)
	}
}

func TestAtlasContinuationPromptPacketRejectsShallowFeatureDepthRecommendations(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("atlas prompt packet shallow feature depth")
	if err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	rollup := BuildFinalRollup(rec)
	rollup.FeatureDepthRecommendations = []FeatureDepthRecommendation{
		{
			ID:                  "shallow",
			Owner:               "ao-atlas",
			Task:                "Continue.",
			Gate:                "too shallow",
			EvidenceRequired:    []string{"one", "two", "three"},
			EstimatedMinutes:    6,
			ContinuationCommand: "ao-mission continue --mission " + rec.MissionID,
			ExactNextAction:     "continue",
			StopCondition:       defaultReturnOnlyWhen,
		},
	}
	_, err = buildAtlasContinuationPromptPacket(rec, index, rollup)
	if err == nil || !strings.Contains(err.Error(), "feature depth recommendations too shallow") {
		t.Fatalf("expected shallow Feature Depth rejection, got %v", err)
	}
}

func TestCLIAtlasContinuationPromptWritesPacket(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "cli atlas prompt packet"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(NewStore(dir), rec.MissionID, "atlas-final-synthesis-readback", writeParentBoundAtlasFinalSynthesisReadback(t, dir, rec.MissionID)); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, "event-index.json")
	evidenceRoot := filepath.Join(dir, "campaign evidence")
	outPath := filepath.Join(dir, "atlas-prompt-packet.json")
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "events", "index", "--out", indexPath}, &out, &errb); code != 0 {
		t.Fatalf("event index: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "final", "atlas-prompt", "--mission", rec.MissionID, "--event-index", indexPath, "--evidence-root", evidenceRoot, "--out", outPath, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("atlas prompt: %s", errb.String())
	}
	var packet AtlasContinuationPromptPacket
	if err := json.Unmarshal(out.Bytes(), &packet); err != nil {
		t.Fatalf("prompt packet did not emit json: %v\n%s", err, out.String())
	}
	if packet.MissionID != rec.MissionID || packet.EventIndexDigest == "" || packet.FinalRollupDigest == "" || packet.Prompt == "" {
		t.Fatalf("bad CLI prompt packet: %+v", packet)
	}
	wantSynthesisCommand := fmt.Sprintf("ao-mission final synthesize --mission %s --evidence-root %q", rec.MissionID, filepath.ToSlash(filepath.Clean(evidenceRoot)))
	foundSynthesisCommand := false
	for _, recommendation := range packet.FeatureDepthRecommendations {
		if recommendation.ID == "final-synthesis-readback" {
			foundSynthesisCommand = true
			if recommendation.ContinuationCommand != wantSynthesisCommand {
				t.Fatalf("atlas prompt recommendation root mismatch: got %q want %q", recommendation.ContinuationCommand, wantSynthesisCommand)
			}
		}
	}
	if !foundSynthesisCommand {
		t.Fatal("atlas prompt omitted final synthesis recommendation")
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatal(err)
	}
}

func TestFinalReconciliationPacketAgreesAcrossAtlasFoundryAndCommand(t *testing.T) {
	rec := Record{
		Schema:          RecordSchema,
		MissionID:       "mission-reconcile",
		Status:          "done",
		CurrentRoute:    "complete",
		CurrentPhase:    "complete",
		ExactNextAction: "mission complete; read final rollup and recommended next tasks",
		Evidence: EvidenceSummary{
			AtlasRecommendation: &AtlasRecommendationReadbackCounts{
				Status:               "completed",
				TotalNodes:           40,
				CompletedNodes:       40,
				ReadyNodes:           0,
				CheckpointCount:      40,
				MinMinutesMet:        true,
				LeaseTimeStatus:      "minimum_minutes_met",
				ReturnGateStatus:     "final_response_allowed",
				FinalResponseAllowed: true,
			},
			FoundryRollup: &FoundryRollupCounts{Status: "completed", CompletedNodes: 40, TotalNodes: 40},
		},
	}
	packet := BuildFinalReconciliationPacket(rec)
	if packet.Status != "ready" || !packet.ArtifactsAgree || !packet.FinalResponseAllowed {
		t.Fatalf("reconciliation should agree for completed evidence: %+v", packet)
	}
	if packet.AtlasRecommendationStatus != "completed" || packet.CommandStatus != "done" || packet.FoundryStatus != "completed" {
		t.Fatalf("packet missing status summary: %+v", packet)
	}
	if packet.PromotionClaimed || !packet.RSIRemainsDenied || packet.ClaimsAuthorityAdvance {
		t.Fatalf("packet widened promotion or RSI boundary: %+v", packet)
	}
}

func TestFinalReconciliationPacketReportsFoundryAtlasMismatch(t *testing.T) {
	rec := Record{
		Schema:       RecordSchema,
		MissionID:    "mission-reconcile-mismatch",
		Status:       "done",
		CurrentRoute: "complete",
		CurrentPhase: "complete",
		Evidence: EvidenceSummary{
			AtlasRecommendation: &AtlasRecommendationReadbackCounts{
				Status:               "completed",
				TotalNodes:           40,
				CompletedNodes:       40,
				ReadyNodes:           0,
				CheckpointCount:      40,
				MinMinutesMet:        true,
				LeaseTimeStatus:      "minimum_minutes_met",
				ReturnGateStatus:     "final_response_allowed",
				FinalResponseAllowed: true,
			},
			FoundryRollup: &FoundryRollupCounts{Status: "completed", CompletedNodes: 39, TotalNodes: 40},
		},
	}
	packet := BuildFinalReconciliationPacket(rec)
	if packet.Status != "blocked" || packet.ArtifactsAgree {
		t.Fatalf("reconciliation mismatch should block: %+v", packet)
	}
	if !strings.Contains(packet.Blocker, "Foundry completed_nodes=39") || !strings.Contains(packet.Blocker, "Atlas completed_nodes=40") {
		t.Fatalf("packet missing exact mismatch blocker: %+v", packet)
	}
}

func TestFinalReconciliationMismatchFixturePreservesExactBlocker(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "final-reconciliation-mismatch-packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	var packet MissionFinalReconciliationPacket
	if err := json.Unmarshal(body, &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Schema != "ao.mission.final-reconciliation-packet.v0.1" || packet.Status != "blocked" || packet.ArtifactsAgree {
		t.Fatalf("unexpected mismatch fixture header: %+v", packet)
	}
	for _, want := range []string{"Foundry completed_nodes=39", "Atlas completed_nodes=40"} {
		if !strings.Contains(packet.Blocker, want) {
			t.Fatalf("mismatch fixture missing blocker detail %q: %+v", want, packet)
		}
	}
	if packet.SafeToExecute || packet.ExecutesWork || packet.ApprovesWork || packet.MutatesRepositories || packet.ClaimsAuthorityAdvance {
		t.Fatalf("mismatch fixture widened authority: %+v", packet)
	}
	if !packet.RSIRemainsDenied || packet.PromotionClaimed {
		t.Fatalf("mismatch fixture should keep promotion denied and RSI denied: %+v", packet)
	}
}

func TestCLIFinalReconcileEmitsPacket(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "cli final reconciliation"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	readbackPath := filepath.Join(dir, "recommendation-readback.json")
	readback := `{"schema":"ao.atlas.recommendation-readback.v0.1","status":"completed","total_nodes":40,"completed_nodes":40,"ready_nodes":0,"checkpoint_count":40,"elapsed_minutes":491,"min_minutes_met":true,"lease_time_status":"minimum_minutes_met","return_gate_status":"final_response_allowed","final_response_allowed":true,"safe_to_execute":false,"executes_work":false,"approves_work":false,"mutates_repositories":false,"provider_calls":false,"release_or_publish":false,"credential_use":false,"direct_main_mutation":false,"concurrent_mutation":false,"claims_authority_advance":false}`
	if err := os.WriteFile(readbackPath, []byte(readback), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "import", "atlas-recommendation-readback", "--mission", rec.MissionID, "--path", readbackPath}, &out, &errb); code != 0 {
		t.Fatalf("import: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "final", "reconcile", "--mission", rec.MissionID}, &out, &errb); code != 0 {
		t.Fatalf("final reconcile: %s", errb.String())
	}
	var packet MissionFinalReconciliationPacket
	if err := json.Unmarshal(out.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Schema != "ao.mission.final-reconciliation-packet.v0.1" || !packet.ArtifactsAgree || !packet.FinalResponseAllowed {
		t.Fatalf("bad reconciliation packet: %+v", packet)
	}
	if packet.ExecutesWork || packet.ApprovesWork || packet.MutatesRepositories {
		t.Fatalf("reconciliation packet widened authority: %+v", packet)
	}
}

func TestCLIFinalSynthesizeEmitsEvidenceRootPacket(t *testing.T) {
	dir := t.TempDir()
	evidenceRoot := filepath.Join(dir, "evidence")
	if err := os.MkdirAll(evidenceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	workgraph := `{"schema":"ao.atlas.workgraph.v0.1","mission":"ao-mission-doubled-wave-v01","status":"completed","minimum_nodes":60,"target_minutes":120,"max_minutes":180,"completed_nodes":60,"ready_nodes":0,"blocked_nodes":0,"final_response_allowed":true,"exact_next_action":"read final synthesis"}`
	if err := os.WriteFile(filepath.Join(evidenceRoot, "workgraph.json"), []byte(workgraph), 0o644); err != nil {
		t.Fatal(err)
	}
	closure := `{"schema":"ao.mission.post-merge-final-closure.v0.1","mission":"ao-mission-doubled-wave-v01","status":"completed","completed_nodes":60,"ready_nodes":0,"blocked_nodes":0,"merged_prs":[101,102],"stale_local_codex_branches_remaining":0,"stale_remote_codex_branches_remaining":0,"final_response_allowed":true,"rsi_remains_denied":true}`
	if err := os.WriteFile(filepath.Join(evidenceRoot, "post-merge-final-closure.json"), []byte(closure), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "cli final synthesis"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "final", "synthesize", "--mission", rec.MissionID, "--evidence-root", evidenceRoot}, &out, &errb); code != 0 {
		t.Fatalf("final synthesize: %s", errb.String())
	}
	var packet AtlasWaveFinalSynthesis
	if err := json.Unmarshal(out.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Schema != "ao.mission.atlas-wave-final-synthesis.v0.1" || packet.Mission != "ao-mission-doubled-wave-v01" || packet.CompletedNodes != 60 || packet.ReadyNodes != 0 || packet.BlockedNodes != 0 || !packet.FinalResponseAllowed {
		t.Fatalf("bad final synthesis packet: %+v", packet)
	}
	if len(packet.FeatureDepthRecommendations) < 20 {
		t.Fatalf("recommendations too shallow: %d", len(packet.FeatureDepthRecommendations))
	}
	wantSynthesisCommand := fmt.Sprintf(
		"ao-mission final synthesize --mission %s --evidence-root %q",
		rec.MissionID,
		filepath.ToSlash(filepath.Clean(evidenceRoot)),
	)
	foundSynthesisCommand := false
	for _, recommendation := range packet.FeatureDepthRecommendations {
		if recommendation.ID == "final-synthesis-readback" {
			foundSynthesisCommand = true
			if recommendation.ContinuationCommand != wantSynthesisCommand {
				t.Fatalf("final synthesis recommendation root mismatch: got %q want %q", recommendation.ContinuationCommand, wantSynthesisCommand)
			}
		}
	}
	if !foundSynthesisCommand {
		t.Fatal("final synthesis omitted its evidence-root recommendation")
	}
	if packet.CurrentNodePRPending || packet.PromotionClaimed || packet.ClaimsAuthorityAdvance || !packet.RSIRemainsDenied || packet.ExecutesWork || packet.ApprovesWork || packet.MutatesRepositories {
		t.Fatalf("final synthesis widened authority or kept stale pending state: %+v", packet)
	}
}

func TestBuildAtlasWaveFinalSynthesisPreservesCorrelationID(t *testing.T) {
	evidenceRoot := t.TempDir()
	workgraph := `{"schema":"ao.atlas.workgraph.v0.1","mission":"mission-correlated","status":"completed","minimum_nodes":8,"target_minutes":145,"max_minutes":180,"completed_nodes":8,"ready_nodes":0,"blocked_nodes":0,"final_response_allowed":true,"exact_next_action":"read final synthesis"}`
	if err := os.WriteFile(filepath.Join(evidenceRoot, "workgraph.json"), []byte(workgraph), 0o644); err != nil {
		t.Fatal(err)
	}
	closure := `{"schema":"ao.mission.post-merge-final-closure.v0.1","mission":"mission-correlated","status":"completed","completed_nodes":8,"ready_nodes":0,"blocked_nodes":0,"merged_prs":[610],"final_response_allowed":true}`
	if err := os.WriteFile(filepath.Join(evidenceRoot, "post-merge-final-closure.json"), []byte(closure), 0o644); err != nil {
		t.Fatal(err)
	}

	packet, err := BuildAtlasWaveFinalSynthesis(Record{
		MissionID:     "mission-correlated",
		CorrelationID: "corr-external-repair-001",
	}, evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if document["correlation_id"] != "corr-external-repair-001" {
		t.Fatalf("final synthesis lost correlation binding: %#v", document)
	}
}

func TestFeatureDepthRecommendationsReturnAtLeastTenActionableTasks(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("long-running atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	rec.Evidence.AtlasWorkgraph = &NodeCounts{Total: 12, Ready: 3, Blocked: 2, Completed: 7}
	rollup := BuildFinalRollup(rec)
	if len(rollup.FeatureDepthRecommendations) < 10 {
		t.Fatalf("recommendations too shallow: %d", len(rollup.FeatureDepthRecommendations))
	}
	for _, item := range rollup.FeatureDepthRecommendations {
		if item.ID == "" || item.Task == "" || item.Owner == "" || item.ExactNextAction == "" {
			t.Fatalf("recommendation is not actionable: %+v", item)
		}
	}
}

func TestFeatureDepthRecommendationsKeepGeneratedEvidenceOutOfPublicDocs(t *testing.T) {
	recommendations := BuildFeatureDepthRecommendations(Record{MissionID: "mission-test"}, 20)
	for _, item := range recommendations {
		if strings.Contains(item.ContinuationCommand, "docs/evidence") || strings.Contains(item.ContinuationCommand, ".ao-mission/evidence") {
			t.Fatalf("recommendation %q falls back to repository-local evidence: %q", item.ID, item.ContinuationCommand)
		}
		if (item.ID == "sentinel-wording-scan" || item.ID == "rollback-record-audit" || item.ID == "final-synthesis-readback") && !strings.Contains(item.ContinuationCommand, "<evidence-root>") {
			t.Fatalf("recommendation %q must require a caller-selected evidence root: %q", item.ID, item.ContinuationCommand)
		}
	}
}

func TestCLIFinalRollupUsesCallerSelectedEvidenceRoot(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(workspace, "non-default mission home")
	evidenceRoot := filepath.Join(workspace, "campaign evidence")
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", home, "start", "caller-selected evidence root"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var record Record
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", home, "final", "rollup", "--mission", record.MissionID, "--evidence-root", evidenceRoot}, &out, &errb); code != 0 {
		t.Fatalf("final rollup: %s", errb.String())
	}
	var rollup FinalRollup
	if err := json.Unmarshal(out.Bytes(), &rollup); err != nil {
		t.Fatal(err)
	}
	root := filepath.ToSlash(filepath.Clean(evidenceRoot))
	want := map[string]string{
		"sentinel-wording-scan":    fmt.Sprintf("ao-sentinel scan %q docs/operator-next-actions.md", root),
		"rollback-record-audit":    fmt.Sprintf("ao-mission artifacts manifest --mission %s --out %q", record.MissionID, filepath.ToSlash(filepath.Join(evidenceRoot, "rollback-audit-manifest.json"))),
		"final-synthesis-readback": fmt.Sprintf("ao-mission final synthesize --mission %s --evidence-root %q", record.MissionID, root),
	}
	for _, recommendation := range rollup.FeatureDepthRecommendations {
		expected, ok := want[recommendation.ID]
		if !ok {
			continue
		}
		if recommendation.ContinuationCommand != expected {
			t.Fatalf("recommendation %q command = %q, want %q", recommendation.ID, recommendation.ContinuationCommand, expected)
		}
		if strings.Contains(recommendation.ContinuationCommand, ".ao-mission/evidence") || strings.Contains(recommendation.ContinuationCommand, "docs/evidence") {
			t.Fatalf("recommendation %q fell back to repository-local evidence: %q", recommendation.ID, recommendation.ContinuationCommand)
		}
		delete(want, recommendation.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing evidence-root recommendations: %v", want)
	}
}

func TestCLIFinalRollupReturnsAtLeastTenActionableFeatureDepthRecommendations(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "long-running atlas workgraph mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", dir, "final", "rollup", "--mission", rec.MissionID}, &out, &errb); code != 0 {
		t.Fatalf("final rollup: %s", errb.String())
	}
	var rollup FinalRollup
	if err := json.Unmarshal(out.Bytes(), &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup.Schema != "ao.mission.final-rollup.v0.1" || rollup.MissionID != rec.MissionID {
		t.Fatalf("bad rollup header: %+v", rollup)
	}
	if len(rollup.FeatureDepthRecommendations) < 10 {
		t.Fatalf("CLI recommendations too shallow: %d", len(rollup.FeatureDepthRecommendations))
	}
	if err := ValidateFeatureDepthRecommendations(rollup.FeatureDepthRecommendations, 10); err != nil {
		t.Fatalf("CLI recommendations are not actionable: %v", err)
	}
	if rollup.SafeToExecute || rollup.ExecutesWork || rollup.ApprovesWork || rollup.ProviderCalls {
		t.Fatalf("final rollup widened authority: %+v", rollup)
	}
}

func TestFeatureDepthRecommendationsEnforceConcreteBudget(t *testing.T) {
	rec := Record{
		Schema:          RecordSchema,
		MissionID:       "mission-feature-depth-budget",
		Status:          "active",
		CurrentRoute:    "ao-atlas",
		CurrentPhase:    "atlas_workgraph_ready",
		ExactNextAction: "continue doubled Atlas wave",
		Evidence: EvidenceSummary{
			AtlasWorkgraph: &NodeCounts{Total: 60, Ready: 53, Blocked: 0, Completed: 7},
		},
	}
	recs := BuildFeatureDepthRecommendations(rec, 20)
	if err := ValidateFeatureDepthRecommendations(recs, 20); err != nil {
		t.Fatalf("recommendations should satisfy concrete budget: %v", err)
	}
	if len(recs) < 20 {
		t.Fatalf("recommendations too shallow: %d", len(recs))
	}
	totalMinutes := 0
	seen := map[string]bool{}
	for _, item := range recs {
		if seen[item.ID] {
			t.Fatalf("duplicate recommendation id: %s", item.ID)
		}
		seen[item.ID] = true
		totalMinutes += item.EstimatedMinutes
		if item.Gate == "" || len(item.EvidenceRequired) < 3 || item.ContinuationCommand == "" {
			t.Fatalf("recommendation missing concrete contract fields: %+v", item)
		}
		if item.EstimatedMinutes < 6 {
			t.Fatalf("recommendation under budget: %+v", item)
		}
	}
	if totalMinutes < 120 {
		t.Fatalf("recommendations under 2-hour budget: %d", totalMinutes)
	}

	shallow := []FeatureDepthRecommendation{
		{ID: "shallow", Owner: "ao-atlas", Task: "Do a thing.", ExactNextAction: "continue"},
	}
	if err := ValidateFeatureDepthRecommendations(shallow, 10); err == nil {
		t.Fatal("shallow recommendations should be rejected")
	}
}

func TestFinalRollupDeniesFinalResponseWhenReadyNodesRemain(t *testing.T) {
	rec := Record{
		Schema:          RecordSchema,
		MissionID:       "mission-ready-remains",
		Status:          "active",
		CurrentRoute:    "ao-foundry",
		CurrentPhase:    "atlas_workgraph_ready",
		ExactNextAction: "send first safe Atlas node to AO Foundry",
		Evidence: EvidenceSummary{
			AtlasWorkgraph: &NodeCounts{Total: 4, Ready: 2, Blocked: 0, Completed: 2},
		},
	}
	rollup := BuildFinalRollup(rec)
	if rollup.FinalResponseAllowed || rollup.ReturnGateStatus != "early_return_denied" {
		t.Fatalf("final response should be denied while ready nodes remain: %+v", rollup)
	}
	if !strings.Contains(rollup.ExactNextAction, "continue") {
		t.Fatalf("rollup did not preserve executable next action: %+v", rollup)
	}
}

func TestFinalRollupClearsExactNextActionWhenFinalResponseAllowed(t *testing.T) {
	rec := Record{
		Schema:          RecordSchema,
		MissionID:       "mission-terminal-rollup",
		Status:          "done",
		CurrentRoute:    "complete",
		CurrentPhase:    "complete",
		ExactNextAction: "use next-wave-recommended-prompt.md for the next 30-node AO Atlas wave",
		Evidence: EvidenceSummary{
			AtlasRecommendation: &AtlasRecommendationReadbackCounts{
				Status:               "completed",
				TotalNodes:           26,
				CompletedNodes:       26,
				ReadyNodes:           0,
				ReturnGateStatus:     "final_response_allowed",
				FinalResponseAllowed: true,
			},
		},
	}
	rollup := BuildFinalRollup(rec)
	if !rollup.FinalResponseAllowed {
		t.Fatalf("terminal rollup should allow final response: %+v", rollup)
	}
	if rollup.ReadyNodesRemaining != 0 {
		t.Fatalf("terminal rollup should have no ready nodes: %+v", rollup)
	}
	if rollup.ExactNextAction != "" {
		t.Fatalf("terminal rollup should clear exact next action, got %q", rollup.ExactNextAction)
	}
}

func TestCLIFinalRollupDeniesFinalResponseWhenReadyNodesRemain(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "final response ready nodes regression"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	workgraphPath := filepath.Join(dir, "atlas-workgraph-ready.json")
	workgraph := `{"schema":"ao.atlas.workgraph.v0.1","nodes":[` +
		`{"id":"node-1","status":"completed"},{"id":"node-2","status":"completed"},{"id":"node-3","status":"completed"},{"id":"node-4","status":"completed"},{"id":"node-5","status":"completed"},` +
		`{"id":"node-6","status":"completed"},{"id":"node-7","status":"completed"},{"id":"node-8","status":"completed"},{"id":"node-9","status":"completed"},{"id":"node-10","status":"completed"},` +
		`{"id":"node-11","status":"ready"},{"id":"node-12","status":"ready"}]}`
	if err := os.WriteFile(workgraphPath, []byte(workgraph), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", dir, "import", "atlas-workgraph", "--mission", rec.MissionID, "--path", workgraphPath}, &out, &errb); code != 0 {
		t.Fatalf("import atlas workgraph: %s", errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--home", dir, "final", "rollup", "--mission", rec.MissionID}, &out, &errb); code != 0 {
		t.Fatalf("final rollup: %s", errb.String())
	}
	var rollup FinalRollup
	if err := json.Unmarshal(out.Bytes(), &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup.FinalResponseAllowed || rollup.ReturnGateStatus != "early_return_denied" || rollup.ReadyNodesRemaining != 2 {
		t.Fatalf("CLI final rollup should deny final response while ready nodes remain: %+v", rollup)
	}
	if rollup.CompletedNodes != 10 || rollup.TotalNodes != 12 || !strings.Contains(rollup.ExactNextAction, "continue") {
		t.Fatalf("CLI final rollup did not preserve ready-work continuation context: %+v", rollup)
	}
	if rollup.SafeToExecute || rollup.ExecutesWork || rollup.ApprovesWork || rollup.ProviderCalls {
		t.Fatalf("final rollup widened authority: %+v", rollup)
	}
}

func TestFinalRollupReadyNodeDenialFixtureValidatesSchema(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "valid", "final-rollup-ready-node-denial.json")
	if result, err := ValidateContractFile(path); err != nil || result.Status != "ready" || result.Contract != "ao.mission.final-rollup.v0.1" {
		t.Fatalf("fixture should validate final-rollup contract: result=%+v err=%v", result, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rollup FinalRollup
	if err := json.Unmarshal(body, &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup.Schema != "ao.mission.final-rollup.v0.1" ||
		rollup.FinalResponseAllowed ||
		rollup.ReturnGateStatus != "early_return_denied" ||
		rollup.ReadyNodesRemaining != 2 ||
		rollup.CompletedNodes != 10 ||
		rollup.TotalNodes != 12 ||
		!strings.Contains(rollup.ExactNextAction, "ready nodes remain") {
		t.Fatalf("bad ready-node denial rollup fixture: %+v", rollup)
	}
	if err := ValidateFeatureDepthRecommendations(rollup.FeatureDepthRecommendations, 10); err != nil {
		t.Fatalf("fixture Feature Depth recommendations should be actionable: %v", err)
	}
	if rollup.SafeToExecute || rollup.ExecutesWork || rollup.ApprovesWork || rollup.ProviderCalls {
		t.Fatalf("fixture widened authority: %+v", rollup)
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramCommandFixtureMatrix(t *testing.T) {
	allowlist := map[string]string{"1001": "admin", "1002": "user"}
	matrix, err := LoadTelegramCommandMatrix(filepath.Join("..", "..", "examples", "valid", "telegram-command-matrix.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range matrix.Commands {
		chat := "1002"
		if tc.Role == "admin" {
			chat = "1001"
		}
		rb := HandleTelegramCommand(TelegramCommand{ChatID: chat, Command: tc.Command, Role: tc.Role}, allowlist)
		if rb.Status != tc.ExpectedStatus || rb.MutationAuthority {
			t.Fatalf("%s/%s: %+v", tc.Command, tc.Role, rb)
		}
	}
}

func TestTelegramInvalidCommandMatrix(t *testing.T) {
	allowlist := map[string]string{"1001": "admin", "1002": "user"}
	matrix, err := LoadTelegramCommandMatrix(filepath.Join("..", "..", "examples", "invalid", "telegram-command-matrix-denied.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range matrix.Commands {
		chat := "1002"
		if tc.Role == "admin" {
			chat = "1001"
		}
		if tc.Role == "none" {
			chat = "9999"
		}
		rb := HandleTelegramCommand(TelegramCommand{ChatID: chat, Command: tc.Command, Role: tc.Role}, allowlist)
		if rb.Status != tc.ExpectedStatus || rb.MutationAuthority {
			t.Fatalf("%s/%s: %+v", tc.Command, tc.Role, rb)
		}
	}
}

func TestA2AJSONRPCHandlerIntentOnly(t *testing.T) {
	server := httptest.NewServer(A2AHandler())
	defer server.Close()
	req := strings.NewReader(`{"jsonrpc":"2.0","id":"req-1","method":"mission.status","params":{"mission_id":"mission-demo"}}`)
	resp, err := http.Post(server.URL+"/", "application/json", req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rpc A2AJSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.JSONRPC != "2.0" || rpc.ID != "req-1" || rpc.Result.MutationAuthority {
		t.Fatalf("bad json-rpc response: %+v", rpc)
	}
	if rpc.Result.Method != "mission.status" || rpc.Result.Status != "intent_recorded" {
		t.Fatalf("bad a2a task: %+v", rpc.Result)
	}
}

func TestA2AJSONRPCHandlerValidatesMethodParams(t *testing.T) {
	server := httptest.NewServer(A2AHandler())
	defer server.Close()
	req := strings.NewReader(`{"jsonrpc":"2.0","id":"req-2","method":"mission.continue","params":{}}`)
	resp, err := http.Post(server.URL+"/", "application/json", req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rpc A2AJSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Result.Status != "invalid" || rpc.Result.MutationAuthority {
		t.Fatalf("expected invalid intent-only response: %+v", rpc)
	}

	req = strings.NewReader(`{"jsonrpc":"2.0","id":"req-3","method":"mission.start","params":{"objective":"build mission gateway readback"}}`)
	resp, err = http.Post(server.URL+"/", "application/json", req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Result.Status != "intent_recorded" || rpc.Result.Method != "mission.start" || rpc.Result.MutationAuthority {
		t.Fatalf("expected valid intent-only response: %+v", rpc)
	}
}

func TestA2AInvalidJSONRPCExamples(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "invalid", "a2a-jsonrpc-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no invalid A2A examples")
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		task := A2ATaskForParams(req.Method, req.Params)
		if task.Status != "invalid" || task.MutationAuthority {
			t.Fatalf("%s expected invalid intent-only task, got %+v", path, task)
		}
	}
}

func TestContractSchemasDeclareRequiredProperties(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "docs", "contracts", "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no contract schemas found")
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(body, &schema); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing properties", path)
		}
		for _, item := range schema["required"].([]any) {
			field := item.(string)
			if _, ok := props[field]; !ok {
				t.Fatalf("%s missing property for required field %s", path, field)
			}
		}
	}
}

func TestGatewayRunbookDocumentsIntentOnlyReferences(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "gateway-readback-runbook.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"Hermes-style", "Telegram", "A2A", "intent/readback only", "mutation_authority=false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("runbook missing %q", want)
		}
	}
	if err := ValidatePublicSafeText(text); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramConfigReadback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telegram.json")
	body := `{"schema":"ao.mission.telegram-config.v0.1","token_env":"AO_MISSION_TELEGRAM_REDACTED","allowed_chats":{"1001":"admin","1002":"user"}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadTelegramConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	rb := TelegramConfigReadback(cfg)
	if rb.AllowedChatCount != 2 || rb.MutationAuthority {
		t.Fatalf("bad gateway readback: %+v", rb)
	}
}

func TestA2AHTTPHandler(t *testing.T) {
	server := httptest.NewServer(A2AHandler())
	defer server.Close()
	resp, err := http.Get(server.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var card A2AAgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	if card.MutationAuthority {
		t.Fatal("agent card must not grant mutation authority")
	}
}

func TestRouteDecisionHistoryPersistsAcrossNextAndImports(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "build atlas workgraph mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.RouteHistory) != 1 || rec.RouteHistory[0].Route != "ao-atlas" {
		t.Fatalf("start did not persist initial route history: %+v", rec.RouteHistory)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "next", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("next: %s", errb.String())
	}
	updated, err := NewStore(dir).Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RouteHistory) != 2 || updated.RouteHistory[1].Route != "ao-atlas" {
		t.Fatalf("next did not append route history: %+v", updated.RouteHistory)
	}
	workgraphPath := filepath.Join(dir, "workgraph.json")
	if err := os.WriteFile(workgraphPath, []byte(`{"schema":"ao.atlas.workgraph.v0.1","nodes":[{"status":"ready"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(NewStore(dir), rec.MissionID, "atlas-workgraph", workgraphPath); err != nil {
		t.Fatal(err)
	}
	updated, err = NewStore(dir).Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	last := updated.RouteHistory[len(updated.RouteHistory)-1]
	if last.Route != "ao-foundry" || last.ExactNextAction != "send first safe Atlas node to AO Foundry" {
		t.Fatalf("atlas import did not append Foundry route history: %+v", updated.RouteHistory)
	}
}

func TestTelegramReplayFixtureProducesIntentOnlyReadback(t *testing.T) {
	readback, err := ReplayTelegramCommandMatrix(
		filepath.Join("..", "..", "examples", "valid", "telegram-command-matrix.json"),
		map[string]string{"1001": "admin", "1002": "user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.telegram-replay-readback.v0.1" || readback.Status != "ready" {
		t.Fatalf("bad replay readback: %+v", readback)
	}
	if readback.Total != len(readback.Results) || readback.Denied != 2 || readback.Invalid != 0 {
		t.Fatalf("unexpected replay counts: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("telegram replay widened authority: %+v", readback)
	}
}

func TestA2AHTTPFixtureReplayProducesIntentOnlyReadback(t *testing.T) {
	readback, err := ReplayA2AHTTPFixture(filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.a2a-http-replay-readback.v0.1" || readback.Status != "ready" {
		t.Fatalf("bad A2A HTTP replay readback: %+v", readback)
	}
	if readback.Total != 3 || readback.Invalid != 1 {
		t.Fatalf("unexpected A2A replay counts: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("A2A replay widened authority: %+v", readback)
	}
	for _, result := range readback.Results {
		if result.RequestID == "" || result.ResponseID != result.RequestID {
			t.Fatalf("A2A HTTP replay did not bind request/response pair IDs: %+v", result)
		}
	}
}

func TestA2AHTTPFixtureReplayRecordsRequestResponsePairIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a2a-http-mismatch.json")
	if err := os.WriteFile(path, []byte(`{
  "schema": "ao.mission.a2a-http-integration-fixture.v0.1",
  "requests": [
    {
      "jsonrpc": "2.0",
      "id": "req-status",
      "method": "mission.status",
      "params": {"mission_id": "mission-demo"},
      "expected_status": "intent_recorded"
    }
  ],
  "mutation_authority": false
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	readback, err := ReplayA2AHTTPFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	if readback.Status != "ready" || len(readback.Results) != 1 || readback.Results[0].ResponseID != "req-status" {
		t.Fatalf("A2A HTTP replay should validate matching request/response IDs: %+v", readback)
	}
}

func TestGatewayIntentLedgerPersistsTelegramAndA2AReplayWithoutAuthority(t *testing.T) {
	telegram, err := ReplayTelegramUpdates(filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json"), map[string]string{"1001": "admin", "1002": "user"})
	if err != nil {
		t.Fatal(err)
	}
	a2a, err := ReplayA2AHTTPFixture(filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"))
	if err != nil {
		t.Fatal(err)
	}
	ledger := BuildGatewayIntentLedger("mission-demo", telegram, a2a)
	if ledger.Schema != "ao.mission.gateway-intent-ledger.v0.1" || ledger.Status != "ready" {
		t.Fatalf("bad ledger: %+v", ledger)
	}
	if ledger.Total != len(ledger.Intents) || ledger.IntentRecorded != 4 || ledger.Denied != 1 || ledger.Invalid != 2 {
		t.Fatalf("bad ledger counts: %+v", ledger)
	}
	if ledger.MutationAuthority || ledger.ExecutesWork || ledger.ApprovesWork {
		t.Fatalf("ledger widened authority: %+v", ledger)
	}
	for _, intent := range ledger.Intents {
		if intent.MissionID != "mission-demo" || intent.MutationAuthority || intent.ExecutesWork || intent.ApprovesWork {
			t.Fatalf("unsafe intent record: %+v", intent)
		}
	}
}

func TestGatewayLedgerCommandWritesReplayLedger(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "gateway-ledger.json")
	var out, errb bytes.Buffer
	code := Run([]string{
		"gateway", "ledger",
		"--mission", "mission-demo",
		"--telegram-updates", filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json"),
		"--telegram-config", filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		"--a2a-http", filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		"--out", outPath,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("gateway ledger failed: %s", errb.String())
	}
	if !strings.Contains(out.String(), "gateway_intent_ledger="+outPath) {
		t.Fatalf("missing ledger path output: %s", out.String())
	}
	var ledger GatewayIntentLedger
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &ledger); err != nil {
		t.Fatal(err)
	}
	if ledger.Total != 7 || ledger.MutationAuthority || ledger.ExecutesWork || ledger.ApprovesWork {
		t.Fatalf("bad written ledger: %+v", ledger)
	}
}

func TestA2ATaskLifecycleFixtureRecordsCancellationAsIntentOnly(t *testing.T) {
	readback, err := ReplayA2ATaskLifecycle(filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.a2a-task-lifecycle-readback.v0.1" || readback.Status != "ready" {
		t.Fatalf("bad lifecycle readback: %+v", readback)
	}
	if readback.Total != 3 || readback.Cancelled != 1 || readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("lifecycle widened authority or bad counts: %+v", readback)
	}
}

func TestA2ATaskLifecycleFixtureRecordsResumeAndCancelEdges(t *testing.T) {
	readback, err := ReplayA2ATaskLifecycle(filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle-edges.json"))
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.a2a-task-lifecycle-readback.v0.1" || readback.Status != "ready" {
		t.Fatalf("bad lifecycle edge readback: %+v", readback)
	}
	if readback.Total != 5 || readback.CancelRequested != 1 || readback.Cancelled != 1 || readback.ResumeRequested != 1 || readback.Resumed != 1 {
		t.Fatalf("bad lifecycle edge counts: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("lifecycle edge replay widened authority: %+v", readback)
	}
}

func TestSchedulerReadbackImportClassifiesFreshness(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("schedule long-running workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scheduler-readback.json")
	body := `{"schema":"ao.mission.scheduler-readback.v0.1","mission_id":"` + rec.MissionID + `","status":"ready","scheduler":"codex-cron","event_loop":true,"reason":"fixture wakeup only","generated_at_utc":"2026-07-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "scheduler-readback", path); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Evidence.SchedulerReadback == nil || updated.Evidence.SchedulerReadback.FreshnessStatus != "stale" {
		t.Fatalf("scheduler freshness was not classified as stale: %+v", updated.Evidence.SchedulerReadback)
	}
	snap := Snapshot(updated)
	if snap.EvidenceFreshnessStatus != "stale" {
		t.Fatalf("snapshot freshness=%s", snap.EvidenceFreshnessStatus)
	}
}

func TestSchedulerRecoveryReadbackImportRecordsMissedWakeupEvidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("schedule long-running workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "examples", "valid", "scheduler-recovery-readback.json")
	if _, err := ImportArtifact(s, rec.MissionID, "scheduler-recovery-readback", path); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Evidence.SchedulerRecovery == nil || updated.Evidence.SchedulerRecovery.MissedWakeups != 2 {
		t.Fatalf("scheduler recovery evidence missing: %+v", updated.Evidence)
	}
	if updated.CurrentPhase != "scheduler_recovery_recorded" || !strings.Contains(updated.ExactNextAction, "continue") {
		t.Fatalf("scheduler recovery did not set continuation action: %+v", updated)
	}
}

func TestMissionEvidenceImportRejectsAuthorityDrift(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("reject evidence authority drift")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		kind string
		body string
		want string
	}{
		{
			name: "scheduler recovery safe to execute",
			kind: "scheduler-recovery-readback",
			body: `{"schema":"ao.mission.scheduler-recovery-readback.v0.1","mission_id":"mission-demo","safe_to_execute":true,"executes_work":false}`,
			want: "safe_to_execute",
		},
		{
			name: "scheduler recovery schedules work",
			kind: "scheduler-recovery-readback",
			body: `{"schema":"ao.mission.scheduler-recovery-readback.v0.1","mission_id":"mission-demo","schedules_work":true,"executes_work":false}`,
			want: "schedules_work",
		},
		{
			name: "ledger compaction repository mutation",
			kind: "ledger-compaction-readback",
			body: `{"schema":"ao.mission.ledger-compaction-readback.v0.1","mission_id":"mission-demo","mutates_repositories":true,"executes_work":false}`,
			want: "mutates_repositories",
		},
		{
			name: "scheduler readback provider call",
			kind: "scheduler-readback",
			body: `{"schema":"ao.mission.scheduler-readback.v0.1","mission_id":"mission-demo","provider_calls":true,"executes_work":false}`,
			want: "provider_calls",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportArtifact(s, rec.MissionID, tc.kind, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestLegacyMissionEvidenceImportRejectsDuplicateJSONWithoutMutation(t *testing.T) {
	tests := map[string]string{
		"root contract":    `{"schema":"ao.mission.scheduler-readback.v0.1","schema":"ao.mission.scheduler-readback.v0.1","mission_id":"%[1]s","status":"ready","scheduler":"codex-cron","event_loop":true}`,
		"nested identity":  `{"schema":"ao.mission.scheduler-readback.v0.1","mission_id":"%[1]s","status":"ready","scheduler":"codex-cron","event_loop":true,"provenance":{"request_id":"first","request_id":"second"}}`,
		"mission identity": `{"schema":"ao.mission.scheduler-readback.v0.1","mission_id":"%[1]s","mission_id":"%[1]s","status":"ready","scheduler":"codex-cron","event_loop":true}`,
		"authority":        `{"schema":"ao.mission.scheduler-readback.v0.1","mission_id":"%[1]s","status":"ready","scheduler":"codex-cron","event_loop":true,"executes_work":true,"executes_work":false}`,
	}
	for name, bodyTemplate := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewStore(dir)
			record, err := store.Start("reject duplicate legacy import JSON")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveCheckpointBundle(BuildCheckpointBundle(record)); err != nil {
				t.Fatal(err)
			}
			paths := store.transactionPaths(record.MissionID)
			beforeRecord := readFileForTransactionTest(t, paths.Record)
			beforeCheckpoint := readFileForTransactionTest(t, paths.Checkpoint)
			body := fmt.Sprintf(bodyTemplate, record.MissionID)
			path := filepath.Join(dir, "duplicate.json")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			if _, err := ImportArtifact(store, record.MissionID, "scheduler-readback", path); err == nil ||
				!strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("duplicate JSON import was accepted: %v", err)
			}
			if got := readFileForTransactionTest(t, paths.Record); !bytes.Equal(got, beforeRecord) {
				t.Fatal("duplicate JSON import changed Mission bytes")
			}
			if got := readFileForTransactionTest(t, paths.Checkpoint); !bytes.Equal(got, beforeCheckpoint) {
				t.Fatal("duplicate JSON import changed checkpoint bytes")
			}
		})
	}
}

func TestArtifactManifestIncludesImportedRecoveryAndCompactionReadbacks(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("manifest recovery and compaction evidence")
	if err != nil {
		t.Fatal(err)
	}
	recoveryPath := filepath.Join("..", "..", "examples", "valid", "scheduler-recovery-readback.json")
	compactionPath := filepath.Join("..", "..", "examples", "valid", "ledger-compaction-readback.json")
	if _, err := ImportArtifact(s, rec.MissionID, "scheduler-recovery-readback", recoveryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, rec.MissionID, "ledger-compaction-readback", compactionPath); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := BuildArtifactManifest(updated)
	kinds := map[string]bool{}
	for _, ref := range manifest.ArtifactRefs {
		kinds[ref.Kind] = true
	}
	if !kinds["scheduler-recovery-readback"] || !kinds["ledger-compaction-readback"] {
		t.Fatalf("manifest missing recovery or compaction refs: %+v", manifest.ArtifactRefs)
	}
	if manifest.ExecutesWork || manifest.ApprovesWork || manifest.SafeToExecute {
		t.Fatalf("manifest widened authority: %+v", manifest)
	}
}

func TestOperatorNextActionsDocsAreConcreteAndPublicSafe(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "operator-next-actions.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"ao-mission start",
		"ao-mission next --mission",
		"ao-mission continue --mission",
		"ao-mission artifacts manifest --mission",
		"Move to AO Atlas",
		"Move to AO Foundry",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("operator docs missing %q", want)
		}
	}
	if err := ValidatePublicSafeText(text); err != nil {
		t.Fatal(err)
	}

	readmeBody, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ao-mission-0.1.6-macos-aarch64.tar.gz",
		"ao-mission init",
		"AO_MISSION_HOME=\"$tmp_home/state\" ao-mission doctor --json",
		"$env:AO_MISSION_HOME = $missionHome",
		"ao-mission.exe doctor --json",
		"gh release view v0.1.6 --repo uesugitorachiyo/ao-mission --json assets",
		"Go 1.26.4 or later",
	} {
		if !strings.Contains(string(readmeBody), want) {
			t.Fatalf("README missing current-user onboarding detail %q", want)
		}
	}
	for _, want := range []string{
		"AO2 Control Plane [v0.1.19]",
		"AO Mission [v0.1.6]",
		"AO Command [v0.1.3]",
		"AO Forge [v0.1.5]",
		"AO Atlas [v0.2.1]",
		"[System.Text.UTF8Encoding]::new($false)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("operator docs missing current release %q", want)
		}
	}

	runbookPath := filepath.Join("..", "..", "docs", "long-run-operator-runbook.md")
	runbookBody, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatal(err)
	}
	runbook := string(runbookBody)
	for _, want := range []string{
		"targets roughly 120 minutes and hard-stops at 180 minutes",
		"6-10",
		"--min-minutes 0",
		"Never wait or",
		"fresh Atlas workgraph",
		"It does not execute repository work",
		"Use one continuation cycle per real node",
		"repeat count must never multiply implicitly",
		"AO Mission Self-Change",
		"Final response remains denied",
	} {
		if !strings.Contains(runbook, want) {
			t.Fatalf("long-run operator runbook missing %q", want)
		}
	}
	if err := ValidatePublicSafeText(runbook); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactManifestCommandWritesOutFile(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "mission artifact manifest output"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	out.Reset()
	if code := Run([]string{"--home", dir, "artifacts", "manifest", "--mission", rec.MissionID, "--out", manifestPath}, &out, &errb); code != 0 {
		t.Fatalf("artifact manifest --out: %s", errb.String())
	}
	if !strings.Contains(out.String(), "artifact_manifest="+manifestPath) {
		t.Fatalf("expected output path summary, got %s", out.String())
	}
	var manifest ArtifactManifest
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.MissionID != rec.MissionID || manifest.ManifestDigest == "" || manifest.ExecutesWork || manifest.ApprovesWork {
		t.Fatalf("bad written manifest: %+v", manifest)
	}
}

func TestArtifactManifestOutMaterializesLegacyReferenceWhenBytesMatch(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("materialize a legacy artifact reference")
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "legacy-artifact.json")
	body := []byte(`{"schema":"ao.mission.route-decision.v0.1"}`)
	if err := os.WriteFile(sourcePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.ArtifactRefs = append(candidate.ArtifactRefs, ArtifactRef{
			Schema: ArtifactRefSchema,
			Ref:    sourcePath,
			Digest: digestBytes(body),
			Kind:   "route_readback",
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "artifact-manifest.json")
	manifest, err := MaterializeArtifactManifest(updated, manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "ao.mission.artifact-manifest.v0.2" || len(manifest.ArtifactRefs) != 1 || manifest.ArtifactRefs[0].ContentRef == "" {
		t.Fatalf("legacy reference did not materialize as v0.2: %+v", manifest)
	}
}

func TestArtifactManifestOutputStagesContentBeforeReplacingManifest(t *testing.T) {
	previous := beforeArtifactManifestStageWrite
	defer func() { beforeArtifactManifestStageWrite = previous }()
	beforeArtifactManifestStageWrite = func(string) error {
		return errors.New("injected staged object write failure")
	}

	store := NewStore(t.TempDir())
	record, err := store.Start("stage artifact manifest content before publication")
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "artifact.json")
	body := []byte(`{"schema":"ao.mission.route-decision.v0.1"}`)
	if err := os.WriteFile(sourcePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.ArtifactRefs = []ArtifactRef{{Schema: ArtifactRefSchema, Ref: sourcePath, Digest: digestBytes(body)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "artifact-manifest.json")
	previousManifest := []byte("previous manifest bytes\n")
	if err := os.WriteFile(outPath, previousManifest, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", store.Root, "artifacts", "manifest", "--mission", record.MissionID, "--out", outPath}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "staged object write failure") {
		t.Fatalf("staged content failure unexpectedly published: code=%d stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, previousManifest) {
		t.Fatalf("staged content failure replaced manifest: got=%q want=%q", got, previousManifest)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(outPath), retainedArtifactDirectory)); !os.IsNotExist(err) {
		t.Fatalf("staged content failure published a bundle object: %v", err)
	}
}

func TestArtifactManifestFinalPublishFailurePreservesExistingOutput(t *testing.T) {
	previous := beforeArtifactManifestPublish
	defer func() { beforeArtifactManifestPublish = previous }()
	beforeArtifactManifestPublish = func(string) error {
		return errors.New("injected final manifest publish failure")
	}

	store := NewStore(t.TempDir())
	record, err := store.Start("atomically publish artifact manifest")
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "artifact.json")
	body := []byte(`{"schema":"ao.mission.route-decision.v0.1"}`)
	if err := os.WriteFile(sourcePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(record.MissionID, func(candidate *Record) error {
		candidate.ArtifactRefs = []ArtifactRef{{Schema: ArtifactRefSchema, Ref: sourcePath, Digest: digestBytes(body)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "artifact-manifest.json")
	previousManifest := []byte("previous manifest bytes\n")
	if err := os.WriteFile(outPath, previousManifest, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", store.Root, "artifacts", "manifest", "--mission", record.MissionID, "--out", outPath}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "final manifest publish failure") {
		t.Fatalf("final publication failure unexpectedly succeeded: code=%d stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, previousManifest) {
		t.Fatalf("final publication failure replaced manifest: got=%q want=%q", got, previousManifest)
	}
}

func TestArtifactManifestRepairFinalPublishFailurePreservesExistingOutput(t *testing.T) {
	previous := beforeArtifactManifestPublish
	defer func() { beforeArtifactManifestPublish = previous }()
	beforeArtifactManifestPublish = func(string) error {
		return errors.New("injected final manifest publish failure")
	}

	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "artifact.json")
	body := []byte(`{"schema":"ao.mission.route-decision.v0.1"}`)
	if err := os.WriteFile(sourcePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	input := FinalizeArtifactManifest(ArtifactManifest{MissionID: "mission-demo", ArtifactRefs: []ArtifactRef{{Schema: ArtifactRefSchema, Ref: sourcePath, Digest: digestBytes(body)}}})
	inputPath := filepath.Join(dir, "input.json")
	inputBody, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, inputBody, 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.json")
	previousManifest := []byte("previous manifest bytes\n")
	if err := os.WriteFile(outPath, previousManifest, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"artifacts", "repair-manifest", "--path", inputPath, "--out", outPath}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "final manifest publish failure") {
		t.Fatalf("repair publication failure unexpectedly succeeded: code=%d stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, previousManifest) {
		t.Fatalf("repair publication failure replaced manifest: got=%q want=%q", got, previousManifest)
	}
}

func TestArtifactManifestRepairCommandRejectsChangedHistoricalDigest(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "route.json")
	if err := os.WriteFile(artifactPath, []byte(`{"schema":"ao.mission.route-decision.v0.1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := FinalizeArtifactManifest(ArtifactManifest{
		MissionID: "mission-demo",
		ArtifactRefs: []ArtifactRef{{
			Schema: ArtifactRefSchema,
			Ref:    artifactPath,
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Kind:   "route_readback",
		}},
	})
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	repairedPath := filepath.Join(dir, "artifact-manifest.repaired.json")
	var out, errb bytes.Buffer
	if code := Run([]string{"artifacts", "repair-manifest", "--path", manifestPath, "--out", repairedPath}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "artifact digest mismatch") {
		t.Fatalf("repair-manifest recomputed a historical digest: code=%d stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if _, err := os.Stat(repairedPath); !os.IsNotExist(err) {
		t.Fatalf("failed repair wrote output: %v", err)
	}
}

func TestArtifactManifestRepairCommandAddsRetainedContentWithoutChangingDigest(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "route.json")
	body := []byte(`{"schema":"ao.mission.route-decision.v0.1"}`)
	if err := os.WriteFile(artifactPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := digestBytes(body)
	manifest := FinalizeArtifactManifest(ArtifactManifest{
		MissionID: "mission-demo",
		ArtifactRefs: []ArtifactRef{{
			Schema: ArtifactRefSchema,
			Ref:    artifactPath,
			Digest: digest,
			Kind:   "route_readback",
		}},
	})
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(manifestBody, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	repairedPath := filepath.Join(dir, "repaired", "artifact-manifest.json")
	var out, errb bytes.Buffer
	if code := Run([]string{"artifacts", "repair-manifest", "--path", manifestPath, "--out", repairedPath}, &out, &errb); code != 0 {
		t.Fatalf("repair-manifest: %s", errb.String())
	}
	var repaired ArtifactManifest
	repairedBody, err := os.ReadFile(repairedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(repairedBody, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Schema != "ao.mission.artifact-manifest.v0.2" || repaired.ArtifactRefs[0].Digest != digest || filepath.IsAbs(repaired.ArtifactRefs[0].ContentRef) {
		t.Fatalf("repair changed declared digest or did not retain contained content: %+v", repaired)
	}
	if result, err := ValidateArtifactManifestFile(repairedPath); err != nil || result.Status != "passed" {
		t.Fatalf("repaired manifest did not validate: result=%+v err=%v", result, err)
	}
}

func TestMissionHistoryCommandExportsRouteHistory(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "history atlas workgraph mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "next", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("next: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "history", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission history: %s", errb.String())
	}
	var history []RouteDecision
	if err := json.Unmarshal(out.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Route != "ao-atlas" || history[1].SafeToExecute {
		t.Fatalf("bad route history: %+v", history)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "history", "--mission", rec.MissionID}, &out, &errb); code != 0 {
		t.Fatalf("mission history text: %s", errb.String())
	}
	if !strings.Contains(out.String(), "route=ao-atlas") || strings.Contains(out.String(), "safe_to_execute=true") {
		t.Fatalf("bad history text: %s", out.String())
	}
}

func TestSchedulerReplayFixtureClassifiesFreshness(t *testing.T) {
	readback, err := ReplaySchedulerReadbacks(filepath.Join("..", "..", "examples", "valid", "scheduler-readback-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.scheduler-replay-readback.v0.1" || readback.Status != "ready" {
		t.Fatalf("bad scheduler replay readback: %+v", readback)
	}
	if readback.Total != 3 || readback.Fresh != 1 || readback.Stale != 1 || readback.Unknown != 1 {
		t.Fatalf("bad scheduler freshness counts: %+v", readback)
	}
	if readback.EvaluatedAtUTC != "2026-07-03T12:00:00Z" {
		t.Fatalf("scheduler replay did not preserve fixture evaluation time: %+v", readback)
	}
	if readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("scheduler replay widened authority: %+v", readback)
	}
}

func TestSchedulerRecoveryRecommendsImmediateContinuationForMissedWakeups(t *testing.T) {
	replay, err := ReplaySchedulerReadbacks(filepath.Join("..", "..", "examples", "valid", "scheduler-readback-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	recovery := BuildSchedulerRecoveryReadback("mission-demo", replay)
	if recovery.Schema != "ao.mission.scheduler-recovery-readback.v0.1" || recovery.Status != "attention_required" {
		t.Fatalf("bad scheduler recovery: %+v", recovery)
	}
	if recovery.MissedWakeups != 2 || recovery.RecoveryMode != "immediate_continue_recommended" {
		t.Fatalf("bad scheduler recovery counts: %+v", recovery)
	}
	if !strings.Contains(recovery.ExactNextAction, "ao-mission continue --mission mission-demo --until-done") {
		t.Fatalf("bad scheduler recovery next action: %+v", recovery)
	}
	if recovery.ExecutesWork || recovery.ApprovesWork {
		t.Fatalf("scheduler recovery widened authority: %+v", recovery)
	}
}

func TestSchedulerAlertSummaryHighlightsStaleReadbacks(t *testing.T) {
	replay, err := ReplaySchedulerReadbacks(filepath.Join("..", "..", "examples", "valid", "scheduler-readback-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	alerts := BuildSchedulerAlertSummary(replay)
	if alerts.Schema != "ao.mission.scheduler-alert-summary.v0.1" || alerts.Status != "attention_required" {
		t.Fatalf("bad scheduler alert summary: %+v", alerts)
	}
	if alerts.Stale != 1 || alerts.Unknown != 1 || len(alerts.Alerts) != 2 {
		t.Fatalf("bad scheduler alerts: %+v", alerts)
	}
	if alerts.ExecutesWork || alerts.ApprovesWork {
		t.Fatalf("scheduler alerts widened authority: %+v", alerts)
	}
}

func TestMissionLedgerCompactionTrimsHistoryAndRecordsEvidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build a long-running atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	rec, err = s.Update(rec.MissionID, func(r *Record) error {
		for i := 0; i < 5; i++ {
			r.Steps = append(r.Steps, ContinuationStep{
				Schema:          StepSchema,
				MissionID:       r.MissionID,
				Iteration:       i + 1,
				Route:           "ao-atlas",
				Result:          "handoff_required",
				ExactNextAction: "continue through Atlas",
				GeneratedAtUTC:  "2026-07-03T00:00:00Z",
			})
			AppendRouteHistory(r, RouteDecision{
				Schema:          RouteSchema,
				MissionID:       r.MissionID,
				Route:           "ao-atlas",
				Reason:          "test route",
				SafeToRequest:   true,
				SafeToExecute:   false,
				SafeToPromote:   false,
				ExactNextAction: "continue through Atlas",
				GeneratedAtUTC:  "2026-07-03T00:00:00Z",
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	readback, err := CompactMissionLedger(s, rec.MissionID, LedgerCompactionOptions{KeepRouteHistory: 2, KeepSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.ledger-compaction-readback.v0.1" || readback.Status != "compacted" {
		t.Fatalf("bad compaction readback: %+v", readback)
	}
	if readback.RouteHistoryBefore <= readback.RouteHistoryAfter || readback.RouteHistoryAfter != 2 || readback.StepsAfter != 3 {
		t.Fatalf("bad compaction counts: %+v", readback)
	}
	if readback.ExecutesWork || readback.ApprovesWork || readback.MutatesRepositories {
		t.Fatalf("compaction widened authority: %+v", readback)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RouteHistory) != 2 || len(updated.Steps) != 3 {
		t.Fatalf("record was not compacted: route_history=%d steps=%d", len(updated.RouteHistory), len(updated.Steps))
	}
	if updated.Evidence.LedgerCompaction == nil || updated.Evidence.LedgerCompaction.RouteHistoryAfter != 2 {
		t.Fatalf("compaction evidence missing: %+v", updated.Evidence)
	}
}

func TestMissionTimelineCompactionEmitsDigestBoundReadback(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "timeline compaction"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	_, err := s.Update(rec.MissionID, func(r *Record) error {
		for i := 0; i < 4; i++ {
			AppendRouteHistory(r, RouteDecision{Schema: RouteSchema, MissionID: r.MissionID, Route: "ao-atlas", SafeToRequest: true, SafeToExecute: false, SafeToPromote: false, GeneratedAtUTC: "2026-07-03T00:00:00Z"})
			r.Steps = append(r.Steps, ContinuationStep{Schema: StepSchema, MissionID: r.MissionID, Iteration: i + 1, Route: "ao-atlas", Result: "recorded", ExactNextAction: "continue", GeneratedAtUTC: "2026-07-03T00:00:00Z"})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "compact", "--mission", rec.MissionID, "--keep-route-history", "2", "--keep-steps", "2", "--timeline"}, &out, &errb); code != 0 {
		t.Fatalf("mission compact timeline: %s", errb.String())
	}
	var readback map[string]any
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.timeline-compaction-readback.v0.1" || readback["status"] != "compacted" {
		t.Fatalf("bad timeline compaction readback: %#v", readback)
	}
	if digest, _ := readback["timeline_digest"].(string); !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("timeline compaction missing digest: %#v", readback)
	}
	if readback["executes_work"] != false || readback["approves_work"] != false || readback["mutates_repositories"] != false {
		t.Fatalf("timeline compaction widened authority: %#v", readback)
	}
}

func TestMissionTimelineCompactionRoundTripsForCorrelatedMission(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	contract, err := s.StartObjective(
		"round-trip correlated compaction evidence",
		ObjectiveStartOptions{CorrelationID: "corr-compaction-roundtrip"},
	)
	if err != nil {
		t.Fatal(err)
	}
	readback, err := CompactMissionTimeline(s, contract.MissionID, LedgerCompactionOptions{
		KeepRouteHistory: 1,
		KeepSteps:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if readback.CorrelationID != contract.CorrelationID {
		t.Fatalf("compaction correlation_id = %q, want %q", readback.CorrelationID, contract.CorrelationID)
	}
	body, err := json.Marshal(readback)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "timeline-compaction-readback.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportArtifact(s, contract.MissionID, "ledger-compaction-readback", path); err != nil {
		t.Fatalf("import emitted compaction readback: %v", err)
	}
}

func TestMissionCompactCLIEmitsReadback(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build a long-running atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(rec.MissionID, func(r *Record) error {
		for i := 0; i < 4; i++ {
			r.Steps = append(r.Steps, ContinuationStep{Schema: StepSchema, MissionID: r.MissionID, Iteration: i + 1, Route: "ao-atlas", Result: "handoff_required", GeneratedAtUTC: "2026-07-03T00:00:00Z"})
			AppendRouteHistory(r, RouteDecision{Schema: RouteSchema, MissionID: r.MissionID, Route: "ao-atlas", SafeToRequest: true, SafeToExecute: false, SafeToPromote: false, GeneratedAtUTC: "2026-07-03T00:00:00Z"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "mission", "compact", "--mission", rec.MissionID, "--keep-route-history", "2", "--keep-steps", "2"}, &out, &errb); code != 0 {
		t.Fatalf("mission compact: %s", errb.String())
	}
	var readback LedgerCompactionReadback
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.ledger-compaction-readback.v0.1" || readback.RouteHistoryAfter != 2 || readback.StepsAfter != 2 {
		t.Fatalf("bad compact CLI readback: %+v", readback)
	}
}

func TestMissionCompactionPreservesConcreteExactNextAction(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("preserve the next ready Atlas node through compaction")
	if err != nil {
		t.Fatal(err)
	}
	const nextAction = "send first safe Atlas node to AO Foundry"
	if _, err := s.Update(rec.MissionID, func(r *Record) error {
		r.ExactNextAction = nextAction
		for i := 0; i < 4; i++ {
			r.Steps = append(r.Steps, ContinuationStep{Schema: StepSchema, MissionID: r.MissionID, Iteration: i + 1, Route: "ao-foundry", Result: "handoff_required", ExactNextAction: nextAction, GeneratedAtUTC: "2026-08-01T00:00:00Z"})
			AppendRouteHistory(r, RouteDecision{Schema: RouteSchema, MissionID: r.MissionID, Route: "ao-foundry", SafeToRequest: true, SafeToExecute: false, SafeToPromote: false, ExactNextAction: nextAction, GeneratedAtUTC: "2026-08-01T00:00:00Z"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	readback, err := CompactMissionLedger(s, rec.MissionID, LedgerCompactionOptions{KeepRouteHistory: 2, KeepSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	if readback.ExactNextAction != nextAction {
		t.Fatalf("compaction readback lost exact next action: got %q want %q", readback.ExactNextAction, nextAction)
	}
	compacted, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if compacted.ExactNextAction != nextAction {
		t.Fatalf("compacted mission lost exact next action: got %q want %q", compacted.ExactNextAction, nextAction)
	}
}

func TestMissionCompactDryRunDoesNotMutateRecord(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("build a long-running atlas workgraph mission")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(rec.MissionID, func(r *Record) error {
		for i := 0; i < 4; i++ {
			r.Steps = append(r.Steps, ContinuationStep{Schema: StepSchema, MissionID: r.MissionID, Iteration: i + 1, Route: "ao-atlas", Result: "handoff_required", GeneratedAtUTC: "2026-07-03T00:00:00Z"})
			AppendRouteHistory(r, RouteDecision{Schema: RouteSchema, MissionID: r.MissionID, Route: "ao-atlas", SafeToRequest: true, SafeToExecute: false, SafeToPromote: false, GeneratedAtUTC: "2026-07-03T00:00:00Z"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	readback, err := CompactMissionLedger(s, rec.MissionID, LedgerCompactionOptions{KeepRouteHistory: 2, KeepSteps: 2, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Status != "dry_run" || readback.RouteHistoryAfter != 2 || readback.StepsAfter != 2 {
		t.Fatalf("bad dry-run compaction readback: %+v", readback)
	}
	updated, err := s.Load(rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RouteHistory) != 5 || len(updated.Steps) != 4 || updated.Evidence.LedgerCompaction != nil {
		t.Fatalf("dry-run compaction mutated record: route_history=%d steps=%d evidence=%+v", len(updated.RouteHistory), len(updated.Steps), updated.Evidence)
	}
}

func TestScheduleRecoverCLIEmitsImmediateContinuationReadback(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"schedule", "recover", "--mission", "mission-demo", "--fixture", filepath.Join("..", "..", "examples", "valid", "scheduler-readback-replay.json")}, &out, &errb); code != 0 {
		t.Fatalf("schedule recover: %s", errb.String())
	}
	var readback SchedulerRecoveryReadback
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.scheduler-recovery-readback.v0.1" || readback.RecoveryMode != "immediate_continue_recommended" {
		t.Fatalf("bad schedule recovery CLI readback: %+v", readback)
	}
	if readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("schedule recovery widened authority: %+v", readback)
	}
}

func TestQualificationOrchestrationRequiresAffectedShardsBeforeFullExactHead(t *testing.T) {
	var out, errb bytes.Buffer
	fixture := filepath.Join("..", "..", "examples", "valid", "stack-qualification-orchestration.json")
	if code := Run([]string{"qualification", "orchestrate", "--fixture", fixture, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("qualification orchestrate: %s", errb.String())
	}
	var readback map[string]any
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.qualification-orchestration-readback.v0.1" ||
		readback["status"] != "ready" ||
		readback["affected_shard_count"] != float64(4) ||
		readback["final_qualification_mode"] != "full_exact_head" ||
		readback["source_head_count"] != float64(4) ||
		readback["exact_head_required"] != true ||
		readback["restart_from_zero_allowed"] != false {
		t.Fatalf("unexpected qualification orchestration readback: %#v", readback)
	}
	for _, flag := range []string{"safe_to_execute", "executes_work", "approves_work", "mutates_repositories", "calls_providers", "releases_or_deploys"} {
		if readback[flag] != false {
			t.Fatalf("qualification orchestration widened %s: %#v", flag, readback)
		}
	}
	if !strings.Contains(readback["exact_next_action"].(string), "run affected Windows qualification shards") ||
		!strings.Contains(readback["exact_next_action"].(string), "final full exact-head qualification") {
		t.Fatalf("qualification orchestration lost exact next action: %#v", readback)
	}
}

func TestQualificationOrchestrationRejectsRestartFromZero(t *testing.T) {
	fixture := filepath.Join("..", "..", "examples", "valid", "stack-qualification-orchestration.json")
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	unsafeBody := strings.Replace(string(body), `"no_restart_from_zero": true`, `"no_restart_from_zero": false`, 1)
	unsafePath := filepath.Join(t.TempDir(), "restart-from-zero.json")
	if err := os.WriteFile(unsafePath, []byte(unsafeBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"qualification", "orchestrate", "--fixture", unsafePath, "--json"}, &out, &errb); code == 0 {
		t.Fatalf("unsafe qualification orchestration unexpectedly passed: %s", out.String())
	}
	if !strings.Contains(errb.String(), "forbid restart from zero") {
		t.Fatalf("unsafe qualification orchestration stderr missing restart denial:\n%s", errb.String())
	}
}

func TestArtifactManifestSelfValidationRejectsTampering(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "route.json")
	if err := os.WriteFile(artifactPath, []byte(`{"schema":"ao.mission.route-decision.v0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := digestBytesForTest(t, artifactPath)
	manifest := ArtifactManifest{
		Schema:    "ao.mission.artifact-manifest.v0.1",
		MissionID: "mission-demo",
		ArtifactRefs: []ArtifactRef{{
			Schema: "ao.mission.artifact-ref.v0.1",
			Ref:    artifactPath,
			Digest: digest,
			Kind:   "route_readback",
		}},
		SafeToExecute: false,
		ExecutesWork:  false,
		ApprovesWork:  false,
	}
	manifest = FinalizeArtifactManifest(manifest)
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateArtifactManifestFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || result.ManifestDigest != manifest.ManifestDigest || result.ArtifactCount != 1 {
		t.Fatalf("bad manifest validation: %+v", result)
	}

	if err := os.WriteFile(artifactPath, []byte(`{"schema":"tampered"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = ValidateArtifactManifestFile(manifestPath)
	if err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "artifact digest mismatch") {
		t.Fatalf("expected tamper failure, result=%+v err=%v", result, err)
	}
}

func TestArtifactManifestV02RejectsTamperedRetainedContent(t *testing.T) {
	dir := t.TempDir()
	digest := digestBytes([]byte("original retained content"))
	contentRef := filepath.ToSlash(filepath.Join(retainedArtifactDirectory, strings.TrimPrefix(digest, "sha256:")))
	contentPath := filepath.Join(dir, contentRef)
	if err := os.MkdirAll(filepath.Dir(contentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contentPath, []byte("original retained content"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := FinalizeArtifactManifest(ArtifactManifest{
		Schema:    "ao.mission.artifact-manifest.v0.2",
		MissionID: "mission-demo",
		ArtifactRefs: []ArtifactRef{{
			Schema:     ArtifactRefSchema,
			Ref:        "/provenance/source.json",
			ContentRef: contentRef,
			Digest:     digest,
			Kind:       "route_readback",
		}},
	})
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(manifestBody, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := ValidateArtifactManifestFile(manifestPath); err != nil || result.Status != "passed" {
		t.Fatalf("v0.2 manifest did not validate: result=%+v err=%v", result, err)
	}
	if err := os.WriteFile(contentPath, []byte("tampered retained content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := ValidateArtifactManifestFile(manifestPath); err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "artifact digest mismatch") {
		t.Fatalf("tampered retained content was accepted: result=%+v err=%v", result, err)
	}
}

func TestArtifactManifestV02RejectsSymlinkedContentDirectories(t *testing.T) {
	body := []byte("retained content")
	digest := digestBytes(body)
	contentRef := artifactManifestContentRef(digest)
	for name, prepare := range map[string]func(t *testing.T, dir string){
		"artifacts symlink": func(t *testing.T, dir string) {
			t.Helper()
			external := t.TempDir()
			externalPath := filepath.Join(external, contentRef)
			if err := os.MkdirAll(filepath.Dir(externalPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(externalPath, body, 0o644); err != nil {
				t.Fatal(err)
			}
			createTestSymlink(t, filepath.Join(external, "artifacts"), filepath.Join(dir, "artifacts"))
		},
		"sha256 symlink": func(t *testing.T, dir string) {
			t.Helper()
			external := t.TempDir()
			externalPath := filepath.Join(external, "sha256", strings.TrimPrefix(digest, "sha256:"))
			if err := os.MkdirAll(filepath.Dir(externalPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(externalPath, body, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "artifacts"), 0o755); err != nil {
				t.Fatal(err)
			}
			createTestSymlink(t, filepath.Join(external, "sha256"), filepath.Join(dir, retainedArtifactDirectory))
		},
		"artifacts regular file": func(t *testing.T, dir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "artifacts"), []byte("not a directory"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"sha256 regular file": func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(dir, "artifacts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, retainedArtifactDirectory), []byte("not a directory"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			prepare(t, dir)
			manifest := FinalizeArtifactManifest(ArtifactManifest{
				Schema:    "ao.mission.artifact-manifest.v0.2",
				MissionID: "mission-demo",
				ArtifactRefs: []ArtifactRef{{
					Schema: ArtifactRefSchema, Ref: "source.json", ContentRef: contentRef, Digest: digest,
				}},
			})
			manifestPath := filepath.Join(dir, "artifact-manifest.json")
			manifestBody, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, manifestBody, 0o644); err != nil {
				t.Fatal(err)
			}
			if result, err := ValidateArtifactManifestFile(manifestPath); err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "retained artifact") {
				t.Fatalf("unsafe artifact component was accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestArtifactManifestMaterializationRejectsSymlinkedContentDirectory(t *testing.T) {
	dir := t.TempDir()
	createTestSymlink(t, t.TempDir(), filepath.Join(dir, "artifacts"))
	sourcePath := filepath.Join(t.TempDir(), "artifact.json")
	body := []byte(`{"schema":"ao.mission.route-decision.v0.1"}`)
	if err := os.WriteFile(sourcePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := MaterializeArtifactManifest(Record{MissionID: "mission-demo", ArtifactRefs: []ArtifactRef{{Schema: ArtifactRefSchema, Ref: sourcePath, Digest: digestBytes(body)}}}, filepath.Join(dir, "artifact-manifest.json"))
	if err == nil || !strings.Contains(err.Error(), "retained artifact directory") {
		t.Fatalf("materialization accepted symlinked artifact directory: %v", err)
	}
}

func TestArtifactManifestV02ValidationRequiresStrictStructureAndSignature(t *testing.T) {
	manifest := FinalizeArtifactManifest(ArtifactManifest{Schema: "ao.mission.artifact-manifest.v0.2", MissionID: "", ArtifactRefs: []ArtifactRef{}})
	baseBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(baseBody, &base); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing schema":          func(doc map[string]any) { delete(doc, "schema") },
		"missing mission id":      func(doc map[string]any) { delete(doc, "mission_id") },
		"missing artifact refs":   func(doc map[string]any) { delete(doc, "artifact_refs") },
		"missing manifest digest": func(doc map[string]any) { delete(doc, "manifest_digest") },
		"missing signature":       func(doc map[string]any) { delete(doc, "signature") },
		"missing safe flag":       func(doc map[string]any) { delete(doc, "safe_to_execute") },
		"missing executes flag":   func(doc map[string]any) { delete(doc, "executes_work") },
		"missing approves flag":   func(doc map[string]any) { delete(doc, "approves_work") },
		"unknown root":            func(doc map[string]any) { doc["unexpected"] = true },
		"invalid signature":       func(doc map[string]any) { doc["signature"] = "ao-mission-local-digest:sha256:invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			doc := make(map[string]any, len(base))
			for key, value := range base {
				doc[key] = value
			}
			mutate(doc)
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "artifact-manifest.json")
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
			if result, err := ValidateArtifactManifestFile(path); err == nil || result.Status != "failed" {
				t.Fatalf("invalid v0.2 structure was accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestArtifactManifestV02ValidationRejectsUnknownArtifactRefField(t *testing.T) {
	dir := t.TempDir()
	body := []byte("retained content")
	digest := digestBytes(body)
	contentRef := artifactManifestContentRef(digest)
	contentPath := filepath.Join(dir, contentRef)
	if err := os.MkdirAll(filepath.Dir(contentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contentPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := FinalizeArtifactManifest(ArtifactManifest{Schema: "ao.mission.artifact-manifest.v0.2", MissionID: "mission-demo", ArtifactRefs: []ArtifactRef{{Schema: ArtifactRefSchema, Ref: "source.json", ContentRef: contentRef, Digest: digest}}})
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(manifestBody, &doc); err != nil {
		t.Fatal(err)
	}
	doc["artifact_refs"].([]any)[0].(map[string]any)["unexpected"] = true
	body, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "artifact-manifest.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := ValidateArtifactManifestFile(path); err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown artifact ref field was accepted: result=%+v err=%v", result, err)
	}
}

func TestArtifactManifestV02ValidationRequiresArtifactRefFieldsAndTypes(t *testing.T) {
	manifest := FinalizeArtifactManifest(ArtifactManifest{Schema: "ao.mission.artifact-manifest.v0.2", MissionID: "mission-demo", ArtifactRefs: []ArtifactRef{}})
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(manifestBody, &base); err != nil {
		t.Fatal(err)
	}
	validRef := map[string]any{
		"schema": ArtifactRefSchema, "ref": "source.json", "content_ref": "artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing content ref": func(ref map[string]any) { delete(ref, "content_ref") },
		"numeric content ref": func(ref map[string]any) { ref["content_ref"] = 42 },
		"numeric digest":      func(ref map[string]any) { ref["digest"] = 42 },
	} {
		t.Run(name, func(t *testing.T) {
			ref := make(map[string]any, len(validRef))
			for key, value := range validRef {
				ref[key] = value
			}
			mutate(ref)
			doc := make(map[string]any, len(base))
			for key, value := range base {
				doc[key] = value
			}
			doc["artifact_refs"] = []any{ref}
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "artifact-manifest.json")
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
			if result, err := ValidateArtifactManifestFile(path); err == nil || result.Status != "failed" {
				t.Fatalf("invalid artifact ref was accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestArtifactManifestV02ValidationRejectsDuplicateAuthorityFlag(t *testing.T) {
	manifest := FinalizeArtifactManifest(ArtifactManifest{Schema: "ao.mission.artifact-manifest.v0.2", MissionID: "mission-demo", ArtifactRefs: []ArtifactRef{}})
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"safe_to_execute":false`), []byte(`"safe_to_execute":false,"safe_to_execute":false`), 1)
	path := filepath.Join(t.TempDir(), "artifact-manifest.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := ValidateArtifactManifestFile(path); err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate authority field was accepted: result=%+v err=%v", result, err)
	}
}

func TestArtifactManifestV02RequiresArtifactRefSchema(t *testing.T) {
	dir := t.TempDir()
	body := []byte("retained content")
	digest := digestBytes(body)
	contentRef := artifactManifestContentRef(digest)
	contentPath := filepath.Join(dir, contentRef)
	if err := os.MkdirAll(filepath.Dir(contentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contentPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := FinalizeArtifactManifest(ArtifactManifest{
		Schema:    "ao.mission.artifact-manifest.v0.2",
		MissionID: "mission-demo",
		ArtifactRefs: []ArtifactRef{{
			Schema:     "unexpected-artifact-ref-schema",
			Ref:        "source.json",
			ContentRef: contentRef,
			Digest:     digest,
		}},
	})
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := ValidateArtifactManifestFile(manifestPath); err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "artifact ref schema") {
		t.Fatalf("v0.2 manifest accepted an invalid artifact-ref schema: result=%+v err=%v", result, err)
	}
}

func TestArtifactManifestV02ContractRequiresStringContentRef(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact-manifest-v0.2.json")
	body := []byte(`{"schema":"ao.mission.artifact-manifest.v0.2","mission_id":"mission-demo","artifact_refs":[{"schema":"ao.mission.artifact-ref.v0.1","ref":"source.json","content_ref":42,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"manifest_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","signature":"ao-mission-local-digest:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","safe_to_execute":false,"executes_work":false,"approves_work":false}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateContractFile(path)
	if err == nil || result.Status != "blocked" || !strings.Contains(strings.Join(result.Blockers, "; "), "content_ref must be string") {
		t.Fatalf("v0.2 content_ref type mismatch was accepted: result=%+v err=%v", result, err)
	}
}

func TestArtifactManifestSelfValidationRejectsInvalidDigestFormat(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "route.json")
	if err := os.WriteFile(artifactPath, []byte(`{"schema":"ao.mission.route-decision.v0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := FinalizeArtifactManifest(ArtifactManifest{
		MissionID: "mission-demo",
		ArtifactRefs: []ArtifactRef{{
			Schema: "ao.mission.artifact-ref.v0.1",
			Ref:    artifactPath,
			Digest: "not-a-sha256-digest",
			Kind:   "route_readback",
		}},
	})
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateArtifactManifestFile(manifestPath)
	if err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "digest must start with sha256:") {
		t.Fatalf("expected digest format failure, result=%+v err=%v", result, err)
	}
}

func TestArtifactManifestValidationNormalizesTextLineEndings(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "route.json")
	lfBody := []byte("{\n  \"schema\": \"ao.mission.route-decision.v0.1\"\n}\n")
	if err := os.WriteFile(artifactPath, []byte("{\r\n  \"schema\": \"ao.mission.route-decision.v0.1\"\r\n}\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(lfBody)
	manifest := FinalizeArtifactManifest(ArtifactManifest{
		MissionID: "mission-demo",
		ArtifactRefs: []ArtifactRef{{
			Schema: "ao.mission.artifact-ref.v0.1",
			Ref:    artifactPath,
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
			Kind:   "route_readback",
		}},
	})
	manifestPath := filepath.Join(dir, "artifact-manifest.json")
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateArtifactManifestFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || result.ArtifactCount != 1 {
		t.Fatalf("bad normalized manifest validation: %+v", result)
	}
}

func TestArtifactManifestFixtureBindsRecoveryAndCompactionEvidence(t *testing.T) {
	result, err := ValidateArtifactManifestFile(filepath.Join("..", "..", "examples", "valid", "artifact-manifest-recovery-compaction.json"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || result.ArtifactCount != 2 || result.ExecutesWork || result.ApprovesWork {
		t.Fatalf("bad recovery/compaction manifest validation: %+v", result)
	}
}

func TestTelegramUpdateReplayFixtureProducesIntentOnlyReadback(t *testing.T) {
	readback, err := ReplayTelegramUpdates(filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json"), map[string]string{"1001": "admin", "1002": "user"})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.telegram-update-replay-readback.v0.1" || readback.Status != "ready" {
		t.Fatalf("bad telegram update replay: %+v", readback)
	}
	if readback.Total != 4 || readback.IntentRecorded != 2 || readback.Denied != 1 || readback.Invalid != 1 {
		t.Fatalf("bad telegram update counts: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("telegram update replay widened authority: %+v", readback)
	}
}

func TestTelegramWebhookReplayFixtureMatchesUpdateReplayBoundary(t *testing.T) {
	readback, err := ReplayTelegramWebhookFixture(filepath.Join("..", "..", "examples", "valid", "telegram-webhook-replay.json"), map[string]string{"1001": "admin", "1002": "user"})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.telegram-webhook-replay-readback.v0.1" || readback.Gateway != "telegram_webhook" || readback.Status != "ready" {
		t.Fatalf("bad webhook replay readback: %+v", readback)
	}
	if readback.Total != 5 || readback.IntentRecorded != 2 || readback.Denied != 2 || readback.Invalid != 1 {
		t.Fatalf("bad webhook replay counts: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("telegram webhook replay widened authority: %+v", readback)
	}
}

func TestTelegramWebhookReplayTracksDuplicateUpdates(t *testing.T) {
	readback, err := ReplayTelegramWebhookFixture(filepath.Join("..", "..", "examples", "valid", "telegram-webhook-duplicate-replay.json"), map[string]string{"1001": "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Total != 3 || readback.Duplicates != 1 || readback.IntentRecorded != 2 {
		t.Fatalf("bad duplicate webhook accounting: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("telegram webhook duplicate replay widened authority: %+v", readback)
	}
}

func TestTelegramWebhookReplayClassifiesFreshness(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "telegram-webhook-freshness.json")
	if err := os.WriteFile(fixturePath, []byte(`{
  "schema": "ao.mission.telegram-webhook-fixture.v0.1",
  "updates": [
    {"update_id": 3001, "chat_id": "1001", "text": "/status", "expected_status": "intent_recorded", "generated_at_utc": "2099-01-01T00:00:00Z"},
    {"update_id": 3002, "chat_id": "1001", "text": "/next", "expected_status": "intent_recorded", "generated_at_utc": "2000-01-01T00:00:00Z"},
    {"update_id": 3003, "chat_id": "1001", "text": "/where", "expected_status": "intent_recorded"}
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	readback, err := ReplayTelegramWebhookFixture(fixturePath, map[string]string{"1001": "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Fresh != 1 || readback.Stale != 1 || readback.UnknownFreshness != 1 || readback.FreshnessStatus != "stale" {
		t.Fatalf("telegram webhook freshness was not classified: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("telegram webhook freshness widened authority: %+v", readback)
	}
}

func TestTelegramWebhookReplayCLIEmitsIntentOnlyReadback(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{
		"telegram", "webhook-replay",
		"--fixture", filepath.Join("..", "..", "examples", "valid", "telegram-webhook-replay.json"),
		"--config", filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
	}, &out, &errb); code != 0 {
		t.Fatalf("telegram webhook replay: %s", errb.String())
	}
	var readback GatewayReplayReadback
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.telegram-webhook-replay-readback.v0.1" || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("bad webhook CLI readback: %+v", readback)
	}
}

func TestA2AServeOnceEmitsReplayableFixtureServerReadback(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"a2a", "serve", "--http", "--once"}, &out, &errb); code != 0 {
		t.Fatalf("a2a serve once: %s", errb.String())
	}
	var readback map[string]any
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.a2a-fixture-server-readback.v0.1" || readback["status"] != "ready" {
		t.Fatalf("bad A2A fixture server readback: %#v", readback)
	}
	if readback["agent_card_path"] != "/.well-known/agent-card.json" || readback["jsonrpc_path"] != "/" {
		t.Fatalf("A2A fixture server readback missing replay paths: %#v", readback)
	}
	if readback["mutation_authority"] != false || readback["executes_work"] != false || readback["approves_work"] != false {
		t.Fatalf("A2A fixture server widened authority: %#v", readback)
	}
}

func TestA2AAgentCardIncludesProtocolMetadata(t *testing.T) {
	card := AgentCard()
	if card.ProtocolVersion == "" || card.Endpoint == "" || card.Description == "" {
		t.Fatalf("agent card missing protocol metadata: %+v", card)
	}
	for _, want := range []string{"streaming=false", "push_notifications=false", "mutation_authority=false"} {
		if !stringSliceContains(card.Capabilities, want) {
			t.Fatalf("agent card missing capability %q: %+v", want, card.Capabilities)
		}
	}
	if !card.CapabilitiesDetail["state_transition_history"] || card.CapabilitiesDetail["streaming"] || card.CapabilitiesDetail["push_notifications"] {
		t.Fatalf("agent card capabilities detail must expose readback-only A2A capabilities: %+v", card.CapabilitiesDetail)
	}
	if len(card.Skills) < 3 {
		t.Fatalf("agent card should expose mission readback skills: %+v", card.Skills)
	}
	if card.Skills[0].ID == "" || len(card.Skills[0].Tags) == 0 {
		t.Fatalf("agent card skill metadata incomplete: %+v", card.Skills[0])
	}
	if card.MutationAuthority {
		t.Fatal("agent card must remain intent/readback only")
	}
}

func TestA2ALifecycleTracksArtifactAndCancelReadbacks(t *testing.T) {
	readback, err := ReplayA2ATaskLifecycle(filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle-artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if readback.Total != 4 || readback.CancelRequested != 1 || readback.Cancelled != 1 || readback.ArtifactReadbacks != 2 {
		t.Fatalf("bad A2A artifact/cancel readback counts: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("A2A lifecycle artifact readbacks widened authority: %+v", readback)
	}
}

func TestA2ATaskCarriesArtifactRefsWithoutMutationAuthority(t *testing.T) {
	task := A2ATaskForParams("mission.artifacts", map[string]any{"mission_id": "mission-demo"})
	if task.Status != "intent_recorded" {
		t.Fatalf("bad artifact task status: %+v", task)
	}
	if len(task.ArtifactRefs) != 1 || task.ArtifactRefs[0].Kind != "mission_artifact_readback" {
		t.Fatalf("artifact task should carry readback artifact refs: %+v", task.ArtifactRefs)
	}
	if task.MutationAuthority {
		t.Fatalf("artifact task widened authority: %+v", task)
	}
}

func TestGatewayReplaySuiteCombinesTelegramAndA2AWithoutAuthority(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "gateway-replay-suite.json")
	var out, errb bytes.Buffer
	code := Run([]string{
		"gateway", "replay-suite",
		"--telegram-config", filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		"--telegram-webhook", filepath.Join("..", "..", "examples", "valid", "telegram-webhook-replay.json"),
		"--telegram-updates", filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json"),
		"--a2a-http", filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		"--a2a-lifecycle", filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle-artifacts.json"),
		"--out", outPath,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("gateway replay-suite failed: %s", errb.String())
	}
	var suite map[string]any
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &suite); err != nil {
		t.Fatal(err)
	}
	if suite["schema"] != "ao.mission.gateway-replay-suite-readback.v0.1" || suite["status"] != "ready" {
		t.Fatalf("bad gateway replay suite: %#v", suite)
	}
	if suite["telegram_replays"] != float64(2) || suite["a2a_replays"] != float64(2) {
		t.Fatalf("gateway replay suite should bind two Telegram and two A2A replays: %#v", suite)
	}
	for _, key := range []string{"mutation_authority", "executes_work", "approves_work"} {
		if suite[key] != false {
			t.Fatalf("gateway replay suite widened %s: %#v", key, suite)
		}
	}
}

func TestA2ACompatibilityCommandValidatesAgentCardTasksAndArtifacts(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "a2a-compatibility.json")
	var out, errb bytes.Buffer
	code := Run([]string{
		"a2a", "compatibility",
		"--agent-card", filepath.Join("..", "..", "examples", "valid", "a2a-agent-card.json"),
		"--http", filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		"--lifecycle", filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle-artifacts.json"),
		"--out", outPath,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("a2a compatibility failed: %s", errb.String())
	}
	var readback map[string]any
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.a2a-compatibility-readback.v0.1" || readback["status"] != "ready" {
		t.Fatalf("bad A2A compatibility readback: %#v", readback)
	}
	if readback["agent_card_skills"] != float64(3) || readback["artifact_readbacks"] != float64(2) {
		t.Fatalf("A2A compatibility should bind skills and artifacts: %#v", readback)
	}
	if readback["mutation_authority"] != false || readback["executes_work"] != false {
		t.Fatalf("A2A compatibility widened authority: %#v", readback)
	}
}

func TestGovernanceSnapshotDiffCommandReportsRouteChangeWithoutAuthority(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("small mission")
	if err != nil {
		t.Fatal(err)
	}
	before := Snapshot(rec)
	rec.CurrentRoute = "ao-atlas"
	rec.CurrentPhase = "atlas_import"
	after := Snapshot(rec)
	beforePath := filepath.Join(dir, "before.json")
	afterPath := filepath.Join(dir, "after.json")
	writeJSONForTest(t, beforePath, before)
	writeJSONForTest(t, afterPath, after)
	var out, errb bytes.Buffer
	if code := Run([]string{"governance", "diff", "--before", beforePath, "--after", afterPath}, &out, &errb); code != 0 {
		t.Fatalf("governance diff: %s", errb.String())
	}
	var diff map[string]any
	if err := json.Unmarshal(out.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if diff["schema"] != "ao.mission.governance-snapshot-diff.v0.1" || diff["changed_fields"] != float64(2) {
		t.Fatalf("bad snapshot diff: %#v", diff)
	}
	if diff["safe_to_execute"] != false || diff["executes_work"] != false || diff["approves_work"] != false {
		t.Fatalf("snapshot diff widened authority: %#v", diff)
	}
}

func TestMissionArchiveExportWritesDigestBoundPublicSafeBundle(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "archive mission evidence"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	archivePath := filepath.Join(dir, "mission-archive.json")
	if code := Run([]string{"--home", dir, "mission", "archive", "--mission", rec.MissionID, "--out", archivePath}, &out, &errb); code != 0 {
		t.Fatalf("mission archive: %s", errb.String())
	}
	var archive map[string]any
	body, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &archive); err != nil {
		t.Fatal(err)
	}
	if archive["schema"] != "ao.mission.archive.v0.1" || archive["mission_id"] != rec.MissionID {
		t.Fatalf("bad archive: %#v", archive)
	}
	if digest, _ := archive["archive_digest"].(string); !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("archive digest missing: %#v", archive)
	}
	if archive["safe_to_execute"] != false || archive["executes_work"] != false || archive["approves_work"] != false {
		t.Fatalf("archive widened authority: %#v", archive)
	}
}

func TestMissionArchiveValidateAndImportRoundTripWithoutAuthority(t *testing.T) {
	exportHome := t.TempDir()
	importHome := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", exportHome, "start", "archive import round trip"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(exportHome, "mission-archive.json")
	out.Reset()
	if code := Run([]string{"--home", exportHome, "mission", "archive", "--mission", rec.MissionID, "--out", archivePath}, &out, &errb); code != 0 {
		t.Fatalf("mission archive: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"mission", "validate-archive", "--path", archivePath}, &out, &errb); code != 0 {
		t.Fatalf("mission validate-archive: %s", errb.String())
	}
	var validation map[string]any
	if err := json.Unmarshal(out.Bytes(), &validation); err != nil {
		t.Fatal(err)
	}
	if validation["schema"] != "ao.mission.archive-validation.v0.1" || validation["status"] != "ready" || validation["mission_id"] != rec.MissionID {
		t.Fatalf("bad archive validation: %#v", validation)
	}
	if validation["safe_to_execute"] != false || validation["executes_work"] != false || validation["approves_work"] != false {
		t.Fatalf("archive validation widened authority: %#v", validation)
	}
	out.Reset()
	if code := Run([]string{"--home", importHome, "mission", "import-archive", "--path", archivePath}, &out, &errb); code != 0 {
		t.Fatalf("mission import-archive: %s", errb.String())
	}
	var imported map[string]any
	if err := json.Unmarshal(out.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported["schema"] != "ao.mission.archive-import-readback.v0.1" || imported["status"] != "ready" || imported["mission_id"] != rec.MissionID {
		t.Fatalf("bad archive import readback: %#v", imported)
	}
	out.Reset()
	if code := Run([]string{"--home", importHome, "mission", "inspect", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("inspect imported mission: %s", errb.String())
	}
	var roundTrip Record
	if err := json.Unmarshal(out.Bytes(), &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.MissionID != rec.MissionID || roundTrip.Objective != rec.Objective {
		t.Fatalf("archive import did not restore record: %#v", roundTrip)
	}
}

func TestMissionArchiveRedactsLocalPathsBeforeValidation(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	objective := "archive local evidence path /" + "Users/example/Documents/public/ao-mission/docs/evidence/demo.json"
	if code := Run([]string{"--home", dir, "start", objective}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	localPath := "/" + "Users/example/Documents/public/ao-mission/docs/evidence/extra.json"
	if _, err := NewStore(dir).Update(rec.MissionID, func(r *Record) error {
		r.Checkpoints = []MissionCheckpoint{{
			Schema:          "ao.mission.checkpoint.v0.3",
			MissionID:       r.MissionID,
			Sequence:        1,
			Iteration:       1,
			Route:           "ao-atlas",
			Phase:           "handoff_required",
			Result:          "recorded",
			ExactNextAction: "continue from " + localPath,
			ResumeCommand:   "ao-mission resume --evidence " + localPath,
		}}
		r.ReturnGate = &ReturnGate{
			Schema:               "ao.mission.return-gate.v0.3",
			MissionID:            r.MissionID,
			Status:               "early_return_denied",
			FinalResponseAllowed: false,
			Reason:               "local evidence remains at " + localPath,
			Blockers:             []string{"redact blocker path " + localPath},
			ExactNextAction:      "inspect " + localPath,
		}
		r.Evidence.AtlasFinalSynthesis = &AtlasFinalSynthesisReadbackCounts{
			MissionID:            r.MissionID,
			ContractVersion:      "ao.atlas.ao-mission-final-synthesis-readback.v0.1",
			Status:               "completed",
			TotalNodes:           1,
			CompletedNodes:       1,
			MinimumNodes:         1,
			ReturnGateStatus:     "final_response_allowed",
			FinalResponseAllowed: true,
			FinalResponseReason:  "source at " + localPath,
			AtlasWorkgraphStatus: "completed",
			CommandReadback:      "ready",
			EventSearchBound:     true,
			BranchCleanupBound:   true,
			RSIRemainsDenied:     true,
			ExactNextAction:      "next path " + localPath,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "mission-archive.json")
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "archive", "--mission", rec.MissionID, "--out", archivePath}, &out, &errb); code != 0 {
		t.Fatalf("mission archive: %s", errb.String())
	}
	body, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "/"+"Users/") {
		t.Fatalf("archive preserved local path: %s", string(body))
	}
	var archive MissionArchive
	if err := json.Unmarshal(body, &archive); err != nil {
		t.Fatal(err)
	}
	if len(archive.PublicSafeRedactions) == 0 || archive.SourceObjectiveDigest != rec.ObjectiveDigest {
		t.Fatalf("archive did not record public-safe redaction metadata: %+v", archive)
	}
	out.Reset()
	if code := Run([]string{"mission", "validate-archive", "--path", archivePath}, &out, &errb); code != 0 {
		t.Fatalf("mission validate-archive: %s", errb.String())
	}
	var validation MissionArchiveValidation
	if err := json.Unmarshal(out.Bytes(), &validation); err != nil {
		t.Fatal(err)
	}
	if validation.Status != "ready" || validation.SafeToExecute || validation.ExecutesWork || validation.ApprovesWork {
		t.Fatalf("bad archive validation: %+v", validation)
	}
}

func TestTelegramRoleMatrixExportListsAllowlistedRolesWithoutAuthority(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "telegram-role-matrix.json")
	var out, errb bytes.Buffer
	if code := Run([]string{
		"telegram", "role-matrix",
		"--config", filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		"--out", outPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("telegram role-matrix: %s", errb.String())
	}
	var matrix map[string]any
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &matrix); err != nil {
		t.Fatal(err)
	}
	if matrix["schema"] != "ao.mission.telegram-role-matrix-readback.v0.1" || matrix["status"] != "ready" {
		t.Fatalf("bad Telegram role matrix: %#v", matrix)
	}
	if matrix["chat_count"] != float64(2) || matrix["admin_count"] != float64(1) || matrix["user_count"] != float64(1) {
		t.Fatalf("bad Telegram role counts: %#v", matrix)
	}
	if matrix["mutation_authority"] != false || matrix["executes_work"] != false || matrix["approves_work"] != false {
		t.Fatalf("Telegram role matrix widened authority: %#v", matrix)
	}
}

func TestA2AStreamingDeniedReadbackRejectsStreamingAgentCard(t *testing.T) {
	var out, errb bytes.Buffer
	code := Run([]string{
		"a2a", "streaming-denial",
		"--agent-card", filepath.Join("..", "..", "examples", "invalid", "a2a-agent-card-streaming.json"),
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("a2a streaming-denial should emit denial readback, got stderr=%s", errb.String())
	}
	var readback map[string]any
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.a2a-streaming-denial-readback.v0.1" || readback["status"] != "denied" {
		t.Fatalf("bad A2A streaming denial: %#v", readback)
	}
	if readback["streaming_requested"] != true || readback["mutation_authority"] != false || readback["executes_work"] != false {
		t.Fatalf("A2A streaming denial missing boundary flags: %#v", readback)
	}
}

func TestA2AStreamingDeniedReadbackRejectsSSEFixture(t *testing.T) {
	var out, errb bytes.Buffer
	code := Run([]string{
		"a2a", "streaming-denial",
		"--agent-card", filepath.Join("..", "..", "examples", "invalid", "a2a-agent-card-streaming-sse.json"),
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("a2a streaming-denial should emit SSE denial readback, got stderr=%s", errb.String())
	}
	var readback map[string]any
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.a2a-streaming-denial-readback.v0.1" || readback["status"] != "denied" {
		t.Fatalf("bad A2A SSE denial: %#v", readback)
	}
	if readback["sse_requested"] != true || readback["streaming_requested"] != true || readback["denied_capability"] != "streaming_or_push" {
		t.Fatalf("A2A SSE denial missing requested capability flags: %#v", readback)
	}
	if readback["mutation_authority"] != false || readback["executes_work"] != false || readback["approves_work"] != false {
		t.Fatalf("A2A SSE denial widened authority: %#v", readback)
	}
}

func TestMissionEventIndexSearchAndCLIReadback(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "event search atlas mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "next", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("next: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "events", "index", "--out", filepath.Join(dir, "mission-event-index.json")}, &out, &errb); code != 0 {
		t.Fatalf("mission events index: %s", errb.String())
	}
	var index MissionEventIndex
	if err := json.Unmarshal(out.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if index.Schema != "ao.mission.event-index.v0.2" || index.IndexVersion != "v0.2" || index.TotalEvents < 2 || index.ExecutesWork || index.ApprovesWork || index.MutatesRepositories {
		t.Fatalf("bad event index: %+v", index)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "events", "search", "--mission", rec.MissionID, "--kind", "route_decision", "--query", "AO Atlas", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission events search: %s", errb.String())
	}
	var results MissionEventSearchReadback
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if results.Schema != "ao.mission.event-search-readback.v0.1" || results.TotalMatches == 0 {
		t.Fatalf("bad event search results: %+v", results)
	}
	if results.ExecutesWork || results.ApprovesWork || results.MutatesRepositories {
		t.Fatalf("event search widened authority: %+v", results)
	}
}

func TestMissionDashboardUsesOneMissionReadPathWithCorruptUnrelatedRecord(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("dashboard one mission read path")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(s, rec.MissionID, ContinueOptions{MaxIterations: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "missions", "unrelated-corrupt.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	dashboard, err := BuildMissionDashboardReadback(s, rec.MissionID, true)
	if err != nil {
		t.Fatalf("dashboard for one healthy mission should not read corrupt unrelated records: %v", err)
	}
	if dashboard.MissionID != rec.MissionID || dashboard.EventCount == 0 || dashboard.SafeToExecute || dashboard.ExecutesWork || dashboard.ApprovesWork || dashboard.MutatesRepositories {
		t.Fatalf("bad one-mission dashboard readback: %+v", dashboard)
	}
}

func TestMissionEventIndexDigestStableAcrossGenerationTime(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	s := NewStore(dir)
	s.Clock = func() time.Time { return stamp }
	if _, err := s.Start("deterministic event index digest mission"); err != nil {
		t.Fatal(err)
	}
	first, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Clock = func() time.Time { return stamp.Add(5 * time.Minute) }
	second, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceDigest != second.SourceDigest {
		t.Fatalf("source digest should be stable for unchanged mission records: first=%s second=%s", first.SourceDigest, second.SourceDigest)
	}
	if first.IndexDigest != second.IndexDigest {
		t.Fatalf("index digest should be stable for unchanged mission records: first=%s second=%s", first.IndexDigest, second.IndexDigest)
	}
}

func TestMissionEventIndexScaleMetricsExposeReadAndEventCounts(t *testing.T) {
	for _, count := range []int{100, 1000, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			s := seedMissionRecordStore(t, count)
			index, err := BuildMissionEventIndex(s)
			if err != nil {
				t.Fatal(err)
			}
			packet := marshalMapForTest(t, index)
			if got, ok := packet["store_file_reads"].(float64); !ok || got != float64(count) {
				t.Fatalf("event index should expose bounded store file reads for %d records, got=%v present=%t total_events=%d", count, packet["store_file_reads"], ok, index.TotalEvents)
			}
			if got, ok := packet["event_construction_count"].(float64); !ok || got != float64(index.TotalEvents) || index.TotalEvents < count {
				t.Fatalf("event index should expose event construction count for %d records, got=%v present=%t total_events=%d", count, packet["event_construction_count"], ok, index.TotalEvents)
			}
		})
	}
}

func TestMissionEventIndexDoesNotRescanTransactionTempsPerRecord(t *testing.T) {
	s := seedMissionRecordStore(t, 100)
	scans := 0
	s.transactionTempCleanup = func(missionTransactionPaths) error {
		scans++
		return nil
	}
	if _, err := BuildMissionEventIndex(s); err != nil {
		t.Fatal(err)
	}
	if scans != 0 {
		t.Fatalf("event index performed %d per-record transaction temp directory scans", scans)
	}
}

func TestMissionEventIndexLoadsRecordsWithBoundedParallelism(t *testing.T) {
	s := seedMissionRecordStore(t, 32)
	var active atomic.Int32
	var maximum atomic.Int32
	s.missionRecordLoad = func(_ string, load func() error) error {
		current := active.Add(1)
		defer active.Add(-1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		time.Sleep(10 * time.Millisecond)
		return load()
	}
	if _, err := BuildMissionEventIndex(s); err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got <= 1 || got > missionRecordLoadWorkerLimit {
		t.Fatalf("record load parallelism=%d, want 2..%d", got, missionRecordLoadWorkerLimit)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("record loads still active after listing: %d", got)
	}
}

func TestMissionRecordParallelLoadReturnsDeterministicFirstError(t *testing.T) {
	s := seedMissionRecordStore(t, 32)
	var active atomic.Int32
	s.missionRecordLoad = func(id string, load func() error) error {
		active.Add(1)
		defer active.Add(-1)
		switch id {
		case "mission-scale-10":
			time.Sleep(50 * time.Millisecond)
			return errors.New("first candidate failure: mission-scale-10")
		case "mission-scale-2":
			return errors.New("later candidate failure: mission-scale-2")
		default:
			return load()
		}
	}
	_, stats, err := s.listFilteredWithStats(ListFilters{})
	if err == nil || !strings.Contains(err.Error(), "first candidate failure: mission-scale-10") {
		t.Fatalf("parallel load error=%v, want deterministic first candidate failure", err)
	}
	if stats.StoreFileReads != 2 {
		t.Fatalf("store file reads=%d, want sequentially visible reads before first error", stats.StoreFileReads)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("record loads still active after error return: %d", got)
	}
}

func TestMissionEventIndexDefersTransactionTempCleanupToDirectLifecycleReads(t *testing.T) {
	s := seedMissionRecordStore(t, 1)
	id := "mission-scale-0"
	stale := filepath.Join(s.Root, "missions", "."+id+".json.tmp-stale")
	writeStale := func() {
		t.Helper()
		if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeStale()
	if _, err := BuildMissionEventIndex(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("read-only event index unexpectedly cleaned transaction temp: %v", err)
	}

	if _, err := s.Load(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("direct lifecycle read did not clean transaction temp: %v", err)
	}
}

func TestMissionDoctorUsesSingleStoreListingMetrics(t *testing.T) {
	s := seedMissionRecordStore(t, 100)
	readback := BuildMissionDoctorReadback(s)
	packet := marshalMapForTest(t, readback)
	if got, ok := packet["store_list_count"].(float64); !ok || got != float64(1) {
		t.Fatalf("doctor should expose exactly one store listing, got=%v present=%t mission_count=%d", packet["store_list_count"], ok, readback.MissionCount)
	}
	if got, ok := packet["store_file_reads"].(float64); !ok || got != float64(readback.MissionCount) {
		t.Fatalf("doctor should reuse the listed records instead of reading the store twice, got=%v present=%t mission_count=%d", packet["store_file_reads"], ok, readback.MissionCount)
	}
}

func TestMissionTimelineQueryIndexBindsEventIndexDigestAndCLIOutput(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "timeline query index atlas checkpoint mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if _, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 2}); err != nil {
		t.Fatal(err)
	}
	eventIndex, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	queryIndex, err := BuildMissionTimelineQueryIndex(eventIndex)
	if err != nil {
		t.Fatal(err)
	}
	if queryIndex.Schema != "ao.mission.timeline-query-index.v0.1" ||
		queryIndex.IndexVersion != "v0.1" ||
		queryIndex.EventIndexDigest != eventIndex.IndexDigest ||
		queryIndex.EventCount != eventIndex.TotalEvents ||
		queryIndex.TermCount == 0 ||
		queryIndex.ExecutesWork ||
		queryIndex.ApprovesWork ||
		queryIndex.MutatesRepositories ||
		queryIndex.SafeToExecute {
		t.Fatalf("bad timeline query index: %+v", queryIndex)
	}
	if err := ValidateMissionTimelineQueryIndexDigest(queryIndex); err != nil {
		t.Fatalf("timeline query index digest did not validate: %v", err)
	}
	if !timelineQueryIndexHasTerm(queryIndex, "ao-atlas", rec.MissionID, "route_decision") ||
		!timelineQueryIndexHasTerm(queryIndex, "checkpoint", rec.MissionID, "checkpoint") {
		t.Fatalf("timeline query index missing expected terms: %+v", queryIndex.Terms)
	}
	queryIndex.Terms[0].Term = "tampered"
	if err := ValidateMissionTimelineQueryIndexDigest(queryIndex); err == nil {
		t.Fatal("tampered timeline query index digest should fail validation")
	}

	out.Reset()
	eventIndexPath := filepath.Join(dir, "event-index.json")
	queryIndexPath := filepath.Join(dir, "timeline-query-index.json")
	if code := Run([]string{"--home", dir, "mission", "events", "index", "--out", eventIndexPath}, &out, &errb); code != 0 {
		t.Fatalf("mission events index: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "events", "query-index", "--index", eventIndexPath, "--out", queryIndexPath}, &out, &errb); code != 0 {
		t.Fatalf("mission events query-index: %s", errb.String())
	}
	var cliIndex MissionTimelineQueryIndex
	if err := json.Unmarshal(out.Bytes(), &cliIndex); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMissionTimelineQueryIndexDigest(cliIndex); err != nil {
		t.Fatalf("CLI timeline query index digest did not validate: %v", err)
	}
	if cliIndex.EventIndexDigest != eventIndex.IndexDigest || cliIndex.TermCount == 0 {
		t.Fatalf("CLI timeline query index did not bind source event index: %+v", cliIndex)
	}
	body, err := os.ReadFile(queryIndexPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted MissionTimelineQueryIndex
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.IndexDigest != cliIndex.IndexDigest {
		t.Fatalf("persisted query index digest mismatch: persisted=%s cli=%s", persisted.IndexDigest, cliIndex.IndexDigest)
	}
}

func TestMissionRestartRecoveryProofBindsIndexedTimelineAfterStoreReload(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("month 6 restart recovery proof over indexed timeline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 3}); err != nil {
		t.Fatal(err)
	}

	proof, err := BuildMissionRestartRecoveryProof(s, rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Schema != "ao.mission.restart-recovery-proof.v0.1" ||
		proof.Status != "restart_recovery_proven" ||
		proof.MissionID != rec.MissionID ||
		!proof.SourceDigestStable ||
		!proof.EventCountStable ||
		!proof.TimelineTermsStable ||
		!proof.TimelineMatchesStable ||
		!proof.NoDuplicateTimelineMatches ||
		!proof.RecoveryProven ||
		proof.BeforeMissionEventCount == 0 ||
		proof.BeforeMissionEventCount != proof.AfterMissionEventCount ||
		proof.BeforeTimelineMatchCount != proof.AfterTimelineMatchCount ||
		proof.SafeToExecute ||
		proof.ExecutesWork ||
		proof.ApprovesWork ||
		proof.MutatesRepositories {
		t.Fatalf("restart recovery proof did not bind indexed timeline safely: %+v", proof)
	}
	if err := ValidateMissionRestartRecoveryProof(proof); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	out.Reset()
	errb.Reset()
	outPath := filepath.Join(dir, "restart-recovery-proof.json")
	if code := Run([]string{"--home", dir, "mission", "events", "restart-proof", "--mission", rec.MissionID, "--out", outPath, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission events restart-proof: %s", errb.String())
	}
	var cliProof MissionRestartRecoveryProof
	if err := json.Unmarshal(out.Bytes(), &cliProof); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMissionRestartRecoveryProof(cliProof); err != nil {
		t.Fatalf("CLI restart recovery proof did not validate: %v", err)
	}
	if !cliProof.RecoveryProven || cliProof.MissionID != rec.MissionID {
		t.Fatalf("CLI restart proof did not preserve recovery state: %+v", cliProof)
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted MissionRestartRecoveryProof
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.BeforeEventSourceDigest != cliProof.BeforeEventSourceDigest ||
		persisted.AfterTimelineTermDigest != cliProof.AfterTimelineTermDigest {
		t.Fatalf("persisted restart proof changed digests: persisted=%+v cli=%+v", persisted, cliProof)
	}
}

func TestMissionTimelineQueryIndexDigestIgnoresGenerationTimestamp(t *testing.T) {
	index := MissionTimelineQueryIndex{
		Schema:           "ao.mission.timeline-query-index.v0.1",
		Status:           "ready",
		IndexVersion:     "v0.1",
		EventIndexDigest: "sha256:" + strings.Repeat("a", 64),
		GeneratedAtUTC:   "2026-08-20T18:55:44Z",
	}
	first, err := digestMissionTimelineQueryIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	index.GeneratedAtUTC = "2026-08-20T18:55:45Z"
	second, err := digestMissionTimelineQueryIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("timeline digest changed with generation timestamp: first=%s second=%s", first, second)
	}
}

func TestMissionTimelineQueryIndexValidationAcceptsLegacyTimestampDigest(t *testing.T) {
	index := MissionTimelineQueryIndex{
		Schema:           "ao.mission.timeline-query-index.v0.1",
		Status:           "ready",
		IndexVersion:     "v0.1",
		EventIndexDigest: "sha256:" + strings.Repeat("a", 64),
		GeneratedAtUTC:   "2026-08-20T18:55:44Z",
	}
	body, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	index.IndexDigest = digestBytes(body)
	if err := ValidateMissionTimelineQueryIndexDigest(index); err != nil {
		t.Fatalf("legacy timestamp-bound digest was rejected: %v", err)
	}
}

func TestMissionCompactionResumePromptBindsLatestEventTimeline(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("resume compacted month six mission without partial final response")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(s, rec.MissionID, ContinueOptions{
		UntilDone:        true,
		MaxIterations:    3,
		MinNodes:         12,
		MinMinutes:       120,
		MaxMinutes:       180,
		ReturnOnlyWhen:   "mission_done_or_true_hard_blocker_or_no_ready_work_and_no_exact_next_action",
		CheckpointPolicy: "after_each_node_or_timed_interval",
	}); err != nil {
		t.Fatal(err)
	}

	prompt, err := BuildMissionCompactionResumePrompt(s, rec.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Schema != "ao.mission.compaction-resume-prompt.v0.1" ||
		prompt.Status != "ready" ||
		prompt.MissionID != rec.MissionID ||
		prompt.MissionStatus != "active" ||
		prompt.CurrentRoute != "ao-atlas" ||
		prompt.LatestRoute != "ao-atlas" ||
		prompt.CompletedNodes != 0 ||
		prompt.ReturnGateStatus != "early_return_denied" ||
		prompt.FinalResponseAllowed ||
		prompt.EventCount == 0 ||
		prompt.TimelineTermCount == 0 ||
		!strings.HasPrefix(prompt.EventIndexDigest, "sha256:") ||
		!strings.HasPrefix(prompt.TimelineIndexDigest, "sha256:") ||
		!strings.Contains(prompt.ExactNextAction, "continue") ||
		!strings.Contains(prompt.ResumePrompt, "Start a Codex goal") ||
		!strings.Contains(prompt.ResumePrompt, rec.MissionID) ||
		!strings.Contains(prompt.ResumePrompt, "Do not return a final answer after partial progress") ||
		!strings.Contains(prompt.ResumePrompt, "RSI remains denied") ||
		prompt.SafeToExecute ||
		prompt.ExecutesWork ||
		prompt.ApprovesWork ||
		prompt.MutatesRepositories {
		t.Fatalf("resume prompt did not bind compact timeline safely: %+v", prompt)
	}
	if err := ValidateMissionCompactionResumePrompt(prompt); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	outPath := filepath.Join(dir, "compaction-resume-prompt.json")
	if code := Run([]string{"--home", dir, "mission", "events", "resume-prompt", "--mission", rec.MissionID, "--out", outPath, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission events resume-prompt: %s", errb.String())
	}
	var cliPrompt MissionCompactionResumePrompt
	if err := json.Unmarshal(out.Bytes(), &cliPrompt); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMissionCompactionResumePrompt(cliPrompt); err != nil {
		t.Fatalf("CLI resume prompt did not validate: %v", err)
	}
	if cliPrompt.ResumePrompt != prompt.ResumePrompt || cliPrompt.EventIndexDigest != prompt.EventIndexDigest {
		t.Fatalf("CLI resume prompt changed core binding: cli=%+v direct=%+v", cliPrompt, prompt)
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted MissionCompactionResumePrompt
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ResumePrompt != cliPrompt.ResumePrompt || persisted.TimelineIndexDigest != cliPrompt.TimelineIndexDigest {
		t.Fatalf("persisted resume prompt changed timeline binding: persisted=%+v cli=%+v", persisted, cliPrompt)
	}
}

func timelineQueryIndexHasTerm(index MissionTimelineQueryIndex, term, missionID, kind string) bool {
	for _, indexedTerm := range index.Terms {
		if indexedTerm.Term != term {
			continue
		}
		for _, match := range indexedTerm.Matches {
			if match.MissionID == missionID && match.Kind == kind {
				return true
			}
		}
	}
	return false
}

func TestDoctorCommandReportsLocalStoreHealthWithoutAuthority(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "doctor mission loop"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "doctor", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("doctor: %s", errb.String())
	}
	var readback MissionDoctorReadback
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.doctor-readback.v0.1" || readback.Status != "ready" || readback.MissionCount != 1 {
		t.Fatalf("bad doctor readback: %+v", readback)
	}
	if readback.SafeToExecute || readback.ExecutesWork || readback.ApprovesWork || readback.MutatesRepositories {
		t.Fatalf("doctor widened authority: %+v", readback)
	}
}

func TestCLIBetaIncidentStopRuleReadbackTriggersPromoterHold(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "beta incident stop-rule mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errb.Reset()
	if code := Run([]string{
		"--home", dir,
		"mission", "beta-incident-stop-rule",
		"--mission", rec.MissionID,
		"--incident", "incident-beta-pilot-ci-regression",
		"--severity", "high",
		"--sentinel-status", "failed",
		"--promoter-status", "hold",
		"--json",
	}, &out, &errb); code != 0 {
		t.Fatalf("beta incident stop-rule: %s", errb.String())
	}
	var readback BetaIncidentStopRuleReadback
	if err := json.Unmarshal(out.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.beta-incident-stop-rule-readback.v0.1" ||
		readback.Status != "hold_required" ||
		readback.MissionID != rec.MissionID ||
		readback.IncidentID != "incident-beta-pilot-ci-regression" ||
		readback.IncidentSeverity != "high" ||
		readback.SentinelStatus != "failed" ||
		readback.PromoterStatus != "hold" ||
		!readback.StopRuleTriggered ||
		!readback.PromoterHoldRequired ||
		readback.ExactNextAction != "hold beta pilot activity, record incident evidence, and require Sentinel plus Promoter clearance before continuation" {
		t.Fatalf("bad beta incident stop-rule readback: %+v", readback)
	}
	if !stringSliceContains(readback.StopReasons, "high severity beta incident") ||
		!stringSliceContains(readback.StopReasons, "Sentinel status is failed") ||
		!stringSliceContains(readback.StopReasons, "Promoter hold is active") {
		t.Fatalf("missing stop reasons: %+v", readback)
	}
	if !readback.ReadOnly ||
		readback.SafeToExecute ||
		readback.ExecutesWork ||
		readback.ApprovesWork ||
		readback.MutatesRepositories ||
		readback.ProviderCallsAllowed ||
		readback.CredentialUseAllowed ||
		readback.ReleaseOrPublishAllowed ||
		readback.ClaimsAuthorityAdvance ||
		!readback.RSIRemainsDenied {
		t.Fatalf("beta incident stop-rule widened authority: %+v", readback)
	}
}

func TestCLIPilotFeedbackPacketIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "pilot feedback mission"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errb.Reset()
	if code := Run([]string{
		"--home", dir,
		"mission", "pilot-feedback-packet",
		"--mission", rec.MissionID,
		"--pilot", "pilot-alpha",
		"--window", "month6-beta-readiness",
		"--json",
	}, &out, &errb); code != 0 {
		t.Fatalf("pilot feedback packet: %s", errb.String())
	}
	var packet PilotFeedbackCapturePacket
	if err := json.Unmarshal(out.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Schema != "ao.mission.pilot-feedback-capture-packet.v0.1" ||
		packet.Status != "ready" ||
		packet.MissionID != rec.MissionID ||
		packet.PilotID != "pilot-alpha" ||
		packet.FeedbackWindow != "month6-beta-readiness" ||
		len(packet.CaptureChannels) != 3 ||
		len(packet.Questions) < 3 ||
		len(packet.EvidenceRequired) < 4 ||
		packet.ExactNextAction != "Collect pilot feedback as read-only evidence before any beta execution or live run." {
		t.Fatalf("bad pilot feedback packet: %+v", packet)
	}
	if !packet.ReadOnly ||
		packet.SafeToExecute ||
		packet.ExecutesWork ||
		packet.ApprovesWork ||
		packet.MutatesRepositories ||
		packet.ProviderCallsAllowed ||
		packet.CredentialUseAllowed ||
		packet.ReleaseOrPublishAllowed ||
		packet.ClaimsAuthorityAdvance ||
		!packet.RSIRemainsDenied {
		t.Fatalf("pilot feedback packet widened authority: %+v", packet)
	}
}

func TestSchedulerRecoveryDoesNotRecommendContinuationWhenReplayFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler-fresh-replay.json")
	if err := os.WriteFile(path, []byte(`{
  "schema": "ao.mission.scheduler-replay-fixture.v0.1",
  "evaluated_at_utc": "2026-07-03T12:00:00Z",
  "readbacks": [
    {
      "schema": "ao.mission.scheduler-readback.v0.1",
      "mission_id": "mission-demo",
      "status": "ready",
      "scheduler": "codex-cron",
      "event_loop": true,
      "generated_at_utc": "2026-07-03T11:55:00Z",
      "executes_work": false,
      "approves_work": false
    }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	replay, err := ReplaySchedulerReadbacks(path)
	if err != nil {
		t.Fatal(err)
	}
	recovery := BuildSchedulerRecoveryReadback("mission-demo", replay)
	if recovery.Status != "ready" || recovery.RecoveryMode != "none_required" || strings.Contains(recovery.ExactNextAction, "continue --mission") {
		t.Fatalf("fresh scheduler replay should not request recovery continuation: %+v", recovery)
	}
	if recovery.ExecutesWork || recovery.ApprovesWork {
		t.Fatalf("fresh scheduler recovery widened authority: %+v", recovery)
	}
}

func TestTelegramCommandReplayMatrixCoversAllAllowedCommandsAndDeniedRoles(t *testing.T) {
	readback, err := ReplayTelegramCommandMatrix(
		filepath.Join("..", "..", "examples", "valid", "telegram-command-matrix.json"),
		map[string]string{"1001": "admin", "1002": "user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, result := range readback.Results {
		covered[result.Command] = true
	}
	for command := range allowedTelegramCommands {
		if !covered[command] {
			t.Fatalf("telegram replay matrix missing allowed command %s", command)
		}
	}
	if readback.Total < len(allowedTelegramCommands)+2 || readback.Denied < 2 {
		t.Fatalf("telegram replay matrix should cover allowed and denied role cases: %+v", readback)
	}
	if readback.MutationAuthority || readback.ExecutesWork || readback.ApprovesWork {
		t.Fatalf("telegram replay matrix widened authority: %+v", readback)
	}
}

func TestMissionEventIndexCarriesVersionAndDigest(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rec, err := s.Start("event index digest proof")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(s, rec.MissionID, ContinueOptions{UntilDone: true, MaxIterations: 1}); err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	if index.Schema != "ao.mission.event-index.v0.2" || index.IndexVersion != "v0.2" {
		t.Fatalf("event index should be versioned as v0.2: %+v", index)
	}
	if !strings.HasPrefix(index.IndexDigest, "sha256:") || index.IndexDigest == "sha256:" {
		t.Fatalf("event index missing digest: %+v", index)
	}
	if !strings.HasPrefix(index.SourceDigest, "sha256:") || index.SourceDigest == "sha256:" {
		t.Fatalf("event index missing source digest: %+v", index)
	}
	if err := ValidateMissionEventIndexDigest(index); err != nil {
		t.Fatalf("event index digest did not validate: %v", err)
	}
	index.Events[0].Summary = "tampered"
	if err := ValidateMissionEventIndexDigest(index); err == nil {
		t.Fatal("tampered event index digest should fail validation")
	}
}

func TestMissionReadinessBundleVerifierBindsLocalRepoSummaries(t *testing.T) {
	dir := t.TempDir()
	missionReady := filepath.Join(dir, "mission-readiness.txt")
	atlasReady := filepath.Join(dir, "atlas-readiness.txt")
	if err := os.WriteFile(missionReady, []byte("AO Mission production readiness: 100/100 status=ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(atlasReady, []byte("status=ready\nscore=100/100\nsummary=target/production-readiness/summary.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readback, err := BuildMissionReadinessBundleReadback([]MissionReadinessBundleInput{
		{Repo: "ao-mission", Path: missionReady},
		{Repo: "ao-atlas", Path: atlasReady},
	})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.readiness-bundle-readback.v0.1" || readback.Status != "ready" || readback.ReadyRepos != 2 {
		t.Fatalf("bad readiness bundle: %+v", readback)
	}
	if len(readback.Repos) != 2 || !strings.HasPrefix(readback.Repos[0].SHA256, "sha256:") {
		t.Fatalf("readiness bundle missing repo digest evidence: %+v", readback)
	}
	if readback.SafeToExecute || readback.ExecutesWork || readback.ApprovesWork || readback.MutatesRepositories {
		t.Fatalf("readiness bundle widened authority: %+v", readback)
	}
	outPath := filepath.Join(dir, "readiness-bundle.json")
	var out, errb bytes.Buffer
	if code := Run([]string{
		"mission", "readiness-bundle",
		"--repo", "ao-mission=" + missionReady,
		"--repo", "ao-atlas=" + atlasReady,
		"--out", outPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("mission readiness-bundle CLI: %s", errb.String())
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"schema": "ao.mission.readiness-bundle-readback.v0.1"`) {
		t.Fatalf("readiness bundle CLI did not write expected readback: %s", string(body))
	}
}

func TestGatewayReplayBundleBindsSchedulerTelegramAndA2A(t *testing.T) {
	readback, err := BuildGatewayReplayBundleReadback(GatewayReplayBundleInputs{
		TelegramConfigPath:  filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		TelegramMatrixPath:  filepath.Join("..", "..", "examples", "valid", "telegram-command-matrix.json"),
		TelegramUpdatesPath: filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json"),
		TelegramWebhookPath: filepath.Join("..", "..", "examples", "valid", "telegram-webhook-replay.json"),
		A2AHTTPPath:         filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		A2ALifecyclePath:    filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle-artifacts.json"),
		SchedulerPath:       filepath.Join("..", "..", "examples", "valid", "scheduler-readback-replay.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if readback.Schema != "ao.mission.gateway-replay-bundle-readback.v0.1" || readback.Status != "ready" {
		t.Fatalf("bad replay bundle: %+v", readback)
	}
	if readback.TelegramReadbacks != 3 || readback.A2AReadbacks != 2 || readback.SchedulerReadbacks != 1 {
		t.Fatalf("replay bundle missing expected readback families: %+v", readback)
	}
	if readback.SafeToExecute || readback.ExecutesWork || readback.ApprovesWork || readback.MutatesRepositories {
		t.Fatalf("replay bundle widened authority: %+v", readback)
	}
	outPath := filepath.Join(t.TempDir(), "gateway-replay-bundle.json")
	var out, errb bytes.Buffer
	if code := Run([]string{
		"gateway", "replay-bundle",
		"--telegram-config", filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		"--telegram-matrix", filepath.Join("..", "..", "examples", "valid", "telegram-command-matrix.json"),
		"--telegram-updates", filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json"),
		"--a2a-http", filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		"--scheduler", filepath.Join("..", "..", "examples", "valid", "scheduler-readback-replay.json"),
		"--out", outPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("gateway replay-bundle CLI: %s", errb.String())
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"schema": "ao.mission.gateway-replay-bundle-readback.v0.1"`) {
		t.Fatalf("replay bundle CLI did not write expected readback: %s", string(body))
	}
}

func TestMissionDashboardReadbackSummarizesCompactOperatorLoop(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", dir, "start", "dashboard readback atlas loop"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "next", "--mission", rec.MissionID, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("next: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"--home", dir, "mission", "dashboard", "--mission", rec.MissionID, "--compact", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("mission dashboard: %s", errb.String())
	}
	var dashboard MissionDashboardReadback
	if err := json.Unmarshal(out.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.Schema != "ao.mission.dashboard-readback.v0.1" || dashboard.MissionID != rec.MissionID || dashboard.EventCount == 0 {
		t.Fatalf("bad dashboard readback: %+v", dashboard)
	}
	if !dashboard.Compact || dashboard.LatestRoute == "" || dashboard.EventIndexDigest == "" {
		t.Fatalf("dashboard missing compact route/index evidence: %+v", dashboard)
	}
	if dashboard.SafeToExecute || dashboard.ExecutesWork || dashboard.ApprovesWork || dashboard.MutatesRepositories {
		t.Fatalf("dashboard widened authority: %+v", dashboard)
	}
}

func TestGatewayReadinessRollupCombinesReadbacksWithoutAuthority(t *testing.T) {
	dir := t.TempDir()
	suitePath := filepath.Join(dir, "suite.json")
	compatPath := filepath.Join(dir, "compat.json")
	archiveValidationPath := filepath.Join(dir, "archive-validation.json")
	diffPath := filepath.Join(dir, "snapshot-diff.json")
	rollupPath := filepath.Join(dir, "gateway-readiness-rollup.json")
	var out, errb bytes.Buffer
	if code := Run([]string{
		"gateway", "replay-suite",
		"--telegram-config", filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		"--telegram-updates", filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json"),
		"--a2a-http", filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		"--out", suitePath,
	}, &out, &errb); code != 0 {
		t.Fatalf("gateway replay-suite: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{
		"a2a", "compatibility",
		"--agent-card", filepath.Join("..", "..", "examples", "valid", "a2a-agent-card.json"),
		"--http", filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		"--lifecycle", filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle-artifacts.json"),
		"--out", compatPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("a2a compatibility: %s", errb.String())
	}
	exportHome := filepath.Join(dir, "export-home")
	out.Reset()
	if code := Run([]string{"--home", exportHome, "start", "gateway readiness rollup"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "archive.json")
	out.Reset()
	if code := Run([]string{"--home", exportHome, "mission", "archive", "--mission", rec.MissionID, "--out", archivePath}, &out, &errb); code != 0 {
		t.Fatalf("archive: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{"mission", "validate-archive", "--path", archivePath, "--out", archiveValidationPath}, &out, &errb); code != 0 {
		t.Fatalf("validate archive: %s", errb.String())
	}
	beforePath := filepath.Join(dir, "before.json")
	afterPath := filepath.Join(dir, "after.json")
	before := Snapshot(rec)
	rec.CurrentRoute = "ao-atlas"
	after := Snapshot(rec)
	writeJSONForTest(t, beforePath, before)
	writeJSONForTest(t, afterPath, after)
	out.Reset()
	if code := Run([]string{"governance", "diff", "--before", beforePath, "--after", afterPath, "--out", diffPath}, &out, &errb); code != 0 {
		t.Fatalf("governance diff: %s", errb.String())
	}
	out.Reset()
	if code := Run([]string{
		"gateway", "readiness-rollup",
		"--mission", rec.MissionID,
		"--suite", suitePath,
		"--a2a-compatibility", compatPath,
		"--archive-validation", archiveValidationPath,
		"--snapshot-diff", diffPath,
		"--out", rollupPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("gateway readiness-rollup: %s", errb.String())
	}
	var rollup map[string]any
	body, err := os.ReadFile(rollupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup["schema"] != "ao.mission.gateway-readiness-rollup.v0.1" || rollup["status"] != "ready" {
		t.Fatalf("bad gateway readiness rollup: %#v", rollup)
	}
	if rollup["mission_id"] != rec.MissionID {
		t.Fatalf("gateway readiness rollup missing mission_id: %#v", rollup)
	}
	if rollup["readback_count"] != float64(4) || rollup["safe_to_execute"] != false || rollup["executes_work"] != false {
		t.Fatalf("gateway readiness rollup missing no-authority evidence: %#v", rollup)
	}
}

func TestGatewayReadinessRollupCarriesCorrelationID(t *testing.T) {
	dir := t.TempDir()
	readbackPath := filepath.Join(dir, "gateway-readback.json")
	outPath := filepath.Join(dir, "gateway-readiness-rollup.json")
	if err := os.WriteFile(readbackPath, []byte(`{
  "schema": "ao.mission.gateway-replay-suite-readback.v0.1",
  "status": "ready",
  "safe_to_execute": false,
  "executes_work": false,
  "approves_work": false,
  "mutation_authority": false
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := Run([]string{
		"gateway", "readiness-rollup",
		"--mission", "mission-demo",
		"--suite", readbackPath,
		"--correlation-id", "corr-gateway-001",
		"--out", outPath,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("gateway readiness-rollup with correlation failed: %s", errb.String())
	}
	var rollup map[string]any
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup["correlation_id"] != "corr-gateway-001" {
		t.Fatalf("rollup missing correlation_id: %#v", rollup)
	}
	if rollup["mission_id"] != "mission-demo" {
		t.Fatalf("rollup missing mission_id: %#v", rollup)
	}
	if rollup["safe_to_execute"] != false || rollup["executes_work"] != false || rollup["approves_work"] != false {
		t.Fatalf("correlated rollup widened authority: %#v", rollup)
	}
}

func TestGatewayReadinessRollupDerivesCorrelationIDFromTelegramReplaySuite(t *testing.T) {
	dir := t.TempDir()
	suitePath := filepath.Join(dir, "gateway-replay-suite.json")
	rollupPath := filepath.Join(dir, "gateway-readiness-rollup.json")
	telegramPath := filepath.Join("..", "..", "examples", "valid", "telegram-update-replay.json")
	var out, errb bytes.Buffer
	if code := Run([]string{
		"gateway", "replay-suite",
		"--telegram-config", filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		"--telegram-updates", telegramPath,
		"--out", suitePath,
	}, &out, &errb); code != 0 {
		t.Fatalf("gateway replay-suite: %s", errb.String())
	}
	var suite map[string]any
	body, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &suite); err != nil {
		t.Fatal(err)
	}
	if suite["correlation_id"] != "corr-telegram-replay-001" {
		t.Fatalf("replay suite did not carry Telegram correlation ID: %#v", suite)
	}
	out.Reset()
	if code := Run([]string{
		"gateway", "readiness-rollup",
		"--mission", "mission-demo",
		"--suite", suitePath,
		"--out", rollupPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("gateway readiness-rollup: %s", errb.String())
	}
	var rollup map[string]any
	body, err = os.ReadFile(rollupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &rollup); err != nil {
		t.Fatal(err)
	}
	if rollup["correlation_id"] != "corr-telegram-replay-001" {
		t.Fatalf("rollup did not derive correlation ID from replay suite: %#v", rollup)
	}
	if rollup["safe_to_execute"] != false || rollup["executes_work"] != false || rollup["approves_work"] != false {
		t.Fatalf("derived correlation rollup widened authority: %#v", rollup)
	}
}

func TestA2ACancellationReplayRequiresRequestAndCancelWithoutAuthority(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "a2a-cancellation-replay.json")
	var out, errb bytes.Buffer
	code := Run([]string{
		"a2a", "cancellation-replay",
		"--lifecycle", filepath.Join("..", "..", "examples", "valid", "a2a-task-lifecycle.json"),
		"--out", outPath,
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("a2a cancellation-replay failed: %s", errb.String())
	}
	var replay map[string]any
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatal(err)
	}
	if replay["schema"] != "ao.mission.a2a-cancellation-replay-readback.v0.1" || replay["status"] != "ready" {
		t.Fatalf("bad cancellation replay: %#v", replay)
	}
	if replay["cancel_requested"] != float64(1) || replay["cancelled"] != float64(1) {
		t.Fatalf("cancellation replay missing request/cancel counts: %#v", replay)
	}
	if replay["mutation_authority"] != false || replay["executes_work"] != false || replay["approves_work"] != false {
		t.Fatalf("cancellation replay widened authority: %#v", replay)
	}
}

func TestMissionV02ReadbackContractFixturesValidate(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "examples", "valid", "mission-event-index-v0.2.json"),
		filepath.Join("..", "..", "examples", "valid", "mission-readiness-bundle-readback.json"),
		filepath.Join("..", "..", "examples", "valid", "gateway-replay-bundle-readback.json"),
		filepath.Join("..", "..", "examples", "valid", "mission-dashboard-readback.json"),
		filepath.Join("..", "..", "examples", "valid", "mission-verification-bundle-readback.json"),
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			result, err := ValidateContractFile(path)
			if err != nil {
				t.Fatalf("valid fixture rejected: result=%+v err=%v", result, err)
			}
			if result.Status != "ready" || result.Executes || result.Approves || result.Mutates {
				t.Fatalf("bad contract validation result: %+v", result)
			}
		})
	}
}

func TestMissionV02ReadbackContractFixturesRejectAuthorityDrift(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "examples", "invalid", "mission-readiness-bundle-authority.json"),
		filepath.Join("..", "..", "examples", "invalid", "gateway-replay-bundle-authority.json"),
		filepath.Join("..", "..", "examples", "invalid", "mission-dashboard-authority.json"),
		filepath.Join("..", "..", "examples", "invalid", "mission-verification-bundle-authority.json"),
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			result, err := ValidateContractFile(path)
			if err == nil || result.Status != "blocked" {
				t.Fatalf("invalid fixture accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestGitHubIssueMonth1SupervisionReadbackKeepsFeaturePRsDraft(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "github-issue-month1-supervision-readback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var readback map[string]any
	if err := json.Unmarshal(body, &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.github-issue-month1-supervision-readback.v0.1" || readback["status"] != "passed" {
		t.Fatalf("bad GitHub issue Month 1 readback: %#v", readback)
	}
	if readback["feature_generated_prs_remain_draft"] != true ||
		readback["feature_generated_pr_auto_merge"] != false ||
		readback["feature_generated_pr_ready_for_review"] != false ||
		readback["feature_generated_pr_review_approval"] != false ||
		readback["github_issue_write_performed"] != false {
		t.Fatalf("feature-generated PR or issue-write boundary widened: %#v", readback)
	}
	for _, key := range []string{
		"external_maintainer_contacted",
		"security_public_disclosure_performed",
		"release_or_publish",
		"tag_created",
		"upload_performed",
		"deployment_performed",
		"provider_pilot",
		"external_beta_launched",
		"promotion_requested",
		"promotion_granted",
		"live_self_modification",
		"safe_to_execute",
		"executes_work",
		"approves_work",
		"mutates_repositories",
	} {
		if readback[key] != false {
			t.Fatalf("%s must remain false: %#v", key, readback)
		}
	}
	if readback["rsi_remains_denied"] != true || readback["month2_unlocked"] != true {
		t.Fatalf("missing RSI denial or Month 2 unlock: %#v", readback)
	}
}

func TestGitHubIssueMonth2SupervisionReadbackRequiresTruthSetAndNoMutation(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "valid", "github-issue-month2-supervision-readback.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var readback map[string]any
	if err := json.Unmarshal(body, &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.github-issue-month2-supervision-readback.v0.1" ||
		readback["status"] != "ready" ||
		readback["roadmap"] != "github-issue-to-draft-pr" {
		t.Fatalf("unexpected Month 2 supervision readback: %#v", readback)
	}
	truth := readback["truth_set"].(map[string]any)
	if truth["total"].(float64) != 13 ||
		truth["authentic_bug"].(float64) != 2 ||
		truth["non_bug_or_blocked"].(float64) != 11 ||
		truth["precision"].(float64) < 0.95 ||
		truth["recall"].(float64) < 0.90 ||
		truth["thresholds_passed"] != true {
		t.Fatalf("truth set did not meet closure thresholds: %#v", truth)
	}
	gate := readback["closure_gate"].(map[string]any)
	for _, key := range []string{
		"failing_pre_patch_reproduction_required",
		"authentic_bug_fixtures_reproduce_before_patch",
		"non_bug_fixtures_avoid_mutation",
		"interruption_resume_without_duplicate_mutation",
		"evidence_repeatable",
	} {
		if gate[key] != true {
			t.Fatalf("closure_gate.%s = %#v, want true", key, gate[key])
		}
	}
	if gate["security_sensitive_fixtures_enter_public_repair"] != false {
		t.Fatalf("security-sensitive fixtures must not enter public repair: %#v", gate)
	}
	denied := readback["denied_actions"].(map[string]any)
	for action, value := range denied {
		if value != false {
			t.Fatalf("denied_actions.%s = %#v, want false", action, value)
		}
	}
	if !strings.Contains(readback["exact_next_action"].(string), "Month 3 isolated repair") {
		t.Fatalf("next action should hand off to Month 3: %s", readback["exact_next_action"])
	}
}

func TestGitHubIssueMonth3SupervisionReadbackRequiresRepairEvidenceAndNoDraftPR(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "valid", "github-issue-month3-supervision-readback.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var readback map[string]any
	if err := json.Unmarshal(body, &readback); err != nil {
		t.Fatal(err)
	}
	if readback["schema"] != "ao.mission.github-issue-month3-supervision-readback.v0.1" ||
		readback["status"] != "ready" ||
		readback["roadmap"] != "github-issue-to-draft-pr" {
		t.Fatalf("unexpected Month 3 supervision readback: %#v", readback)
	}
	gate := readback["repair_gate"].(map[string]any)
	for _, key := range []string{
		"bounded_repair_workgraph_exists",
		"pre_patch_regression_failed_expected",
		"negative_control_passed",
		"post_patch_verification_passed",
		"rollback_exact_state_restored",
		"replay_digest_match",
		"resume_without_duplicate_edits",
		"false_fixes_rejected",
	} {
		if gate[key] != true {
			t.Fatalf("repair_gate.%s = %#v, want true", key, gate[key])
		}
	}
	if gate["feature_generated_draft_pr_exists"] != false {
		t.Fatalf("Month 3 must not create a feature-generated draft PR: %#v", gate)
	}
	denied := readback["denied_actions"].(map[string]any)
	for action, value := range denied {
		if value != false {
			t.Fatalf("denied_actions.%s = %#v, want false", action, value)
		}
	}
	if !strings.Contains(readback["exact_next_action"].(string), "Month 4") {
		t.Fatalf("next action should hand off to Month 4: %s", readback["exact_next_action"])
	}
}

func TestMissionVerificationBundleBindsReadbacksAndRejectsAuthorityDrift(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "home"))
	rec, err := s.Start("verification bundle operator handoff")
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(dir, "readiness.txt")
	if err := os.WriteFile(readyPath, []byte("status=ready\nscore=100/100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readiness, err := BuildMissionReadinessBundleReadback([]MissionReadinessBundleInput{{Repo: "ao-mission", Path: readyPath}})
	if err != nil {
		t.Fatal(err)
	}
	readinessPath := filepath.Join(dir, "readiness-bundle.json")
	writeJSONForTest(t, readinessPath, readiness)
	replay, err := BuildGatewayReplayBundleReadback(GatewayReplayBundleInputs{
		TelegramConfigPath: filepath.Join("..", "..", "examples", "valid", "telegram-config.json"),
		TelegramMatrixPath: filepath.Join("..", "..", "examples", "valid", "telegram-command-matrix.json"),
		A2AHTTPPath:        filepath.Join("..", "..", "examples", "valid", "a2a-http-integration.json"),
		SchedulerPath:      filepath.Join("..", "..", "examples", "valid", "scheduler-readback-replay.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayPath := filepath.Join(dir, "gateway-replay-bundle.json")
	writeJSONForTest(t, replayPath, replay)
	bundle, err := BuildMissionVerificationBundleReadback(s, rec.MissionID, MissionVerificationBundleOptions{
		ReadinessBundlePath:     readinessPath,
		GatewayReplayBundlePath: replayPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Schema != "ao.mission.verification-bundle-readback.v0.1" || bundle.Status != "ready" || bundle.ComponentCount < 5 {
		t.Fatalf("bad verification bundle: %+v", bundle)
	}
	if !strings.HasPrefix(bundle.BundleDigest, "sha256:") {
		t.Fatalf("verification bundle missing digest: %+v", bundle)
	}
	if bundle.SafeToExecute || bundle.ExecutesWork || bundle.ApprovesWork || bundle.MutatesRepositories {
		t.Fatalf("verification bundle widened authority: %+v", bundle)
	}

	var unsafe map[string]any
	body, err := os.ReadFile(readinessPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &unsafe); err != nil {
		t.Fatal(err)
	}
	unsafe["safe_to_execute"] = true
	unsafePath := filepath.Join(dir, "unsafe-readiness-bundle.json")
	writeJSONForTest(t, unsafePath, unsafe)
	if _, err := BuildMissionVerificationBundleReadback(s, rec.MissionID, MissionVerificationBundleOptions{ReadinessBundlePath: unsafePath}); err == nil {
		t.Fatal("verification bundle accepted authority drift")
	}
}

func TestMissionVerificationBundleCLIWritesDigestManifest(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	var out, errb bytes.Buffer
	if code := Run([]string{"--home", home, "start", "verification bundle cli"}, &out, &errb); code != 0 {
		t.Fatalf("start: %s", errb.String())
	}
	var rec Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(dir, "readiness.txt")
	if err := os.WriteFile(readyPath, []byte("AO Mission production readiness: 100/100 status=ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readiness, err := BuildMissionReadinessBundleReadback([]MissionReadinessBundleInput{{Repo: "ao-mission", Path: readyPath}})
	if err != nil {
		t.Fatal(err)
	}
	readinessPath := filepath.Join(dir, "readiness-bundle.json")
	writeJSONForTest(t, readinessPath, readiness)
	outPath := filepath.Join(dir, "verification-bundle.json")
	out.Reset()
	if code := Run([]string{
		"--home", home,
		"mission", "verification-bundle",
		"--mission", rec.MissionID,
		"--readiness-bundle", readinessPath,
		"--out", outPath,
	}, &out, &errb); code != 0 {
		t.Fatalf("mission verification-bundle: %s", errb.String())
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle MissionVerificationBundleReadback
	if err := json.Unmarshal(body, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.MissionID != rec.MissionID || bundle.ComponentCount == 0 || !strings.HasPrefix(bundle.BundleDigest, "sha256:") {
		t.Fatalf("bad CLI verification bundle: %+v", bundle)
	}
}

func digestBytesForTest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeJSONForTest(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func marshalMapForTest(t *testing.T, value any) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func seedMissionRecordStore(t *testing.T, count int) Store {
	t.Helper()
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		id := "mission-scale-" + strconv.Itoa(i)
		rec := Record{
			Schema:          RecordSchema,
			MissionID:       id,
			Objective:       "scale mission " + strconv.Itoa(i),
			ObjectiveDigest: DigestObjective("scale mission " + strconv.Itoa(i)),
			Status:          "active",
			CreatedAtUTC:    "2026-07-19T00:00:00Z",
			UpdatedAtUTC:    "2026-07-19T00:00:00Z",
			CurrentRoute:    "ao-atlas",
			CurrentPhase:    "routing",
			ExactNextAction: "continue bounded mission",
			ArtifactRefs:    []ArtifactRef{},
			Blockers:        []string{},
			Steps:           []ContinuationStep{},
		}
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "missions", id+".json"), append(body, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
