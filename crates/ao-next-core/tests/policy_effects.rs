use std::collections::BTreeSet;
use std::path::Path;

use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, EffectKind, EffectRequest, ExternalEffectPolicy,
    NetworkPolicy,
};
use ao_next_core::effects::{EffectBroker, LocalEffectBroker};
use ao_next_core::evidence::digest_bytes;
use ao_next_core::policy::PolicyDenial;
use chrono::{DateTime, Utc};
use tempfile::TempDir;

const ZERO_DIGEST: &str = "sha256:0000000000000000000000000000000000000000000000000000000000000000";

fn timestamp(value: &str) -> DateTime<Utc> {
    DateTime::parse_from_rfc3339(value)
        .expect("fixture timestamp")
        .with_timezone(&Utc)
}

fn authority(root: &Path) -> AuthorityEnvelope {
    AuthorityEnvelope {
        schema_version: "ao.next.authority-envelope.v1".into(),
        issued_by: "operator".into(),
        issued_at: timestamp("2026-08-05T00:00:00Z"),
        expires_at: timestamp("2026-08-06T00:00:00Z"),
        capabilities: BTreeSet::new(),
        allowed_roots: vec![root.to_path_buf()],
        allowed_programs: BTreeSet::new(),
        network: NetworkPolicy::Denied,
        allowed_network_hosts: BTreeSet::new(),
        external_effects: ExternalEffectPolicy::Denied,
    }
}

fn request(kind: EffectKind) -> EffectRequest {
    EffectRequest {
        effect_id: "effect-01".into(),
        run_id: "run-01".into(),
        kind,
        program: None,
        content: None,
        args: Vec::new(),
        paths: Vec::new(),
        timeout_ms: 0,
        input_digest: Digest::new(ZERO_DIGEST).expect("fixture digest"),
    }
}

#[test]
fn network_credentials_and_external_mutations_fail_closed() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.extend([
        Capability::NetworkAccess,
        Capability::CredentialAccess,
        Capability::RemoteMutation,
        Capability::Release,
        Capability::Deployment,
        Capability::Publication,
    ]);

    assert_eq!(
        broker
            .authorize(&request(EffectKind::Network), &authorized)
            .expect_err("network policy denied"),
        PolicyDenial::NetworkDenied
    );

    let mut missing_credential = authorized.clone();
    missing_credential
        .capabilities
        .remove(&Capability::CredentialAccess);
    assert_eq!(
        broker
            .authorize(&request(EffectKind::Credential), &missing_credential)
            .expect_err("credential denied"),
        PolicyDenial::MissingCapability(Capability::CredentialAccess)
    );

    for kind in [
        EffectKind::RemoteMutation,
        EffectKind::Release,
        EffectKind::Deployment,
        EffectKind::Publication,
    ] {
        assert_eq!(
            broker
                .authorize(&request(kind.clone()), &authorized)
                .expect_err("external effect denied"),
            PolicyDenial::ExternalEffectDenied(kind)
        );
    }
}

#[test]
fn path_containment_rejects_outside_traversal_symlink_and_non_regular_inputs() {
    let workspace = TempDir::new().expect("workspace");
    let outside = TempDir::new().expect("outside");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::ReadWorkspace);

    let mut read = request(EffectKind::ReadFile);
    read.paths = vec![outside.path().join("secret.txt")];
    assert_eq!(
        broker
            .authorize(&read, &authorized)
            .expect_err("outside denied"),
        PolicyDenial::PathOutsideAllowedRoots(read.paths[0].clone())
    );

    read.paths = vec!["nested/../escape".into()];
    assert_eq!(
        broker
            .authorize(&read, &authorized)
            .expect_err("traversal denied"),
        PolicyDenial::ParentTraversal(read.paths[0].clone())
    );

    #[cfg(unix)]
    {
        let outside_file = outside.path().join("secret.txt");
        std::fs::write(&outside_file, b"secret").expect("outside fixture");
        let link = workspace.path().join("link");
        std::os::unix::fs::symlink(&outside_file, &link).expect("symlink fixture");
        read.paths = vec!["link".into()];
        assert_eq!(
            broker
                .authorize(&read, &authorized)
                .expect_err("symlink denied"),
            PolicyDenial::SymlinkNotAllowed(link)
        );
    }

    #[cfg(windows)]
    {
        let outside_file = outside.path().join("secret.txt");
        std::fs::write(&outside_file, b"secret").expect("outside fixture");
        let link = workspace.path().join("link");
        match std::os::windows::fs::symlink_file(&outside_file, &link) {
            Ok(()) => {
                read.paths = vec!["link".into()];
                assert_eq!(
                    broker
                        .authorize(&read, &authorized)
                        .expect_err("symlink denied"),
                    PolicyDenial::SymlinkNotAllowed(link)
                );
            }
            Err(error)
                if (error.kind() == std::io::ErrorKind::PermissionDenied
                    || error.raw_os_error() == Some(1314))
                    && matches!(
                        std::fs::symlink_metadata(&link),
                        Err(error) if error.kind() == std::io::ErrorKind::NotFound
                    ) => {}
            Err(error) => panic!("symlink fixture: {error}"),
        }
    }

    let directory = workspace.path().join("directory");
    std::fs::create_dir(&directory).expect("directory fixture");
    read.paths = vec!["directory".into()];
    assert_eq!(
        broker
            .authorize(&read, &authorized)
            .expect_err("directory denied"),
        PolicyDenial::NonRegularFile(Path::new("directory").to_path_buf())
    );
}

#[test]
fn native_effect_paths_are_relative_and_exclude_git_control_data() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::ReadWorkspace);
    std::fs::write(workspace.path().join("source.txt"), b"source").expect("source fixture");

    let mut read = request(EffectKind::ReadFile);
    read.paths = vec![workspace.path().join("source.txt")];
    assert!(
        broker.authorize(&read, &authorized).is_err(),
        "absolute model path contradicted workspace-relative provider contract"
    );

    std::fs::create_dir(workspace.path().join(".git")).expect("Git control directory");
    std::fs::write(workspace.path().join(".git/config"), b"control").expect("Git control fixture");
    read.paths = vec![Path::new(".git").join("config")];
    assert!(
        broker.authorize(&read, &authorized).is_err(),
        "model effect accessed Git control data"
    );
    read.paths = vec![Path::new(".GIT").join("config")];
    assert!(
        broker.authorize(&read, &authorized).is_err(),
        "case-insensitive model effect accessed Git control data"
    );
}

#[test]
fn nonexistent_write_target_requires_an_existing_descriptor_bound_parent() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::WriteWorkspace);
    let mut write = request(EffectKind::WriteFile);
    write.paths = vec!["new/subdirectory/result.txt".into()];
    write.content = Some("result".into());

    broker
        .authorize(&write, &authorized)
        .expect_err("missing parent cannot be descriptor-bound");
}

#[test]
fn allowlisted_program_cannot_be_a_model_write_escape() {
    let workspace = TempDir::new().expect("workspace");
    let outside = TempDir::new().expect("outside");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let marker = outside.path().join("escaped.txt");
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::RunLocalProgram);
    authorized
        .allowed_programs
        .insert("/usr/bin/python3".into());
    let mut command = request(EffectKind::RunProgram);
    command.program = Some("/usr/bin/python3".into());
    command.timeout_ms = 100;
    command.args = vec![
        "-c".into(),
        format!("from pathlib import Path; Path({marker:?}).write_text('escaped')"),
    ];
    command.paths = vec![workspace.path().to_path_buf()];

    broker
        .execute(&command, &authorized)
        .expect_err("model-controlled programs are never executable effects");

    authorized.allowed_programs.insert("/bin/sh".into());
    command.program = Some("/bin/sh".into());
    command.args = vec![
        "-c".into(),
        format!("printf escaped > '{}'", marker.display()),
    ];
    broker
        .execute(&command, &authorized)
        .expect_err("model-controlled shells are never executable effects");

    assert!(!marker.exists());
}

#[test]
fn native_effects_reject_irrelevant_or_contradictory_fields() {
    let workspace = TempDir::new().expect("workspace");
    let file = workspace.path().join("source.txt");
    std::fs::write(&file, b"visible").expect("source fixture");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::ReadWorkspace);
    let mut read = request(EffectKind::ReadFile);
    read.paths = vec![file];
    read.timeout_ms = 0;
    read.program = Some("/usr/bin/printf".into());
    read.args = vec!["contradiction".into()];

    broker
        .authorize(&read, &authorized)
        .expect_err("native reads reject program fields and arguments");
}

#[test]
fn bounded_native_read_create_and_replace_are_preimage_bound() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut authorized = authority(workspace.path());
    authorized
        .capabilities
        .extend([Capability::ReadWorkspace, Capability::WriteWorkspace]);
    let target = workspace.path().join("product.txt");
    let mut create = request(EffectKind::WriteFile);
    create.paths = vec!["product.txt".into()];
    create.timeout_ms = 0;
    create.content = Some("first\n".into());
    create.input_digest = digest_bytes(b"ao.next.file-does-not-exist.v1");

    broker
        .execute(&create, &authorized)
        .expect("native create succeeds");
    assert_eq!(std::fs::read(&target).expect("created bytes"), b"first\n");

    let mut replace = create.clone();
    replace.effect_id = "effect-02".into();
    replace.content = Some("second\n".into());
    replace.input_digest = digest_bytes(b"first\n");
    broker
        .execute(&replace, &authorized)
        .expect("native replace succeeds");

    let mut read = request(EffectKind::ReadFile);
    read.paths = vec!["product.txt".into()];
    read.timeout_ms = 0;
    read.input_digest = digest_bytes(b"second\n");
    let output = broker
        .execute(&read, &authorized)
        .expect("bounded native read succeeds");
    assert_eq!(output.status, 0);
    assert_eq!(output.stdout, b"second\n");
    assert!(output.stderr.is_empty());
}

#[test]
fn native_write_rejects_stale_oversized_and_symlink_targets() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::WriteWorkspace);
    let target = workspace.path().join("product.txt");
    std::fs::write(&target, b"live").expect("existing target");
    let mut write = request(EffectKind::WriteFile);
    write.paths = vec!["product.txt".into()];
    write.timeout_ms = 0;
    write.content = Some("next".into());
    write.input_digest = digest_bytes(b"stale");

    broker
        .execute(&write, &authorized)
        .expect_err("stale preimage denied");
    assert_eq!(std::fs::read(&target).expect("unchanged bytes"), b"live");

    write.input_digest = digest_bytes(b"live");
    write.content = Some("oversized".into());
    broker
        .execute(&write, &authorized)
        .expect_err("oversized content denied");
    assert_eq!(std::fs::read(&target).expect("unchanged bytes"), b"live");

    #[cfg(unix)]
    {
        let outside = TempDir::new().expect("outside");
        let outside_target = outside.path().join("outside.txt");
        std::fs::write(&outside_target, b"outside").expect("outside target");
        let link = workspace.path().join("link.txt");
        std::os::unix::fs::symlink(&outside_target, &link).expect("symlink target");
        write.paths = vec!["link.txt".into()];
        write.content = Some("evil".into());
        write.input_digest = digest_bytes(b"outside");
        broker
            .execute(&write, &authorized)
            .expect_err("symlink write denied");
        assert_eq!(
            std::fs::read(outside_target).expect("outside unchanged"),
            b"outside"
        );
    }
}

#[test]
fn authorized_native_write_rejects_a_regular_parent_substitution() {
    let workspace = TempDir::new().expect("workspace");
    let original_parent = workspace.path().join("nested");
    std::fs::create_dir(&original_parent).expect("original parent");
    let broker = LocalEffectBroker::new(1_000, 4_096, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::WriteWorkspace);
    let mut write = request(EffectKind::WriteFile);
    write.paths = vec![Path::new("nested").join("product.txt")];
    write.content = Some("ready\n".into());
    write.input_digest = digest_bytes(b"ao.next.file-does-not-exist.v1");
    let admitted = broker
        .authorize(&write, &authorized)
        .expect("write admitted against the original parent");

    #[cfg(unix)]
    {
        let moved_parent = workspace.path().join("original-nested");
        std::fs::rename(&original_parent, &moved_parent).expect("move original parent");
        std::fs::create_dir(&original_parent).expect("substitute parent");
        broker
            .execute_authorized(&admitted)
            .expect_err("regular parent substitution must invalidate admission");
        assert!(!original_parent.join("product.txt").exists());
        assert!(!moved_parent.join("product.txt").exists());
    }
    #[cfg(windows)]
    {
        assert!(
            std::fs::rename(&original_parent, workspace.path().join("replacement")).is_err(),
            "admitted parent handle must block substitution"
        );
        broker
            .execute_authorized(&admitted)
            .expect("unchanged admitted parent remains valid");
        assert_eq!(
            std::fs::read(original_parent.join("product.txt")).expect("written product"),
            b"ready\n"
        );
    }
}
