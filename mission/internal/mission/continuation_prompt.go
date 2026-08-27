package mission

import (
	"encoding/json"
	"fmt"
	"strings"
)

func BuildAtlasContinuationPromptPacket(r Record, index MissionEventIndex) (AtlasContinuationPromptPacket, error) {
	return buildAtlasContinuationPromptPacket(r, index, BuildFinalRollup(r))
}

func buildAtlasContinuationPromptPacket(r Record, index MissionEventIndex, rollup FinalRollup) (AtlasContinuationPromptPacket, error) {
	if err := ValidateMissionEventIndexDigest(index); err != nil {
		return AtlasContinuationPromptPacket{}, err
	}
	missionEventCount := 0
	for _, event := range index.Events {
		if event.MissionID == r.MissionID {
			missionEventCount++
		}
	}
	if missionEventCount == 0 {
		return AtlasContinuationPromptPacket{}, fmt.Errorf("event index does not contain mission %s", r.MissionID)
	}
	if err := ValidateFeatureDepthRecommendations(rollup.FeatureDepthRecommendations, defaultMinNodes); err != nil {
		return AtlasContinuationPromptPacket{}, fmt.Errorf("atlas continuation prompt feature depth invalid: %w", err)
	}
	rollupDigest, err := digestJSONValue(rollup)
	if err != nil {
		return AtlasContinuationPromptPacket{}, err
	}
	prompt := buildAtlasContinuationPromptText(r, rollup, index.IndexDigest, rollupDigest)
	packet := AtlasContinuationPromptPacket{
		Schema:                      "ao.mission.atlas-continuation-prompt-packet.v0.1",
		MissionID:                   r.MissionID,
		Status:                      "ready",
		CurrentRoute:                r.CurrentRoute,
		CurrentPhase:                r.CurrentPhase,
		EventIndexDigest:            index.IndexDigest,
		FinalRollupDigest:           rollupDigest,
		CompletedNodes:              rollup.CompletedNodes,
		TotalNodes:                  rollup.TotalNodes,
		ReadyNodesRemaining:         rollup.ReadyNodesRemaining,
		FinalResponseAllowed:        rollup.FinalResponseAllowed,
		ReturnGateStatus:            rollup.ReturnGateStatus,
		ExactNextAction:             rollup.ExactNextAction,
		Prompt:                      prompt,
		FeatureDepthRecommendations: append([]FeatureDepthRecommendation(nil), rollup.FeatureDepthRecommendations...),
		SafeToExecute:               false,
		ExecutesWork:                false,
		ApprovesWork:                false,
		MutatesRepositories:         false,
		GeneratedAtUTC:              now(nil),
	}
	if strings.TrimSpace(packet.Prompt) == "" {
		return AtlasContinuationPromptPacket{}, fmt.Errorf("atlas continuation prompt must not be empty")
	}
	return packet, nil
}

func digestJSONValue(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func buildAtlasContinuationPromptText(r Record, rollup FinalRollup, eventIndexDigest, finalRollupDigest string) string {
	return fmt.Sprintf("You are AO Atlas, continuing AO Mission %s.\n\nLoad the current Mission record, event index digest, final rollup digest, and readiness state before selecting work.\n\nEvidence bindings:\n- event_index_digest=%s\n- final_rollup_digest=%s\n\nCurrent readiness:\n- current_route=%s\n- current_phase=%s\n- completed_nodes=%d\n- total_nodes=%d\n- ready_nodes=%d\n- final_response_allowed=%t\n- return_gate_status=%s\n- exact_next_action=%s\n\nDo not produce a final response if ready_nodes > 0 or exact_next_action remains.\nSelect exactly one bounded Atlas-owned sequencing node or emit a Foundry import for exactly one ready implementation node. Keep safe_to_execute=false, executes_work=false, approves_work=false, mutates_repositories=false, and RSI denied unless separate governed evidence proves otherwise.",
		r.MissionID,
		eventIndexDigest,
		finalRollupDigest,
		r.CurrentRoute,
		r.CurrentPhase,
		rollup.CompletedNodes,
		rollup.TotalNodes,
		rollup.ReadyNodesRemaining,
		rollup.FinalResponseAllowed,
		rollup.ReturnGateStatus,
		rollup.ExactNextAction,
	)
}
