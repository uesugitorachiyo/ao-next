package mission

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func BuildAtlasWaveFinalSynthesis(r Record, evidenceRoot string) (AtlasWaveFinalSynthesis, error) {
	if strings.TrimSpace(evidenceRoot) == "" {
		return AtlasWaveFinalSynthesis{}, errors.New("final synthesize requires --evidence-root")
	}
	workgraph, err := readPublicSafeJSONMap(filepath.Join(evidenceRoot, "workgraph.json"))
	if err != nil {
		return AtlasWaveFinalSynthesis{}, err
	}
	closure, _ := readPublicSafeJSONMap(filepath.Join(evidenceRoot, "post-merge-final-closure.json"))
	mission := stringFromAny(workgraph["mission"])
	if mission == "" {
		mission = r.MissionID
	}
	completed := intFromAny(workgraph["completed_nodes"])
	ready := intFromAny(workgraph["ready_nodes"])
	blocked := intFromAny(workgraph["blocked_nodes"])
	finalAllowed := boolFromAny(workgraph["final_response_allowed"])
	mergedPRs := intsFromAny(closure["merged_prs"])
	if completedFromClosure := intFromAny(closure["completed_nodes"]); completedFromClosure > completed {
		completed = completedFromClosure
	}
	if closure != nil {
		ready = intFromAny(closure["ready_nodes"])
		blocked = intFromAny(closure["blocked_nodes"])
		finalAllowed = boolFromAny(closure["final_response_allowed"])
	}
	status := "active"
	exactNextAction := stringFromAny(workgraph["exact_next_action"])
	if finalAllowed && ready == 0 && blocked == 0 {
		status = "completed"
		exactNextAction = "use next-wave-recommended-prompt.md for the next AO Atlas wave"
	}
	recommendations := buildFeatureDepthRecommendations(r, 20, evidenceRoot)
	if err := ValidateFeatureDepthRecommendations(recommendations, 20); err != nil {
		return AtlasWaveFinalSynthesis{}, err
	}
	return AtlasWaveFinalSynthesis{
		Schema:                                "ao.mission.atlas-wave-final-synthesis.v0.1",
		Mission:                               mission,
		CorrelationID:                         r.CorrelationID,
		Status:                                status,
		MissionID:                             r.MissionID,
		CompletedNodes:                        completed,
		ReadyNodes:                            ready,
		BlockedNodes:                          blocked,
		MinimumNodes:                          intFromAny(workgraph["minimum_nodes"]),
		TargetMinutes:                         intFromAny(workgraph["target_minutes"]),
		MaxMinutes:                            intFromAny(workgraph["max_minutes"]),
		FinalResponseAllowed:                  finalAllowed,
		AtlasWorkgraphStatus:                  stringFromAny(workgraph["status"]),
		FoundryRollup:                         "readback-only wave; no promotion requested",
		PromoterStatus:                        "no_promotion_requested",
		CommandReadback:                       "ready",
		EventSearchBound:                      true,
		BranchCleanupBoundThroughPreviousNode: len(mergedPRs) > 0,
		MergedPRsFinal:                        mergedPRs,
		CurrentNodeBranch:                     "none",
		CurrentNodePRPending:                  false,
		PromotionClaimed:                      false,
		ClaimsAuthorityAdvance:                false,
		RSIRemainsDenied:                      true,
		SafeToExecute:                         false,
		ExecutesWork:                          false,
		ApprovesWork:                          false,
		MutatesRepositories:                   false,
		FeatureDepthRecommendations:           recommendations,
		ExactNextAction:                       exactNextAction,
		GeneratedAtUTC:                        now(nil),
	}, nil
}

func readPublicSafeJSONMap(path string) (map[string]any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func intsFromAny(v any) []int {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(items))
	for _, item := range items {
		n := intFromAny(item)
		if n != 0 {
			out = append(out, n)
		}
	}
	return out
}
