package mission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestRoutingParityAcrossOwnershipBoundary(t *testing.T) {
	tests := []struct {
		name       string
		objective  string
		route      string
		reason     string
		nextAction string
	}{
		{
			name:       "underspecified",
			objective:  "Figure it out",
			route:      "ao-blueprint",
			reason:     "objective is underspecified",
			nextAction: "send objective to AO Blueprint for requirements and authorization",
		},
		{
			name:       "workgraph",
			objective:  "Build a bounded implementation workgraph",
			route:      "ao-atlas",
			reason:     "objective requires workgraph, context, or long-running task management",
			nextAction: "send authorized pack to AO Atlas",
		},
		{
			name:       "ready node",
			objective:  "Please process this ready node now",
			route:      "ao-foundry",
			reason:     "ready workgraph node is present",
			nextAction: "send first safe node to AO Foundry",
		},
		{
			name:       "specified default",
			objective:  "Update the operator documentation example",
			route:      "ao-atlas",
			reason:     "specified objective should be sequenced by AO Atlas before Foundry execution",
			nextAction: "send objective to AO Atlas for workgraph sequencing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := DecideRoute("mission-parity", tt.objective, nil)
			if decision.Schema != RouteSchema ||
				decision.MissionID != "mission-parity" ||
				decision.Route != tt.route ||
				decision.Reason != tt.reason ||
				decision.ExactNextAction != tt.nextAction {
				t.Fatalf("route parity changed: %+v", decision)
			}
			if !decision.SafeToRequest || decision.SafeToExecute || decision.SafeToPromote {
				t.Fatalf("route authority boundary changed: %+v", decision)
			}
		})
	}
}

func TestCampaignLeaseGateParityAcrossOwnershipBoundary(t *testing.T) {
	tests := []struct {
		name        string
		record      Record
		status      string
		allowed     bool
		hardBlocker bool
		completed   int
		ready       int
		reason      string
		exactNext   string
	}{
		{
			name: "lease met",
			record: Record{
				MissionID: "mission-lease-met",
				Status:    "active",
				GoalLease: &GoalLease{MinNodes: 2},
				Evidence: EvidenceSummary{
					AtlasWorkgraph: &NodeCounts{Total: 2, Completed: 2},
				},
			},
			status:    "return_allowed",
			allowed:   true,
			completed: 2,
			reason:    "mission has no ready work, no unmet lease minimum, and no exact next action",
			exactNext: "read final rollup and preserve denied authority boundaries",
		},
		{
			name: "atlas gate denied",
			record: Record{
				MissionID:       "mission-atlas-denied",
				Status:          "active",
				ExactNextAction: "refresh stale checkpoint",
				GoalLease:       &GoalLease{MinNodes: 2},
				Evidence: EvidenceSummary{
					AtlasRecommendation: &AtlasRecommendationReadbackCounts{
						CompletedNodes:       2,
						ElapsedMinutes:       30,
						LeaseTimeStatus:      "minimum_unmet",
						ReturnGateStatus:     "early_return_denied",
						FinalResponseAllowed: false,
						ExactNextAction:      "refresh stale checkpoint",
					},
				},
			},
			status:    "early_return_denied",
			allowed:   false,
			completed: 2,
			reason:    "Atlas recommendation readback return gate blocked: early_return_denied lease_time_status=minimum_unmet elapsed_minutes=30",
			exactNext: "continue mission: refresh stale checkpoint",
		},
		{
			name: "hard blocker allows operator return",
			record: Record{
				MissionID: "mission-blocked",
				Status:    "blocked",
				Blockers:  []string{"terminal infrastructure blocker"},
				GoalLease: &GoalLease{MinNodes: 10},
			},
			status:      "return_allowed",
			allowed:     true,
			hardBlocker: true,
			reason:      "mission has a terminal hard blocker for operator review",
			exactNext:   "read final rollup and preserve denied authority boundaries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := EvaluateReturnGate(tt.record)
			if gate.Status != tt.status ||
				gate.FinalResponseAllowed != tt.allowed ||
				gate.HardBlocker != tt.hardBlocker ||
				gate.CompletedNodes != tt.completed ||
				gate.ReadyNodesRemaining != tt.ready ||
				gate.Reason != tt.reason ||
				gate.ExactNextAction != tt.exactNext {
				t.Fatalf("return gate parity changed: %+v", gate)
			}
		})
	}
}

func TestContinuationCheckpointTransactionParityAcrossOwnershipBoundary(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("Supervise a bounded implementation workgraph")
	if err != nil {
		t.Fatal(err)
	}

	continued, err := Continue(store, record.MissionID, ContinueOptions{
		UntilDone:     true,
		MaxIterations: 1,
		MinNodes:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.LoadCheckpointBundle(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	event, err := store.LoadEventLoopDecision(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}

	if len(continued.Steps) != 1 || len(persisted.Steps) != 1 ||
		len(persisted.Checkpoints) != 1 || bundle.CheckpointCount != 1 ||
		bundle.LatestCheckpoint == nil {
		t.Fatalf("transaction did not persist one complete continuation: record=%+v bundle=%+v", persisted, bundle)
	}
	step := persisted.Steps[0]
	checkpoint := persisted.Checkpoints[0]
	if step.Iteration != checkpoint.Iteration ||
		step.Iteration != event.Iteration ||
		step.Route != checkpoint.Route ||
		step.Route != event.Route ||
		step.ExactNextAction != checkpoint.ExactNextAction ||
		step.ExactNextAction != event.ExactNextAction ||
		step.GeneratedAtUTC != checkpoint.GeneratedAtUTC ||
		step.GeneratedAtUTC != event.GeneratedAtUTC {
		t.Fatalf("record/checkpoint/event transaction parity changed: step=%+v checkpoint=%+v event=%+v", step, checkpoint, event)
	}
	if persisted.GoalLease == nil || persisted.GoalLease.MinNodes != 3 ||
		persisted.ReturnGate == nil || persisted.ReturnGate.FinalResponseAllowed ||
		bundle.ReturnGate == nil || bundle.ReturnGate.FinalResponseAllowed ||
		event.ExecutesWork || event.ApprovesWork || event.MutatesRepositories {
		t.Fatalf("continuation authority or gate parity changed: record=%+v bundle=%+v event=%+v", persisted, bundle, event)
	}
}

func TestCorrelationContinuationPromptProjectionParity(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("Build a bounded Atlas workgraph")
	if err != nil {
		t.Fatal(err)
	}
	index, err := BuildMissionEventIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	rollup := BuildFinalRollup(record)
	rollup.GeneratedAtUTC = "2026-07-27T00:00:00Z"

	packet, err := buildAtlasContinuationPromptPacket(record, index, rollup)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(rollup)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	wantRollupDigest := "sha256:" + hex.EncodeToString(sum[:])

	if packet.EventIndexDigest != index.IndexDigest ||
		packet.FinalRollupDigest != wantRollupDigest ||
		!strings.Contains(packet.Prompt, "event_index_digest="+index.IndexDigest) ||
		!strings.Contains(packet.Prompt, "final_rollup_digest="+wantRollupDigest) ||
		packet.ExactNextAction != rollup.ExactNextAction ||
		len(packet.FeatureDepthRecommendations) != len(rollup.FeatureDepthRecommendations) {
		t.Fatalf("prompt evidence projection parity changed: %+v", packet)
	}
	if packet.SafeToExecute || packet.ExecutesWork || packet.ApprovesWork || packet.MutatesRepositories {
		t.Fatalf("prompt projection widened authority: %+v", packet)
	}
}

func TestCLIFamilyDispatchErrorParity(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "mission",
			args: []string{"mission"},
			want: "error: mission requires list or inspect\n",
		},
		{
			name: "continuation",
			args: []string{"checkpoint"},
			want: "error: checkpoint requires create or inspect\n",
		},
		{
			name: "correlation",
			args: []string{"correlation"},
			want: "error: correlation requires build or validate\n",
		},
		{
			name: "unknown",
			args: []string{"not-a-command"},
			want: "error: unknown command \"not-a-command\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, &stdout, &stderr); code != 1 {
				t.Fatalf("exit code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || stderr.String() != tt.want {
				t.Fatalf("CLI parity changed: stdout=%q stderr=%q want_stderr=%q", stdout.String(), stderr.String(), tt.want)
			}
		})
	}
}
