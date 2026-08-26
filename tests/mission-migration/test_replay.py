import hashlib
import importlib.util
import json
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


if __name__ == "__main__":
    unittest.main()
