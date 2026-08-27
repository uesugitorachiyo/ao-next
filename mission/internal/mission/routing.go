package mission

import (
	"fmt"
	"strings"
)

func DecideRoute(missionID, objective string, artifacts []ArtifactRef) RouteDecision {
	lower := strings.ToLower(objective)
	words := len(strings.Fields(objective))
	decision := RouteDecision{Schema: RouteSchema, MissionID: missionID, SafeToRequest: true, SafeToExecute: false, SafeToPromote: false, GeneratedAtUTC: now(nil)}
	switch {
	case words < 4 || strings.Contains(lower, "figure out") || strings.Contains(lower, "not sure"):
		decision.Route = "ao-blueprint"
		decision.Reason = "objective is underspecified"
		decision.ExactNextAction = "send objective to AO Blueprint for requirements and authorization"
	case strings.Contains(lower, "workgraph") || strings.Contains(lower, "long-running") || strings.Contains(lower, "long run") || strings.Contains(lower, "batch") || strings.Contains(lower, "implementation") || strings.Contains(lower, "evidence node") || strings.Contains(lower, "bounded") || strings.Contains(lower, "supervise") || strings.Contains(lower, "oversized") || strings.Contains(lower, "mutation-class") || strings.Contains(lower, "atlas"):
		decision.Route = "ao-atlas"
		decision.Reason = "objective requires workgraph, context, or long-running task management"
		decision.ExactNextAction = "send authorized pack to AO Atlas"
	case strings.Contains(lower, "ready node") || strings.Contains(lower, "foundry import"):
		decision.Route = "ao-foundry"
		decision.Reason = "ready workgraph node is present"
		decision.ExactNextAction = "send first safe node to AO Foundry"
	default:
		decision.Route = "ao-atlas"
		decision.Reason = "specified objective should be sequenced by AO Atlas before Foundry execution"
		decision.ExactNextAction = "send objective to AO Atlas for workgraph sequencing"
	}
	return decision
}

func NextAction(r Record) RouteDecision { return DecideRoute(r.MissionID, r.Objective, r.ArtifactRefs) }

func DecideObjectiveWorkflow(missionID, correlationID, objective, generatedAt string) ObjectiveWorkflowContract {
	lower := strings.ToLower(objective)
	words := len(strings.Fields(objective))
	contract := ObjectiveWorkflowContract{
		Schema:            ObjectiveWorkflowSchema,
		Status:            "ready",
		MissionID:         missionID,
		CorrelationID:     correlationID,
		ObjectiveDigest:   DigestObjective(objective),
		AcceptanceStatus:  "accepted",
		LifecycleCommands: objectiveLifecycleCommands(missionID),
		SafeToExecute:     false,
		ExecutesWork:      false,
		ApprovesWork:      false,
		GeneratedAtUTC:    generatedAt,
	}
	switch {
	case words < 4 || strings.Contains(lower, "figure out") || strings.Contains(lower, "not sure"):
		contract.RoutingClass = "pending_blueprint"
		contract.AcceptanceStatus = "pending_blueprint"
		contract.InitialRoute = "ao-blueprint"
		contract.ExactNextAction = "send objective to AO Blueprint for requirements and acceptance"
		contract.Stages = objectiveStages("required", "conditional", "conditional")
	case strings.Contains(lower, "workgraph") || strings.Contains(lower, "long-running") ||
		strings.Contains(lower, "long run") || strings.Contains(lower, "multi-file") ||
		strings.Contains(lower, "implementation") || strings.Contains(lower, "evidence node") ||
		strings.Contains(lower, "bounded") || strings.Contains(lower, "supervise") ||
		strings.Contains(lower, "oversized") || strings.Contains(lower, "mutation-class") ||
		strings.Contains(lower, "atlas"):
		contract.RoutingClass = "complex"
		contract.InitialRoute = "ao-atlas"
		contract.ExactNextAction = "send accepted objective to AO Atlas for workgraph sequencing"
		contract.Stages = objectiveStages("omitted", "required", "conditional")
	default:
		contract.RoutingClass = "reduced"
		contract.InitialRoute = "ao-foundry"
		contract.ExactNextAction = "send accepted reduced objective directly to AO Foundry"
		contract.Stages = objectiveStages("omitted", "omitted", "required")
	}
	return contract
}

func objectiveStages(blueprint, atlas, foundry string) []ObjectiveWorkflowStage {
	return []ObjectiveWorkflowStage{
		objectiveStage("ao-blueprint", blueprint),
		objectiveStage("ao-atlas", atlas),
		objectiveStage("ao-foundry", foundry),
		objectiveStage("ao-forge", "conditional"),
		objectiveStage("ao-covenant", "conditional"),
		objectiveStage("ao2", "conditional"),
		objectiveStage("tests", "required"),
		objectiveStage("ao-control-plane", "conditional"),
		objectiveStage("ao-command", "required"),
		objectiveStage("ao-mission-reconciliation", "required"),
	}
}

func objectiveStage(name, status string) ObjectiveWorkflowStage {
	reasons := map[string]string{
		"required":    "the routing class requires this stage",
		"conditional": "the stage is required only when the objective or resulting evidence activates it",
		"omitted":     "the accepted routing class does not require this stage",
	}
	return ObjectiveWorkflowStage{Name: name, Status: status, Reason: reasons[status]}
}

func objectiveLifecycleCommands(missionID string) []string {
	return []string{
		fmt.Sprintf("ao-mission status --mission %s --json", missionID),
		fmt.Sprintf("ao-mission continue --mission %s", missionID),
		fmt.Sprintf("ao-mission pause --mission %s", missionID),
		fmt.Sprintf("ao-mission resume --mission %s", missionID),
		fmt.Sprintf("ao-mission mission verification-bundle --mission %s --json", missionID),
		fmt.Sprintf("ao-mission final reconcile --mission %s", missionID),
	}
}

func NextActionForRecord(r Record) RouteDecision {
	if r.WorkflowContract == nil && len(r.ArtifactRefs) == 0 {
		return NextAction(r)
	}
	return RouteDecision{
		Schema:          RouteSchema,
		MissionID:       r.MissionID,
		Route:           r.CurrentRoute,
		Reason:          "follow persisted workflow state",
		SafeToRequest:   true,
		SafeToExecute:   false,
		SafeToPromote:   false,
		ExactNextAction: r.ExactNextAction,
		GeneratedAtUTC:  now(nil),
	}
}

func AppendRouteHistory(r *Record, decision RouteDecision) {
	if decision.Schema == "" {
		decision.Schema = RouteSchema
	}
	if decision.MissionID == "" {
		decision.MissionID = r.MissionID
	}
	if decision.CorrelationID == "" {
		decision.CorrelationID = r.CorrelationID
	}
	if decision.GeneratedAtUTC == "" {
		decision.GeneratedAtUTC = now(nil)
	}
	r.RouteHistory = append(r.RouteHistory, decision)
}
