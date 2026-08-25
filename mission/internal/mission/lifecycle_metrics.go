package mission

import "fmt"

const MissionLifecycleMetricsSchema = "ao.mission.lifecycle-metrics.v0.1"

type MissionLifecycleMetrics struct {
	Schema                            string `json:"schema"`
	MissionID                         string `json:"mission_id"`
	Status                            string `json:"status"`
	HandoffSteps                      int    `json:"handoff_steps"`
	EvidenceCompletedNodes            int    `json:"evidence_completed_nodes"`
	CompletedNodes                    int    `json:"completed_nodes"`
	TotalNodes                        int    `json:"total_nodes"`
	ReadyNodes                        int    `json:"ready_nodes"`
	BlockedNodes                      int    `json:"blocked_nodes"`
	FailedNodes                       int    `json:"failed_nodes"`
	CompletionBasis                   string `json:"completion_basis"`
	HandoffStepsCountAsCompletedNodes bool   `json:"handoff_steps_count_as_completed_nodes"`
	FinalResponseAllowed              bool   `json:"final_response_allowed"`
	ReturnGateStatus                  string `json:"return_gate_status"`
	ExactNextAction                   string `json:"exact_next_action"`
	SafeToExecute                     bool   `json:"safe_to_execute"`
	ExecutesWork                      bool   `json:"executes_work"`
	ApprovesWork                      bool   `json:"approves_work"`
	MutatesRepositories               bool   `json:"mutates_repositories"`
	RSIRemainsDenied                  bool   `json:"rsi_remains_denied"`
	GeneratedAtUTC                    string `json:"generated_at_utc"`
}

func BuildMissionLifecycleMetrics(r Record) MissionLifecycleMetrics {
	gate := EvaluateReturnGate(r)
	completed := completedEvidenceNodes(r)
	total, blocked, failed := missionNodeTotals(r)
	exactNextAction := gate.ExactNextAction
	if gate.FinalResponseAllowed {
		exactNextAction = ""
	}
	return MissionLifecycleMetrics{
		Schema:                            MissionLifecycleMetricsSchema,
		MissionID:                         r.MissionID,
		Status:                            "audited",
		HandoffSteps:                      len(r.Steps),
		EvidenceCompletedNodes:            completed,
		CompletedNodes:                    completed,
		TotalNodes:                        total,
		ReadyNodes:                        readyNodesRemaining(r),
		BlockedNodes:                      blocked,
		FailedNodes:                       failed,
		CompletionBasis:                   "downstream_evidence_not_handoff_steps",
		HandoffStepsCountAsCompletedNodes: false,
		FinalResponseAllowed:              gate.FinalResponseAllowed,
		ReturnGateStatus:                  gate.Status,
		ExactNextAction:                   exactNextAction,
		SafeToExecute:                     false,
		ExecutesWork:                      false,
		ApprovesWork:                      false,
		MutatesRepositories:               false,
		RSIRemainsDenied:                  true,
		GeneratedAtUTC:                    now(nil),
	}
}

func missionNodeTotals(r Record) (total, blocked, failed int) {
	if r.Evidence.AtlasWorkgraph != nil {
		total = r.Evidence.AtlasWorkgraph.Total
		blocked = r.Evidence.AtlasWorkgraph.Blocked
		failed = r.Evidence.AtlasWorkgraph.Failed
	}
	if r.Evidence.AtlasRecommendation != nil && r.Evidence.AtlasRecommendation.TotalNodes > total {
		total = r.Evidence.AtlasRecommendation.TotalNodes
	}
	if r.Evidence.FoundryRollup != nil && r.Evidence.FoundryRollup.TotalNodes > total {
		total = r.Evidence.FoundryRollup.TotalNodes
	}
	return total, blocked, failed
}

func ValidateMissionLifecycleMetrics(metrics MissionLifecycleMetrics) error {
	if metrics.Schema != MissionLifecycleMetricsSchema {
		return fmt.Errorf("mission lifecycle metrics schema must be %s", MissionLifecycleMetricsSchema)
	}
	if metrics.MissionID == "" {
		return fmt.Errorf("mission lifecycle metrics mission_id is required")
	}
	if metrics.Status != "audited" {
		return fmt.Errorf("mission lifecycle metrics status must be audited")
	}
	for name, value := range map[string]int{
		"handoff_steps":            metrics.HandoffSteps,
		"evidence_completed_nodes": metrics.EvidenceCompletedNodes,
		"completed_nodes":          metrics.CompletedNodes,
		"total_nodes":              metrics.TotalNodes,
		"ready_nodes":              metrics.ReadyNodes,
		"blocked_nodes":            metrics.BlockedNodes,
		"failed_nodes":             metrics.FailedNodes,
	} {
		if value < 0 {
			return fmt.Errorf("mission lifecycle metrics %s must be non-negative", name)
		}
	}
	if metrics.CompletedNodes != metrics.EvidenceCompletedNodes {
		return fmt.Errorf("completed_nodes must equal evidence_completed_nodes")
	}
	if metrics.HandoffStepsCountAsCompletedNodes {
		return fmt.Errorf("handoff_steps_count_as_completed_nodes must be false")
	}
	if metrics.CompletionBasis != "downstream_evidence_not_handoff_steps" {
		return fmt.Errorf("completion_basis must describe downstream evidence")
	}
	if metrics.TotalNodes > 0 && metrics.CompletedNodes > metrics.TotalNodes {
		return fmt.Errorf("completed_nodes must not exceed total_nodes")
	}
	if metrics.FinalResponseAllowed && (metrics.ReadyNodes > 0 || metrics.ExactNextAction != "") {
		return fmt.Errorf("final response cannot be allowed with ready nodes or an exact next action")
	}
	if metrics.SafeToExecute || metrics.ExecutesWork || metrics.ApprovesWork || metrics.MutatesRepositories {
		return fmt.Errorf("lifecycle metrics must not claim execution or approval authority")
	}
	if !metrics.RSIRemainsDenied {
		return fmt.Errorf("rsi_remains_denied must be true")
	}
	return nil
}
