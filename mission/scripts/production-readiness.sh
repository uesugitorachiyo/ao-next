#!/usr/bin/env sh
set -eu
export PYTHONDONTWRITEBYTECODE=1

tmp_home="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_home"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

mission_bin="$tmp_home/ao-mission"
json_helper="scripts/production_readiness_json.py"
readiness_fixtures="testdata/production-readiness"
json_check() {
  python3 "$json_helper" check "$@"
}

go test ./... -count=1 >"$tmp_home/go-test.log"
go vet ./... >"$tmp_home/go-vet.log"
go build -o "$tmp_home/ao-mission" ./cmd/ao-mission
git ls-files -z '*.go' | xargs -0 gofmt -d >"$tmp_home/gofmt.diff"
test ! -s "$tmp_home/gofmt.diff"
git diff --check
python3 scripts/test_public_safety_scan.py
python3 scripts/test_production_readiness_json.py
python3 scripts/public-safety-scan.py \
  --root . \
  README.md docs examples testdata internal cmd scripts

mission_json="$tmp_home/mission.json"
import_json="$tmp_home/import.json"
inspect_json="$tmp_home/inspect.json"
reconcile_json="$tmp_home/reconcile.json"
event_index_json="$tmp_home/event-index.json"
timeline_query_index_json="$tmp_home/timeline-query-index.json"
restart_recovery_proof_json="$tmp_home/restart-recovery-proof.json"
event_search_json="$tmp_home/event-search.json"
atlas_prompt_json="$tmp_home/atlas-prompt.json"
synthesis_json="$tmp_home/synthesis.json"
doctor_json="$tmp_home/doctor.json"
final_synthesis_import_json="$tmp_home/final-synthesis-import.json"
final_synthesis_inspect_json="$tmp_home/final-synthesis-inspect.json"
final_synthesis_checkpoint_json="$tmp_home/final-synthesis-checkpoint.json"
final_synthesis_readback_json="$tmp_home/final-synthesis-readback.json"

"$mission_bin" --home "$tmp_home/state" start "import completed Atlas recommendation wave" >"$mission_json"
mission_id="$(python3 "$json_helper" extract-mission-id "$mission_json")"
"$mission_bin" --home "$tmp_home/state" import atlas-recommendation-readback --mission "$mission_id" --path examples/valid/atlas-recommendation-readback.json >"$import_json"
json_check atlas_recommendation_import "$import_json"
"$mission_bin" --home "$tmp_home/state" mission inspect --mission "$mission_id" --json >"$inspect_json"
json_check atlas_recommendation_inspect "$inspect_json"
"$mission_bin" --home "$tmp_home/state" final reconcile --mission "$mission_id" >"$reconcile_json"
json_check final_reconciliation_runtime "$reconcile_json"
"$mission_bin" --home "$tmp_home/state" mission events index --out "$event_index_json" >/dev/null
"$mission_bin" --home "$tmp_home/state" mission events query-index --index "$event_index_json" --out "$timeline_query_index_json" >/dev/null
json_check timeline_query_index "$timeline_query_index_json"
"$mission_bin" --home "$tmp_home/state" mission events restart-proof --mission "$mission_id" --out "$restart_recovery_proof_json" --json >/dev/null
json_check restart_recovery_proof "$restart_recovery_proof_json" --mission-id "$mission_id"
"$mission_bin" --home "$tmp_home/state" mission events search --mission "$mission_id" --kind final_reconciliation --index "$event_index_json" --json >"$event_search_json"
json_check event_search_runtime "$event_search_json"
"$mission_bin" --home "$tmp_home/state" final atlas-prompt --mission "$mission_id" --event-index "$event_index_json" --out "$atlas_prompt_json" >/dev/null
json_check atlas_continuation_prompt "$atlas_prompt_json" --mission-id "$mission_id"
"$mission_bin" --home "$tmp_home/state" final synthesize --mission "$mission_id" --evidence-root "$readiness_fixtures/ao-mission-doubled-wave-v01" >"$synthesis_json"
json_check atlas_wave_synthesis_runtime "$synthesis_json"

"$mission_bin" --home "$tmp_home/state" start "import Atlas final synthesis readback" >"$mission_json"
final_synthesis_mission_id="$(python3 "$json_helper" extract-mission-id "$mission_json")"
python3 "$json_helper" bind-mission-id examples/valid/atlas-final-synthesis-readback.json "$final_synthesis_readback_json" "$final_synthesis_mission_id"
"$mission_bin" --home "$tmp_home/state" import atlas-final-synthesis-readback --mission "$final_synthesis_mission_id" --path "$final_synthesis_readback_json" >"$final_synthesis_import_json"
json_check atlas_final_synthesis_import "$final_synthesis_import_json"
"$mission_bin" --home "$tmp_home/state" mission inspect --mission "$final_synthesis_mission_id" --json >"$final_synthesis_inspect_json"
json_check atlas_final_synthesis_inspect "$final_synthesis_inspect_json"
"$mission_bin" --home "$tmp_home/state" checkpoint inspect --mission "$final_synthesis_mission_id" --json >"$final_synthesis_checkpoint_json"
json_check checkpoint_resume_bundle "$final_synthesis_checkpoint_json" --mission-id "$final_synthesis_mission_id"

"$mission_bin" --home "$tmp_home/state" start "doctor active lease health smoke" >"$mission_json"
doctor_mission_id="$(python3 "$json_helper" extract-mission-id "$mission_json")"
"$mission_bin" --home "$tmp_home/state" continue --mission "$doctor_mission_id" --until-done --max-iterations 2 >/dev/null
"$mission_bin" --home "$tmp_home/state" doctor --json >"$doctor_json"
json_check doctor_runtime "$doctor_json"

grep -Fq "ao-mission final rollup --mission <mission-id>" docs/operator-next-actions.md
grep -q "Do not stop before 25 completed nodes" "$readiness_fixtures/ao-mission-atlas-wave-import-v01/next-recommended-prompt.md"
grep -Fq "ao-mission command status --mission <mission-id> --json" docs/operator-next-actions.md
grep -Fq "ao-mission final reconcile --mission <mission-id>" docs/operator-next-actions.md
grep -Fq "final-reconciliation-packet.json" docs/operator-next-actions.md
json_check final_reconciliation_fixture examples/valid/final-reconciliation-packet.json
json_check final_reconciliation_mismatch_fixture examples/valid/final-reconciliation-mismatch-packet.json
json_check final_rollup_ready_node_denial examples/valid/final-rollup-ready-node-denial.json
json_check sentinel_public_safety_scan "$readiness_fixtures/ao-mission-atlas-wave-import-v01/sentinel-public-safety-scan.json"
json_check production_readiness_branch_cleanup "$readiness_fixtures/ao-mission-atlas-wave-import-v01/production-readiness-branch-cleanup.json"
json_check promoter_no_promotion_summary "$readiness_fixtures/ao-mission-atlas-wave-import-v01/promoter-no-promotion-summary.json"
json_check foundry_terminal_state_binding examples/valid/foundry-terminal-state-binding.json
json_check command_compact_timeline examples/valid/command-compact-timeline-readback.json
json_check mission_status_timeline_vector examples/valid/mission-status-timeline-compatibility-vector.json
json_check command_status_lease_checkpoint examples/valid/command-status-lease-checkpoint-readback.json
json_check doctor_command_compact_risk examples/valid/doctor-command-compact-early-return-risk.json
json_check beta_incident_stop_rule examples/valid/beta-incident-stop-rule-readback.json
json_check pilot_feedback_capture examples/valid/pilot-feedback-capture-packet.json
json_check final_reconciliation_event_search examples/valid/final-reconciliation-event-search-readback.json
python3 "$json_helper" check-tree promoter_no_promotion_node "$readiness_fixtures/ao-mission-atlas-wave-import-v01/nodes" promoter-no-promotion.json
python3 "$json_helper" check-tree sentinel_public_safety_node "$readiness_fixtures/ao-mission-atlas-wave-import-v01/nodes" sentinel-public-safety.json
json_check wave_boundary_readiness "$readiness_fixtures/ao-mission-atlas-wave-import-v01/wave-boundary-readiness.json"
json_check merged_pr_branch_cleanup "$readiness_fixtures/ao-mission-atlas-wave-import-v01/merged-pr-branch-cleanup.json"
json_check atlas_wave_final_synthesis_fixture "$readiness_fixtures/ao-mission-atlas-wave-import-v01/final-synthesis.json"
json_check post_merge_final_closure "$readiness_fixtures/ao-mission-atlas-wave-import-v01/post-merge-final-closure.json"
grep -q "Do not stop before 30 completed nodes" "$readiness_fixtures/ao-mission-atlas-wave-import-v01/next-wave-recommended-prompt.md"
json_check wave_duration_ledger "$readiness_fixtures/ao-mission-doubled-wave-v01/duration-ledger.json"
json_check codex_session_duration "$readiness_fixtures/ao-mission-doubled-wave-v01/codex-session-duration-readback.json"
json_check atlas_final_synthesis_fixture examples/valid/atlas-final-synthesis-readback.json
json_check event_search_production_smoke "$readiness_fixtures/ao-mission-atlas-wave-import-v01/event-search-production-smoke.json"
json_check event_evidence_alias_readback "$readiness_fixtures/ao-mission-doubled-wave-v01/nodes/node-10-event-evidence-aliases/event-alias-search-readbacks.json"
json_check event_evidence_alias_searches examples/valid/event-evidence-alias-search-readbacks.json
json_check bounded_autonomy_month3 examples/valid/bounded-autonomy-month3-recovery-readback.json
json_check bounded_autonomy_month4 examples/valid/bounded-autonomy-month4-controlled-improvement-readback.json
json_check bounded_autonomy_month5 examples/valid/bounded-autonomy-month5-dogfood-readback.json
json_check bounded_autonomy_month6 examples/valid/bounded-autonomy-month6-qualification-readback.json
json_check bounded_autonomy_repair examples/valid/bounded-autonomy-repair-from-month3-readback.json
json_check sqlite_migration_dry_run examples/valid/mission-sqlite-migration-dry-run.json

echo "AO Mission production readiness: 100/100 status=ready"
