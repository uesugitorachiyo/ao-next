use std::path::{Component, Path, PathBuf};

use thiserror::Error;

use crate::contracts::{
    AuthorityEnvelope, Capability, EffectKind, EffectRequest, ExternalEffectPolicy, NetworkPolicy,
};

#[derive(Clone, Debug, Error, Eq, PartialEq)]
pub enum PolicyDenial {
    #[error("missing capability: {0:?}")]
    MissingCapability(Capability),
    #[error("run_program requires a program")]
    MissingProgram,
    #[error("program is not allowlisted: {0}")]
    ProgramNotAllowed(String),
    #[error("shell programs are not supported: {0}")]
    ShellProgramDenied(String),
    #[error("effect timeout {requested_ms}ms exceeds maximum {maximum_ms}ms")]
    TimeoutExceeded { requested_ms: u64, maximum_ms: u64 },
    #[error("path is outside allowed roots: {0}")]
    PathOutsideAllowedRoots(PathBuf),
    #[error("parent traversal is not allowed: {0}")]
    ParentTraversal(PathBuf),
    #[error("symlinks are not allowed: {0}")]
    SymlinkNotAllowed(PathBuf),
    #[error("path is not a regular file: {0}")]
    NonRegularFile(PathBuf),
    #[error("allowed root is unavailable: {0}")]
    AllowedRootUnavailable(PathBuf),
    #[error("network policy denies the request")]
    NetworkDenied,
    #[error("external effect policy denies {0:?}")]
    ExternalEffectDenied(EffectKind),
}

pub(crate) fn validate_effect_request(
    request: &EffectRequest,
    authority: &AuthorityEnvelope,
    maximum_timeout_ms: u64,
) -> Result<(), PolicyDenial> {
    let capability = required_capability(&request.kind);
    if !authority.capabilities.contains(&capability) {
        return Err(PolicyDenial::MissingCapability(capability));
    }

    if request.timeout_ms > maximum_timeout_ms {
        return Err(PolicyDenial::TimeoutExceeded {
            requested_ms: request.timeout_ms,
            maximum_ms: maximum_timeout_ms,
        });
    }

    match request.kind {
        EffectKind::RunProgram => validate_program(request, authority)?,
        EffectKind::Network if authority.network == NetworkPolicy::Denied => {
            return Err(PolicyDenial::NetworkDenied);
        }
        EffectKind::RemoteMutation
        | EffectKind::Release
        | EffectKind::Deployment
        | EffectKind::Publication
            if authority.external_effects != ExternalEffectPolicy::AuthorizedCapabilitiesOnly =>
        {
            return Err(PolicyDenial::ExternalEffectDenied(request.kind.clone()));
        }
        _ => {}
    }

    let require_regular = matches!(request.kind, EffectKind::ReadFile | EffectKind::WriteFile);
    for path in &request.paths {
        validate_path(path, &authority.allowed_roots, require_regular)?;
    }
    Ok(())
}

fn required_capability(kind: &EffectKind) -> Capability {
    match kind {
        EffectKind::ReadFile => Capability::ReadWorkspace,
        EffectKind::WriteFile => Capability::WriteWorkspace,
        EffectKind::RunProgram => Capability::RunLocalProgram,
        EffectKind::Network => Capability::NetworkAccess,
        EffectKind::Credential => Capability::CredentialAccess,
        EffectKind::RemoteMutation => Capability::RemoteMutation,
        EffectKind::Release => Capability::Release,
        EffectKind::Deployment => Capability::Deployment,
        EffectKind::Publication => Capability::Publication,
    }
}

fn validate_program(
    request: &EffectRequest,
    authority: &AuthorityEnvelope,
) -> Result<(), PolicyDenial> {
    let program = request
        .program
        .as_deref()
        .ok_or(PolicyDenial::MissingProgram)?;
    let base_name = Path::new(program)
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or(program)
        .to_ascii_lowercase();
    if matches!(
        base_name.as_str(),
        "sh" | "bash" | "zsh" | "dash" | "fish" | "pwsh" | "powershell" | "cmd.exe"
    ) {
        return Err(PolicyDenial::ShellProgramDenied(program.to_owned()));
    }
    if !authority.allowed_programs.contains(program) {
        return Err(PolicyDenial::ProgramNotAllowed(program.to_owned()));
    }
    Ok(())
}

fn validate_path(
    path: &Path,
    allowed_roots: &[PathBuf],
    require_regular: bool,
) -> Result<(), PolicyDenial> {
    if path
        .components()
        .any(|component| component == Component::ParentDir)
    {
        return Err(PolicyDenial::ParentTraversal(path.to_path_buf()));
    }
    if !path.is_absolute() {
        return Err(PolicyDenial::PathOutsideAllowedRoots(path.to_path_buf()));
    }

    for root in allowed_roots {
        let root_metadata = std::fs::symlink_metadata(root)
            .map_err(|_| PolicyDenial::AllowedRootUnavailable(root.clone()))?;
        if root_metadata.file_type().is_symlink() {
            return Err(PolicyDenial::SymlinkNotAllowed(root.clone()));
        }
        let canonical_root = std::fs::canonicalize(root)
            .map_err(|_| PolicyDenial::AllowedRootUnavailable(root.clone()))?;
        let (base, relative) = if let Ok(relative) = path.strip_prefix(root) {
            (root.clone(), relative)
        } else if let Ok(relative) = path.strip_prefix(&canonical_root) {
            (canonical_root.clone(), relative)
        } else {
            continue;
        };

        let mut current = base.clone();
        let mut nearest_existing = base;
        for component in relative.components() {
            if let Component::Normal(segment) = component {
                current.push(segment);
                match std::fs::symlink_metadata(&current) {
                    Ok(metadata) => {
                        if metadata.file_type().is_symlink() {
                            return Err(PolicyDenial::SymlinkNotAllowed(current));
                        }
                        nearest_existing.clone_from(&current);
                    }
                    Err(error) if error.kind() == std::io::ErrorKind::NotFound => break,
                    Err(_) => return Err(PolicyDenial::NonRegularFile(current)),
                }
            }
        }

        let canonical_ancestor = std::fs::canonicalize(&nearest_existing)
            .map_err(|_| PolicyDenial::PathOutsideAllowedRoots(path.to_path_buf()))?;
        if !canonical_ancestor.starts_with(&canonical_root) {
            return Err(PolicyDenial::PathOutsideAllowedRoots(path.to_path_buf()));
        }

        if require_regular {
            match std::fs::symlink_metadata(path) {
                Ok(metadata) if !metadata.file_type().is_file() => {
                    return Err(PolicyDenial::NonRegularFile(path.to_path_buf()));
                }
                Ok(_) => {}
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
                Err(_) => return Err(PolicyDenial::NonRegularFile(path.to_path_buf())),
            }
        }
        return Ok(());
    }

    Err(PolicyDenial::PathOutsideAllowedRoots(path.to_path_buf()))
}
