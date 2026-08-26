import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "tests" / "mission-migration" / "replay.py"


def load_module():
    spec = importlib.util.spec_from_file_location("mission_replay", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ReplayTests(unittest.TestCase):
    def test_frozen_operations_have_exact_handlers(self):
        spec = importlib.util.spec_from_file_location("mission_replay", MODULE_PATH)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        corpus = json.loads(
            (ROOT / "tests/fixtures/mission-migration/corpus-v1.json")
            .read_text(encoding="utf-8")
        )
        self.assertEqual(
            {vector["operation"] for vector in corpus["vectors"]},
            set(module.OPERATION_HANDLERS),
        )

    def test_duplicate_operation_id_is_rejected(self):
        module = load_module()
        source = ROOT / "tests/fixtures/mission-migration/corpus-v1.json"
        corpus = json.loads(source.read_text(encoding="utf-8"))
        corpus["vectors"][1]["id"] = corpus["vectors"][0]["id"]
        corpus["manifest_digest"] = module.semantic_digest(corpus)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "corpus.json"
            path.write_text(json.dumps(corpus), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "duplicate operation ID"):
                module.load_corpus(path)

    def test_corpus_trust_boundary_rejects_structural_drift(self):
        module = load_module()
        source = ROOT / "tests/fixtures/mission-migration/corpus-v1.json"
        original = json.loads(source.read_text(encoding="utf-8"))
        mutations = {
            "unknown top-level field": lambda value: value.update(extra=True),
            "wrong schema": lambda value: value.update(schema_version="wrong"),
            "wrong source head": lambda value: value.update(source_head="0" * 40),
            "reordered vectors": lambda value: value["vectors"].reverse(),
            "missing vector": lambda value: value["vectors"].pop(),
            "extra vector": lambda value: value["vectors"].append(value["vectors"][0]),
            "unsafe fixture path": lambda value: value["vectors"][0].update(
                fixture_path="../escape.json"
            ),
        }
        for message, mutate in mutations.items():
            with self.subTest(message=message), tempfile.TemporaryDirectory() as directory:
                corpus = json.loads(json.dumps(original))
                mutate(corpus)
                corpus["manifest_digest"] = module.semantic_digest(corpus)
                path = Path(directory) / "corpus.json"
                path.write_text(json.dumps(corpus), encoding="utf-8")
                with self.assertRaisesRegex(ValueError, message):
                    module.load_corpus(path, fixture_root=source.parent)

    def test_corpus_rejects_duplicate_json_key_and_oversize_before_decoding(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            duplicate = root / "duplicate.json"
            duplicate.write_text(
                '{"schema_version":"first","schema_version":"second"}',
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "duplicate JSON key"):
                module.load_corpus(duplicate)
            oversized = root / "oversized.json"
            oversized.write_bytes(b" " * (1024 * 1024 + 1))
            with self.assertRaisesRegex(ValueError, "corpus exceeds 1 MiB"):
                module.load_corpus(oversized)

    def test_reference_archive_without_git_uses_frozen_source_head(self):
        module = load_module()
        self.assertEqual(
            module.validate_reference_head(None), module.EXPECTED_SOURCE_HEAD
        )
        self.assertEqual(
            module.validate_reference_head(module.EXPECTED_SOURCE_HEAD),
            module.EXPECTED_SOURCE_HEAD,
        )
        with self.assertRaisesRegex(ValueError, "reference source head drift"):
            module.validate_reference_head("0" * 40)

    def test_windows_source_inventory_uses_archive_manifest_mode(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "script.sh"
            path.write_text("#!/bin/sh\n", encoding="utf-8")
            self.assertEqual(module.source_mode(path, "100755", "nt"), "100755")
            self.assertEqual(module.source_mode(path, "100644", "nt"), "100644")

    def test_normalization_replaces_only_dynamic_fields_and_their_digests(self):
        module = load_module()
        mission_id = "mission-0123456789abcdef"
        timestamp = "2026-08-26T12:34:56.123456Z"
        temporary_root = "/tmp/AO Next replay 123"
        derived = "sha256:" + hashlib.sha256(mission_id.encode()).hexdigest()
        value = {
            "mission_id": mission_id,
            "generated_at_utc": timestamp,
            "artifact_path": temporary_root + "/artifact.json",
            "continuation_command": "ao-mission continue --mission " + mission_id,
            "identity_digest": derived,
            "objective": "mission-0123456789abcdef is literal text",
            "stable_digest": "sha256:" + "a" * 64,
            "not_a_timestamp": "2026-08-26",
        }
        self.assertEqual(
            module.normalize_record(
                value,
                mission_ids={mission_id},
                temporary_roots={temporary_root},
            ),
            {
                "mission_id": "${mission_id}",
                "generated_at_utc": "${timestamp}",
                "artifact_path": "${temporary_root}/artifact.json",
                "continuation_command": "ao-mission continue --mission ${mission_id}",
                "identity_digest": "${digest:mission_id}",
                "objective": "mission-0123456789abcdef is literal text",
                "stable_digest": "sha256:" + "a" * 64,
                "not_a_timestamp": "2026-08-26",
            },
        )

    def test_archive_digest_is_recomputed_from_raw_archive_fields(self):
        module = load_module()
        wrong = "sha256:" + "0" * 64
        raw = (
            '{"schema":"ao.mission.archive.v0.1","archive_digest":"'
            + wrong
            + '","objective":"<日本>"}'
        ).encode()
        digest_input = (
            '{"schema":"ao.mission.archive.v0.1","archive_digest":"",'
            '"objective":"\\u003c日本\\u003e"}'
        ).encode()
        expected = "sha256:" + hashlib.sha256(digest_input).hexdigest()
        self.assertEqual(module.archive_digest_from_raw(raw), expected)
        self.assertNotEqual(module.archive_digest_from_raw(raw), wrong)

    @unittest.skipIf(os.name == "nt", "Unix symlink boundary")
    def test_cli_paths_reject_symlink_components_and_dangling_output(self):
        module = load_module()
        temporary_base = Path("/private/tmp")
        if not temporary_base.is_dir():
            temporary_base = Path(tempfile.gettempdir())
        with tempfile.TemporaryDirectory(
            prefix="AO Next 路径 boundary ", dir=temporary_base
        ) as directory:
            root = Path(directory)
            real = root / "real root"
            real.mkdir()
            linked = root / "linked root"
            linked.symlink_to(real, target_is_directory=True)
            dangling_output = root / "dangling output.json"
            dangling_output.symlink_to(root / "missing output.json")
            clean = module.argparse.Namespace(
                corpus=str(ROOT / "tests/fixtures/mission-migration/corpus-v1.json"),
                reference_source=str(ROOT / "mission"),
                candidate_source=str(ROOT / "mission"),
                evidence_root=str(root / "证据 root with spaces"),
                output=str(root / "result with spaces.json"),
            )
            paths = module.validate_cli_paths(clean)
            self.assertEqual(paths["evidence_root"], root / "证据 root with spaces")
            for field, value in (
                ("corpus", linked / "corpus.json"),
                ("reference_source", linked),
                ("candidate_source", linked),
                ("evidence_root", linked / "evidence"),
                ("output", dangling_output),
                ("output", linked / "output.json"),
            ):
                args = module.argparse.Namespace(**vars(clean))
                setattr(args, field, str(value))
                with self.subTest(field=field, value=value), self.assertRaisesRegex(
                    ValueError, "symlink or reparse"
                ):
                    module.validate_cli_paths(args)

    def test_windows_reparse_attribute_is_rejected(self):
        module = load_module()
        metadata = type("Metadata", (), {"st_file_attributes": 0x400})()
        self.assertTrue(module.is_symlink_or_reparse(metadata))

    def test_process_capture_preserves_normal_stdout_and_stderr(self):
        module = load_module()
        interpreter = module.shutil.which("python3") or module.shutil.which("python")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = module.run_process(
                [
                    interpreter,
                    "-c",
                    "import sys; sys.stdout.write('out'); sys.stderr.write('err')",
                ],
                root,
                root / "stdout",
                root / "stderr",
            )
            self.assertEqual(
                result, {"exit_code": 0, "stdout": "out", "stderr": "err"}
            )
            self.assertEqual((root / "stdout").read_bytes(), b"out")
            self.assertEqual((root / "stderr").read_bytes(), b"err")

    def test_process_capture_terminates_at_combined_output_limit(self):
        module = load_module()
        interpreter = module.shutil.which("python3") or module.shutil.which("python")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stdout = root / "stdout"
            stderr = root / "stderr"
            marker = root / "child-completed"
            original_limit = module.OUTPUT_LIMIT
            module.OUTPUT_LIMIT = 4096
            try:
                child = (
                    "import pathlib,sys,time\n"
                    "for _ in range(1000):\n"
                    " sys.stdout.buffer.write(b'x'*1024)\n"
                    " sys.stdout.buffer.flush()\n"
                    " time.sleep(0.005)\n"
                    f"pathlib.Path({str(marker)!r}).write_text('completed')\n"
                )
                with self.assertRaisesRegex(
                    RuntimeError, "process output exceeded 16 MiB"
                ):
                    module.run_process(
                        [interpreter, "-c", child], root, stdout, stderr
                    )
            finally:
                module.OUTPUT_LIMIT = original_limit
            self.assertFalse(marker.exists())
            self.assertLessEqual(stdout.stat().st_size + stderr.stat().st_size, 4096)

    def test_native_workflow_binds_checkout_and_manifest_to_event_head(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        native = workflow.split("  native:\n", 1)[1]
        event_head = "${{ github.event.pull_request.head.sha || github.sha }}"
        self.assertIn(f"          ref: {event_head}", native)
        self.assertIn(f"          EXPECTED_CANDIDATE_HEAD: {event_head}", native)
        self.assertIn('test "$observed_head" = "$EXPECTED_CANDIDATE_HEAD"', native)
        self.assertIn('"source_head": os.environ["EXPECTED_CANDIDATE_HEAD"]', native)
        self.assertIn(
            'value["candidate_source_head"] != os.environ["EXPECTED_CANDIDATE_HEAD"]',
            native,
        )


if __name__ == "__main__":
    unittest.main()
