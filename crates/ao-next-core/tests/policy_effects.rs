use std::collections::BTreeSet;
use std::path::Path;

use ao_next_core::contracts::{
    AuthorityEnvelope, Capability, Digest, EffectKind, EffectRequest, ExternalEffectPolicy,
    NetworkPolicy,
};
use ao_next_core::effects::{EffectBroker, EffectBrokerError, LocalEffectBroker};
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
        args: Vec::new(),
        paths: Vec::new(),
        timeout_ms: 100,
        input_digest: Digest::new(ZERO_DIGEST).expect("fixture digest"),
    }
}

#[test]
fn capability_and_program_must_both_be_operator_authorized() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let mut command = request(EffectKind::RunProgram);
    command.program = Some("/usr/bin/printf".into());

    let missing_capability = broker
        .authorize(&command, &authority(workspace.path()))
        .expect_err("capability denied");
    assert_eq!(
        missing_capability,
        PolicyDenial::MissingCapability(Capability::RunLocalProgram)
    );

    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::RunLocalProgram);
    let missing_program = broker
        .authorize(&command, &authorized)
        .expect_err("program denied");
    assert_eq!(
        missing_program,
        PolicyDenial::ProgramNotAllowed("/usr/bin/printf".into())
    );
}

#[test]
fn network_credentials_and_external_mutations_fail_closed() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096);
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
fn timeout_and_shell_programs_are_never_admitted() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(100, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::RunLocalProgram);
    authorized.allowed_programs.insert("/bin/sh".into());

    let mut command = request(EffectKind::RunProgram);
    command.program = Some("/bin/sh".into());
    command.args = vec!["-c".into(), "touch forbidden".into()];
    assert_eq!(
        broker
            .authorize(&command, &authorized)
            .expect_err("shell denied"),
        PolicyDenial::ShellProgramDenied("/bin/sh".into())
    );

    command.program = Some("/usr/bin/printf".into());
    authorized.allowed_programs.insert("/usr/bin/printf".into());
    command.timeout_ms = 101;
    assert_eq!(
        broker
            .authorize(&command, &authorized)
            .expect_err("timeout denied"),
        PolicyDenial::TimeoutExceeded {
            requested_ms: 101,
            maximum_ms: 100
        }
    );
}

#[test]
fn path_containment_rejects_outside_traversal_symlink_and_non_regular_inputs() {
    let workspace = TempDir::new().expect("workspace");
    let outside = TempDir::new().expect("outside");
    let broker = LocalEffectBroker::new(1_000, 4_096);
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

    read.paths = vec![workspace.path().join("nested/../escape")];
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
        read.paths = vec![link.clone()];
        assert_eq!(
            broker
                .authorize(&read, &authorized)
                .expect_err("symlink denied"),
            PolicyDenial::SymlinkNotAllowed(link)
        );
    }

    read.paths = vec![workspace.path().to_path_buf()];
    assert_eq!(
        broker
            .authorize(&read, &authorized)
            .expect_err("directory denied"),
        PolicyDenial::NonRegularFile(workspace.path().to_path_buf())
    );
}

#[test]
fn nonexistent_write_target_is_bound_to_its_nearest_existing_ancestor() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::WriteWorkspace);
    let target = workspace.path().join("new/subdirectory/result.txt");
    let mut write = request(EffectKind::WriteFile);
    write.paths = vec![target.clone()];

    let admitted = broker
        .authorize(&write, &authorized)
        .expect("contained target admitted");
    assert_eq!(admitted.request().paths, vec![target]);
}

#[test]
fn process_arguments_with_shell_metacharacters_remain_literal() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::RunLocalProgram);
    authorized.allowed_programs.insert("/usr/bin/printf".into());
    let marker = workspace.path().join("must-not-exist");
    let literal = format!("$(touch {});*", marker.display());
    let mut command = request(EffectKind::RunProgram);
    command.program = Some("/usr/bin/printf".into());
    command.args = vec!["%s".into(), literal.clone()];

    let output = broker
        .execute(&command, &authorized)
        .expect("structured command executes");
    assert_eq!(output.status, 0);
    assert_eq!(output.stdout, literal.as_bytes());
    assert!(!marker.exists());
}

#[test]
fn denied_process_request_never_starts() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let marker = workspace.path().join("denied-marker");
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::RunLocalProgram);
    let mut command = request(EffectKind::RunProgram);
    command.program = Some("/usr/bin/touch".into());
    command.args = vec![marker.display().to_string()];

    let error = broker
        .execute(&command, &authorized)
        .expect_err("program denied");
    assert!(matches!(
        error,
        EffectBrokerError::Denied(PolicyDenial::ProgramNotAllowed(ref program))
            if program == "/usr/bin/touch"
    ));
    assert!(!marker.exists());
}

#[test]
fn run_program_requires_a_program_field() {
    let workspace = TempDir::new().expect("workspace");
    let broker = LocalEffectBroker::new(1_000, 4_096);
    let mut authorized = authority(workspace.path());
    authorized.capabilities.insert(Capability::RunLocalProgram);
    assert_eq!(
        broker
            .authorize(&request(EffectKind::RunProgram), &authorized)
            .expect_err("missing program denied"),
        PolicyDenial::MissingProgram
    );
}
