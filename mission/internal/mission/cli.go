package mission

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var (
	BuildVersion   = "dev"
	BuildSourceSHA = "unknown"
)

type repeatedStringFlag []string

func (values *repeatedStringFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *repeatedStringFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func printJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func runCLICommand(s Store, args []string, stdout io.Writer) error {
	switch args[0] {
	case "init":
		if err := s.Init(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "status=initialized")
		return nil
	case "start":
		if len(args) < 2 {
			return errors.New("start requires objective")
		}
		r, err := s.Start(strings.Join(args[1:], " "))
		if err != nil {
			return err
		}
		return printJSON(stdout, r)
	case "objective":
		if len(args) < 2 || args[1] != "start" {
			return errors.New("objective requires start")
		}
		fs := flag.NewFlagSet("objective start", flag.ContinueOnError)
		objective := fs.String("objective", "", "")
		correlationID := fs.String("correlation-id", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("objective start does not accept positional arguments")
		}
		contract, err := s.StartObjective(*objective, ObjectiveStartOptions{CorrelationID: *correlationID})
		if err != nil {
			return err
		}
		return printJSON(stdout, contract)
	case "mission":
		if len(args) < 2 {
			return errors.New("mission requires list or inspect")
		}
		switch args[1] {
		case "list":
			fs := flag.NewFlagSet("mission list", flag.ContinueOnError)
			jsonOut := fs.Bool("json", false, "")
			statusFilter := fs.String("status", "", "")
			routeFilter := fs.String("route", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			records, err := s.ListFiltered(ListFilters{Status: *statusFilter, Route: *routeFilter})
			if err != nil {
				return err
			}
			if *jsonOut {
				return printJSON(stdout, records)
			}
			for _, rec := range records {
				fmt.Fprintf(stdout, "mission=%s status=%s route=%s\n", rec.MissionID, rec.Status, rec.CurrentRoute)
			}
			return nil
		case "inspect":
			fs := flag.NewFlagSet("mission inspect", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			terminalState := fs.String("terminal-state", "", "")
			jsonOut := fs.Bool("json", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			r, err = projectRecordWithTerminalState(r, *terminalState)
			if err != nil {
				return err
			}
			if *jsonOut {
				return printJSON(stdout, r)
			}
			fmt.Fprintf(stdout, "mission=%s\nstatus=%s\nphase=%s\nroute=%s\nnext=%s\n", r.MissionID, r.Status, r.CurrentPhase, r.CurrentRoute, r.ExactNextAction)
			if r.TerminalProjectionReadOnly {
				fmt.Fprintf(stdout, "source_status=%s\nterminal_status=%s\nterminal_read_only=true\neffective_status=%s\n", r.SourceRecordStatus, r.TerminalProjectionStatus, r.EffectiveOperatorStatus)
			}
			return nil
		case "metrics":
			fs := flag.NewFlagSet("mission metrics", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "json output")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("mission metrics requires --mission")
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			metrics := BuildMissionLifecycleMetrics(r)
			if err := ValidateMissionLifecycleMetrics(metrics); err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(metrics, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut {
				return printJSON(stdout, metrics)
			}
			fmt.Fprintf(stdout, "mission=%s\ncompleted_nodes=%d\nevidence_completed_nodes=%d\nhandoff_steps=%d\ntotal_nodes=%d\nready_nodes=%d\ncompletion_basis=%s\nfinal_response_allowed=%t\n",
				metrics.MissionID, metrics.CompletedNodes, metrics.EvidenceCompletedNodes, metrics.HandoffSteps, metrics.TotalNodes, metrics.ReadyNodes, metrics.CompletionBasis, metrics.FinalResponseAllowed)
			return nil
		case "beta-incident-stop-rule":
			fs := flag.NewFlagSet("mission beta-incident-stop-rule", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			incidentID := fs.String("incident", "", "")
			severity := fs.String("severity", "", "")
			sentinelStatus := fs.String("sentinel-status", "", "")
			promoterStatus := fs.String("promoter-status", "", "")
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "json output")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("mission beta-incident-stop-rule requires --mission")
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			readback := BuildBetaIncidentStopRuleReadback(r, BetaIncidentStopRuleOptions{
				IncidentID:     *incidentID,
				Severity:       *severity,
				SentinelStatus: *sentinelStatus,
				PromoterStatus: *promoterStatus,
			})
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(readback, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut {
				return printJSON(stdout, readback)
			}
			if strings.TrimSpace(*outPath) != "" {
				fmt.Fprintf(stdout, "beta_incident_stop_rule=%s\nmission=%s\nstatus=%s\nstop_rule_triggered=%t\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.MissionID, readback.Status, readback.StopRuleTriggered)
				return nil
			}
			fmt.Fprintf(stdout, "mission=%s\nstatus=%s\nstop_rule_triggered=%t\npromoter_hold_required=%t\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\nnext=%s\n", readback.MissionID, readback.Status, readback.StopRuleTriggered, readback.PromoterHoldRequired, readback.ExactNextAction)
			return nil
		case "pilot-feedback-packet":
			fs := flag.NewFlagSet("mission pilot-feedback-packet", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			pilotID := fs.String("pilot", "", "")
			feedbackWindow := fs.String("window", "", "")
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "json output")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("mission pilot-feedback-packet requires --mission")
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			packet := BuildPilotFeedbackCapturePacket(r, PilotFeedbackCaptureOptions{
				PilotID:        *pilotID,
				FeedbackWindow: *feedbackWindow,
			})
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(packet, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut {
				return printJSON(stdout, packet)
			}
			if strings.TrimSpace(*outPath) != "" {
				fmt.Fprintf(stdout, "pilot_feedback_packet=%s\nmission=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, packet.MissionID, packet.Status)
				return nil
			}
			fmt.Fprintf(stdout, "mission=%s\nstatus=%s\npilot=%s\nfeedback_window=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\nnext=%s\n", packet.MissionID, packet.Status, packet.PilotID, packet.FeedbackWindow, packet.ExactNextAction)
			return nil
		case "projection":
			fs := flag.NewFlagSet("mission projection", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "json output")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("mission projection requires --mission")
			}
			projection, err := BuildMissionLifecycleProjection(s, *id)
			if err != nil {
				return err
			}
			if err := ValidateMissionLifecycleProjection(projection); err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(projection, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut {
				return printJSON(stdout, projection)
			}
			fmt.Fprintf(stdout, "mission=%s\nstatus=%s\nevent_count=%d\ncurrent_route=%s\nfinal_response_allowed=%t\n", projection.MissionID, projection.Status, projection.EventCount, projection.CurrentRoute, projection.FinalResponseAllowed)
			return nil
		case "history":
			fs := flag.NewFlagSet("mission history", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			jsonOut := fs.Bool("json", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			if *jsonOut {
				return printJSON(stdout, r.RouteHistory)
			}
			for _, item := range r.RouteHistory {
				fmt.Fprintf(stdout, "route=%s reason=%s safe_to_execute=%t next=%s\n", item.Route, item.Reason, item.SafeToExecute, item.ExactNextAction)
			}
			return nil
		case "compact":
			fs := flag.NewFlagSet("mission compact", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			keepRouteHistory := fs.Int("keep-route-history", 25, "")
			keepSteps := fs.Int("keep-steps", 25, "")
			dryRun := fs.Bool("dry-run", false, "")
			timeline := fs.Bool("timeline", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("mission compact requires --mission")
			}
			if *timeline {
				readback, err := CompactMissionTimeline(s, *id, LedgerCompactionOptions{KeepRouteHistory: *keepRouteHistory, KeepSteps: *keepSteps, DryRun: *dryRun})
				if err != nil {
					return err
				}
				return printJSON(stdout, readback)
			}
			readback, err := CompactMissionLedger(s, *id, LedgerCompactionOptions{KeepRouteHistory: *keepRouteHistory, KeepSteps: *keepSteps, DryRun: *dryRun})
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		case "archive":
			fs := flag.NewFlagSet("mission archive", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" || strings.TrimSpace(*outPath) == "" {
				return errors.New("mission archive requires --mission and --out")
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			archive, err := BuildMissionArchive(r)
			if err != nil {
				return err
			}
			body, err := json.MarshalIndent(archive, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "mission_archive=%s\nmission=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, archive.MissionID)
			return nil
		case "validate-archive":
			fs := flag.NewFlagSet("mission validate-archive", flag.ContinueOnError)
			path := fs.String("path", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*path) == "" {
				return errors.New("mission validate-archive requires --path")
			}
			validation, err := ValidateMissionArchive(*path)
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, validation)
			}
			body, err := json.MarshalIndent(validation, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "mission_archive_validation=%s\nmission=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, validation.MissionID)
			return nil
		case "import-archive":
			fs := flag.NewFlagSet("mission import-archive", flag.ContinueOnError)
			path := fs.String("path", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*path) == "" {
				return errors.New("mission import-archive requires --path")
			}
			readback, err := ImportMissionArchive(s, *path)
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		case "events":
			if len(args) < 3 {
				return errors.New("mission events requires index or search")
			}
			switch args[2] {
			case "index":
				fs := flag.NewFlagSet("mission events index", flag.ContinueOnError)
				outPath := fs.String("out", "", "")
				if err := fs.Parse(args[3:]); err != nil {
					return err
				}
				index, err := BuildMissionEventIndex(s)
				if err != nil {
					return err
				}
				if strings.TrimSpace(*outPath) != "" {
					body, err := json.MarshalIndent(index, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
						return err
					}
				}
				return printJSON(stdout, index)
			case "query-index":
				fs := flag.NewFlagSet("mission events query-index", flag.ContinueOnError)
				indexPath := fs.String("index", "", "")
				outPath := fs.String("out", "", "")
				if err := fs.Parse(args[3:]); err != nil {
					return err
				}
				var eventIndex MissionEventIndex
				if strings.TrimSpace(*indexPath) != "" {
					body, err := os.ReadFile(*indexPath)
					if err != nil {
						return err
					}
					if err := json.Unmarshal(body, &eventIndex); err != nil {
						return err
					}
				} else {
					var err error
					eventIndex, err = BuildMissionEventIndex(s)
					if err != nil {
						return err
					}
				}
				queryIndex, err := BuildMissionTimelineQueryIndex(eventIndex)
				if err != nil {
					return err
				}
				if strings.TrimSpace(*outPath) != "" {
					body, err := json.MarshalIndent(queryIndex, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
						return err
					}
				}
				return printJSON(stdout, queryIndex)
			case "restart-proof":
				fs := flag.NewFlagSet("mission events restart-proof", flag.ContinueOnError)
				id := fs.String("mission", "", "")
				outPath := fs.String("out", "", "")
				jsonOut := fs.Bool("json", false, "")
				if err := fs.Parse(args[3:]); err != nil {
					return err
				}
				if strings.TrimSpace(*id) == "" {
					return errors.New("mission events restart-proof requires --mission")
				}
				proof, err := BuildMissionRestartRecoveryProof(s, *id)
				if err != nil {
					return err
				}
				if strings.TrimSpace(*outPath) != "" {
					body, err := json.MarshalIndent(proof, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
						return err
					}
				}
				if *jsonOut {
					return printJSON(stdout, proof)
				}
				fmt.Fprintf(stdout, "mission=%s\nstatus=%s\nrecovery_proven=%t\nsource_digest_stable=%t\ntimeline_terms_stable=%t\nno_duplicate_timeline_matches=%t\n",
					proof.MissionID, proof.Status, proof.RecoveryProven, proof.SourceDigestStable, proof.TimelineTermsStable, proof.NoDuplicateTimelineMatches)
				return nil
			case "resume-prompt":
				fs := flag.NewFlagSet("mission events resume-prompt", flag.ContinueOnError)
				id := fs.String("mission", "", "")
				outPath := fs.String("out", "", "")
				jsonOut := fs.Bool("json", false, "")
				if err := fs.Parse(args[3:]); err != nil {
					return err
				}
				if strings.TrimSpace(*id) == "" {
					return errors.New("mission events resume-prompt requires --mission")
				}
				prompt, err := BuildMissionCompactionResumePrompt(s, *id)
				if err != nil {
					return err
				}
				if strings.TrimSpace(*outPath) != "" {
					body, err := json.MarshalIndent(prompt, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
						return err
					}
				}
				if *jsonOut {
					return printJSON(stdout, prompt)
				}
				fmt.Fprintf(stdout, "mission=%s\nstatus=%s\nreturn_gate=%s\nfinal_response_allowed=%t\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\nnext=%s\n",
					prompt.MissionID, prompt.Status, prompt.ReturnGateStatus, prompt.FinalResponseAllowed, prompt.ExactNextAction)
				return nil
			case "search":
				fs := flag.NewFlagSet("mission events search", flag.ContinueOnError)
				missionID := fs.String("mission", "", "")
				kind := fs.String("kind", "", "")
				query := fs.String("query", "", "")
				indexPath := fs.String("index", "", "")
				outPath := fs.String("out", "", "")
				jsonOut := fs.Bool("json", false, "")
				if err := fs.Parse(args[3:]); err != nil {
					return err
				}
				var index MissionEventIndex
				if strings.TrimSpace(*indexPath) != "" {
					body, err := os.ReadFile(*indexPath)
					if err != nil {
						return err
					}
					if err := json.Unmarshal(body, &index); err != nil {
						return err
					}
					if err := ValidateMissionEventIndexDigest(index); err != nil {
						return err
					}
				} else {
					var err error
					index, err = BuildMissionEventIndex(s)
					if err != nil {
						return err
					}
				}
				readback := SearchMissionEvents(index, MissionEventSearchFilters{MissionID: *missionID, Kind: *kind, Query: *query})
				if strings.TrimSpace(*outPath) != "" {
					body, err := json.MarshalIndent(readback, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
						return err
					}
				}
				if *jsonOut {
					return printJSON(stdout, readback)
				}
				fmt.Fprintf(stdout, "mission_events=%d status=%s safe_to_execute=false executes_work=false approves_work=false\n", readback.TotalMatches, readback.Status)
				for _, event := range readback.Events {
					fmt.Fprintf(stdout, "mission=%s kind=%s route=%s summary=%s\n", event.MissionID, event.Kind, event.Route, event.Summary)
				}
				return nil
			default:
				return errors.New("mission events requires index or search")
			}
		case "readiness-bundle":
			fs := flag.NewFlagSet("mission readiness-bundle", flag.ContinueOnError)
			var repos readinessRepoFlags
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "")
			fs.Var(&repos, "repo", "repo=path readiness summary input")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			inputs, err := repos.inputs()
			if err != nil {
				return err
			}
			readback, err := BuildMissionReadinessBundleReadback(inputs)
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(readback, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut || strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, readback)
			}
			fmt.Fprintf(stdout, "mission_readiness_bundle=%s\nstatus=%s\nready_repos=%d\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.Status, readback.ReadyRepos)
			return nil
		case "dashboard":
			fs := flag.NewFlagSet("mission dashboard", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			compact := fs.Bool("compact", false, "")
			terminalState := fs.String("terminal-state", "", "")
			jsonOut := fs.Bool("json", false, "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("mission dashboard requires --mission")
			}
			record, err := s.Load(*id)
			if err != nil {
				return err
			}
			record, err = projectRecordWithTerminalState(record, *terminalState)
			if err != nil {
				return err
			}
			readback, err := buildMissionDashboardReadback(s, record, *compact)
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(readback, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut || strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, readback)
			}
			fmt.Fprintf(stdout, "mission_dashboard=%s\nmission=%s\nstatus=%s\nlatest_route=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.MissionID, readback.Status, readback.LatestRoute)
			if readback.TerminalProjectionReadOnly {
				fmt.Fprintf(stdout, "source_status=%s\nterminal_status=%s\nterminal_read_only=true\neffective_status=%s\n", readback.SourceRecordStatus, readback.TerminalProjectionStatus, readback.EffectiveOperatorStatus)
			}
			return nil
		case "verification-bundle":
			fs := flag.NewFlagSet("mission verification-bundle", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			readinessBundlePath := fs.String("readiness-bundle", "", "")
			gatewayReplayBundlePath := fs.String("gateway-replay-bundle", "", "")
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("mission verification-bundle requires --mission")
			}
			readback, err := BuildMissionVerificationBundleReadback(s, *id, MissionVerificationBundleOptions{
				ReadinessBundlePath:     *readinessBundlePath,
				GatewayReplayBundlePath: *gatewayReplayBundlePath,
			})
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(readback, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut || strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, readback)
			}
			fmt.Fprintf(stdout, "mission_verification_bundle=%s\nmission=%s\nstatus=%s\ncomponent_count=%d\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.MissionID, readback.Status, readback.ComponentCount)
			return nil
		default:
			return errors.New("mission requires list, inspect, metrics, projection, history, compact, archive, validate-archive, import-archive, events, readiness-bundle, dashboard, or verification-bundle")
		}
	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		readback := BuildMissionDoctorReadback(s)
		if *jsonOut {
			return printJSON(stdout, readback)
		}
		fmt.Fprintf(stdout, "status=%s\nmissions=%d\nevents=%d\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", readback.Status, readback.MissionCount, readback.EventCount)
		return nil
	case "status":
		fs := flag.NewFlagSet("status", flag.ContinueOnError)
		id := fs.String("mission", "", "")
		terminalState := fs.String("terminal-state", "", "")
		jsonOut := fs.Bool("json", false, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		r, err := s.Load(*id)
		if err != nil {
			return err
		}
		r, err = projectRecordWithTerminalState(r, *terminalState)
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(stdout, r)
		}
		fmt.Fprintf(stdout, "mission=%s\nstatus=%s\nroute=%s\nnext=%s\n", r.MissionID, r.Status, r.CurrentRoute, r.ExactNextAction)
		if r.TerminalProjectionReadOnly {
			fmt.Fprintf(stdout, "source_status=%s\nterminal_status=%s\nterminal_read_only=true\neffective_status=%s\n", r.SourceRecordStatus, r.TerminalProjectionStatus, r.EffectiveOperatorStatus)
		}
		return nil
	case "next":
		fs := flag.NewFlagSet("next", flag.ContinueOnError)
		id := fs.String("mission", "", "")
		jsonOut := fs.Bool("json", false, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var d RouteDecision
		_, err := s.Update(*id, func(r *Record) error {
			d = NextActionForRecord(*r)
			AppendRouteHistory(r, d)
			return nil
		})
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(stdout, d)
		}
		fmt.Fprintf(stdout, "route=%s\nreason=%s\nnext=%s\n", d.Route, d.Reason, d.ExactNextAction)
		return nil
	case "continue":
		fs := flag.NewFlagSet("continue", flag.ContinueOnError)
		id := fs.String("mission", "", "")
		until := fs.Bool("until-done", false, "")
		max := fs.Int("max-iterations", 1, "")
		minNodes := fs.Int("min-nodes", 0, "")
		minMinutes := fs.Int("min-minutes", 0, "")
		maxMinutes := fs.Int("max-minutes", 0, "")
		returnOnlyWhen := fs.String("return-only-when", "", "")
		checkpointPolicy := fs.String("checkpoint-policy", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		minMinutesSet := false
		fs.Visit(func(current *flag.Flag) {
			if current.Name == "min-minutes" {
				minMinutesSet = true
			}
		})
		r, err := Continue(s, *id, ContinueOptions{
			UntilDone:        *until,
			MaxIterations:    *max,
			MinNodes:         *minNodes,
			MinMinutes:       *minMinutes,
			MinMinutesSet:    minMinutesSet,
			MaxMinutes:       *maxMinutes,
			ReturnOnlyWhen:   *returnOnlyWhen,
			CheckpointPolicy: *checkpointPolicy,
		})
		if err != nil {
			return err
		}
		return printJSON(stdout, r)
	case "checkpoint":
		if len(args) < 2 || (args[1] != "inspect" && args[1] != "create") {
			return errors.New("checkpoint requires create or inspect")
		}
		fs := flag.NewFlagSet("checkpoint "+args[1], flag.ContinueOnError)
		id := fs.String("mission", "", "")
		jsonOut := fs.Bool("json", false, "")
		var slice, evidenceDigest *string
		if args[1] == "create" {
			slice = fs.String("slice", "", "")
			evidenceDigest = fs.String("evidence-digest", "", "")
		}
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if strings.TrimSpace(*id) == "" {
			return errors.New("checkpoint " + args[1] + " requires --mission")
		}
		var bundle MissionCheckpointBundle
		var err error
		if args[1] == "create" {
			hasSlice := strings.TrimSpace(*slice) != ""
			hasEvidence := strings.TrimSpace(*evidenceDigest) != ""
			if hasSlice != hasEvidence {
				return errors.New("checkpoint create requires --slice and --evidence-digest together")
			}
			if hasSlice {
				bundle, err = CreateSliceCheckpoint(s, *id, SliceCheckpointOptions{
					Slice: *slice, EvidenceDigest: *evidenceDigest,
				})
			} else {
				bundle, err = CreateMissionCheckpoint(s, *id)
			}
		} else {
			bundle, err = s.LoadCheckpointBundle(*id)
		}
		if err != nil {
			return err
		}
		if *jsonOut {
			return printJSON(stdout, bundle)
		}
		finalAllowed := false
		if bundle.ReturnGate != nil {
			finalAllowed = bundle.ReturnGate.FinalResponseAllowed
		}
		fmt.Fprintf(stdout, "mission=%s\nstatus=%s\ncheckpoints=%d\nfinal_response_allowed=%t\nresume=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", bundle.MissionID, bundle.Status, bundle.CheckpointCount, finalAllowed, bundle.ResumePrompt)
		return nil
	case "pause":
		id := missionFlag(args[1:])
		r, err := Pause(s, id)
		if err != nil {
			return err
		}
		return printJSON(stdout, r)
	case "resume":
		id := missionFlag(args[1:])
		r, err := Resume(s, id)
		if err != nil {
			return err
		}
		return printJSON(stdout, r)
	case "stop":
		id := missionFlag(args[1:])
		r, err := Stop(s, id)
		if err != nil {
			return err
		}
		return printJSON(stdout, r)
	case "schedule":
		if len(args) >= 2 && args[1] == "replay" {
			fs := flag.NewFlagSet("schedule replay", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *fixturePath == "" {
				return errors.New("schedule replay requires --fixture")
			}
			readback, err := ReplaySchedulerReadbacks(*fixturePath)
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		}
		if len(args) >= 2 && args[1] == "alerts" {
			fs := flag.NewFlagSet("schedule alerts", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *fixturePath == "" {
				return errors.New("schedule alerts requires --fixture")
			}
			readback, err := ReplaySchedulerReadbacks(*fixturePath)
			if err != nil {
				return err
			}
			return printJSON(stdout, BuildSchedulerAlertSummary(readback))
		}
		if len(args) >= 2 && args[1] == "recover" {
			fs := flag.NewFlagSet("schedule recover", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			fixturePath := fs.String("fixture", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" || strings.TrimSpace(*fixturePath) == "" {
				return errors.New("schedule recover requires --mission and --fixture")
			}
			readback, err := ReplaySchedulerReadbacks(*fixturePath)
			if err != nil {
				return err
			}
			return printJSON(stdout, BuildSchedulerRecoveryReadback(*id, readback))
		}
		fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
		id := fs.String("mission", "", "")
		every := fs.String("every", "", "")
		eventLoop := fs.Bool("event-loop", false, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		_ = every
		return printJSON(stdout, ScheduleReadback(*id, *every, *eventLoop))
	case "qualification":
		if len(args) < 2 {
			return errors.New("qualification requires orchestrate, soak-plan, or soak-canary")
		}
		switch args[1] {
		case "orchestrate":
			fs := flag.NewFlagSet("qualification orchestrate", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			jsonOut := fs.Bool("json", false, "json output")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*fixturePath) == "" {
				return errors.New("qualification orchestrate requires --fixture")
			}
			readback, err := BuildQualificationOrchestrationReadback(*fixturePath)
			if err != nil {
				return err
			}
			if *jsonOut {
				return printJSON(stdout, readback)
			}
			fmt.Fprintf(stdout, "qualification_orchestration=%s\nmission=%s\naffected_shards=%d\nfinal_qualification_mode=%s\nsource_heads=%d\nexact_head_required=%t\ncheckpoint_after_each_shard=%t\nrestart_from_zero_allowed=%t\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\nmutates_repositories=false\ncalls_providers=false\nreleases_or_deploys=false\nnext=%s\n",
				readback.Status,
				readback.MissionID,
				readback.AffectedShardCount,
				readback.FinalQualificationMode,
				readback.SourceHeadCount,
				readback.ExactHeadRequired,
				readback.CheckpointAfterEachShard,
				readback.RestartFromZeroAllowed,
				readback.ExactNextAction)
			return nil
		case "soak-plan":
			fs := flag.NewFlagSet("qualification soak-plan", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			jsonOut := fs.Bool("json", false, "json output")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 0 || strings.TrimSpace(*fixturePath) == "" {
				return errors.New("qualification soak-plan requires --fixture")
			}
			if filepath.Clean(*fixturePath) != *fixturePath {
				return errors.New("qualification soak-plan rejects fixture traversal")
			}
			input, err := LoadSoakPlanInput(filepath.Dir(*fixturePath), *fixturePath)
			if err != nil {
				return err
			}
			readback, err := BuildSoakPlan(input)
			if err != nil {
				return err
			}
			if *jsonOut {
				return printJSON(stdout, readback)
			}
			fmt.Fprintf(stdout, "plan=%s\nmission=%s\npartitions=%d\nactivation_allowed=%t\nconflicts=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\nmutates_repositories=false\ncalls_providers=false\npublishes=false\nreleases=false\ndeploys=false\nadvances_authority=false\nrsi_remains_denied=true\nnext=%s\n",
				readback.PlanID,
				readback.MissionID,
				len(readback.Partitions),
				readback.ActivationAllowed,
				strings.Join(readback.ConflictCodes, ","),
				readback.ExactNextAction)
			return nil
		case "soak-canary":
			return runSoakCanaryCLI(args[2:], stdout)
		default:
			return errors.New("qualification requires orchestrate, soak-plan, or soak-canary")
		}
	case "daemon":
		if len(args) < 2 {
			return errors.New("daemon requires install/status/uninstall")
		}
		fmt.Fprintf(stdout, "daemon=%s\nstatus=readback_only\n", args[1])
		return nil
	case "telegram":
		if len(args) >= 2 && args[1] == "webhook-replay" {
			fs := flag.NewFlagSet("telegram webhook-replay", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			configPath := fs.String("config", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *fixturePath == "" || *configPath == "" {
				return errors.New("telegram webhook-replay requires --fixture and --config")
			}
			cfg, err := LoadTelegramConfig(*configPath)
			if err != nil {
				return err
			}
			readback, err := ReplayTelegramWebhookFixture(*fixturePath, cfg.AllowedChats)
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		}
		if len(args) >= 2 && args[1] == "replay-updates" {
			fs := flag.NewFlagSet("telegram replay-updates", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			configPath := fs.String("config", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *fixturePath == "" || *configPath == "" {
				return errors.New("telegram replay-updates requires --fixture and --config")
			}
			cfg, err := LoadTelegramConfig(*configPath)
			if err != nil {
				return err
			}
			readback, err := ReplayTelegramUpdates(*fixturePath, cfg.AllowedChats)
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		}
		if len(args) >= 2 && args[1] == "replay" {
			fs := flag.NewFlagSet("telegram replay", flag.ContinueOnError)
			matrixPath := fs.String("matrix", "", "")
			configPath := fs.String("config", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *matrixPath == "" || *configPath == "" {
				return errors.New("telegram replay requires --matrix and --config")
			}
			cfg, err := LoadTelegramConfig(*configPath)
			if err != nil {
				return err
			}
			readback, err := ReplayTelegramCommandMatrix(*matrixPath, cfg.AllowedChats)
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		}
		if len(args) >= 2 && args[1] == "role-matrix" {
			fs := flag.NewFlagSet("telegram role-matrix", flag.ContinueOnError)
			configPath := fs.String("config", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*configPath) == "" {
				return errors.New("telegram role-matrix requires --config")
			}
			cfg, err := LoadTelegramConfig(*configPath)
			if err != nil {
				return err
			}
			matrix := BuildTelegramRoleMatrix(cfg)
			if strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, matrix)
			}
			body, err := json.MarshalIndent(matrix, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "telegram_role_matrix=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, matrix.Status)
			return nil
		}
		if len(args) >= 2 && args[1] == "serve" {
			fs := flag.NewFlagSet("telegram serve", flag.ContinueOnError)
			configPath := fs.String("config", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *configPath == "" {
				return printJSON(stdout, TelegramReadback{Schema: TelegramReadbackSchema, Status: "disabled", Message: "telegram gateway disabled by default; configure environment token name and allowlist", MutationAuthority: false})
			}
			cfg, err := LoadTelegramConfig(*configPath)
			if err != nil {
				return err
			}
			return printJSON(stdout, TelegramConfigReadback(cfg))
		}
		return errors.New("telegram requires serve, replay, replay-updates, webhook-replay, or role-matrix")
	case "a2a":
		if len(args) >= 2 && args[1] == "replay" {
			fs := flag.NewFlagSet("a2a replay", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *fixturePath == "" {
				return errors.New("a2a replay requires --fixture")
			}
			readback, err := ReplayA2AHTTPFixture(*fixturePath)
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		}
		if len(args) >= 2 && args[1] == "lifecycle" {
			fs := flag.NewFlagSet("a2a lifecycle", flag.ContinueOnError)
			fixturePath := fs.String("fixture", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *fixturePath == "" {
				return errors.New("a2a lifecycle requires --fixture")
			}
			readback, err := ReplayA2ATaskLifecycle(*fixturePath)
			if err != nil {
				return err
			}
			return printJSON(stdout, readback)
		}
		if len(args) >= 2 && args[1] == "compatibility" {
			fs := flag.NewFlagSet("a2a compatibility", flag.ContinueOnError)
			agentCardPath := fs.String("agent-card", "", "")
			httpPath := fs.String("http", "", "")
			lifecyclePath := fs.String("lifecycle", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*agentCardPath) == "" || strings.TrimSpace(*httpPath) == "" || strings.TrimSpace(*lifecyclePath) == "" {
				return errors.New("a2a compatibility requires --agent-card, --http, and --lifecycle")
			}
			readback, err := BuildA2ACompatibilityReadback(*agentCardPath, *httpPath, *lifecyclePath)
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, readback)
			}
			body, err := json.MarshalIndent(readback, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "a2a_compatibility=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.Status)
			return nil
		}
		if len(args) >= 2 && args[1] == "streaming-denial" {
			fs := flag.NewFlagSet("a2a streaming-denial", flag.ContinueOnError)
			agentCardPath := fs.String("agent-card", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*agentCardPath) == "" {
				return errors.New("a2a streaming-denial requires --agent-card")
			}
			readback, err := BuildA2AStreamingDenialReadback(*agentCardPath)
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, readback)
			}
			body, err := json.MarshalIndent(readback, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "a2a_streaming_denial=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.Status)
			return nil
		}
		if len(args) >= 2 && args[1] == "cancellation-replay" {
			fs := flag.NewFlagSet("a2a cancellation-replay", flag.ContinueOnError)
			lifecyclePath := fs.String("lifecycle", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*lifecyclePath) == "" {
				return errors.New("a2a cancellation-replay requires --lifecycle")
			}
			readback, err := BuildA2ACancellationReplayReadback(*lifecyclePath)
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, readback)
			}
			body, err := json.MarshalIndent(readback, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "a2a_cancellation_replay=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.Status)
			return nil
		}
		if len(args) >= 2 && args[1] == "serve" {
			fs := flag.NewFlagSet("a2a serve", flag.ContinueOnError)
			httpMode := fs.Bool("http", false, "")
			listen := fs.String("listen", "127.0.0.1:0", "")
			once := fs.Bool("once", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if !*httpMode {
				return printJSON(stdout, AgentCard())
			}
			ln, err := net.Listen("tcp", *listen)
			if err != nil {
				return err
			}
			server := &http.Server{Handler: A2AHandler()}
			if *once {
				addr := ln.Addr().String()
				_ = ln.Close()
				return printJSON(stdout, map[string]any{
					"schema":             "ao.mission.a2a-fixture-server-readback.v0.1",
					"gateway":            "a2a",
					"status":             "ready",
					"listen":             addr,
					"agent_card_path":    "/.well-known/agent-card.json",
					"jsonrpc_path":       "/",
					"methods":            AgentCard().Methods,
					"message":            "A2A local HTTP fixture server can bind and records intents only",
					"mutation_authority": false,
					"executes_work":      false,
					"approves_work":      false,
					"generated_at_utc":   now(nil),
				})
			}
			fmt.Fprintf(stdout, "a2a_listen=%s\nmutation_authority=false\n", ln.Addr().String())
			return server.Serve(ln)
		}
		return errors.New("a2a requires serve, replay, lifecycle, compatibility, streaming-denial, or cancellation-replay")
	case "gateway":
		if len(args) >= 2 && args[1] == "replay-bundle" {
			fs := flag.NewFlagSet("gateway replay-bundle", flag.ContinueOnError)
			telegramConfigPath := fs.String("telegram-config", "", "")
			telegramMatrixPath := fs.String("telegram-matrix", "", "")
			telegramUpdatesPath := fs.String("telegram-updates", "", "")
			telegramWebhookPath := fs.String("telegram-webhook", "", "")
			a2aHTTPPath := fs.String("a2a-http", "", "")
			a2aLifecyclePath := fs.String("a2a-lifecycle", "", "")
			schedulerPath := fs.String("scheduler", "", "")
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			readback, err := BuildGatewayReplayBundleReadback(GatewayReplayBundleInputs{
				TelegramConfigPath:  *telegramConfigPath,
				TelegramMatrixPath:  *telegramMatrixPath,
				TelegramUpdatesPath: *telegramUpdatesPath,
				TelegramWebhookPath: *telegramWebhookPath,
				A2AHTTPPath:         *a2aHTTPPath,
				A2ALifecyclePath:    *a2aLifecyclePath,
				SchedulerPath:       *schedulerPath,
			})
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(readback, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut || strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, readback)
			}
			fmt.Fprintf(stdout, "gateway_replay_bundle=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, readback.Status)
			return nil
		}
		if len(args) >= 2 && args[1] == "replay-suite" {
			fs := flag.NewFlagSet("gateway replay-suite", flag.ContinueOnError)
			telegramConfigPath := fs.String("telegram-config", "", "")
			telegramWebhookPath := fs.String("telegram-webhook", "", "")
			telegramUpdatesPath := fs.String("telegram-updates", "", "")
			a2aHTTPPath := fs.String("a2a-http", "", "")
			a2aLifecyclePath := fs.String("a2a-lifecycle", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) == "" {
				return errors.New("gateway replay-suite requires --out")
			}
			readbacks := []GatewayReplayReadback{}
			refs := []string{}
			var lifecycle *A2ATaskLifecycleReadback
			var allowedChats map[string]string
			if strings.TrimSpace(*telegramWebhookPath) != "" || strings.TrimSpace(*telegramUpdatesPath) != "" {
				if strings.TrimSpace(*telegramConfigPath) == "" {
					return errors.New("gateway replay-suite requires --telegram-config with Telegram fixtures")
				}
				cfg, err := LoadTelegramConfig(*telegramConfigPath)
				if err != nil {
					return err
				}
				allowedChats = cfg.AllowedChats
			}
			if strings.TrimSpace(*telegramWebhookPath) != "" {
				readback, err := ReplayTelegramWebhookFixture(*telegramWebhookPath, allowedChats)
				if err != nil {
					return err
				}
				readbacks = append(readbacks, readback)
				refs = append(refs, filepath.ToSlash(*telegramWebhookPath))
			}
			if strings.TrimSpace(*telegramUpdatesPath) != "" {
				readback, err := ReplayTelegramUpdates(*telegramUpdatesPath, allowedChats)
				if err != nil {
					return err
				}
				readbacks = append(readbacks, readback)
				refs = append(refs, filepath.ToSlash(*telegramUpdatesPath))
			}
			if strings.TrimSpace(*a2aHTTPPath) != "" {
				readback, err := ReplayA2AHTTPFixture(*a2aHTTPPath)
				if err != nil {
					return err
				}
				readbacks = append(readbacks, readback)
				refs = append(refs, filepath.ToSlash(*a2aHTTPPath))
			}
			if strings.TrimSpace(*a2aLifecyclePath) != "" {
				readback, err := ReplayA2ATaskLifecycle(*a2aLifecyclePath)
				if err != nil {
					return err
				}
				lifecycle = &readback
				refs = append(refs, filepath.ToSlash(*a2aLifecyclePath))
			}
			if len(readbacks) == 0 && lifecycle == nil {
				return errors.New("gateway replay-suite requires at least one replay input")
			}
			suite := BuildGatewayReplaySuite(readbacks, lifecycle, refs)
			body, err := json.MarshalIndent(suite, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "gateway_replay_suite=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, suite.Status)
			return nil
		}
		if len(args) >= 2 && args[1] == "ledger" {
			fs := flag.NewFlagSet("gateway ledger", flag.ContinueOnError)
			missionID := fs.String("mission", "", "")
			telegramUpdatesPath := fs.String("telegram-updates", "", "")
			telegramConfigPath := fs.String("telegram-config", "", "")
			a2aHTTPPath := fs.String("a2a-http", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*missionID) == "" || strings.TrimSpace(*outPath) == "" {
				return errors.New("gateway ledger requires --mission and --out")
			}
			readbacks := []GatewayReplayReadback{}
			if strings.TrimSpace(*telegramUpdatesPath) != "" {
				if strings.TrimSpace(*telegramConfigPath) == "" {
					return errors.New("gateway ledger requires --telegram-config with --telegram-updates")
				}
				cfg, err := LoadTelegramConfig(*telegramConfigPath)
				if err != nil {
					return err
				}
				readback, err := ReplayTelegramUpdates(*telegramUpdatesPath, cfg.AllowedChats)
				if err != nil {
					return err
				}
				readbacks = append(readbacks, readback)
			}
			if strings.TrimSpace(*a2aHTTPPath) != "" {
				readback, err := ReplayA2AHTTPFixture(*a2aHTTPPath)
				if err != nil {
					return err
				}
				readbacks = append(readbacks, readback)
			}
			if len(readbacks) == 0 {
				return errors.New("gateway ledger requires at least one replay input")
			}
			ledger := BuildGatewayIntentLedger(*missionID, readbacks...)
			body, err := json.MarshalIndent(ledger, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "gateway_intent_ledger=%s\nmission=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, ledger.MissionID)
			return nil
		}
		if len(args) >= 2 && args[1] == "readiness-rollup" {
			fs := flag.NewFlagSet("gateway readiness-rollup", flag.ContinueOnError)
			missionID := fs.String("mission", "", "")
			suitePath := fs.String("suite", "", "")
			a2aCompatibilityPath := fs.String("a2a-compatibility", "", "")
			archiveValidationPath := fs.String("archive-validation", "", "")
			snapshotDiffPath := fs.String("snapshot-diff", "", "")
			correlationID := fs.String("correlation-id", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) == "" {
				return errors.New("gateway readiness-rollup requires --out")
			}
			if strings.TrimSpace(*missionID) == "" {
				return errors.New("gateway readiness-rollup requires --mission")
			}
			rollup, err := BuildGatewayReadinessRollupWithMissionAndCorrelation(*missionID, *correlationID, *suitePath, *a2aCompatibilityPath, *archiveValidationPath, *snapshotDiffPath)
			if err != nil {
				return err
			}
			body, err := json.MarshalIndent(rollup, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "gateway_readiness_rollup=%s\nstatus=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, rollup.Status)
			return nil
		}
		return errors.New("gateway requires ledger, replay-suite, replay-bundle, or readiness-rollup")
	case "governance":
		if len(args) >= 2 && args[1] == "snapshot" {
			id := missionFlag(args[2:])
			r, err := s.Load(id)
			if err != nil {
				return err
			}
			return printJSON(stdout, Snapshot(r))
		}
		if len(args) >= 2 && args[1] == "diff" {
			fs := flag.NewFlagSet("governance diff", flag.ContinueOnError)
			beforePath := fs.String("before", "", "")
			afterPath := fs.String("after", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*beforePath) == "" || strings.TrimSpace(*afterPath) == "" {
				return errors.New("governance diff requires --before and --after")
			}
			before, err := LoadGovernanceSnapshot(*beforePath)
			if err != nil {
				return err
			}
			after, err := LoadGovernanceSnapshot(*afterPath)
			if err != nil {
				return err
			}
			diff := DiffGovernanceSnapshots(before, after)
			if strings.TrimSpace(*outPath) == "" {
				return printJSON(stdout, diff)
			}
			body, err := json.MarshalIndent(diff, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "governance_snapshot_diff=%s\nmission=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, diff.MissionID)
			return nil
		}
		return errors.New("governance requires snapshot or diff")
	case "command":
		if len(args) >= 2 && args[1] == "status" {
			fs := flag.NewFlagSet("command status", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			terminalState := fs.String("terminal-state", "", "")
			jsonOut := fs.Bool("json", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			r, err = projectRecordWithTerminalState(r, *terminalState)
			if err != nil {
				return err
			}
			status := BuildCommandStatus(r)
			if *jsonOut {
				return printJSON(stdout, status)
			}
			fmt.Fprintf(stdout, "mission=%s\nstatus=%s\nread_only=%t\nexecutes_work=%t\ncheckpoint_freshness=%s\ncheckpoints=%d\nreturn_gate=%s\nnext=%s\n", status.MissionID, status.Status, status.ReadOnly, status.ExecutesWork, status.CheckpointFreshnessStatus, status.CheckpointCount, status.ReturnGateStatus, status.ExactNextAction)
			if status.TerminalProjectionReadOnly {
				fmt.Fprintf(stdout, "source_status=%s\nterminal_status=%s\nterminal_read_only=true\neffective_status=%s\n", status.SourceRecordStatus, status.TerminalProjectionStatus, status.EffectiveOperatorStatus)
			}
			if status.GoalLease != nil {
				fmt.Fprintf(stdout, "goal_lease=min_nodes:%d min_minutes:%d max_minutes:%d checkpoint_policy:%s\n", status.GoalLease.MinNodes, status.GoalLease.MinMinutes, status.GoalLease.MaxMinutes, status.GoalLease.CheckpointPolicy)
			}
			if status.AtlasRecommendation != nil {
				fmt.Fprintf(stdout, "atlas_recommendation=%s completed_nodes=%d total_nodes=%d ready_nodes=%d final_response_allowed=%t\n", status.AtlasRecommendation.Status, status.AtlasRecommendation.CompletedNodes, status.AtlasRecommendation.TotalNodes, status.AtlasRecommendation.ReadyNodes, status.AtlasRecommendation.FinalResponseAllowed)
			}
			return nil
		}
		return errors.New("command requires status")
	case "artifacts":
		if len(args) >= 2 && args[1] == "manifest" {
			fs := flag.NewFlagSet("artifacts manifest", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			if *outPath == "" {
				return printJSON(stdout, BuildArtifactManifest(r))
			}
			manifest, err := MaterializeArtifactManifest(r, *outPath)
			if err != nil {
				return err
			}
			if err := writeArtifactManifestFile(*outPath, manifest); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "artifact_manifest=%s\nmission=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, manifest.MissionID)
			return nil
		}
		if len(args) >= 2 && args[1] == "validate-manifest" {
			fs := flag.NewFlagSet("artifacts validate-manifest", flag.ContinueOnError)
			path := fs.String("path", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*path) == "" {
				return errors.New("artifacts validate-manifest requires --path")
			}
			result, err := ValidateArtifactManifestFile(*path)
			if printErr := printJSON(stdout, result); printErr != nil {
				return printErr
			}
			return err
		}
		if len(args) >= 2 && args[1] == "repair-manifest" {
			fs := flag.NewFlagSet("artifacts repair-manifest", flag.ContinueOnError)
			path := fs.String("path", "", "")
			outPath := fs.String("out", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*path) == "" || strings.TrimSpace(*outPath) == "" {
				return errors.New("artifacts repair-manifest requires --path and --out")
			}
			manifest, err := repairArtifactManifestFile(*path, *outPath)
			if err != nil {
				return err
			}
			if err := writeArtifactManifestFile(*outPath, manifest); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "artifact_manifest_repaired=%s\nmission=%s\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\n", *outPath, manifest.MissionID)
			return nil
		}
		id := missionFlag(args[1:])
		r, err := s.Load(id)
		if err != nil {
			return err
		}
		return printJSON(stdout, r.ArtifactRefs)
	case "correlation":
		if len(args) >= 2 && args[1] == "build" {
			fs := flag.NewFlagSet("correlation build", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			outPath := fs.String("out", "", "")
			var artifactFlags repeatedStringFlag
			fs.Var(&artifactFlags, "artifact", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" || strings.TrimSpace(*outPath) == "" || len(artifactFlags) == 0 {
				return errors.New("correlation build requires --mission, at least one --artifact <role>=<path>, and --out")
			}
			record, err := s.Load(*id)
			if err != nil {
				return err
			}
			specs := make([]CorrelationArtifactSpec, 0, len(artifactFlags))
			for _, value := range artifactFlags {
				role, path, found := strings.Cut(value, "=")
				if !found || strings.TrimSpace(role) == "" || strings.TrimSpace(path) == "" {
					return fmt.Errorf("correlation artifact %q must be <role>=<path>", value)
				}
				specs = append(specs, CorrelationArtifactSpec{Role: role, Path: path})
			}
			chain, err := BuildCorrelationChain(record, specs)
			if err != nil {
				return err
			}
			if err := WriteCorrelationChainFile(*outPath, chain); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "correlation_chain=%s\nmission=%s\nartifacts=%d\nsafe_to_execute=false\nexecutes_work=false\napproves_work=false\nmutates_repositories=false\nwidens_policy=false\npublishes_artifacts=false\n", *outPath, chain.MissionID, len(chain.Entries))
			return nil
		}
		if len(args) >= 2 && args[1] == "validate" {
			fs := flag.NewFlagSet("correlation validate", flag.ContinueOnError)
			path := fs.String("path", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*path) == "" {
				return errors.New("correlation validate requires --path")
			}
			validation, err := ValidateCorrelationChainFile(*path)
			if printErr := printJSON(stdout, validation); printErr != nil {
				return printErr
			}
			return err
		}
		return errors.New("correlation requires build or validate")
	case "validate":
		if len(args) >= 2 && args[1] == "contract" {
			fs := flag.NewFlagSet("validate contract", flag.ContinueOnError)
			path := fs.String("path", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			result, err := ValidateContractFile(*path)
			if printErr := printJSON(stdout, result); printErr != nil {
				return printErr
			}
			return err
		}
		return errors.New("validate requires contract --path <file>")
	case "import":
		if len(args) < 2 {
			return errors.New("import requires correlation-evidence, blueprint-authorization, atlas-workgraph, atlas-recommendation-readback, atlas-final-synthesis-readback, foundry-run-link, foundry-final-rollup, scheduler-readback, scheduler-recovery-readback, ledger-compaction-readback, ao-next-terminal, or ao-next-journal-prefix")
		}
		fs := flag.NewFlagSet("import "+args[1], flag.ContinueOnError)
		id := fs.String("mission", "", "")
		path := fs.String("path", "", "")
		correlationChainPath := fs.String("correlation-chain", "", "")
		correlationRole := fs.String("correlation-role", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		var rb ImportReadback
		var err error
		if args[1] == correlationEvidenceImportKind {
			if strings.TrimSpace(*correlationChainPath) == "" || strings.TrimSpace(*correlationRole) == "" {
				return errors.New("correlation-evidence import requires --correlation-chain and --correlation-role")
			}
			rb, err = ImportCorrelationEvidence(
				s,
				*id,
				*path,
				*correlationChainPath,
				*correlationRole,
			)
		} else if strings.TrimSpace(*correlationRole) != "" {
			return errors.New("--correlation-role is only valid for correlation-evidence import")
		} else if strings.TrimSpace(*correlationChainPath) == "" {
			rb, err = ImportArtifact(s, *id, args[1], *path)
		} else {
			rb, err = ImportArtifactWithCorrelationChain(s, *id, args[1], *path, *correlationChainPath)
		}
		if printErr := printJSON(stdout, rb); printErr != nil {
			return printErr
		}
		return err
	case "final":
		if len(args) >= 2 && args[1] == "rollup" {
			fs := flag.NewFlagSet("final rollup", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			evidenceRoot := fs.String("evidence-root", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if fs.NArg() != 0 {
				return errors.New("final rollup does not accept positional arguments")
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			return printJSON(stdout, buildFinalRollup(r, *evidenceRoot))
		}
		if len(args) >= 2 && args[1] == "reconcile" {
			fs := flag.NewFlagSet("final reconcile", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			correlationChainPath := fs.String("correlation-chain", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			if strings.TrimSpace(*correlationChainPath) != "" {
				packet, err := BuildFinalReconciliationPacketWithCorrelationChain(r, *correlationChainPath)
				if err != nil {
					return err
				}
				return printJSON(stdout, packet)
			}
			return printJSON(stdout, BuildFinalReconciliationPacket(r))
		}
		if len(args) >= 2 && args[1] == "synthesize" {
			fs := flag.NewFlagSet("final synthesize", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			evidenceRoot := fs.String("evidence-root", "", "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			synthesis, err := BuildAtlasWaveFinalSynthesis(r, *evidenceRoot)
			if err != nil {
				return err
			}
			return printJSON(stdout, synthesis)
		}
		if len(args) >= 2 && args[1] == "atlas-prompt" {
			fs := flag.NewFlagSet("final atlas-prompt", flag.ContinueOnError)
			id := fs.String("mission", "", "")
			eventIndexPath := fs.String("event-index", "", "")
			evidenceRoot := fs.String("evidence-root", "", "")
			outPath := fs.String("out", "", "")
			jsonOut := fs.Bool("json", false, "")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			r, err := s.Load(*id)
			if err != nil {
				return err
			}
			body, err := os.ReadFile(*eventIndexPath)
			if err != nil {
				return err
			}
			var index MissionEventIndex
			if err := json.Unmarshal(body, &index); err != nil {
				return err
			}
			packet, err := buildAtlasContinuationPromptPacket(r, index, buildFinalRollup(r, *evidenceRoot))
			if err != nil {
				return err
			}
			if strings.TrimSpace(*outPath) != "" {
				body, err := json.MarshalIndent(packet, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
					return err
				}
			}
			if *jsonOut {
				return printJSON(stdout, packet)
			}
			fmt.Fprintf(stdout, "status=%s\nmission=%s\natlas_prompt_packet=%s\n", packet.Status, packet.MissionID, *outPath)
			return nil
		}
		return errors.New("final requires rollup --mission <id>, reconcile --mission <id>, synthesize --mission <id> --evidence-root <path>, or atlas-prompt --mission <id> --event-index <path>")
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func parseGlobalHome(args []string) (string, []string, error) {
	if len(args) == 0 || args[0] != "--home" {
		return "", args, nil
	}
	if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
		return "", args, errors.New("--home requires a directory")
	}
	return args[1], args[2:], nil
}
func missionFlag(args []string) string {
	fs := flag.NewFlagSet("mission", flag.ContinueOnError)
	id := fs.String("mission", "", "")
	_ = fs.Parse(args)
	return *id
}

type readinessRepoFlags []string

func (f *readinessRepoFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *readinessRepoFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("--repo requires repo=path")
	}
	*f = append(*f, value)
	return nil
}

func (f readinessRepoFlags) inputs() ([]MissionReadinessBundleInput, error) {
	inputs := []MissionReadinessBundleInput{}
	for _, value := range f {
		repo, path, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(repo) == "" || strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("--repo must be repo=path")
		}
		inputs = append(inputs, MissionReadinessBundleInput{Repo: strings.TrimSpace(repo), Path: strings.TrimSpace(path)})
	}
	return inputs, nil
}
