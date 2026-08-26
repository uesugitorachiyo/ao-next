use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use ao_next_core::evidence::digest_bytes;
use ao_next_core::strict_json::canonical_digest;
use ao_next_eval::mission_corpus::{
    MissionCorpusError, VerifiedMissionCorpus, verify_mission_corpus,
    verify_mission_corpus_snapshot,
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

#[cfg(unix)]
#[test]
fn exact_source_rejects_nested_symlink_ancestors() {
    use std::os::unix::fs::symlink;

    let fixture = ExactSourceFixture::new("source-symlink-ancestor");
    let internal = fixture.source_root.join("internal");
    let target = fixture.scratch.0.join("source-internal-target");
    fs::rename(&internal, &target).expect("move source ancestor");
    symlink(&target, &internal).expect("source ancestor symlink");

    assert_unsafe_file(&verify_mission_corpus(
        &fixture.manifest_path,
        &fixture.corpus_root,
        &fixture.source_root,
    ));
}

#[cfg(windows)]
#[test]
fn vector_inventory_rejects_reparse_roots() {
    let scratch = Scratch::new("vector-reparse-root");
    let target = scratch.0.join("corpus-target");
    copy_corpus(&corpus_root(), &target);
    let root = scratch.0.join("corpus-link");
    junction(&target, &root);

    assert_unsafe_file(&verify_mission_corpus_snapshot(
        &root.join("corpus-v1.json"),
        &root,
    ));
}

#[cfg(windows)]
#[test]
fn vector_inventory_rejects_reparse_ancestors() {
    let scratch = Scratch::new("vector-reparse-ancestor");
    let root = scratch.0.join("corpus");
    copy_corpus(&corpus_root(), &root);
    let vectors = root.join("vectors");
    let target = scratch.0.join("vectors-target");
    fs::rename(&vectors, &target).expect("move vector directory");
    junction(&target, &vectors);

    assert_unsafe_file(&verify_mission_corpus_snapshot(
        &root.join("corpus-v1.json"),
        &root,
    ));
}

#[cfg(windows)]
#[test]
fn vector_inventory_rejects_reparse_leaves() {
    use std::os::windows::fs::symlink_file;

    let scratch = Scratch::new("vector-reparse-leaf");
    let root = scratch.0.join("corpus");
    copy_corpus(&corpus_root(), &root);
    let vector = root.join("vectors/archive-import.json");
    let target = scratch.0.join("archive-import-target.json");
    fs::rename(&vector, &target).expect("move vector file");
    symlink_file(&target, &vector).expect("vector file reparse point");

    assert_unsafe_file(&verify_mission_corpus_snapshot(
        &root.join("corpus-v1.json"),
        &root,
    ));
}

#[cfg(windows)]
#[test]
fn exact_source_rejects_reparse_roots() {
    let fixture = ExactSourceFixture::new("source-reparse-root");
    let source_link = fixture.scratch.0.join("source-link");
    junction(&fixture.source_root, &source_link);

    assert_unsafe_file(&verify_mission_corpus(
        &fixture.manifest_path,
        &fixture.corpus_root,
        &source_link,
    ));
}

#[cfg(windows)]
#[test]
fn exact_source_rejects_reparse_ancestors() {
    let fixture = ExactSourceFixture::new("source-reparse-ancestor");
    let internal = fixture.source_root.join("internal");
    let target = fixture.scratch.0.join("source-internal-target");
    fs::rename(&internal, &target).expect("move source ancestor");
    junction(&target, &internal);

    assert_unsafe_file(&verify_mission_corpus(
        &fixture.manifest_path,
        &fixture.corpus_root,
        &fixture.source_root,
    ));
}

#[cfg(windows)]
#[test]
fn exact_source_rejects_reparse_leaves() {
    use std::os::windows::fs::symlink_file;

    let fixture = ExactSourceFixture::new("source-reparse-leaf");
    let source = fixture.source_root.join("internal/mission/cli.go");
    let target = fixture.scratch.0.join("cli-target.go");
    fs::rename(&source, &target).expect("move source file");
    symlink_file(&target, &source).expect("source file reparse point");

    assert_unsafe_file(&verify_mission_corpus(
        &fixture.manifest_path,
        &fixture.corpus_root,
        &fixture.source_root,
    ));
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

fn copy_corpus(source: &Path, target: &Path) {
    fs::create_dir_all(target).expect("target corpus");
    fs::copy(source.join("corpus-v1.json"), target.join("corpus-v1.json")).expect("copy manifest");
    copy_vectors(source, target);
}

fn assert_unsafe_file(result: &Result<VerifiedMissionCorpus, MissionCorpusError>) {
    assert!(matches!(result, Err(MissionCorpusError::UnsafeFile(_))));
}

#[cfg(windows)]
fn junction(target: &Path, link: &Path) {
    run(Command::new("cmd").args([
        "/C",
        "mklink",
        "/J",
        link.to_str().expect("junction path"),
        target.to_str().expect("junction target"),
    ]));
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

struct ExactSourceFixture {
    scratch: Scratch,
    corpus_root: PathBuf,
    manifest_path: PathBuf,
    source_root: PathBuf,
}

impl ExactSourceFixture {
    fn new(label: &str) -> Self {
        let scratch = Scratch::new(label);
        let corpus_root = scratch.0.join("corpus with space 資料");
        let source_root = scratch.0.join("source with space 資料");
        copy_corpus(&crate::corpus_root(), &corpus_root);
        fs::create_dir(&source_root).expect("source root");

        let manifest_path = corpus_root.join("corpus-v1.json");
        let mut manifest: Value =
            serde_json::from_slice(&fs::read(&manifest_path).expect("read copied manifest"))
                .expect("manifest JSON");
        let vector_sources = manifest["vectors"]
            .as_array()
            .expect("vectors")
            .iter()
            .flat_map(|vector| vector["source_paths"].as_array().expect("source paths"))
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect::<std::collections::BTreeSet<_>>();
        let source_files = manifest["source_files"]
            .as_array_mut()
            .expect("source files");
        let rename = source_files
            .iter_mut()
            .find(|entry| !vector_sources.contains(entry["path"].as_str().expect("source path")))
            .expect("unbound source entry");
        rename["path"] = json!("migration fixtures/資料 source.txt");
        source_files.sort_by(|left, right| {
            left["path"]
                .as_str()
                .expect("left path")
                .cmp(right["path"].as_str().expect("right path"))
        });
        for entry in source_files {
            let path = entry["path"].as_str().expect("source path");
            let bytes = format!("exact source: {path}\n").into_bytes();
            let file = source_root.join(path);
            fs::create_dir_all(file.parent().expect("source parent")).expect("source parent");
            fs::write(&file, &bytes).expect("source file");
            entry["mode"] = json!("100644");
            entry["bytes"] = json!(bytes.len());
            entry["digest"] =
                serde_json::to_value(digest_bytes(&bytes)).expect("source digest JSON");
        }

        manifest["source_head"] = json!(initialize_source_repository(&source_root));
        rewrite_digest(&mut manifest);
        fs::write(
            &manifest_path,
            serde_json::to_vec(&manifest).expect("synthetic manifest"),
        )
        .expect("write synthetic manifest");

        verify_mission_corpus(&manifest_path, &corpus_root, &source_root)
            .expect("synthetic exact source baseline");
        Self {
            scratch,
            corpus_root,
            manifest_path,
            source_root,
        }
    }
}

fn initialize_source_repository(source_root: &Path) -> String {
    let root = source_root.to_str().expect("source root");
    run(Command::new("git").args(["-C", root, "init", "--quiet"]));
    for (key, value) in [
        ("user.name", "AO Next Test"),
        ("user.email", "ao-next@example.invalid"),
        ("commit.gpgsign", "false"),
        ("core.autocrlf", "false"),
        ("core.filemode", "false"),
    ] {
        run(Command::new("git").args(["-C", root, "config", key, value]));
    }
    run(Command::new("git").args(["-C", root, "add", "--force", "--all"]));
    run(Command::new("git").args(["-C", root, "commit", "--quiet", "-m", "fixture"]));
    let head = Command::new("git")
        .args(["-C", root, "rev-parse", "HEAD"])
        .output()
        .expect("source head");
    assert!(head.status.success(), "source head failed");
    String::from_utf8(head.stdout)
        .expect("source head UTF-8")
        .trim()
        .to_owned()
}
