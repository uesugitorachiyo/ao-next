package mission

import (
	"errors"
	"fmt"
	"strings"
)

const pauseNextAction = "resume mission before continuation"
const pauseBoundaryReason = "mission pause boundary"

type ContinueOptions struct {
	UntilDone        bool
	MaxIterations    int
	MinNodes         int
	MinMinutes       int
	MinMinutesSet    bool
	MaxMinutes       int
	ReturnOnlyWhen   string
	CheckpointPolicy string
}

func Continue(s Store, missionID string, opts ContinueOptions) (Record, error) {
	if opts.MinMinutes < 0 {
		return Record{}, errors.New("min-minutes must be zero or greater")
	}
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = 1
	}
	var record Record
	for i := 0; i < opts.MaxIterations; i++ {
		iterationAdded := false
		var err error
		record, err = s.updateWithCheckpointAndEventDecisionTransaction(missionID, func(r *Record) (*EventLoopDecision, error) {
			if r.Status == "stopped" {
				return nil, errors.New("mission is stopped")
			}
			if r.Status == "paused" {
				return nil, errors.New("mission is paused")
			}
			var eventDecision *EventLoopDecision
			if _, err := ensureGoalLease(r, opts); err != nil {
				return nil, err
			}
			if r.Status != "done" && !hardBlockerExists(*r) {
				decision := NextActionForRecord(*r)
				step := ContinuationStep{Schema: StepSchema, MissionID: r.MissionID, CorrelationID: r.CorrelationID, Iteration: len(r.Steps) + 1, Route: decision.Route, Result: "handoff_required", ExactNextAction: decision.ExactNextAction, GeneratedAtUTC: now(s.Clock)}
				r.Steps = append(r.Steps, step)
				r.CurrentRoute = decision.Route
				r.CurrentPhase = "handoff_required"
				r.ExactNextAction = decision.ExactNextAction
				appendMissionCheckpoint(r, step)
				gate := EvaluateReturnGate(*r)
				r.ReturnGate = &gate
				reconciliation := BuildRouteReconciliation(*r)
				r.Reconciliation = &reconciliation
				decisionRecord := EventLoopDecision{
					Schema:              EventLoopDecisionSchema,
					MissionID:           r.MissionID,
					CorrelationID:       r.CorrelationID,
					Iteration:           step.Iteration,
					Status:              step.Result,
					Route:               step.Route,
					ExactNextAction:     step.ExactNextAction,
					ExecutesWork:        false,
					ApprovesWork:        false,
					MutatesRepositories: false,
					GeneratedAtUTC:      step.GeneratedAtUTC,
				}
				eventDecision = &decisionRecord
				iterationAdded = true
			}
			gate := EvaluateReturnGate(*r)
			r.ReturnGate = &gate
			reconciliation := BuildRouteReconciliation(*r)
			r.Reconciliation = &reconciliation
			return eventDecision, nil
		})
		if err != nil {
			return record, err
		}
		if !iterationAdded || !opts.UntilDone {
			break
		}
		if record.ReturnGate != nil && record.ReturnGate.FinalResponseAllowed &&
			record.GoalLease != nil && len(record.Steps) >= record.GoalLease.MinNodes {
			break
		}
	}
	return record, nil
}

func Pause(s Store, id string) (Record, error) {
	return s.Update(id, func(r *Record) error {
		if usableResumeState(r.CurrentRoute, r.CurrentPhase, r.ExactNextAction) {
			AppendRouteHistory(r, RouteDecision{
				Schema:          RouteSchema,
				MissionID:       r.MissionID,
				CorrelationID:   r.CorrelationID,
				Route:           r.CurrentRoute,
				Reason:          pauseBoundaryReason,
				SafeToRequest:   true,
				SafeToExecute:   false,
				SafeToPromote:   false,
				ExactNextAction: r.ExactNextAction,
				GeneratedAtUTC:  now(s.Clock),
			})
		}
		r.Status = "paused"
		r.CurrentPhase = "paused"
		r.ExactNextAction = pauseNextAction
		gate := EvaluateReturnGate(*r)
		gate.ExactNextAction = pauseNextAction
		r.ReturnGate = &gate
		reconciliation := BuildRouteReconciliation(*r)
		reconciliation.ExactNextAction = pauseNextAction
		r.Reconciliation = &reconciliation
		return nil
	})
}
func Resume(s Store, id string) (Record, error) {
	return s.Update(id, func(r *Record) error {
		state, err := durableResumeState(*r)
		if err != nil {
			return err
		}
		r.Status = "active"
		r.CurrentRoute = state.Route
		r.CurrentPhase = state.Phase
		r.ExactNextAction = state.ExactNextAction
		gate := EvaluateReturnGate(*r)
		r.ReturnGate = &gate
		reconciliation := BuildRouteReconciliation(*r)
		r.Reconciliation = &reconciliation
		return nil
	})
}

type resumeState struct {
	Route           string
	Phase           string
	ExactNextAction string
}

func durableResumeState(r Record) (resumeState, error) {
	for index, checkpoint := range r.Checkpoints {
		if checkpoint.Schema != MissionCheckpointSchema || checkpoint.MissionID != r.MissionID {
			return resumeState{}, fmt.Errorf("Mission checkpoint %d identity is invalid", index+1)
		}
		if checkpoint.CorrelationID != r.CorrelationID {
			return resumeState{}, fmt.Errorf("Mission checkpoint correlation does not match record")
		}
	}
	for index, step := range r.Steps {
		if step.Schema != StepSchema || step.MissionID != r.MissionID {
			return resumeState{}, fmt.Errorf("Mission continuation step %d identity is invalid", index+1)
		}
		if step.CorrelationID != r.CorrelationID {
			return resumeState{}, fmt.Errorf("Mission continuation step correlation does not match record")
		}
	}
	for index, route := range r.RouteHistory {
		if route.Schema != RouteSchema || route.MissionID != r.MissionID {
			return resumeState{}, fmt.Errorf("Mission route history %d identity is invalid", index+1)
		}
		if route.CorrelationID != "" && route.CorrelationID != r.CorrelationID {
			return resumeState{}, fmt.Errorf("Mission route history correlation does not match record")
		}
	}
	for index := len(r.RouteHistory) - 1; index >= 0; index-- {
		route := r.RouteHistory[index]
		if route.Reason == pauseBoundaryReason && route.CorrelationID == r.CorrelationID &&
			usableResumeState(route.Route, "routing", route.ExactNextAction) {
			return resumeState{
				Route: route.Route, Phase: "routing",
				ExactNextAction: route.ExactNextAction,
			}, nil
		}
	}
	for index := len(r.Checkpoints) - 1; index >= 0; index-- {
		checkpoint := r.Checkpoints[index]
		if usableResumeState(checkpoint.Route, checkpoint.Phase, checkpoint.ExactNextAction) {
			return resumeState{
				Route: checkpoint.Route, Phase: checkpoint.Phase,
				ExactNextAction: checkpoint.ExactNextAction,
			}, nil
		}
	}
	for index := len(r.Steps) - 1; index >= 0; index-- {
		step := r.Steps[index]
		if usableResumeState(step.Route, "handoff_required", step.ExactNextAction) {
			return resumeState{
				Route: step.Route, Phase: "handoff_required",
				ExactNextAction: step.ExactNextAction,
			}, nil
		}
	}
	for index := len(r.RouteHistory) - 1; index >= 0; index-- {
		route := r.RouteHistory[index]
		if route.CorrelationID == r.CorrelationID {
			if usableResumeState(route.Route, "routing", route.ExactNextAction) {
				return resumeState{
					Route: route.Route, Phase: "routing",
					ExactNextAction: route.ExactNextAction,
				}, nil
			}
		}
	}
	if r.WorkflowContract != nil &&
		usableResumeState(r.WorkflowContract.InitialRoute, "routing", r.WorkflowContract.ExactNextAction) {
		return resumeState{
			Route: r.WorkflowContract.InitialRoute, Phase: "routing",
			ExactNextAction: r.WorkflowContract.ExactNextAction,
		}, nil
	}
	decision := NextAction(r)
	return resumeState{
		Route: decision.Route, Phase: "routing", ExactNextAction: decision.ExactNextAction,
	}, nil
}

func usableResumeState(route, phase, action string) bool {
	return strings.TrimSpace(route) != "" && route != "complete" &&
		strings.TrimSpace(phase) != "" && phase != "paused" && phase != "complete" &&
		strings.TrimSpace(action) != "" && action != pauseNextAction
}

func Stop(s Store, id string) (Record, error) {
	return s.Update(id, func(r *Record) error {
		r.Status = "stopped"
		r.CurrentPhase = "stopped"
		r.ExactNextAction = "mission stopped by operator kill switch"
		return nil
	})
}
