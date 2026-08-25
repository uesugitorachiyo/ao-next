#!/usr/bin/env python3
"""Tests for the dependency-free production-readiness JSON checks."""

import importlib.util
import json
import math
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / "scripts" / "production_readiness_json.py"
READINESS = ROOT / "scripts" / "production-readiness.sh"

EXPECTED_PROFILES = {
    "atlas_recommendation_import",
    "atlas_recommendation_inspect",
    "final_reconciliation_runtime",
    "timeline_query_index",
    "restart_recovery_proof",
    "event_search_runtime",
    "atlas_continuation_prompt",
    "atlas_wave_synthesis_runtime",
    "atlas_final_synthesis_import",
    "atlas_final_synthesis_inspect",
    "checkpoint_resume_bundle",
    "doctor_runtime",
    "final_reconciliation_fixture",
    "final_reconciliation_mismatch_fixture",
    "final_rollup_ready_node_denial",
    "sentinel_public_safety_scan",
    "production_readiness_branch_cleanup",
    "promoter_no_promotion_summary",
    "foundry_terminal_state_binding",
    "command_compact_timeline",
    "mission_status_timeline_vector",
    "command_status_lease_checkpoint",
    "doctor_command_compact_risk",
    "beta_incident_stop_rule",
    "pilot_feedback_capture",
    "final_reconciliation_event_search",
    "promoter_no_promotion_node",
    "sentinel_public_safety_node",
    "wave_boundary_readiness",
    "merged_pr_branch_cleanup",
    "atlas_wave_final_synthesis_fixture",
    "post_merge_final_closure",
    "wave_duration_ledger",
    "codex_session_duration",
    "atlas_final_synthesis_fixture",
    "event_search_production_smoke",
    "event_evidence_alias_readback",
    "event_evidence_alias_searches",
    "bounded_autonomy_month3",
    "bounded_autonomy_month4",
    "bounded_autonomy_month5",
    "bounded_autonomy_month6",
    "bounded_autonomy_repair",
    "sqlite_migration_dry_run",
}

STATIC_CASES = {
    "final_reconciliation_fixture": "examples/valid/final-reconciliation-packet.json",
    "final_reconciliation_mismatch_fixture": "examples/valid/final-reconciliation-mismatch-packet.json",
    "final_rollup_ready_node_denial": "examples/valid/final-rollup-ready-node-denial.json",
    "sentinel_public_safety_scan": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/sentinel-public-safety-scan.json",
    "production_readiness_branch_cleanup": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/production-readiness-branch-cleanup.json",
    "promoter_no_promotion_summary": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/promoter-no-promotion-summary.json",
    "foundry_terminal_state_binding": "examples/valid/foundry-terminal-state-binding.json",
    "command_compact_timeline": "examples/valid/command-compact-timeline-readback.json",
    "mission_status_timeline_vector": "examples/valid/mission-status-timeline-compatibility-vector.json",
    "command_status_lease_checkpoint": "examples/valid/command-status-lease-checkpoint-readback.json",
    "doctor_command_compact_risk": "examples/valid/doctor-command-compact-early-return-risk.json",
    "beta_incident_stop_rule": "examples/valid/beta-incident-stop-rule-readback.json",
    "pilot_feedback_capture": "examples/valid/pilot-feedback-capture-packet.json",
    "final_reconciliation_event_search": "examples/valid/final-reconciliation-event-search-readback.json",
    "wave_boundary_readiness": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/wave-boundary-readiness.json",
    "merged_pr_branch_cleanup": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/merged-pr-branch-cleanup.json",
    "atlas_wave_final_synthesis_fixture": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/final-synthesis.json",
    "post_merge_final_closure": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/post-merge-final-closure.json",
    "wave_duration_ledger": "testdata/production-readiness/ao-mission-doubled-wave-v01/duration-ledger.json",
    "codex_session_duration": "testdata/production-readiness/ao-mission-doubled-wave-v01/codex-session-duration-readback.json",
    "atlas_final_synthesis_fixture": "examples/valid/atlas-final-synthesis-readback.json",
    "event_search_production_smoke": "testdata/production-readiness/ao-mission-atlas-wave-import-v01/event-search-production-smoke.json",
    "event_evidence_alias_readback": "testdata/production-readiness/ao-mission-doubled-wave-v01/nodes/node-10-event-evidence-aliases/event-alias-search-readbacks.json",
    "event_evidence_alias_searches": "examples/valid/event-evidence-alias-search-readbacks.json",
    "bounded_autonomy_month3": "examples/valid/bounded-autonomy-month3-recovery-readback.json",
    "bounded_autonomy_month4": "examples/valid/bounded-autonomy-month4-controlled-improvement-readback.json",
    "bounded_autonomy_month5": "examples/valid/bounded-autonomy-month5-dogfood-readback.json",
    "bounded_autonomy_month6": "examples/valid/bounded-autonomy-month6-qualification-readback.json",
    "bounded_autonomy_repair": "examples/valid/bounded-autonomy-repair-from-month3-readback.json",
    "sqlite_migration_dry_run": "examples/valid/mission-sqlite-migration-dry-run.json",
}


def run_helper(*args):
    return subprocess.run(
        [sys.executable, str(HELPER), *map(str, args)],
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
    )


class ProductionReadinessJSONTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("production_readiness_json", HELPER)
        if spec is None or spec.loader is None:
            raise AssertionError("could not load production readiness JSON helper")
        cls.helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.helper)

    def write_json(self, directory, name, value):
        path = Path(directory) / name
        path.write_text(json.dumps(value), encoding="utf-8")
        return path

    def test_named_profiles_cover_every_readiness_predicate(self):
        self.assertEqual(set(self.helper.CHECKS), EXPECTED_PROFILES)

        body = READINESS.read_text(encoding="utf-8")
        used = set()
        for line in body.splitlines():
            words = line.split()
            if words[:1] == ["json_check"]:
                used.add(words[1])
            elif "check-tree" in words:
                used.add(words[words.index("check-tree") + 1])
        self.assertEqual(used, EXPECTED_PROFILES)
        self.assertEqual(body.count("extract-mission-id"), 3)
        self.assertEqual(body.count("bind-mission-id"), 1)

    def test_every_static_profile_accepts_its_repository_fixture(self):
        self.assertEqual(len(STATIC_CASES), 30)
        for profile, path in STATIC_CASES.items():
            with self.subTest(profile=profile):
                result = run_helper("check", profile, ROOT / path)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_extract_mission_id_uses_strict_json(self):
        with tempfile.TemporaryDirectory() as directory:
            valid = self.write_json(directory, "valid.json", {"mission_id": "mission-123"})
            result = run_helper("extract-mission-id", valid)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, "mission-123\n")

            duplicate = Path(directory) / "duplicate.json"
            duplicate.write_text('{"mission_id":"a","mission_id":"b"}', encoding="utf-8")
            result = run_helper("extract-mission-id", duplicate)
            self.assertEqual(result.returncode, 2)
            self.assertIn("duplicate JSON key: mission_id", result.stderr)

            malformed = Path(directory) / "malformed.json"
            malformed.write_text("{", encoding="utf-8")
            result = run_helper("extract-mission-id", malformed)
            self.assertEqual(result.returncode, 2)
            self.assertIn("invalid JSON", result.stderr)

            missing = self.write_json(directory, "missing.json", {})
            result = run_helper("extract-mission-id", missing)
            self.assertEqual(result.returncode, 2)
            self.assertIn("mission_id must be a non-empty string", result.stderr)

    def test_all_cli_json_operations_reject_non_finite_numbers(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "non-finite.json"
            destination = Path(directory) / "bound.json"
            for constant in ("NaN", "Infinity", "-Infinity", "1e999", "-1e999"):
                source.write_text(
                    '{"mission_id":"mission-123","unchecked":{"nested":' + constant + "}}",
                    encoding="utf-8",
                )
                commands = (
                    ("extract-mission-id", source),
                    ("bind-mission-id", source, destination, "mission-new"),
                    ("check", "sqlite_migration_dry_run", source),
                )
                for command in commands:
                    with self.subTest(constant=constant, command=command[0]):
                        result = run_helper(*command)
                        self.assertEqual(result.returncode, 2)
                        self.assertIn(f"non-finite JSON number: {constant}", result.stderr)

    def test_bind_serialization_defensively_disallows_non_finite_values(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "bound.json"
            with mock.patch.object(
                self.helper,
                "load_json",
                return_value={"mission_id": "old", "nested": {"value": math.nan}},
            ):
                with self.assertRaisesRegex(self.helper.ValidationError, "non-finite output number"):
                    self.helper.main(
                        ["bind-mission-id", "ignored.json", str(destination), "mission-new"]
                    )
            self.assertFalse(destination.exists())

    def test_numeric_helpers_reject_non_finite_values_defensively(self):
        for numeric in (math.nan, math.inf, -math.inf):
            with self.subTest(numeric=numeric):
                with self.assertRaisesRegex(self.helper.ValidationError, "finite number"):
                    self.helper.number({"metric": numeric}, "metric")

    def test_load_rejects_oversize_nonregular_symlink_replacement_and_growth(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            oversized = root / "oversized.json"
            with oversized.open("wb") as handle:
                handle.seek(self.helper.MAX_JSON_BYTES)
                handle.write(b"x")
            result = run_helper("extract-mission-id", oversized)
            self.assertEqual(result.returncode, 2)
            self.assertIn("exceeds", result.stderr)

            result = run_helper("extract-mission-id", root)
            self.assertEqual(result.returncode, 2)
            self.assertIn("regular file", result.stderr)

            target = self.write_json(root, "target.json", {"mission_id": "mission-123"})
            link = root / "link.json"
            try:
                link.symlink_to(target)
            except OSError as error:
                if os.name == "nt" and getattr(error, "winerror", None) == 1314:
                    pass
                else:
                    raise
            else:
                result = run_helper("extract-mission-id", link)
                self.assertEqual(result.returncode, 2)
                self.assertIn("symlink", result.stderr)

            opened = target.stat()
            replacement = SimpleNamespace(
                st_mode=stat.S_IFREG,
                st_size=opened.st_size,
                st_dev=opened.st_dev + 1,
                st_ino=opened.st_ino,
            )
            with mock.patch.object(self.helper.os, "fstat", return_value=replacement):
                with self.assertRaisesRegex(self.helper.ValidationError, "replaced"):
                    self.helper.load_json(target)

            unavailable_identity = SimpleNamespace(
                st_mode=stat.S_IFREG,
                st_size=opened.st_size,
            )
            with self.assertRaisesRegex(self.helper.ValidationError, "identity fields are unavailable"):
                self.helper.load_json(target, expected_info=unavailable_identity)

            growth_source = self.write_json(root, "growth.json", {})
            with mock.patch.object(self.helper, "MAX_JSON_BYTES", 8), mock.patch.object(
                self.helper.os, "read", return_value=b"x" * 9
            ):
                with self.assertRaisesRegex(self.helper.ValidationError, "grew beyond"):
                    self.helper.load_json(growth_source)

    def test_bind_mission_id_preserves_document_and_writes_utf8(self):
        with tempfile.TemporaryDirectory() as directory:
            source = self.write_json(directory, "source.json", {"mission_id": "old", "label": "caf\u00e9"})
            destination = Path(directory) / "bound.json"
            result = run_helper("bind-mission-id", source, destination, "mission-new")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                json.loads(destination.read_text(encoding="utf-8")),
                {"mission_id": "mission-new", "label": "caf\u00e9"},
            )

    def test_checks_fail_closed_for_malformed_missing_and_wrong_types(self):
        with tempfile.TemporaryDirectory() as directory:
            malformed = Path(directory) / "malformed.json"
            malformed.write_text("{", encoding="utf-8")
            for path in (malformed, self.write_json(directory, "missing.json", {})):
                result = run_helper("check", "final_reconciliation_fixture", path)
                self.assertEqual(result.returncode, 2)
                self.assertIn("production-readiness JSON error:", result.stderr)

            wrong_type = self.write_json(
                directory,
                "wrong-type.json",
                {
                    "schema": "ao.mission.final-reconciliation-packet.v0.1",
                    "status": "ready",
                    "artifacts_agree": "true",
                    "promotion_claimed": False,
                    "rsi_remains_denied": True,
                    "claims_authority_advance": False,
                    "safe_to_execute": False,
                    "executes_work": False,
                    "approves_work": False,
                },
            )
            result = run_helper("check", "final_reconciliation_fixture", wrong_type)
            self.assertEqual(result.returncode, 2)
            self.assertIn("artifacts_agree", result.stderr)

    def test_batch_check_validates_every_matching_file(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            good = {
                "promotion_claimed": False,
            }
            self.write_json(root, "promoter-no-promotion.json", good)
            nested = root / "nested"
            nested.mkdir()
            self.write_json(nested, "promoter-no-promotion.json", good)
            result = run_helper(
                "check-tree", "promoter_no_promotion_node", root, "promoter-no-promotion.json"
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.write_json(nested, "promoter-no-promotion.json", {"promotion_claimed": True})
            result = run_helper(
                "check-tree", "promoter_no_promotion_node", root, "promoter-no-promotion.json"
            )
            self.assertEqual(result.returncode, 2)
            self.assertIn("nested", result.stderr)

    def test_batch_check_rejects_unsafe_names_roots_matches_and_empty_batches(self):
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory() as outside_directory:
            root = Path(directory)
            outside = self.write_json(
                outside_directory, "promoter-no-promotion.json", {"promotion_claimed": False}
            )
            invalid_names = (
                "",
                "../promoter-no-promotion.json",
                "nested/promoter-no-promotion.json",
                "nested\\promoter-no-promotion.json",
                ".",
                "..",
                str(outside.resolve()),
            )
            for filename in invalid_names:
                with self.subTest(filename=filename):
                    result = run_helper("check-tree", "promoter_no_promotion_node", root, filename)
                    self.assertEqual(result.returncode, 2)
                    self.assertIn("basename", result.stderr)

            result = run_helper(
                "check-tree", "promoter_no_promotion_node", root, "promoter-no-promotion.json"
            )
            self.assertEqual(result.returncode, 2)
            self.assertIn("no files", result.stderr)

            root_file = self.write_json(outside_directory, "not-a-root.json", {})
            result = run_helper(
                "check-tree", "promoter_no_promotion_node", root_file, "promoter-no-promotion.json"
            )
            self.assertEqual(result.returncode, 2)
            self.assertIn("root must be a directory", result.stderr)

            root_link = Path(outside_directory) / "root-link"
            try:
                root_link.symlink_to(root, target_is_directory=True)
            except OSError as error:
                if os.name == "nt" and getattr(error, "winerror", None) == 1314:
                    root_link = None
                else:
                    raise
            if root_link is not None:
                result = run_helper(
                    "check-tree", "promoter_no_promotion_node", root_link, "promoter-no-promotion.json"
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("root must not be a symlink", result.stderr)

                match_link = root / "promoter-no-promotion.json"
                match_link.symlink_to(outside)
                result = run_helper(
                    "check-tree", "promoter_no_promotion_node", root, "promoter-no-promotion.json"
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("match must be a regular non-symlink file", result.stderr)

            with self.assertRaisesRegex(self.helper.ValidationError, "escapes root"):
                self.helper.validate_tree_candidate(root.resolve(), outside)

    def test_batch_read_rejects_ancestor_swap_after_containment_validation(self):
        with tempfile.TemporaryDirectory() as parent_directory:
            parent = Path(parent_directory)
            root = parent / "root"
            original_parent = root / "records"
            outside_parent = parent / "outside"
            saved_parent = root / "saved-original"
            original_parent.mkdir(parents=True)
            outside_parent.mkdir()
            filename = "promoter-no-promotion.json"
            self.write_json(original_parent, filename, {"promotion_claimed": False})
            self.write_json(outside_parent, filename, {"promotion_claimed": True})

            swapped = False

            def swap_ancestor(_candidate):
                nonlocal swapped
                if swapped:
                    return
                original_parent.replace(saved_parent)
                outside_parent.replace(original_parent)
                swapped = True

            with self.assertRaisesRegex(self.helper.ValidationError, "replaced before open"):
                self.helper.run_tree_checks(
                    "promoter_no_promotion_node",
                    root,
                    filename,
                    before_open=swap_ancestor,
                )
            self.assertTrue(swapped)

    def test_readiness_script_is_jq_free_cleanup_safe_and_read_only_formatting(self):
        body = READINESS.read_text(encoding="utf-8")
        executable = "\n".join(
            line for line in body.splitlines() if line.strip() and not line.lstrip().startswith("#")
        )
        self.assertNotRegex(executable, r"(^|[|;&( ])jq([ )]|$)")
        self.assertNotIn("gofmt -w", executable)
        self.assertIn("PYTHONDONTWRITEBYTECODE=1", executable)
        self.assertRegex(executable, r"trap .*EXIT")
        self.assertNotRegex(executable, r"go build ./cmd/ao-mission")
        self.assertRegex(executable, r"go build -o \"\$tmp_home/")


if __name__ == "__main__":
    unittest.main()
