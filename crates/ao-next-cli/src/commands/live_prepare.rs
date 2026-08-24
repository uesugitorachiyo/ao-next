#[cfg(unix)]
use std::ffi::OsString;
use std::fs::File;
use std::io::Write as _;
use std::path::{Component, Path, PathBuf};

#[cfg(unix)]
use rustix::fs::{AtFlags, Mode, OFlags};
#[cfg(unix)]
use rustix::io::Errno;
#[cfg(windows)]
use std::os::windows::fs::{MetadataExt as _, OpenOptionsExt as _};

use ao_next_core::contracts::PreparedRunReceipt;
use ao_next_core::evidence::digest_bytes;
use ao_next_core::recovery::{CheckpointIdentity, CheckpointJournal};
use ao_next_core::strict_json::{canonical_digest, canonical_json_bytes};
use chrono::Utc;

use super::live::{
    LiveVariant, execution_journal_maximum_bytes, execution_journal_root, load_trusted_live_input,
    prepare_git_workspace, revalidate_prepared_live_input,
};
use super::{CommandFailure, CommandOutput, PrepareLiveArgs, read_bounded_regular};

pub fn execute(args: &PrepareLiveArgs) -> Result<CommandOutput, CommandFailure> {
    if std::env::var_os("AO_NEXT_LIVE_PROVIDER_CALLS").is_some() {
        return Err(CommandFailure::authorization(
            "provider authorization must be absent during live preparation",
        ));
    }
    let prepared_output = PreparedOutput::new(&args.out)?;
    let prepared_at = Utc::now();
    let trusted_input = load_trusted_live_input(
        &args.input,
        LiveVariant::N7,
        &args.trusted_corpus_digest,
        &args.trusted_verifier_profile_digest,
        prepared_at,
    )?;
    let input_bytes = trusted_input.bytes;
    let input = trusted_input.input;
    let journal = CheckpointJournal::new(
        execution_journal_root(&input),
        execution_journal_maximum_bytes(&input.request),
    )
    .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    journal
        .bind_pristine_request(&input.request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    let git = prepare_git_workspace(
        &input.request.workspace.root,
        &input.request.authority.allowed_roots,
        &input.request.workspace.seed_digest,
    )?;
    revalidate_prepared_live_input(&input, &git)?;
    let journal_identity = CheckpointIdentity::from_request(&input.request)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    if read_bounded_regular(&args.input)? != input_bytes {
        return Err(CommandFailure::invalid_input(
            "live input drifted during workspace preparation",
        ));
    }
    let receipt = PreparedRunReceipt {
        schema_version: "ao.next.prepared-run.v1".into(),
        run_id: input.request.run_id.clone(),
        input_digest: digest_bytes(&input_bytes),
        request_digest: canonical_digest(&input.request)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        repository_root: git.repository_root,
        common_directory: git.common_dir,
        branch: git.branch.into(),
        base_commit: git.head_commit,
        control_digest: git.control_digest,
        index_digest: git.index_digest,
        workspace_digest: input.request.workspace.seed_digest.clone(),
        journal_identity_digest: canonical_digest(&journal_identity)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        prepared_at,
        expires_at: input.request.authority.expires_at,
        provider_calls: 0,
        safe_to_execute: false,
    };
    let bytes = canonical_json_bytes(&receipt)
        .map_err(|error| CommandFailure::evidence(error.to_string()))?;
    prepared_output.publish(&bytes)?;
    Ok(CommandOutput::new(
        serde_json::to_value(&receipt)
            .map_err(|error| CommandFailure::evidence(error.to_string()))?,
        "prepared exact live Git identity without a provider call",
        0,
    ))
}

#[cfg(unix)]
struct PreparedOutput {
    root: File,
    parent: File,
    parent_relative: PathBuf,
    name: OsString,
}

#[cfg(unix)]
impl PreparedOutput {
    fn new(path: &Path) -> Result<Self, CommandFailure> {
        let path = absolute_output_path(path)?;
        let name = path
            .file_name()
            .ok_or_else(|| unsafe_output("prepared-run output name is missing"))?
            .to_os_string();
        let parent_path = path
            .parent()
            .ok_or_else(|| unsafe_output("prepared-run output parent is missing"))?;
        let root = rustix::fs::open(
            "/",
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map(File::from)
        .map_err(|error| unsafe_output(error.to_string()))?;
        let parent_relative = parent_path
            .strip_prefix("/")
            .map_err(|error| unsafe_output(error.to_string()))?
            .to_path_buf();
        let parent = open_unix_directory(&root, &parent_relative)?;
        require_unix_absent(&parent, &name)?;
        Ok(Self {
            root,
            parent,
            parent_relative,
            name,
        })
    }

    fn publish(self, bytes: &[u8]) -> Result<(), CommandFailure> {
        self.verify_parent_binding()?;
        let mut file = rustix::fs::openat(
            &self.parent,
            &self.name,
            OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::from_raw_mode(0o600),
        )
        .map(File::from)
        .map_err(|error| unsafe_output(error.to_string()))?;
        let result = file.write_all(bytes).and_then(|()| file.sync_all());
        drop(file);
        if let Err(error) = result {
            remove_unix_output(&self.parent, &self.name);
            return Err(CommandFailure::evidence(error.to_string()));
        }
        if let Err(error) = self.verify_parent_binding() {
            remove_unix_output(&self.parent, &self.name);
            return Err(error);
        }
        if let Err(error) = rustix::fs::fsync(&self.parent) {
            remove_unix_output(&self.parent, &self.name);
            return Err(CommandFailure::evidence(error.to_string()));
        }
        Ok(())
    }

    fn verify_parent_binding(&self) -> Result<(), CommandFailure> {
        let observed = open_unix_directory(&self.root, &self.parent_relative)?;
        let expected =
            rustix::fs::fstat(&self.parent).map_err(|error| unsafe_output(error.to_string()))?;
        let observed =
            rustix::fs::fstat(observed).map_err(|error| unsafe_output(error.to_string()))?;
        if expected.st_dev != observed.st_dev || expected.st_ino != observed.st_ino {
            return Err(unsafe_output("prepared-run output parent changed"));
        }
        Ok(())
    }
}

#[cfg(unix)]
fn open_unix_directory(root: &File, relative: &Path) -> Result<File, CommandFailure> {
    let mut directory = root
        .try_clone()
        .map_err(|error| unsafe_output(error.to_string()))?;
    for component in relative.components() {
        let Component::Normal(name) = component else {
            return Err(unsafe_output("prepared-run output path is unsafe"));
        };
        directory = rustix::fs::openat(
            &directory,
            name,
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map(File::from)
        .map_err(|error| unsafe_output(error.to_string()))?;
    }
    Ok(directory)
}

#[cfg(unix)]
fn require_unix_absent(parent: &File, name: &OsString) -> Result<(), CommandFailure> {
    match rustix::fs::statat(parent, name, AtFlags::SYMLINK_NOFOLLOW) {
        Err(Errno::NOENT) => Ok(()),
        Ok(_) => Err(unsafe_output("prepared-run output already exists")),
        Err(error) => Err(unsafe_output(error.to_string())),
    }
}

#[cfg(unix)]
fn remove_unix_output(parent: &File, name: &OsString) {
    let _ = rustix::fs::unlinkat(parent, name, AtFlags::empty());
    let _ = rustix::fs::fsync(parent);
}

#[cfg(windows)]
struct PreparedOutput {
    path: PathBuf,
    _anchors: Vec<File>,
}

#[cfg(windows)]
impl PreparedOutput {
    fn new(path: &Path) -> Result<Self, CommandFailure> {
        let path = absolute_output_path(path)?;
        let parent = path
            .parent()
            .ok_or_else(|| unsafe_output("prepared-run output parent is missing"))?;
        let mut ancestors = parent
            .ancestors()
            .filter(|value| !value.as_os_str().is_empty())
            .collect::<Vec<_>>();
        ancestors.reverse();
        let anchors = ancestors
            .into_iter()
            .map(open_windows_directory)
            .collect::<Result<Vec<_>, _>>()?;
        match open_windows_output(&path) {
            Ok(file) => {
                drop(file);
                return Err(unsafe_output("prepared-run output already exists"));
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => return Err(unsafe_output(error.to_string())),
        }
        Ok(Self {
            path,
            _anchors: anchors,
        })
    }

    fn publish(self, bytes: &[u8]) -> Result<(), CommandFailure> {
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .share_mode(FILE_SHARE_READ)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(&self.path)
            .map_err(|error| unsafe_output(error.to_string()))?;
        let metadata = file
            .metadata()
            .map_err(|error| unsafe_output(error.to_string()))?;
        if !metadata.is_file() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
            drop(file);
            let _ = std::fs::remove_file(&self.path);
            return Err(unsafe_output("prepared-run output is not regular data"));
        }
        if let Err(error) = file.write_all(bytes).and_then(|()| file.sync_all()) {
            drop(file);
            let _ = std::fs::remove_file(&self.path);
            return Err(CommandFailure::evidence(error.to_string()));
        }
        Ok(())
    }
}

#[cfg(windows)]
const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x400;
#[cfg(windows)]
const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
#[cfg(windows)]
const FILE_FLAG_OPEN_REPARSE_POINT: u32 = 0x0020_0000;
#[cfg(windows)]
const FILE_SHARE_READ: u32 = 0x1;
#[cfg(windows)]
const FILE_SHARE_WRITE: u32 = 0x2;

#[cfg(windows)]
fn open_windows_directory(path: &Path) -> Result<File, CommandFailure> {
    let file = std::fs::OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
        .map_err(|error| unsafe_output(error.to_string()))?;
    let metadata = file
        .metadata()
        .map_err(|error| unsafe_output(error.to_string()))?;
    if !metadata.is_dir() || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        return Err(unsafe_output(
            "prepared-run output ancestor is a reparse point",
        ));
    }
    Ok(file)
}

#[cfg(windows)]
fn open_windows_output(path: &Path) -> std::io::Result<File> {
    std::fs::OpenOptions::new()
        .read(true)
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
}

fn absolute_output_path(path: &Path) -> Result<PathBuf, CommandFailure> {
    let path = if path.is_absolute() {
        path.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|error| unsafe_output(error.to_string()))?
            .join(path)
    };
    if path.file_name().is_none()
        || path.components().any(|component| {
            !matches!(
                component,
                Component::Prefix(_) | Component::RootDir | Component::Normal(_)
            )
        })
    {
        return Err(unsafe_output("prepared-run output path is unsafe"));
    }
    Ok(path)
}

fn unsafe_output(message: impl Into<String>) -> CommandFailure {
    CommandFailure::invalid_input(message)
}

#[cfg(test)]
mod tests {
    use super::PreparedOutput;
    use tempfile::TempDir;

    #[cfg(unix)]
    #[test]
    fn prepared_output_rejects_parent_replacement_before_publication() {
        let temporary = TempDir::new().expect("temporary");
        let root = std::fs::canonicalize(temporary.path()).expect("canonical temporary");
        let parent = root.join("approved");
        let moved = root.join("moved-approved");
        std::fs::create_dir(&parent).expect("approved parent");
        let output =
            PreparedOutput::new(&parent.join("prepared-run.json")).expect("prepared output");
        std::fs::rename(&parent, &moved).expect("replace approved parent");
        std::fs::create_dir(&parent).expect("attacker parent");

        assert!(output.publish(b"receipt").is_err());
        assert!(!parent.join("prepared-run.json").exists());
        assert!(!moved.join("prepared-run.json").exists());
    }

    #[cfg(windows)]
    #[test]
    fn prepared_output_rejects_a_windows_junction_ancestor() {
        let temporary = TempDir::new().expect("temporary");
        let target = temporary.path().join("target");
        let junction = temporary.path().join("junction");
        std::fs::create_dir(&target).expect("junction target");
        let result = std::process::Command::new("cmd")
            .args([
                "/D",
                "/C",
                "mklink",
                "/J",
                junction.to_str().expect("junction path"),
                target.to_str().expect("target path"),
            ])
            .output()
            .expect("create junction");
        assert!(
            result.status.success(),
            "stdout={} stderr={}",
            String::from_utf8_lossy(&result.stdout),
            String::from_utf8_lossy(&result.stderr)
        );

        assert!(PreparedOutput::new(&junction.join("prepared-run.json")).is_err());
    }
}
