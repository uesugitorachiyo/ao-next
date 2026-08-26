#!/usr/bin/env python3
"""Provider-free old/new AO Mission behavior equivalence replay."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time


ROOT = Path(__file__).resolve().parents[2]
CORPUS_LIMIT = 1024 * 1024
OUTPUT_LIMIT = 16 * 1024 * 1024
TIMEOUT_SECONDS = 60
EXPECTED_SCHEMA = "ao.next.mission-equivalence-corpus.v1"
EXPECTED_SOURCE_HEAD = "05567fdd7c3fc64814ca4122b3f431d4ed9aaded"
EXPECTED_CORPUS_DIGEST = (
    "sha256:c82ae75a836c8a0c94686087a98dc2c5b7a525c59afcfa52fcbb7a2b1a3ed428"
)
EXPECTED_TOP_LEVEL_FIELDS = {
    "schema_version",
    "source_repository",
    "source_head",
    "status_domains",
    "source_files",
    "vectors",
    "manifest_digest",
}
EXPECTED_VECTOR_FIELDS = {
    "id",
    "operation",
    "arguments",
    "setup_state",
    "expected_result",
    "expected_error",
    "expected_state",
    "source_paths",
    "fixture_path",
    "bytes",
    "digest",
}
EXPECTED_VECTORS = (
    ("archive-validate-import-round-trip", "archive_validate_import_round_trip"),
    ("command-status", "command_status"),
    ("missing-objective-contract", "validate_contract_rejected"),
    ("lifecycle-pause-resume", "lifecycle_pause_resume"),
    ("mission-record-contract", "validate_contract_accepted"),
    ("public-safety-safe", "public_safety_accepted"),
    ("public-safety-symlink", "public_safety_rejected"),
)
ALLOWED_TOKENS = {
    "${home}",
    "${export_home}",
    "${import_home}",
    "${mission_id}",
    "${archive_path}",
    "${fixture}",
    "${scan_root}",
}
DYNAMIC_DIGEST_FIELDS = {
    "archive_digest",
    "mission_digest",
    "record_digest",
    "route_history_digest",
}


def canonical_bytes(value):
    return json.dumps(
        value, ensure_ascii=False, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")


def sha256_bytes(value):
    return "sha256:" + hashlib.sha256(value).hexdigest()


def semantic_digest(corpus):
    return sha256_bytes(
        canonical_bytes(
            [
                corpus["schema_version"],
                corpus["source_repository"],
                corpus["source_head"],
                corpus["status_domains"],
                corpus["source_files"],
                corpus["vectors"],
            ]
        )
    )


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def require_regular(path, description="input"):
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"{description} must be a regular non-symlink file")


def require_directory(path, description="input"):
    if path.is_symlink() or not path.is_dir():
        raise ValueError(f"{description} must be a non-symlink directory")


def validate_relative_path(value):
    if (
        not isinstance(value, str)
        or not value
        or "\\" in value
        or value.startswith("/")
        or ":" in value.split("/", 1)[0]
        or any(part in ("", ".", "..") for part in value.split("/"))
    ):
        raise ValueError("unsafe fixture path")


def load_corpus(path, fixture_root=None):
    require_regular(path, "corpus")
    size = path.stat().st_size
    if size > CORPUS_LIMIT:
        raise ValueError("corpus exceeds 1 MiB")
    corpus = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=strict_object)
    if not isinstance(corpus, dict):
        raise ValueError("corpus must be an object")
    fields = set(corpus)
    if fields != EXPECTED_TOP_LEVEL_FIELDS:
        raise ValueError("unknown top-level field" if fields - EXPECTED_TOP_LEVEL_FIELDS else "missing top-level field")
    if corpus["schema_version"] != EXPECTED_SCHEMA:
        raise ValueError("wrong schema")
    if corpus["source_repository"] != "ao-mission":
        raise ValueError("wrong source repository")
    if corpus["source_head"] != EXPECTED_SOURCE_HEAD:
        raise ValueError("wrong source head")
    if corpus["status_domains"] != {
        "mission_durable_source": "durable_ao_mission_source_status",
        "engine_projection": "future_read_only_engine_projection",
        "conflated": False,
    }:
        raise ValueError("status domains are invalid")
    vectors = corpus["vectors"]
    if not isinstance(vectors, list):
        raise ValueError("vectors must be an array")
    if len(vectors) < len(EXPECTED_VECTORS):
        raise ValueError("missing vector")
    if len(vectors) > len(EXPECTED_VECTORS):
        raise ValueError("extra vector")
    ids = [vector.get("id") if isinstance(vector, dict) else None for vector in vectors]
    if len(ids) != len(set(ids)):
        raise ValueError("duplicate operation ID")
    observed_vectors = tuple(
        (vector.get("id"), vector.get("operation")) if isinstance(vector, dict) else (None, None)
        for vector in vectors
    )
    if observed_vectors != EXPECTED_VECTORS:
        raise ValueError("reordered vectors")
    for vector in vectors:
        if set(vector) != EXPECTED_VECTOR_FIELDS:
            raise ValueError("vector fields are invalid")
        validate_relative_path(vector["fixture_path"])
        if not vector["fixture_path"].startswith("vectors/"):
            raise ValueError("unsafe fixture path")
        for command in vector["arguments"]:
            if not isinstance(command, list) or not command:
                raise ValueError("vector arguments are invalid")
            for argument in command:
                if not isinstance(argument, str) or not argument:
                    raise ValueError("vector arguments are invalid")
                remainder = argument
                for token in ALLOWED_TOKENS:
                    remainder = remainder.replace(token, "")
                if "${" in remainder:
                    raise ValueError("vector contains an unknown token")
    if semantic_digest(corpus) != corpus["manifest_digest"]:
        raise ValueError("wrong semantic digest")
    if corpus["manifest_digest"] != EXPECTED_CORPUS_DIGEST:
        raise ValueError("wrong semantic digest")
    fixture_root = Path(fixture_root) if fixture_root is not None else path.parent
    require_directory(fixture_root, "fixture root")
    expected_paths = [vector["fixture_path"] for vector in vectors]
    vector_root = fixture_root / "vectors"
    require_directory(vector_root, "vector root")
    actual_paths = []
    for fixture in vector_root.rglob("*"):
        if fixture.is_dir() and not fixture.is_symlink():
            continue
        require_regular(fixture, "fixture")
        actual_paths.append(fixture.relative_to(fixture_root).as_posix())
    if sorted(actual_paths) != sorted(expected_paths):
        raise ValueError("fixture inventory drift")
    for vector in vectors:
        fixture = fixture_root / vector["fixture_path"]
        require_regular(fixture, "fixture")
        body = fixture.read_bytes()
        if len(body) != vector["bytes"] or sha256_bytes(body) != vector["digest"]:
            raise ValueError("fixture size or digest drift")
    return corpus


def is_rfc3339(value):
    if not isinstance(value, str) or len(value) < 20:
        return False
    if value.endswith("Z"):
        body = value[:-1]
    elif len(value) >= 25 and value[-6] in "+-" and value[-3] == ":":
        if not (value[-5:-3].isdigit() and value[-2:].isdigit()):
            return False
        body = value[:-6]
    else:
        return False
    if "." in body:
        body, fraction = body.split(".", 1)
        if not fraction or not fraction.isdigit():
            return False
    try:
        time.strptime(body, "%Y-%m-%dT%H:%M:%S")
    except ValueError:
        return False
    return True


def normalize_record(value, mission_ids=None, temporary_roots=None, field=None):
    mission_ids = set(mission_ids or ())
    temporary_roots = set(str(root) for root in (temporary_roots or ()))
    dynamic_values = mission_ids | temporary_roots
    dynamic_digests = {
        sha256_bytes(item.encode("utf-8")): (
            "${digest:mission_id}" if item in mission_ids else "${digest:temporary_root}"
        )
        for item in dynamic_values
    }
    if isinstance(value, dict):
        return {
            key: normalize_record(item, mission_ids, temporary_roots, key)
            for key, item in value.items()
        }
    if isinstance(value, list):
        return [normalize_record(item, mission_ids, temporary_roots, field) for item in value]
    if not isinstance(value, str):
        return value
    if value in mission_ids:
        return "${mission_id}"
    if is_rfc3339(value):
        return "${timestamp}"
    if value in dynamic_digests:
        return dynamic_digests[value]
    if field in DYNAMIC_DIGEST_FIELDS and value.startswith("sha256:") and len(value) == 71:
        return "${digest:dynamic_fields}"
    normalized = value
    path_like = "/" in value or "\\" in value
    for root in sorted(temporary_roots, key=len, reverse=True):
        normalized = normalized.replace(root, "${temporary_root}")
    if path_like or field != "objective":
        for mission_id in mission_ids:
            normalized = normalized.replace(mission_id, "${mission_id}")
    return normalized


def provider_free_environment():
    environment = dict(os.environ)
    denied_names = {
        "AO_NEXT_LIVE_PROVIDER_CALLS",
        "AO_NEXT_PROVIDER_FREE_PROGRAM",
        "AO_NEXT_PROVIDER_FREE_PROGRAM_DIGEST",
        "ANTHROPIC_API_KEY",
        "OPENAI_API_KEY",
        "GOOGLE_API_KEY",
        "GEMINI_API_KEY",
    }
    for name in denied_names:
        environment.pop(name, None)
    return environment


def run_process(arguments, cwd, stdout_path, stderr_path):
    process = subprocess.Popen(
        [str(argument) for argument in arguments],
        cwd=cwd,
        env=provider_free_environment(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        shell=False,
    )
    try:
        stdout, stderr = process.communicate(timeout=TIMEOUT_SECONDS)
    except subprocess.TimeoutExpired:
        process.kill()
        process.communicate()
        raise RuntimeError("process exceeded 60-second timeout")
    if len(stdout) > OUTPUT_LIMIT or len(stderr) > OUTPUT_LIMIT:
        raise RuntimeError("process output exceeded 16 MiB")
    stdout_path.write_bytes(stdout)
    stderr_path.write_bytes(stderr)
    try:
        return {
            "exit_code": process.returncode,
            "stdout": stdout.decode("utf-8"),
            "stderr": stderr.decode("utf-8"),
        }
    except UnicodeDecodeError as error:
        raise RuntimeError("process output was not UTF-8") from error


def write_json_new(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("xb") as output:
        output.write(canonical_bytes(value) + b"\n")


def source_mode(path, expected, platform_name=os.name):
    if platform_name == "nt":
        return expected
    return "100755" if path.stat().st_mode & 0o111 else "100644"


def source_inventory(source, corpus, require_exact):
    entries = []
    for expected in corpus["source_files"]:
        path = source / expected["path"]
        require_regular(path, "Mission source file")
        body = path.read_bytes()
        mode = source_mode(path, expected["mode"])
        observed = {
            "path": expected["path"],
            "mode": mode,
            "bytes": len(body),
            "digest": sha256_bytes(body),
        }
        if require_exact and observed != expected:
            raise ValueError(f"reference source inventory drift: {expected['path']}")
        entries.append(observed)
    return sha256_bytes(canonical_bytes(entries))


def git_head(source):
    result = subprocess.run(
        ["git", "-C", str(source), "rev-parse", "HEAD"],
        check=False,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        shell=False,
        timeout=TIMEOUT_SECONDS,
    )
    if result.returncode != 0:
        return None
    head = result.stdout.decode("ascii", errors="strict").strip()
    return head if len(head) == 40 and all(character in "0123456789abcdef" for character in head) else None


def validate_reference_head(observed):
    if observed is not None and observed != EXPECTED_SOURCE_HEAD:
        raise ValueError("reference source head drift")
    return EXPECTED_SOURCE_HEAD


def build_binary(source, command, output, evidence):
    result = run_process(
        ["go", "build", "-trimpath", "-o", output, f"./cmd/{command}"],
        source,
        evidence / "build.stdout",
        evidence / "build.stderr",
    )
    if result["exit_code"] != 0:
        raise RuntimeError(f"{command} build failed")
    require_regular(output, "built Mission binary")


def parsed_stdout(result):
    text = result["stdout"].strip()
    if not text:
        return ""
    try:
        return json.loads(text, object_pairs_hook=strict_object)
    except json.JSONDecodeError:
        return result["stdout"]


def assert_fields(value, expected, label):
    if not isinstance(value, dict):
        raise AssertionError(f"{label} did not return a JSON object")
    for field, wanted in expected.items():
        if value.get(field) != wanted:
            raise AssertionError(
                f"{label} {field} drift: expected {wanted!r}, observed {value.get(field)!r}"
            )


def render_arguments(arguments, bindings):
    rendered = []
    for argument in arguments:
        value = argument
        for token in ALLOWED_TOKENS:
            if token in value:
                if token not in bindings:
                    raise AssertionError(f"token {token} is unavailable")
                value = value.replace(token, str(bindings[token]))
        if "${" in value:
            raise AssertionError("unknown corpus token survived substitution")
        rendered.append(value)
    return rendered


def prepare_case(context, vector):
    case_root = context["case_root"]
    state_root = case_root / "state with spaces"
    scan_root = case_root / "scan root with spaces"
    state_root.mkdir(parents=True)
    scan_root.mkdir()
    fixture = context["fixture_root"] / vector["fixture_path"]
    bindings = {
        "${home}": state_root / "home with spaces",
        "${export_home}": state_root / "export home with spaces",
        "${import_home}": state_root / "import home with spaces",
        "${archive_path}": state_root / "archive output.json",
        "${fixture}": fixture,
        "${scan_root}": scan_root,
    }
    for token in ("${home}", "${export_home}", "${import_home}"):
        Path(bindings[token]).mkdir()
    if vector["operation"] == "public_safety_accepted":
        shutil.copyfile(fixture, scan_root / "safe.json")
    if vector["operation"] == "public_safety_rejected":
        outside = scan_root / "outside.txt"
        outside.write_bytes(fixture.read_bytes())
        try:
            os.symlink("outside.txt", scan_root / "escape")
        except OSError as error:
            raise RuntimeError(f"real symlink creation failed: {type(error).__name__}") from error
    context["state_root"] = state_root
    context["scan_root"] = scan_root
    context["bindings"] = bindings
    return fixture


def execute_commands(context, vector):
    commands = []
    mission_ids = set()
    for index, arguments in enumerate(vector["arguments"], 1):
        rendered = render_arguments(arguments, context["bindings"])
        if rendered[0].endswith("public-safety-scan.py"):
            executable = shutil.which("python3") or shutil.which("python")
            if not executable:
                raise RuntimeError("Python interpreter unavailable")
            command = [executable, context["source"] / rendered[0], *rendered[1:]]
        else:
            command = [context["binary"], *rendered]
        result = run_process(
            command,
            context["source"],
            context["case_root"] / f"command-{index:02d}.stdout",
            context["case_root"] / f"command-{index:02d}.stderr",
        )
        parsed = parsed_stdout(result)
        if isinstance(parsed, dict) and isinstance(parsed.get("mission_id"), str):
            mission_ids.add(parsed["mission_id"])
            context["bindings"]["${mission_id}"] = parsed["mission_id"]
        commands.append({**result, "stdout": parsed})
    return commands, mission_ids


def load_state_record(home, mission_id):
    path = home / "missions" / f"{mission_id}.json"
    require_regular(path, "durable Mission record")
    return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=strict_object)


def replay_command_status(context, vector):
    prepare_case(context, vector)
    commands, mission_ids = execute_commands(context, vector)
    if [command["exit_code"] for command in commands] != [0, 0]:
        raise AssertionError("command-status exit disposition drift")
    start, status = commands[0]["stdout"], commands[1]["stdout"]
    assert_fields(start, {"objective": "command status replay", "status": "active"}, "command-status start")
    mission_id = start["mission_id"]
    assert_fields(
        status,
        {
            "schema": "ao.command.mission-status.v0.1",
            "mission_id": mission_id,
            "read_only": True,
            "executes_work": False,
            "approves_work": False,
            "mutates_repositories": False,
        },
        "command status",
    )
    durable = load_state_record(Path(context["bindings"]["${home}"]), mission_id)
    assert_fields(durable, {"objective": "command status replay", "status": "active"}, "durable command-status record")
    return {"commands": commands, "durable_record": durable}, mission_ids


def replay_lifecycle(context, vector):
    prepare_case(context, vector)
    commands, mission_ids = execute_commands(context, vector)
    if [command["exit_code"] for command in commands] != [0, 0, 0, 0]:
        raise AssertionError("lifecycle exit disposition drift")
    start, pause, resume, status = [command["stdout"] for command in commands]
    assert_fields(start, {"objective": "lifecycle replay"}, "lifecycle start")
    assert_fields(
        pause,
        {"status": "paused", "current_phase": "paused", "exact_next_action": "resume mission before continuation"},
        "lifecycle pause",
    )
    assert_fields(resume, {"status": "active", "current_phase": "routing"}, "lifecycle resume")
    assert_fields(status, {"status": "active"}, "lifecycle status")
    durable = load_state_record(Path(context["bindings"]["${home}"]), start["mission_id"])
    if not any(item.get("reason") == "mission pause boundary" for item in durable.get("route_history", [])):
        raise AssertionError("durable route history lacks mission pause boundary")
    return {"commands": commands, "durable_record": durable}, mission_ids


def replay_archive_round_trip(context, vector):
    prepare_case(context, vector)
    commands, mission_ids = execute_commands(context, vector)
    if [command["exit_code"] for command in commands] != [0, 0, 0, 0, 0]:
        raise AssertionError("archive round-trip exit disposition drift")
    start = commands[0]["stdout"]
    assert_fields(start, {"objective": "archive import round trip"}, "archive start")
    archive_path = Path(context["bindings"]["${archive_path}"])
    require_regular(archive_path, "Mission archive")
    archive = json.loads(archive_path.read_text(encoding="utf-8"), object_pairs_hook=strict_object)
    assert_fields(
        archive,
        {"schema": "ao.mission.archive.v0.1", "safe_to_execute": False, "executes_work": False, "approves_work": False},
        "archive",
    )
    assert_fields(
        commands[2]["stdout"],
        {"schema": "ao.mission.archive-validation.v0.1", "status": "ready", "safe_to_execute": False, "executes_work": False, "approves_work": False},
        "archive validation",
    )
    assert_fields(commands[3]["stdout"], {"schema": "ao.mission.archive-import-readback.v0.1", "status": "ready"}, "archive import")
    assert_fields(commands[4]["stdout"], {"objective": "archive import round trip"}, "archive inspect")
    restored = load_state_record(Path(context["bindings"]["${import_home}"]), start["mission_id"])
    assert_fields(restored, {"objective": "archive import round trip"}, "restored archive record")
    return {"commands": commands, "archive": archive, "restored_record": restored}, mission_ids


def replay_contract_accepted(context, vector):
    fixture = prepare_case(context, vector)
    before = sha256_bytes(fixture.read_bytes())
    commands, mission_ids = execute_commands(context, vector)
    result = commands[0]
    if result["exit_code"] != 0 or result["stderr"] != "":
        raise AssertionError("accepted contract disposition drift")
    assert_fields(
        result["stdout"],
        {"schema": "ao.mission.contract-validation.v0.1", "status": "ready", "contract": "ao.mission.record.v0.1", "blockers": [], "read_only": True, "executes_work": False, "approves_work": False, "mutates_repositories": False},
        "accepted contract",
    )
    if sha256_bytes(fixture.read_bytes()) != before:
        raise AssertionError("accepted contract fixture mutated")
    return {"commands": commands, "fixture_digest": before}, mission_ids


def replay_contract_rejected(context, vector):
    fixture = prepare_case(context, vector)
    before = sha256_bytes(fixture.read_bytes())
    commands, mission_ids = execute_commands(context, vector)
    result = commands[0]
    if result["exit_code"] != 1 or vector["expected_error"] not in result["stderr"]:
        raise AssertionError("rejected contract disposition drift")
    assert_fields(
        result["stdout"],
        {"schema": "ao.mission.contract-validation.v0.1", "status": "blocked", "contract": "", "blockers": ["schema or contract_version is required"], "read_only": True, "executes_work": False, "approves_work": False, "mutates_repositories": False},
        "rejected contract",
    )
    if sha256_bytes(fixture.read_bytes()) != before:
        raise AssertionError("rejected contract fixture mutated")
    return {"commands": commands, "fixture_digest": before}, mission_ids


def replay_public_safety_accepted(context, vector):
    prepare_case(context, vector)
    before = state_manifest(context["scan_root"])
    commands, mission_ids = execute_commands(context, vector)
    result = commands[0]
    if result != {"exit_code": 0, "stdout": "", "stderr": ""}:
        raise AssertionError("accepted public-safety disposition drift")
    if state_manifest(context["scan_root"]) != before:
        raise AssertionError("accepted public-safety scan root mutated")
    return {"commands": commands, "scan_manifest": before}, mission_ids


def replay_public_safety_rejected(context, vector):
    prepare_case(context, vector)
    outside = context["scan_root"] / "outside.txt"
    before = sha256_bytes(outside.read_bytes())
    commands, mission_ids = execute_commands(context, vector)
    result = commands[0]
    if result["exit_code"] != 1 or result["stdout"] != "" or vector["expected_error"] not in result["stderr"]:
        raise AssertionError("rejected public-safety disposition drift")
    if not (context["scan_root"] / "escape").is_symlink():
        raise AssertionError("public-safety negative no longer uses a real symlink")
    if sha256_bytes(outside.read_bytes()) != before:
        raise AssertionError("public-safety outside file mutated")
    return {"commands": commands, "scan_manifest": state_manifest(context["scan_root"])}, mission_ids


def state_manifest(root):
    entries = []
    for path in sorted(root.rglob("*"), key=lambda item: item.relative_to(root).as_posix()):
        relative = path.relative_to(root).as_posix()
        if path.is_symlink():
            entries.append({"path": relative, "type": "symlink", "target": os.readlink(path)})
        elif path.is_dir():
            entries.append({"path": relative, "type": "directory"})
        elif path.is_file():
            body = path.read_bytes()
            entries.append({"path": relative, "type": "file", "bytes": len(body), "digest": sha256_bytes(body)})
        else:
            raise RuntimeError("state contains a non-regular entry")
    return entries


OPERATION_HANDLERS = {
    "archive_validate_import_round_trip": replay_archive_round_trip,
    "command_status": replay_command_status,
    "lifecycle_pause_resume": replay_lifecycle,
    "public_safety_accepted": replay_public_safety_accepted,
    "public_safety_rejected": replay_public_safety_rejected,
    "validate_contract_accepted": replay_contract_accepted,
    "validate_contract_rejected": replay_contract_rejected,
}


def run_side(name, source, binary, corpus, fixture_root, evidence_root):
    records = []
    for vector in corpus["vectors"]:
        case_root = evidence_root / "raw" / name / vector["id"]
        case_root.mkdir(parents=True)
        context = {
            "source": source,
            "binary": binary,
            "fixture_root": fixture_root,
            "case_root": case_root,
        }
        observation, mission_ids = OPERATION_HANDLERS[vector["operation"]](context, vector)
        manifest = {
            "state": state_manifest(context["state_root"]),
            "scan": state_manifest(context["scan_root"]),
        }
        write_json_new(case_root / "state-manifest.json", manifest)
        normalized = normalize_record(
            observation,
            mission_ids=mission_ids,
            temporary_roots={case_root, context["state_root"], context["scan_root"]},
        )
        write_json_new(case_root / "normalized-record.json", normalized)
        records.append(normalized)
    return records


def prepare_empty_directory(path, description):
    if path.exists() or path.is_symlink():
        require_directory(path, description)
        if any(path.iterdir()):
            raise ValueError(f"{description} must be empty")
    else:
        path.mkdir(parents=True)


def replay(args):
    corpus_path = Path(args.corpus).resolve()
    fixture_root = corpus_path.parent
    corpus = load_corpus(corpus_path, fixture_root)
    reference_source = Path(args.reference_source).resolve()
    candidate_source = Path(args.candidate_source).resolve()
    require_directory(reference_source, "reference source")
    require_directory(candidate_source, "candidate source")
    reference_head = validate_reference_head(git_head(reference_source))
    evidence_root = Path(args.evidence_root).resolve()
    output = Path(args.output).resolve()
    if output.exists() or output.is_symlink():
        raise ValueError("output must not already exist")
    prepare_empty_directory(evidence_root, "evidence root")
    build_root = evidence_root / "build with spaces"
    build_root.mkdir()
    extension = ".exe" if os.name == "nt" else ""
    reference_binary = build_root / f"ao-mission{extension}"
    candidate_binary = build_root / f"ao-next-mission{extension}"
    reference_build_evidence = evidence_root / "raw" / "reference" / "build"
    candidate_build_evidence = evidence_root / "raw" / "candidate" / "build"
    reference_build_evidence.mkdir(parents=True)
    candidate_build_evidence.mkdir(parents=True)
    reference_tree_digest = source_inventory(reference_source, corpus, True)
    candidate_tree_digest = source_inventory(candidate_source, corpus, False)
    build_binary(reference_source, "ao-mission", reference_binary, reference_build_evidence)
    build_binary(candidate_source, "ao-next-mission", candidate_binary, candidate_build_evidence)
    reference_records = run_side(
        "reference", reference_source, reference_binary, corpus, fixture_root, evidence_root
    )
    candidate_records = run_side(
        "candidate", candidate_source, candidate_binary, corpus, fixture_root, evidence_root
    )
    cases = []
    for vector, reference, candidate in zip(corpus["vectors"], reference_records, candidate_records):
        reference_bytes = canonical_bytes(reference)
        candidate_bytes = canonical_bytes(candidate)
        if reference_bytes != candidate_bytes:
            raise AssertionError(f"normalized old/new drift for {vector['id']}")
        cases.append(
            {
                "id": vector["id"],
                "operation": vector["operation"],
                "normalized_digest": sha256_bytes(reference_bytes),
                "status": "passed",
            }
        )
    candidate_head = git_head(candidate_source)
    readback = {
        "schema_version": "ao.next.mission-equivalence-readback.v1",
        "corpus_digest": corpus["manifest_digest"],
        "reference_source_head": reference_head,
        "reference_source_tree_digest": reference_tree_digest,
        "candidate_source_head": candidate_head or "unversioned-source",
        "candidate_source_tree_digest": candidate_tree_digest,
        "case_count": len(cases),
        "cases": cases,
        "normalized_readback_digest": sha256_bytes(canonical_bytes(reference_records)),
        "provider_calls": 0,
        "provider_environment": [],
        "safe_to_execute": False,
        "executes_work": False,
        "approves_work": False,
        "mutates_repositories": False,
        "publishes_artifacts": False,
        "status": "passed",
    }
    write_json_new(output, readback)
    return readback


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--corpus", required=True)
    parser.add_argument("--reference-source", required=True)
    parser.add_argument("--candidate-source", required=True)
    parser.add_argument("--evidence-root", required=True)
    parser.add_argument("--output", required=True)
    return parser.parse_args()


def main():
    args = parse_args()
    output = Path(args.output).resolve()
    try:
        replay(args)
    except Exception as error:
        if not output.exists() and not output.is_symlink():
            write_json_new(
                output,
                {
                    "schema_version": "ao.next.mission-equivalence-readback.v1",
                    "provider_calls": 0,
                    "safe_to_execute": False,
                    "executes_work": False,
                    "approves_work": False,
                    "mutates_repositories": False,
                    "publishes_artifacts": False,
                    "status": "failed",
                    "error": str(error),
                },
            )
        raise


if __name__ == "__main__":
    main()
