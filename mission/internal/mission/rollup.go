package mission

type FinalRollup struct {
	Schema                      string                       `json:"schema"`
	MissionID                   string                       `json:"mission_id"`
	CorrelationID               string                       `json:"correlation_id,omitempty"`
	Status                      string                       `json:"status"`
	CompletedSteps              int                          `json:"completed_steps"`
	ArtifactRefs                []ArtifactRef                `json:"artifact_refs"`
	HighestProvenLiveClass      string                       `json:"highest_proven_live_class"`
	NextDeniedClass             string                       `json:"next_denied_class"`
	SafeToExecute               bool                         `json:"safe_to_execute"`
	ExecutesWork                bool                         `json:"executes_work"`
	ApprovesWork                bool                         `json:"approves_work"`
	ProviderCalls               bool                         `json:"provider_calls"`
	FinalResponseAllowed        bool                         `json:"final_response_allowed"`
	ReturnGateStatus            string                       `json:"return_gate_status"`
	ReadyNodesRemaining         int                          `json:"ready_nodes_remaining"`
	CompletedNodes              int                          `json:"completed_nodes"`
	TotalNodes                  int                          `json:"total_nodes"`
	AtlasContinuationPrompt     string                       `json:"atlas_continuation_prompt"`
	FeatureDepthRecommendations []FeatureDepthRecommendation `json:"feature_depth_recommendations"`
	ExactNextAction             string                       `json:"exact_next_action"`
	RemainingDeniedSurfaces     []string                     `json:"remaining_denied_surfaces"`
	GeneratedAtUTC              string                       `json:"generated_at_utc"`
}

func BuildFinalRollup(r Record) FinalRollup {
	return buildFinalRollup(r, "")
}

func buildFinalRollup(r Record, evidenceRoot string) FinalRollup {
	gate := EvaluateReturnGate(r)
	recommendations := buildFeatureDepthRecommendations(r, defaultMinNodes, evidenceRoot)
	exactNextAction := r.ExactNextAction
	if !gate.FinalResponseAllowed {
		exactNextAction = gate.ExactNextAction
	} else {
		exactNextAction = ""
	}
	totalNodes := 0
	if r.Evidence.AtlasWorkgraph != nil {
		totalNodes = r.Evidence.AtlasWorkgraph.Total
	}
	if r.Evidence.FoundryRollup != nil && r.Evidence.FoundryRollup.TotalNodes > totalNodes {
		totalNodes = r.Evidence.FoundryRollup.TotalNodes
	}
	return FinalRollup{
		Schema:                      "ao.mission.final-rollup.v0.1",
		MissionID:                   r.MissionID,
		CorrelationID:               r.CorrelationID,
		Status:                      r.Status,
		CompletedSteps:              len(r.Steps),
		ArtifactRefs:                r.ArtifactRefs,
		HighestProvenLiveClass:      "public_safe_unrestricted_self_modification_authority_request_dry_run_four_attempts",
		NextDeniedClass:             "unrestricted_self_modification",
		SafeToExecute:               false,
		ExecutesWork:                false,
		ApprovesWork:                false,
		ProviderCalls:               false,
		FinalResponseAllowed:        gate.FinalResponseAllowed,
		ReturnGateStatus:            gate.Status,
		ReadyNodesRemaining:         gate.ReadyNodesRemaining,
		CompletedNodes:              gate.CompletedNodes,
		TotalNodes:                  totalNodes,
		AtlasContinuationPrompt:     "AO Atlas: load Mission record, event index, final rollup, and current readiness; select exactly one ready bounded node; return Foundry import material and no-authority readback.",
		FeatureDepthRecommendations: recommendations,
		ExactNextAction:             exactNextAction,
		RemainingDeniedSurfaces: []string{
			"unrestricted_self_modification",
			"hidden_instruction_mutation",
			"policy_changing_autonomy",
			"provider_calls",
			"credential_use",
			"release_deploy_publish_upload_tag",
			"dependency_updates",
			"direct_main_mutation",
			"concurrent_mutation",
			"broad_RSI",
		},
		GeneratedAtUTC: now(nil),
	}
}
