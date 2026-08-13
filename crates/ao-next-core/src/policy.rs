use std::path::{Component, Path, PathBuf};

use thiserror::Error;

use crate::contracts::{
    AuthorityEnvelope, Capability, EffectKind, EffectRequest, ExternalEffectPolicy, NetworkPolicy,
};

#[derive(Clone, Debug, Error, Eq, PartialEq)]
pub enum PolicyDenial {
    #[error("missing capability: {0:?}")]
    MissingCapability(Capability),
    #[error("model-controlled programs are not supported")]
    ModelProgramDenied,
    #[error("effect fields contradict the requested kind")]
    ContradictoryFields,
    #[error("effect timeout {requested_ms}ms exceeds maximum {maximum_ms}ms")]
    TimeoutExceeded { requested_ms: u64, maximum_ms: u64 },
    #[error("path is outside allowed roots: {0}")]
    PathOutsideAllowedRoots(PathBuf),
    #[error("parent traversal is not allowed: {0}")]
    ParentTraversal(PathBuf),
    #[error("protected workspace control path is not allowed: {0}")]
    ProtectedControlPath(PathBuf),
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
    if request.kind == EffectKind::RunProgram {
        return Err(PolicyDenial::ModelProgramDenied);
    }
    validate_fields(request)?;
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

    resolve_effect_paths(request, authority, request.kind == EffectKind::ReadFile)?;
    Ok(())
}

pub(crate) fn resolve_effect_paths(
    request: &EffectRequest,
    authority: &AuthorityEnvelope,
    require_existing_regular: bool,
) -> Result<Vec<PathBuf>, PolicyDenial> {
    request
        .paths
        .iter()
        .map(|path| resolve_path(path, &authority.allowed_roots, require_existing_regular))
        .collect()
}

fn validate_fields(request: &EffectRequest) -> Result<(), PolicyDenial> {
    if request.effect_id.trim().is_empty() || request.run_id.trim().is_empty() {
        return Err(PolicyDenial::ContradictoryFields);
    }
    let valid = match request.kind {
        EffectKind::ReadFile => {
            request.program.is_none()
                && request.content.is_none()
                && request.args.is_empty()
                && request.paths.len() == 1
                && request.timeout_ms == 0
        }
        EffectKind::WriteFile => {
            request.program.is_none()
                && request.content.is_some()
                && request.args.is_empty()
                && request.paths.len() == 1
                && request.timeout_ms == 0
        }
        EffectKind::RunProgram => {
            request.program.is_some() && request.content.is_none() && request.timeout_ms > 0
        }
        EffectKind::Network
        | EffectKind::Credential
        | EffectKind::RemoteMutation
        | EffectKind::Release
        | EffectKind::Deployment
        | EffectKind::Publication => {
            request.program.is_none()
                && request.content.is_none()
                && request.args.is_empty()
                && request.paths.is_empty()
                && request.timeout_ms == 0
        }
    };
    if valid {
        Ok(())
    } else {
        Err(PolicyDenial::ContradictoryFields)
    }
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

fn resolve_path(
    path: &Path,
    allowed_roots: &[PathBuf],
    require_existing_regular: bool,
) -> Result<PathBuf, PolicyDenial> {
    if path.is_absolute() {
        return Err(PolicyDenial::PathOutsideAllowedRoots(path.to_path_buf()));
    }
    if path
        .components()
        .any(|component| matches!(component, Component::ParentDir | Component::CurDir))
    {
        return Err(PolicyDenial::ParentTraversal(path.to_path_buf()));
    }

    if path.components().any(|component| {
        matches!(component, Component::Normal(segment) if segment.eq_ignore_ascii_case(".git"))
    }) {
        return Err(PolicyDenial::ProtectedControlPath(path.to_path_buf()));
    }

    let candidate = if allowed_roots.len() == 1
        && path
            .components()
            .all(|component| matches!(component, Component::Normal(_)))
    {
        allowed_roots[0].join(path)
    } else {
        return Err(PolicyDenial::PathOutsideAllowedRoots(path.to_path_buf()));
    };

    for root in allowed_roots {
        let root_metadata = std::fs::symlink_metadata(root)
            .map_err(|_| PolicyDenial::AllowedRootUnavailable(root.clone()))?;
        if root_metadata.file_type().is_symlink() {
            return Err(PolicyDenial::SymlinkNotAllowed(root.clone()));
        }
        let canonical_root = std::fs::canonicalize(root)
            .map_err(|_| PolicyDenial::AllowedRootUnavailable(root.clone()))?;
        let (base, relative) = if let Ok(relative) = candidate.strip_prefix(root) {
            (root.clone(), relative)
        } else if let Ok(relative) = candidate.strip_prefix(&canonical_root) {
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

        if require_existing_regular {
            match std::fs::symlink_metadata(&candidate) {
                Ok(metadata) if !metadata.file_type().is_file() => {
                    return Err(PolicyDenial::NonRegularFile(path.to_path_buf()));
                }
                Ok(_) => {}
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                    return Err(PolicyDenial::NonRegularFile(path.to_path_buf()));
                }
                Err(_) => return Err(PolicyDenial::NonRegularFile(path.to_path_buf())),
            }
        }
        return Ok(candidate);
    }

    Err(PolicyDenial::PathOutsideAllowedRoots(path.to_path_buf()))
}
