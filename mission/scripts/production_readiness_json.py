#!/usr/bin/env python3
"""Strict, dependency-free JSON checks for production-readiness.sh."""

import argparse
import json
import math
import os
import re
import stat
import sys
from pathlib import Path


MAX_JSON_BYTES = 16 * 1024 * 1024
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")


class ValidationError(Exception):
    pass


def _reject_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValidationError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _reject_constant(constant):
    raise ValidationError(f"non-finite JSON number: {constant}")


def _parse_finite_float(token):
    parsed = float(token)
    if not math.isfinite(parsed):
        raise ValidationError(f"non-finite JSON number: {token}")
    return parsed


def _identity(info):
    try:
        device = info.st_dev
        inode = info.st_ino
    except AttributeError as error:
        raise ValidationError("file identity fields are unavailable") from error
    if not isinstance(device, int) or not isinstance(inode, int):
        raise ValidationError("file identity fields are unavailable")
    return (device, inode)


def load_json(path, expected_info=None, before_open=None):
    path = Path(path)
    if expected_info is None:
        try:
            initial = path.lstat()
        except OSError as error:
            raise ValidationError(f"cannot inspect {path}: {error}") from error
    else:
        initial = expected_info
    initial_identity = _identity(initial)
    if stat.S_ISLNK(initial.st_mode):
        raise ValidationError(f"JSON path must not be a symlink: {path}")
    if not stat.S_ISREG(initial.st_mode):
        raise ValidationError(f"JSON path must be a regular file: {path}")
    if initial.st_size > MAX_JSON_BYTES:
        raise ValidationError(f"JSON file exceeds {MAX_JSON_BYTES} bytes: {path}")
    if before_open is not None:
        before_open(path)
    flags = os.O_RDONLY | getattr(os, "O_BINARY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        raise ValidationError(f"cannot open JSON file {path}: {error}") from error
    try:
        opened = os.fstat(descriptor)
        if not stat.S_ISREG(opened.st_mode):
            raise ValidationError(f"opened JSON path must be a regular file: {path}")
        if _identity(opened) != initial_identity:
            raise ValidationError(f"JSON path was replaced before open: {path}")
        if opened.st_size > MAX_JSON_BYTES:
            raise ValidationError(f"JSON file exceeds {MAX_JSON_BYTES} bytes: {path}")
        chunks = []
        remaining = MAX_JSON_BYTES + 1
        while remaining:
            chunk = os.read(descriptor, min(64 * 1024, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        raw = b"".join(chunks)
        final = os.fstat(descriptor)
        if not stat.S_ISREG(final.st_mode) or _identity(final) != _identity(opened):
            raise ValidationError(f"opened JSON file was replaced while reading: {path}")
        if len(raw) > MAX_JSON_BYTES or final.st_size > MAX_JSON_BYTES:
            raise ValidationError(f"JSON file grew beyond {MAX_JSON_BYTES} bytes while reading: {path}")
    except OSError as error:
        raise ValidationError(f"cannot read JSON file {path}: {error}") from error
    finally:
        os.close(descriptor)
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as error:
        raise ValidationError(f"JSON is not UTF-8: {path}") from error
    try:
        return json.loads(
            text,
            object_pairs_hook=_reject_duplicate_keys,
            parse_constant=_reject_constant,
            parse_float=_parse_finite_float,
        )
    except ValidationError:
        raise
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise ValidationError(f"invalid JSON: {path}: {error}") from error


def _path_parts(path):
    return path if isinstance(path, tuple) else tuple(path.split("."))


def value(document, path):
    current = document
    traversed = []
    for part in _path_parts(path):
        traversed.append(str(part))
        label = ".".join(traversed)
        if isinstance(current, dict) and part in current:
            current = current[part]
        elif isinstance(current, list) and str(part).isdigit():
            index = int(part)
            if index >= len(current):
                raise ValidationError(f"missing JSON path: {label}")
            current = current[index]
        else:
            raise ValidationError(f"missing JSON path: {label}")
    return current


def require(condition, message):
    if not condition:
        raise ValidationError(message)


def same(actual, expected):
    if (
        isinstance(actual, (int, float))
        and not isinstance(actual, bool)
        and isinstance(expected, (int, float))
        and not isinstance(expected, bool)
    ):
        return actual == expected
    return type(actual) is type(expected) and actual == expected


def eq(document, path, expected):
    actual = value(document, path)
    require(same(actual, expected), f"{path} must equal {expected!r}; got {actual!r}")


def eqs(document, expected):
    for path, wanted in expected.items():
        eq(document, path, wanted)


def sequence(document, path):
    result = value(document, path)
    require(isinstance(result, list), f"{path} must be an array")
    return result


def string(document, path):
    result = value(document, path)
    require(isinstance(result, str), f"{path} must be a string")
    return result


def number(document, path):
    result = value(document, path)
    require(isinstance(result, (int, float)) and not isinstance(result, bool), f"{path} must be a number")
    require(math.isfinite(result), f"{path} must be a finite number")
    return result


def at_least(document, path, minimum):
    actual = number(document, path)
    require(actual >= minimum, f"{path} must be at least {minimum}; got {actual}")


def length_at_least(document, path, minimum):
    actual = value(document, path)
    require(isinstance(actual, (list, str)), f"{path} must be an array or string")
    require(len(actual) >= minimum, f"{path} length must be at least {minimum}; got {len(actual)}")


def contains(document, path, needle):
    actual = value(document, path)
    require(isinstance(actual, (list, str)), f"{path} must be an array or string")
    require(needle in actual, f"{path} must contain {needle!r}")


def digest(document, path):
    actual = string(document, path)
    require(DIGEST_RE.fullmatch(actual) is not None, f"{path} must be a sha256 digest")


def false_authority(document, *extra):
    for path in ("safe_to_execute", "executes_work", "approves_work", *extra):
        eq(document, path, False)


def recommendations(document, minimum_count, minimum_minutes):
    items = sequence(document, "feature_depth_recommendations")
    require(len(items) >= minimum_count, f"feature_depth_recommendations must contain at least {minimum_count} entries")
    qualified = 0
    total_minutes = 0
    for index, item in enumerate(items):
        require(isinstance(item, dict), f"feature_depth_recommendations.{index} must be an object")
        try:
            if (
                isinstance(item.get("gate"), str)
                and item["gate"]
                and isinstance(item.get("continuation_command"), str)
                and item["continuation_command"]
                and isinstance(item.get("estimated_minutes"), (int, float))
                and not isinstance(item.get("estimated_minutes"), bool)
                and item["estimated_minutes"] >= 6
                and isinstance(item.get("evidence_required"), list)
                and len(item["evidence_required"]) >= 3
            ):
                qualified += 1
            minutes = item.get("estimated_minutes")
            require(isinstance(minutes, (int, float)) and not isinstance(minutes, bool), f"feature_depth_recommendations.{index}.estimated_minutes must be a number")
            require(math.isfinite(minutes), f"feature_depth_recommendations.{index}.estimated_minutes must be a finite number")
            total_minutes += minutes
        except KeyError as error:
            raise ValidationError(f"feature_depth_recommendations.{index} missing {error.args[0]}") from error
    require(qualified >= minimum_count, f"at least {minimum_count} recommendations must satisfy the readiness bounds")
    require(total_minutes >= minimum_minutes, f"recommendation minutes must total at least {minimum_minutes}")


def atlas_recommendation_import(doc, _mission):
    eq(doc, "kind", "atlas-recommendation-readback")
    false_authority(doc)


def atlas_recommendation_inspect(doc, _mission):
    eqs(doc, {"status": "done", "current_route": "complete", "current_phase": "complete", "evidence.atlas_recommendation.completed_nodes": 40, "return_gate.final_response_allowed": True})


def final_reconciliation_runtime(doc, _mission):
    eqs(doc, {"schema": "ao.mission.final-reconciliation-packet.v0.1", "artifacts_agree": True, "final_response_allowed": True, "claims_authority_advance": False, "rsi_remains_denied": True})


def timeline_query_index(doc, _mission):
    eqs(doc, {"schema": "ao.mission.timeline-query-index.v0.1", "status": "ready"})
    digest(doc, "event_index_digest")
    digest(doc, "index_digest")
    at_least(doc, "event_count", 1)
    at_least(doc, "term_count", 1)
    terms = sequence(doc, "terms")
    require(any(isinstance(item, dict) and item.get("term") in {"final_reconciliation", "final"} for item in terms), "terms must include final_reconciliation or final")
    false_authority(doc, "mutates_repositories")


def restart_recovery_proof(doc, mission):
    eqs(doc, {"schema": "ao.mission.restart-recovery-proof.v0.1", "status": "restart_recovery_proven", "mission_id": mission, "source_digest_stable": True, "event_count_stable": True, "timeline_terms_stable": True, "timeline_matches_stable": True, "no_duplicate_timeline_matches": True, "recovery_proven": True})
    false_authority(doc, "mutates_repositories")


def event_search_runtime(doc, _mission):
    eqs(doc, {"schema": "ao.mission.event-search-readback.v0.1", "status": "ready", "events.0.kind": "final_reconciliation"})
    at_least(doc, "total_matches", 1)
    false_authority(doc)


def atlas_continuation_prompt(doc, mission):
    eqs(doc, {"schema": "ao.mission.atlas-continuation-prompt-packet.v0.1", "status": "ready", "mission_id": mission})
    digest(doc, "event_index_digest")
    digest(doc, "final_rollup_digest")
    contains(doc, "prompt", "AO Atlas")
    contains(doc, "prompt", "Do not produce a final response if ready_nodes > 0 or exact_next_action remains.")
    recommendations(doc, 10, 60)
    false_authority(doc, "mutates_repositories")


def atlas_wave_synthesis_runtime(doc, _mission):
    eqs(doc, {"schema": "ao.mission.atlas-wave-final-synthesis.v0.1", "mission": "ao-mission-doubled-wave-v01", "rsi_remains_denied": True})
    at_least(doc, "completed_nodes", 20)
    at_least(doc, "ready_nodes", 0)
    if number(doc, "ready_nodes") > 0:
        eq(doc, "final_response_allowed", False)
    recommendations(doc, 20, 120)
    false_authority(doc)


def atlas_final_synthesis_import(doc, _mission):
    eq(doc, "kind", "atlas-final-synthesis-readback")
    false_authority(doc)


def atlas_final_synthesis_inspect(doc, _mission):
    eqs(doc, {"status": "done", "current_route": "complete", "current_phase": "complete", "evidence.atlas_final_synthesis.command_readback": "ready", "evidence.atlas_final_synthesis.promoter_status": "no_promotion_requested", "route_reconciliation.command_readback_bound": True, "route_reconciliation.promoter_readback_bound": True, "route_reconciliation.atlas_ready_nodes": 0, "return_gate.final_response_allowed": True})


def checkpoint_resume_bundle(doc, mission):
    eqs(doc, {"schema": "ao.mission.checkpoint-resume-bundle.v0.3", "mission_id": mission, "status": "ready", "return_gate.final_response_allowed": True})
    false_authority(doc, "mutates_repositories")


def doctor_runtime(doc, _mission):
    eqs(doc, {"schema": "ao.mission.doctor-readback.v0.1", "status": "ready", "lease_health_status": "healthy", "checkpoint_freshness_status": "fresh", "early_return_risk_status": "risk_detected", "stale_route_decision_status": "clear"})
    risks = sequence(doc, "risk_missions")
    require(any(isinstance(item, dict) and item.get("kind") == "early_return" for item in risks), "risk_missions must include early_return")
    length_at_least(doc, "exact_next_action", 1)
    false_authority(doc, "mutates_repositories")


def final_reconciliation_fixture(doc, _mission):
    eqs(doc, {"schema": "ao.mission.final-reconciliation-packet.v0.1", "status": "ready", "artifacts_agree": True, "promotion_claimed": False, "rsi_remains_denied": True, "claims_authority_advance": False})
    false_authority(doc)


def final_reconciliation_mismatch_fixture(doc, _mission):
    eqs(doc, {"schema": "ao.mission.final-reconciliation-packet.v0.1", "status": "blocked", "artifacts_agree": False, "promotion_claimed": False, "rsi_remains_denied": True, "claims_authority_advance": False})
    contains(doc, "blocker", "Foundry completed_nodes=39")
    contains(doc, "blocker", "Atlas completed_nodes=40")
    false_authority(doc)


def final_rollup_ready_node_denial(doc, _mission):
    eqs(doc, {"schema": "ao.mission.final-rollup.v0.1", "final_response_allowed": False, "return_gate_status": "early_return_denied", "ready_nodes_remaining": 2, "completed_nodes": 10, "total_nodes": 12, "provider_calls": False})
    contains(doc, "exact_next_action", "ready nodes remain")
    recommendations(doc, 10, 60)
    false_authority(doc)


def sentinel_public_safety_scan(doc, _mission):
    eqs(doc, {"schema": "ao.sentinel.public-safety-wording-readback.v0.1", "status": "passed", "unsafe_public_wording_found": False, "claims_authority_advance": False, "rsi_remains_denied": True})
    contains(doc, "scanned_artifacts", "docs/evidence/ao-mission-atlas-wave-import-v01/next-recommended-prompt.md")
    contains(doc, "scanned_artifacts", "examples/valid/final-reconciliation-packet.json")


def production_readiness_branch_cleanup(doc, _mission):
    eqs(doc, {"schema": "ao.mission.production-readiness-branch-cleanup.v0.1", "status": "passed", "mission": "ao-mission-atlas-wave-import-v01", "completed_nodes_at_recording": 15, "local_verification_passed": True, "github_ci_passed_through_previous_node": True, "stale_local_codex_branches_remaining": 0, "stale_remote_codex_branches_remaining": 0, "current_node_branch_cleanup_pending_pr_merge": True, "direct_main_mutation": False, "promotion_claimed": False, "rsi_remains_denied": True})


def promoter_no_promotion_summary(doc, _mission):
    eqs(doc, {"schema": "ao.promoter.no-promotion-readback.v0.1", "status": "no_promotion_requested", "mission_id": "ao-mission-atlas-wave-import-v01", "completed_nodes_at_recording": 16, "safe_to_promote": False, "promotion_claimed": False, "claims_authority_advance": False, "broad_RSI": "denied", "rsi_remains_denied": True, "executes_work": False, "approves_work": False})


def foundry_terminal_state_binding(doc, _mission):
    eqs(doc, {"schema": "ao.foundry.terminal-state-binding.v0.1", "rsi_remains_denied": True})
    states = sequence(doc, "states")
    require(len(states) == 4, "states must contain exactly 4 entries")
    statuses = [item.get("status") for item in states if isinstance(item, dict)]
    require(all(status in statuses for status in ("completed", "promoted", "denied", "blocked")), "states must contain completed, promoted, denied, and blocked")
    for index, item in enumerate(states):
        require(isinstance(item, dict), f"states.{index} must be an object")
        expected = "done" if item.get("status") in {"completed", "promoted"} else "blocked"
        require(item.get("expected_mission_status") == expected, f"states.{index}.expected_mission_status must equal {expected!r}")
    false_authority(doc)


def command_compact_timeline(doc, _mission):
    eqs(doc, {"schema": "ao.command.compact-timeline-readback.v0.1", "status": "ready", "compact": True, "rsi_remains_denied": True})
    for kind in ("atlas_recommendation", "final_reconciliation"):
        contains(doc, "includes_event_kinds", kind)
        require(any(isinstance(item, dict) and item.get("kind") == kind for item in sequence(doc, "recent_events")), f"recent_events must include {kind}")
    false_authority(doc, "mutates_repositories")


def mission_status_timeline_vector(doc, _mission):
    eqs(doc, {"schema_version": "ao.compatibility.mission-status-timeline-vector.v1", "edge": "ao-mission.run_status_timeline -> ao-command.operator_timeline", "run_status.schema": "ao.mission.run-status-timeline.v0.1", "timeline.schema": "ao.mission.compact-timeline.v0.1", "expected_command_operator_timeline.schema": "ao-command.operator-timeline.v1", "compatibility.tested_edge_count": 2, "compatibility.full_stack_compatibility_complete": False})
    events = sequence(doc, "timeline.events")
    require(len(events) == 3, "timeline.events must contain exactly 3 entries")
    eq(doc, "expected_command_operator_timeline.timeline_event_count", len(events))
    require(events and isinstance(events[-1], dict), "timeline.events final entry must be an object")
    eq(doc, "expected_command_operator_timeline.latest_event", events[-1].get("kind"))
    for prefix in ("run_status", "timeline"):
        for flag in ("safe_to_execute", "executes_work", "approves_work", "mutates_repositories"):
            eq(doc, f"{prefix}.{flag}", False)


def command_status_lease_checkpoint(doc, _mission):
    eqs(doc, {"schema": "ao.command.mission-status.v0.1", "goal_lease.schema": "ao.mission.goal-lease.v0.3", "goal_lease.min_nodes": 15, "goal_lease.min_minutes": 120, "goal_lease.max_minutes": 180, "goal_lease.checkpoint_policy": "after_each_node_or_timed_interval", "checkpoint_count": 2, "checkpoint_freshness_status": "fresh", "return_gate_status": "early_return_denied", "read_only": True})
    false_authority(doc, "mutates_repositories")


def doctor_command_compact_risk(doc, _mission):
    eqs(doc, {"schema": "ao.mission.doctor-command-compact-early-return-risk.v0.1", "status": "risk_detected", "mission": "ao-mission-doubled-wave-v01", "doctor.schema": "ao.mission.doctor-readback.v0.1", "doctor.early_return_risk_status": "risk_detected", "command_compact.schema": "ao.command.compact-timeline-readback.v0.1", "binding.doctor_risk_kind": "early_return", "binding.command_event_kind": "doctor_risk", "binding.exact_next_action_bound": True, "binding.final_response_allowed": False, "binding.final_response_denial_bound": True, "binding.command_compact_risk_bound": True, "rsi_remains_denied": True})
    require(any(isinstance(item, dict) and item.get("kind") == "early_return" for item in sequence(doc, "doctor.risk_missions")), "doctor.risk_missions must include early_return")
    contains(doc, "command_compact.includes_event_kinds", "doctor_risk")
    recent = sequence(doc, "command_compact.recent_events")
    require(any(isinstance(item, dict) and item.get("kind") == "doctor_risk" and isinstance(item.get("summary"), str) and "final_response_allowed=false" in item["summary"] for item in recent), "command_compact.recent_events must include bound doctor_risk")
    false_authority(doc, "mutates_repositories")


def beta_incident_stop_rule(doc, _mission):
    eqs(doc, {"schema": "ao.mission.beta-incident-stop-rule-readback.v0.1", "status": "hold_required", "incident_severity": "high", "sentinel_status": "failed", "promoter_status": "hold", "stop_rule_triggered": True, "promoter_hold_required": True, "read_only": True, "provider_calls_allowed": False, "credential_use_allowed": False, "release_or_publish_allowed": False, "claims_authority_advance": False, "rsi_remains_denied": True})
    false_authority(doc, "mutates_repositories")


def pilot_feedback_capture(doc, _mission):
    eqs(doc, {"schema": "ao.mission.pilot-feedback-capture-packet.v0.1", "status": "ready", "pilot_id": "pilot-alpha", "read_only": True, "provider_calls_allowed": False, "credential_use_allowed": False, "release_or_publish_allowed": False, "claims_authority_advance": False, "rsi_remains_denied": True})
    require(len(sequence(doc, "capture_channels")) == 3, "capture_channels must contain exactly 3 entries")
    length_at_least(doc, "questions", 3)
    length_at_least(doc, "evidence_required", 4)
    false_authority(doc, "mutates_repositories")


def final_reconciliation_event_search(doc, _mission):
    eqs(doc, {"schema": "ao.mission.event-search-readback.v0.1", "status": "ready", "kind": "final_reconciliation", "total_matches": 1, "events.0.kind": "final_reconciliation"})
    contains(doc, "events.0.summary", "artifacts_agree=true")
    contains(doc, "events.0.summary", "rsi_remains_denied=true")
    false_authority(doc, "mutates_repositories")


def promoter_no_promotion_node(doc, _mission):
    eq(doc, "promotion_claimed", False)


def sentinel_public_safety_node(doc, _mission):
    eqs(doc, {"claims_authority_advance": False, "rsi_remains_denied": True})


def wave_boundary_readiness(doc, _mission):
    eqs(doc, {"schema": "ao.mission.wave-boundary-readiness.v0.1", "status": "passed", "mission": "ao-mission-atlas-wave-import-v01", "completed_nodes_at_recording": 23, "promotion_claimed": False, "claims_authority_advance": False, "rsi_remains_denied": True})
    at_least(doc, "promoter_no_promotion_records", 23)
    at_least(doc, "sentinel_public_safety_records", 23)


def merged_pr_branch_cleanup(doc, _mission):
    eqs(doc, {"schema": "ao.mission.merged-pr-branch-cleanup.v0.1", "status": "passed", "mission": "ao-mission-atlas-wave-import-v01", "completed_nodes_through_previous_node": 23, "stale_local_codex_branches_remaining": 0, "stale_remote_codex_branches_remaining": 0, "current_node_branch_cleanup_pending_pr_merge": True, "direct_main_mutation": False, "rsi_remains_denied": True})
    prs = sequence(doc, "merged_prs")
    require(len(prs) == 23, "merged_prs must contain exactly 23 entries")
    require(prs[0] == 21 and prs[-1] == 43, "merged_prs bounds must be 21 and 43")


def atlas_wave_final_synthesis_fixture(doc, _mission):
    eqs(doc, {"schema": "ao.mission.atlas-wave-final-synthesis.v0.1", "status": "completed", "mission": "ao-mission-atlas-wave-import-v01", "ready_nodes": 0, "blocked_nodes": 0, "final_response_allowed": True, "current_node_pr_pending": False, "promotion_claimed": False, "claims_authority_advance": False, "rsi_remains_denied": True})
    at_least(doc, "completed_nodes", 25)
    length_at_least(doc, "feature_depth_recommendations", 10)


def post_merge_final_closure(doc, _mission):
    eqs(doc, {"schema": "ao.mission.post-merge-final-closure.v0.1", "status": "completed", "mission": "ao-mission-atlas-wave-import-v01", "completed_nodes": 26, "ready_nodes": 0, "blocked_nodes": 0, "stale_local_codex_branches_remaining": 0, "stale_remote_codex_branches_remaining": 0, "final_response_allowed": True, "rsi_remains_denied": True})
    prs = sequence(doc, "merged_prs")
    require(len(prs) == 25, "merged_prs must contain exactly 25 entries")
    require(prs[0] == 21 and prs[-1] == 45, "merged_prs bounds must be 21 and 45")


def wave_duration_ledger(doc, _mission):
    eqs(doc, {"schema": "ao.mission.wave-duration-ledger.v0.1", "mission": "ao-mission-doubled-wave-v01", "status": "active", "minimum_minutes": 120, "target_minutes": 120, "max_minutes": 180, "minimum_minutes_met": False, "final_response_allowed": False, "rsi_remains_denied": True})
    false_authority(doc)


def codex_session_duration(doc, _mission):
    eqs(doc, {"schema": "ao.mission.codex-session-duration-readback.v0.1", "mission": "ao-mission-doubled-wave-v01", "status": "metadata_available", "content_read": False, "secret_values_read": False, "rsi_remains_denied": True})
    at_least(doc, "session_log_files_found", 1)
    false_authority(doc)


def atlas_final_synthesis_fixture(doc, _mission):
    eqs(doc, {"contract_version": "ao.atlas.ao-mission-final-synthesis-readback.v0.1", "completed_nodes": 26, "ready_nodes": 0, "blocked_nodes": 0, "final_response_allowed": True, "command_readback": "ready", "promoter_status": "no_promotion_requested", "rsi_remains_denied": True})
    false_authority(doc)


def event_search_production_smoke(doc, _mission):
    eqs(doc, {"schema": "ao.mission.event-search-production-smoke.v0.1", "status": "passed", "mission": "ao-mission-atlas-wave-import-v01", "searched_kind": "final_reconciliation", "total_matches_minimum": 1, "rsi_remains_denied": True})
    false_authority(doc)


def event_evidence_alias_readback(doc, _mission):
    eqs(doc, {"schema": "ao.mission.event-evidence-alias-readback.v0.1", "status": "passed", "mission": "ao-mission-doubled-wave-v01", "rsi_remains_denied": True})
    kinds = sequence(doc, "event_kinds")
    expected = ("route_evidence", "node_evidence", "pr_evidence", "ci_evidence", "rollup_evidence", "blocker_evidence")
    require(len(kinds) == 6 and all(kind in kinds for kind in expected), "event_kinds must contain the six evidence aliases")
    false_authority(doc)


def event_evidence_alias_searches(doc, _mission):
    eqs(doc, {"schema": "ao.mission.event-evidence-alias-search-readbacks.v0.1", "status": "passed", "mission": "ao-mission-doubled-wave-v01", "rsi_remains_denied": True})
    event_evidence_alias_readback({**doc, "schema": "ao.mission.event-evidence-alias-readback.v0.1"}, None)
    searches = sequence(doc, "searches")
    require(len(searches) == 6, "searches must contain exactly 6 entries")
    for index, item in enumerate(searches):
        require(isinstance(item, dict), f"searches.{index} must be an object")
        at_least(item, "total_matches", 1)
        false_authority(item, "mutates_repositories")
    false_authority(doc, "mutates_repositories")


def bounded_autonomy_month3(doc, _mission):
    eqs(doc, {"schema": "ao.mission.bounded-autonomy-month3-recovery-readback.v0.1", "status": "passed", "failure_injection_count": 10, "recovery_proof_count": 9, "duplicate_mutation_detected": False, "ready_nodes_remaining": 0, "exact_next_action_remaining": False, "stale_task_branches_remaining": 0, "stale_worktrees_remaining": 0, "final_response_denied_while_ready_work_remained": True, "compatibility_gate_active": False, "release_or_publish": False, "provider_pilot": False, "external_beta_launched": False, "promotion_requested": False, "promotion_granted": False, "rsi_remains_denied": True})


def bounded_autonomy_month4(doc, _mission):
    eqs(doc, {"schema": "ao.mission.bounded-autonomy-month4-controlled-improvement-readback.v0.1", "status": "passed", "candidate_classes_evaluated": 3, "accepted_candidate_count": 3, "all_accepted_candidates_have_measurable_gain": True, "green_ci_required_for_accepted_candidates": True, "rejected_candidates_leave_no_mutation": True, "rollback_evidence_matches": True, "command_presents_proposal_approval_measurement_decision_rollback": True, "sentinel_rejects_self_improvement_and_rsi_overclaims": True, "promoter_records_no_promotion_external_beta_gate_activation_or_rsi": True, "compatibility_gate_active": False, "release_or_publish": False, "provider_pilot": False, "external_beta_launched": False, "promotion_requested": False, "promotion_granted": False, "live_self_modification": False, "rsi_remains_denied": True, "calls_providers": False, "releases_or_deploys": False})
    false_authority(doc, "mutates_repositories")


def bounded_autonomy_month5(doc, _mission):
    eqs(doc, {"schema": "ao.mission.bounded-autonomy-month5-dogfood-readback.v0.1", "status": "passed", "task_portfolio_count": 5, "completed_task_count": 5, "accepted_tasks_merge_after_green_ci": True, "denied_tasks_have_correct_reason_and_safe_next_action": True, "month1_comparison.completion_rate": 1, "month1_comparison.first_pass_verification_rate": 1, "month1_comparison.recovery_rate": 1, "month1_comparison.duplicate_or_orphan_work": False, "month1_comparison.rollback_reliability": "passed", "month1_comparison.unsupported_claim_count": 0, "operator_friction_fixed": True, "no_unreviewed_autonomous_mutation": True, "scoped_repositories_clean_and_synced": True, "compatibility_gate_active": False, "release_or_publish": False, "provider_pilot": False, "external_beta_launched": False, "promotion_requested": False, "promotion_granted": False, "live_self_modification": False, "rsi_remains_denied": True, "calls_providers": False, "releases_or_deploys": False})
    false_authority(doc, "mutates_repositories")


def bounded_autonomy_month6(doc, _mission):
    eqs(doc, {"schema": "ao.mission.bounded-autonomy-month6-qualification-readback.v0.1", "status": "passed", "months_1_to_5_closed": True, "month1_benchmark_rerun": "passed", "month2_workflow_rerun": "passed", "month3_recovery_rerun": "passed", "month4_candidate_rerun": "passed", "month5_dogfood_reconciled": "passed", "current_release_metadata_verified": True, "compatibility_edges_verified": 16, "canonical_vectors_verified": 16, "consumer_tests_verified": 16, "production_readiness": "passed", "architecture_verification": "passed", "command_verification": "passed", "ao2_verification": "passed", "control_plane_verification": "passed", "release_decision": "control_plane_patch_release", "ao2_release_needed": False, "control_plane_release_needed": True, "control_plane_release_published": True, "control_plane_public_asset_verification": "passed", "public_asset_replacement_required": True, "stable_publication_required": True, "controlled_rsi_research_authorized": False, "compatibility_gate_active": False, "release_or_publish": True, "provider_pilot": False, "external_beta_launched": False, "promotion_requested": False, "promotion_granted": False, "live_self_modification": False, "rsi_remains_denied": True, "calls_providers": False, "releases_or_deploys": False, "ready_nodes_remaining": 0, "exact_next_action_remaining": False})
    false_authority(doc, "mutates_repositories")


def bounded_autonomy_repair(doc, _mission):
    eqs(doc, {"schema": "ao.mission.bounded-autonomy-repair-from-month3-readback.v0.1", "status": "passed", "classification_before_repair": "partial_invalid_closure", "month3_recovery_repaired": True, "month3_final_reconciliation.status": "ready", "month3_final_reconciliation.artifacts_agree": True, "month3_final_reconciliation.mission_status": "done", "month3_final_reconciliation.command_status": "done", "month3_final_reconciliation.completed_nodes": 26, "month3_final_reconciliation.total_nodes": 26, "month3_final_reconciliation.ready_nodes": 0, "month3_final_reconciliation.final_response_allowed": True, "month3_final_reconciliation.exact_next_action_remaining": False, "month4_rollback_repaired": True, "month4_original_ao_mission_revert_conflicted": True, "month4_explicit_rollback_verified": True, "month4_restore_matched_before": True, "month5_genuine_dogfood_completed": True, "month5_reused_prior_task_as_substitute": False, "month5_completed_task_count": 5, "month6_requalified": True, "month6_release_decision": "control_plane_patch_release", "control_plane_spin_change_binary_impact_reviewed": True, "control_plane_release_needed": True, "control_plane_release_published": True, "control_plane_public_asset_verification": "passed", "ao2_release_needed": False, "compatibility_gate_active": False, "release_or_publish": True, "provider_pilot": False, "external_beta_launched": False, "promotion_requested": False, "promotion_granted": False, "live_self_modification": False, "rsi_remains_denied": True, "calls_providers": False, "releases_or_deploys": False})
    false_authority(doc, "mutates_repositories")


def sqlite_migration_dry_run(doc, _mission):
    eqs(doc, {"schema": "ao.mission.sqlite-migration-dry-run.v0.1", "status": "ready", "mission": "ao-stack-month6-recommendations", "dry_run_only": True, "source_store_kind": "json_ledger", "target_store_kind": "sqlite", "records_written": 0, "sqlite_file_created": False, "source_mutated": False, "migration_started": False, "rollback_receipt_ready": True, "provider_calls": False, "credential_use": False, "release_or_publish": False, "direct_main_mutation": False, "rsi_remains_denied": True})
    at_least(doc, "records_scanned", 1)
    eq(doc, "records_planned", value(doc, "records_scanned"))
    digest(doc, "plan_digest")
    false_authority(doc, "mutates_repositories")


CHECKS = {
    name: globals()[name]
    for name in (
        "atlas_recommendation_import", "atlas_recommendation_inspect", "final_reconciliation_runtime",
        "timeline_query_index", "restart_recovery_proof", "event_search_runtime", "atlas_continuation_prompt",
        "atlas_wave_synthesis_runtime", "atlas_final_synthesis_import", "atlas_final_synthesis_inspect",
        "checkpoint_resume_bundle", "doctor_runtime", "final_reconciliation_fixture",
        "final_reconciliation_mismatch_fixture", "final_rollup_ready_node_denial", "sentinel_public_safety_scan",
        "production_readiness_branch_cleanup", "promoter_no_promotion_summary", "foundry_terminal_state_binding",
        "command_compact_timeline", "mission_status_timeline_vector", "command_status_lease_checkpoint",
        "doctor_command_compact_risk", "beta_incident_stop_rule", "pilot_feedback_capture",
        "final_reconciliation_event_search", "promoter_no_promotion_node", "sentinel_public_safety_node",
        "wave_boundary_readiness", "merged_pr_branch_cleanup", "atlas_wave_final_synthesis_fixture",
        "post_merge_final_closure", "wave_duration_ledger", "codex_session_duration",
        "atlas_final_synthesis_fixture", "event_search_production_smoke", "event_evidence_alias_readback",
        "event_evidence_alias_searches", "bounded_autonomy_month3", "bounded_autonomy_month4",
        "bounded_autonomy_month5", "bounded_autonomy_month6", "bounded_autonomy_repair", "sqlite_migration_dry_run",
    )
}


def run_check(profile, path, mission_id=None, expected_info=None, before_open=None):
    try:
        check = CHECKS[profile]
    except KeyError as error:
        raise ValidationError(f"unknown check profile: {profile}") from error
    check(load_json(path, expected_info=expected_info, before_open=before_open), mission_id)


def validate_tree_filename(filename):
    require(
        bool(filename)
        and filename not in {".", ".."}
        and not Path(filename).is_absolute()
        and "/" not in filename
        and "\\" not in filename
        and Path(filename).name == filename,
        "check-tree filename must be a non-empty basename",
    )


def validate_tree_root(root):
    root = Path(root)
    try:
        info = root.lstat()
    except OSError as error:
        raise ValidationError(f"cannot inspect check-tree root {root}: {error}") from error
    require(not stat.S_ISLNK(info.st_mode), f"check-tree root must not be a symlink: {root}")
    require(stat.S_ISDIR(info.st_mode), f"check-tree root must be a directory: {root}")
    try:
        return root.resolve(strict=True)
    except OSError as error:
        raise ValidationError(f"cannot resolve check-tree root {root}: {error}") from error


def validate_tree_candidate(root_resolved, path):
    path = Path(path)
    try:
        info = path.lstat()
    except OSError as error:
        raise ValidationError(f"cannot inspect check-tree match {path}: {error}") from error
    require(
        stat.S_ISREG(info.st_mode) and not stat.S_ISLNK(info.st_mode),
        f"check-tree match must be a regular non-symlink file: {path}",
    )
    _identity(info)
    require(info.st_size <= MAX_JSON_BYTES, f"JSON file exceeds {MAX_JSON_BYTES} bytes: {path}")
    try:
        resolved = path.resolve(strict=True)
        contained = os.path.commonpath((str(root_resolved), str(resolved))) == str(root_resolved)
    except (OSError, ValueError) as error:
        raise ValidationError(f"cannot resolve check-tree match {path}: {error}") from error
    require(contained, f"check-tree match escapes root: {path}")
    return resolved, info


def run_tree_checks(profile, root, filename, before_open=None):
    validate_tree_filename(filename)
    root = Path(root)
    root_resolved = validate_tree_root(root)
    try:
        paths = sorted(root.rglob(filename))
    except OSError as error:
        raise ValidationError(f"cannot enumerate check-tree root {root}: {error}") from error
    require(bool(paths), f"no files named {filename} under {root}")
    for path in paths:
        try:
            resolved, info = validate_tree_candidate(root_resolved, path)
            hook = None
            if before_open is not None:
                hook = lambda _path, candidate=resolved: before_open(candidate)
            run_check(profile, resolved, expected_info=info, before_open=hook)
        except ValidationError as error:
            raise ValidationError(f"{path}: {error}") from error


def main(argv=None):
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)
    extract = subparsers.add_parser("extract-mission-id")
    extract.add_argument("path")
    bind = subparsers.add_parser("bind-mission-id")
    bind.add_argument("source")
    bind.add_argument("destination")
    bind.add_argument("mission_id")
    check = subparsers.add_parser("check")
    check.add_argument("profile", choices=sorted(CHECKS))
    check.add_argument("path")
    check.add_argument("--mission-id")
    tree = subparsers.add_parser("check-tree")
    tree.add_argument("profile", choices=sorted(CHECKS))
    tree.add_argument("root")
    tree.add_argument("filename")
    args = parser.parse_args(argv)

    if args.command == "extract-mission-id":
        document = load_json(args.path)
        require(isinstance(document, dict), "mission JSON must be an object")
        mission_id = document.get("mission_id")
        require(isinstance(mission_id, str) and mission_id, "mission_id must be a non-empty string")
        print(mission_id)
    elif args.command == "bind-mission-id":
        require(bool(args.mission_id), "mission_id must be a non-empty string")
        document = load_json(args.source)
        require(isinstance(document, dict), "bound JSON document must be an object")
        document["mission_id"] = args.mission_id
        try:
            encoded = json.dumps(document, ensure_ascii=False, indent=2, allow_nan=False) + "\n"
        except ValueError as error:
            raise ValidationError("bound JSON contains a non-finite output number") from error
        Path(args.destination).write_bytes(encoded.encode("utf-8"))
    elif args.command == "check":
        run_check(args.profile, args.path, args.mission_id)
    else:
        run_tree_checks(args.profile, args.root, args.filename)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ValidationError as error:
        print(f"production-readiness JSON error: {error}", file=sys.stderr)
        raise SystemExit(2)
