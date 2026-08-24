use ao_next_core::capture::{CaptureIndexStore, CapturePublication};
use ao_next_core::evidence::digest_bytes;
use tempfile::TempDir;

fn store() -> (TempDir, CaptureIndexStore) {
    let root = TempDir::new().expect("capture root");
    let store = CaptureIndexStore::open(root.path().to_path_buf(), 1024).expect("capture store");
    (root, store)
}

#[test]
fn publish_creates_one_final_index_and_removes_incomplete_name() {
    let (root, store) = store();
    let bytes = br#"{"schema_version":"capture.test.v1"}"#;
    let result = store.publish(bytes).expect("publish");
    assert_eq!(result, CapturePublication::Published(digest_bytes(bytes)));
    assert_eq!(
        std::fs::read(root.path().join("capture-index.json")).expect("final index"),
        bytes
    );
    assert!(!root.path().join("capture-index.json.incomplete").exists());
}

#[test]
fn stage_persists_incomplete_bytes_before_final_publication() {
    let (root, store) = store();
    let bytes = br#"{"schema_version":"capture.test.v1"}"#;

    let digest = store.stage_incomplete(bytes).expect("stage incomplete");

    assert_eq!(digest, digest_bytes(bytes));
    assert_eq!(
        std::fs::read(root.path().join("capture-index.json.incomplete")).expect("incomplete index"),
        bytes
    );
    assert!(!root.path().join("capture-index.json").exists());

    assert_eq!(
        store.publish_staged(&digest).expect("publish staged"),
        CapturePublication::Published(digest)
    );
    assert!(root.path().join("capture-index.json").is_file());
    assert!(!root.path().join("capture-index.json.incomplete").exists());
}

#[cfg(unix)]
#[test]
fn staged_incomplete_index_is_owner_only_at_creation() {
    use std::os::unix::fs::PermissionsExt as _;

    let (root, store) = store();
    store
        .stage_incomplete(b"private")
        .expect("stage incomplete");

    let mode = std::fs::symlink_metadata(root.path().join("capture-index.json.incomplete"))
        .expect("incomplete metadata")
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o600);
}

#[test]
fn recover_removes_an_identical_incomplete_name_without_rewriting_final() {
    let (root, store) = store();
    let bytes = br#"{"schema_version":"capture.test.v1"}"#;
    let incomplete = root.path().join("capture-index.json.incomplete");
    let final_path = root.path().join("capture-index.json");
    std::fs::write(&incomplete, bytes).expect("incomplete");
    std::fs::hard_link(&incomplete, &final_path).expect("published hard link");
    let result = store
        .recover(&digest_bytes(bytes))
        .expect("repair published pair");
    assert_eq!(result, CapturePublication::Repaired(digest_bytes(bytes)));
    assert_eq!(std::fs::read(final_path).expect("final index"), bytes);
    assert!(!incomplete.exists());
}

#[test]
fn recover_publishes_a_verified_incomplete_only_index() {
    let (root, store) = store();
    let bytes = br#"{"schema_version":"capture.test.v1"}"#;
    std::fs::write(root.path().join("capture-index.json.incomplete"), bytes).expect("incomplete");
    let result = store.recover(&digest_bytes(bytes)).expect("repair");
    assert_eq!(result, CapturePublication::Repaired(digest_bytes(bytes)));
    assert_eq!(
        std::fs::read(root.path().join("capture-index.json")).expect("final index"),
        bytes
    );
    assert!(!root.path().join("capture-index.json.incomplete").exists());
}

#[test]
fn recover_rejects_contradictory_final_and_incomplete_bytes() {
    let (root, store) = store();
    std::fs::write(root.path().join("capture-index.json"), b"final").expect("final");
    std::fs::write(
        root.path().join("capture-index.json.incomplete"),
        b"different",
    )
    .expect("incomplete");
    assert!(store.recover(&digest_bytes(b"final")).is_err());
    assert!(root.path().join("capture-index.json").exists());
    assert!(root.path().join("capture-index.json.incomplete").exists());
}
