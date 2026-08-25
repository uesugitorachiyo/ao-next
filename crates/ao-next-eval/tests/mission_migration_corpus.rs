use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use ao_next_core::strict_json::canonical_digest;
use ao_next_eval::mission_corpus::{
    MissionCorpusError, verify_mission_corpus, verify_mission_corpus_snapshot,
};
use serde_json::{Value, json};

fn repository_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .and_then(|path| path.parent())
        .expect("workspace root")
        .to_path_buf()
}

fn corpus_root() -> PathBuf {
    repository_root().join("tests/fixtures/mission-migration")
}

#[test]
fn frozen_mission_corpus_verifies_without_the_source_checkout() {
    let root = corpus_root();
    let verified = verify_mission_corpus_snapshot(&root.join("corpus-v1.json"), &root)
        .expect("frozen Mission corpus must verify");

    assert_eq!(
        verified.source_head,
        "05567fdd7c3fc64814ca4122b3f431d4ed9aaded"
    );
    assert_eq!(verified.source_file_count, 346);
    assert_eq!(verified.vector_count, 7);
}

#[test]
fn behavior_vectors_bind_replayable_operations_and_exact_expectations() {
    let manifest: Value =
        serde_json::from_slice(&fs::read(corpus_root().join("corpus-v1.json")).expect("manifest"))
            .expect("manifest JSON");
    let vectors = manifest["vectors"].as_array().expect("vectors");
    let operations = vectors
        .iter()
        .map(|vector| {
            assert!(
                !vector["arguments"]
                    .as_array()
                    .expect("arguments")
                    .is_empty()
            );
            assert!(
                !vector["setup_state"]
                    .as_array()
                    .expect("setup state")
                    .is_empty()
            );
            assert!(
                !vector["expected_result"]
                    .as_array()
                    .expect("result")
                    .is_empty()
            );
            assert!(vector["expected_error"].is_string());
            assert!(
                !vector["expected_state"]
                    .as_array()
                    .expect("state")
                    .is_empty()
            );
            vector["operation"].as_str().expect("operation")
        })
        .collect::<std::collections::BTreeSet<_>>();

    assert_eq!(
        operations,
        std::collections::BTreeSet::from([
            "archive_validate_import_round_trip",
            "command_status",
            "lifecycle_pause_resume",
            "public_safety_accepted",
            "public_safety_rejected",
            "validate_contract_accepted",
            "validate_contract_rejected",
        ])
    );
}

#[test]
fn strict_manifest_rejects_duplicate_unknown_and_oversized_input() {
    let source = fs::read(corpus_root().join("corpus-v1.json")).expect("manifest");
    let scratch = Scratch::new("strict");
    copy_vectors(&corpus_root(), &scratch.0);

    let mut duplicate = br#"{"schema_version":"duplicate","#.to_vec();
    duplicate.extend_from_slice(&source[1..]);
    assert_rejected(&scratch.0, &duplicate);

    let mut unknown: Value = serde_json::from_slice(&source).expect("manifest JSON");
    unknown["projected_status"] = json!("passed");
    assert_rejected(
        &scratch.0,
        &serde_json::to_vec(&unknown).expect("unknown manifest"),
    );

    assert_rejected(&scratch.0, &vec![b' '; 1024 * 1024 + 1]);
}

#[test]
fn manifest_rejects_paths_reordering_digest_drift_and_status_conflation() {
    let source = fs::read(corpus_root().join("corpus-v1.json")).expect("manifest");
    let scratch = Scratch::new("manifest");
    copy_vectors(&corpus_root(), &scratch.0);

    let mut traversal: Value = serde_json::from_slice(&source).expect("manifest JSON");
    traversal["source_files"][0]["path"] = json!("../escape");
    rewrite_digest(&mut traversal);
    assert_rejected(
        &scratch.0,
        &serde_json::to_vec(&traversal).expect("traversal manifest"),
    );

    let mut reordered: Value = serde_json::from_slice(&source).expect("manifest JSON");
    reordered["source_files"]
        .as_array_mut()
        .expect("source files")
        .swap(0, 1);
    rewrite_digest(&mut reordered);
    assert_rejected(
        &scratch.0,
        &serde_json::to_vec(&reordered).expect("reordered manifest"),
    );

    let mut drifted: Value = serde_json::from_slice(&source).expect("manifest JSON");
    drifted["vectors"][0]["digest"] =
        json!("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
    assert_rejected(
        &scratch.0,
        &serde_json::to_vec(&drifted).expect("drifted manifest"),
    );

    let mut conflated: Value = serde_json::from_slice(&source).expect("manifest JSON");
    conflated["status_domains"]["engine_projection"] =
        conflated["status_domains"]["mission_durable_source"].clone();
    rewrite_digest(&mut conflated);
    assert_rejected(
        &scratch.0,
        &serde_json::to_vec(&conflated).expect("conflated manifest"),
    );

    let mut replay_drift: Value = serde_json::from_slice(&source).expect("manifest JSON");
    replay_drift["vectors"][0]["arguments"][0][0] = json!("not-a-mission-operation");
    rewrite_digest(&mut replay_drift);
    assert_rejected(
        &scratch.0,
        &serde_json::to_vec(&replay_drift).expect("replay drift manifest"),
    );
}

#[test]
fn vector_inventory_rejects_missing_extra_and_non_regular_entries() {
    let manifest = fs::read(corpus_root().join("corpus-v1.json")).expect("manifest");

    let missing = Scratch::new("missing");
    copy_vectors(&corpus_root(), &missing.0);
    fs::remove_file(missing.0.join("vectors/archive-import.json")).expect("remove vector");
    assert_rejected(&missing.0, &manifest);

    let extra = Scratch::new("extra");
    copy_vectors(&corpus_root(), &extra.0);
    fs::write(extra.0.join("vectors/extra.json"), b"{}\n").expect("extra vector");
    assert_rejected(&extra.0, &manifest);

    let non_regular = Scratch::new("non-regular");
    copy_vectors(&corpus_root(), &non_regular.0);
    fs::remove_file(non_regular.0.join("vectors/archive-import.json")).expect("remove vector");
    fs::create_dir(non_regular.0.join("vectors/archive-import.json")).expect("directory vector");
    assert_rejected(&non_regular.0, &manifest);
}

#[test]
fn vector_inventory_rejects_cumulative_declared_bytes_over_32_mib() {
    let mut manifest: Value =
        serde_json::from_slice(&fs::read(corpus_root().join("corpus-v1.json")).expect("manifest"))
            .expect("manifest JSON");
    let originals = manifest["vectors"].as_array().expect("vectors").clone();
    let mut vectors = Vec::new();
    for index in 0..33 {
        let mut vector = originals[index.min(originals.len() - 1)].clone();
        vector["id"] = json!(format!("oversized-{index:02}"));
        vector["fixture_path"] = json!(format!("vectors/{index:02}.json"));
        vector["bytes"] = json!(1024 * 1024);
        vector["digest"] =
            json!("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
        vectors.push(vector);
    }
    manifest["vectors"] = Value::Array(vectors);
    rewrite_digest(&mut manifest);

    let scratch = Scratch::new("vector-total");
    copy_vectors(&corpus_root(), &scratch.0);
    let path = scratch.0.join("corpus-v1.json");
    fs::write(
        &path,
        serde_json::to_vec(&manifest).expect("manifest bytes"),
    )
    .expect("oversized manifest");
    assert!(matches!(
        verify_mission_corpus_snapshot(&path, &scratch.0),
        Err(MissionCorpusError::Oversized)
    ));
}

#[cfg(unix)]
#[test]
fn vector_inventory_rejects_symlinks() {
    use std::os::unix::fs::symlink;

    let manifest = fs::read(corpus_root().join("corpus-v1.json")).expect("manifest");
    let scratch = Scratch::new("symlink");
    copy_vectors(&corpus_root(), &scratch.0);
    let link = scratch.0.join("vectors/archive-import.json");
    fs::remove_file(&link).expect("remove vector");
    symlink("command-status.json", link).expect("vector symlink");
    assert_rejected(&scratch.0, &manifest);
}

#[test]
#[ignore = "requires AO_MISSION_SOURCE_ROOT bound to the canonical read-only checkout"]
fn frozen_mission_corpus_matches_the_bound_source_tree_and_rejects_drift() {
    let source_root = PathBuf::from(
        std::env::var_os("AO_MISSION_SOURCE_ROOT").expect("AO_MISSION_SOURCE_ROOT is required"),
    );
    let root = corpus_root();
    let manifest = root.join("corpus-v1.json");
    let verified = verify_mission_corpus(&manifest, &root, &source_root)
        .expect("frozen Mission corpus must verify");
    assert_eq!(verified.source_file_count, 346);

    let mut head_drift: Value =
        serde_json::from_slice(&fs::read(&manifest).expect("manifest")).expect("manifest JSON");
    head_drift["source_head"] = json!("0000000000000000000000000000000000000000");
    rewrite_digest(&mut head_drift);
    let head_drift_path = root.join("head-drift.json");
    fs::write(
        &head_drift_path,
        serde_json::to_vec(&head_drift).expect("head drift manifest"),
    )
    .expect("write head drift");
    assert!(verify_mission_corpus(&head_drift_path, &root, &source_root).is_err());
    fs::remove_file(head_drift_path).expect("remove head drift");

    let scratch = Scratch::new("source");
    let clone = scratch.0.join("ao-mission");
    run(Command::new("git").args([
        "clone",
        "--quiet",
        "--no-hardlinks",
        source_root.to_str().expect("source path"),
        clone.to_str().expect("clone path"),
    ]));

    let readme = clone.join("README.md");
    let original = fs::read(&readme).expect("README");
    fs::write(&readme, b"digest drift\n").expect("drift source");
    assert!(verify_mission_corpus(&manifest, &root, &clone).is_err());
    fs::write(&readme, original).expect("restore source");

    let original = fs::read(&readme).expect("README");
    fs::write(&readme, b"staged-only digest drift\n").expect("stage source drift");
    run(Command::new("git").args(["-C", clone.to_str().expect("clone"), "add", "README.md"]));
    fs::write(&readme, original).expect("restore only the worktree");
    assert!(verify_mission_corpus(&manifest, &root, &clone).is_err());
    run(Command::new("git").args([
        "-C",
        clone.to_str().expect("clone"),
        "reset",
        "--quiet",
        "HEAD",
        "--",
        "README.md",
    ]));

    fs::write(clone.join("unexpected.txt"), b"extra\n").expect("extra source");
    run(Command::new("git").args([
        "-C",
        clone.to_str().expect("clone"),
        "add",
        "unexpected.txt",
    ]));
    assert!(verify_mission_corpus(&manifest, &root, &clone).is_err());
}

fn assert_rejected(root: &Path, manifest: &[u8]) {
    let path = root.join("corpus-v1.json");
    fs::write(&path, manifest).expect("write manifest");
    assert!(verify_mission_corpus_snapshot(&path, root).is_err());
}

fn rewrite_digest(value: &mut Value) {
    let payload = json!([
        value["schema_version"].clone(),
        value["source_repository"].clone(),
        value["source_head"].clone(),
        value["status_domains"].clone(),
        value["source_files"].clone(),
        value["vectors"].clone(),
    ]);
    value["manifest_digest"] =
        serde_json::to_value(canonical_digest(&payload).expect("canonical digest"))
            .expect("digest JSON");
}

fn copy_vectors(source: &Path, target: &Path) {
    let target_vectors = target.join("vectors");
    fs::create_dir_all(&target_vectors).expect("target vectors");
    for entry in fs::read_dir(source.join("vectors")).expect("source vectors") {
        let entry = entry.expect("vector entry");
        fs::copy(entry.path(), target_vectors.join(entry.file_name())).expect("copy vector");
    }
}

fn run(command: &mut Command) {
    let output = command.output().expect("run command");
    assert!(
        output.status.success(),
        "command failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
}

struct Scratch(PathBuf);

impl Scratch {
    fn new(label: &str) -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "ao-next-mission-corpus-{label}-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("scratch directory");
        Self(path)
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}
