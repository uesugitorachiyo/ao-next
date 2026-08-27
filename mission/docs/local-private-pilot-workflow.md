# Local Private Pilot Workflow

Use this workflow when AO Mission supervises a real local codebase without remote lifecycle work. It is for internal AO Stack evidence, not a public claim.

The workflow fits pilots like local framework migration, local app integration, local device runtime smoke, or private customer-style app checks where the target repository must stay private and local.

## Boundaries

Record these boundaries before the first command:

- target repositories and paths;
- AO repository that supervises the work;
- exact baseline commits or known dirty paths;
- forbidden actions: fetch, pull, push, PR, tag, upload, deploy, external contact, provider pilot, or public claim;
- provided libraries that must stay provided, such as OpenCV, TensorFlowLite, Paddle, Torch, OCR/barcode runtimes, model runtimes, provider frameworks, and binary frameworks;
- files that must not change, such as public Swift API files or public C ABI headers;
- artifact classes that must not be committed: models, binaries, `.framework` directories, `.xcframework` directories, archives, generated artifacts, build outputs, and compiled libraries.

If the target worktree is dirty, save a status snapshot before making changes. Preserve unrelated user work. If the target is read-only for the AO task, do not modify it.

## Evidence Directory

Create one evidence directory outside the target repository:

```sh
mkdir -p /path/to/evidence/{logs,snapshots,mutations}
```

Save:

- `git status --short --branch --untracked-files=all` for every local repo inspected;
- `git rev-parse HEAD` for every local repo inspected;
- commands and full logs for every build, test, install, launch, and guard;
- mutation diffs before each local commit;
- final status snapshots after verification.

Evidence paths should be local filesystem paths. Do not upload them.

## Phase Order

1. Confirm AO repo status and preserve unrelated state.
2. Confirm target repo status and baseline.
3. Reproduce the current failure or verify the current checkpoint.
4. Diagnose the smallest local cause before editing.
5. Apply the smallest safe source, settings, or project wiring change.
6. Commit only source/docs/settings/project changes that are safe and local.
7. Rerun the narrow failed check after each fix.
8. Run the full required verification after the final mutation.
9. Write the AO evidence report.
10. Confirm final statuses and boundary compliance.

When the goal is launch/runtime evidence only, reuse the validated build/install context where safe. Do not redo unrelated migration checks unless the new evidence depends on them.

## Provided-Library Boundaries

Keep provided libraries in place unless the operator explicitly authorizes a replacement. For local private pilots, treat these as boundaries:

- OpenCV remains a provided external library.
- TensorFlowLite, Paddle, Torch, OCR/barcode runtimes, model runtimes, and provider frameworks remain provided dependencies.
- Local ignored provider frameworks and models may be used for build/runtime only when already present on disk.
- Do not commit provider frameworks, models, compiled libraries, archives, or generated build products.

If a build needs a missing provider asset, document the exact path and command failure. Stop only when no safe local substitute or local configuration fix remains.

## Artifact Guard

Run an artifact guard over every touched repo before final reporting:

```sh
git diff --cached --name-only --diff-filter=ACMRT | rg -i '(\.framework/|\.xcframework/|\.a$|\.dylib$|\.so$|\.o$|\.ipa$|\.app$|\.xcarchive/|\.mlmodel$|\.tflite$|\.onnx$|\.pt$|\.pth$|\.bin$|\.zip$|DerivedData|build/)' || true
git ls-files | rg -i '(\.framework/|\.xcframework/|\.a$|\.dylib$|\.so$|\.o$|\.ipa$|\.app$|\.xcarchive/|\.mlmodel$|\.tflite$|\.onnx$|\.pt$|\.pth$|\.bin$|DerivedData|build/)' || true
```

The report should state whether forbidden paths are staged or tracked. It should not print credential values or private data.

## Public API And C ABI Checks

When the pilot touches a framework or language bridge, name the public API and ABI files up front. For Swift/C bridge work, record:

- whether the public Swift API file changed;
- whether the public C ABI header changed;
- the exact diff command used;
- whether approval was required before any such change.

Example:

```sh
git diff -- xcore/XCore/Core.swift > snapshots/core-swift-worktree-diff.txt
git diff -- xcore/XCore/cpp/wrapper.h > snapshots/wrapper-h-worktree-diff.txt
```

Empty diff files are useful evidence.

## iOS/Xcode App Smoke

For a local iOS app smoke:

1. List workspace schemes and project schemes.
2. Pick the smallest local app target that consumes the changed stack.
3. Use a local DerivedData path under the evidence directory.
4. Build direct framework targets first when the app depends on them.
5. Build the app for the selected SDK/device.
6. If a physical device is required, list destinations and confirm the exact UDID.
7. Install with local device tooling only.
8. Launch with local device tooling only.
9. Observe for at least 30 seconds when tooling permits.
10. Capture process presence and crash-log evidence if available.

If the phone is locked, signing is unavailable, or a local provider asset is missing, save the exact log and classify the blocker. Do not fall back to a simulator unless the operator authorized that path.

## Runtime Evidence

A runtime report should include:

- install result or reason install was not rerun;
- launch command and result;
- process ID and executable path when available;
- process presence after the observation window;
- crash-log query result when available;
- permission prompts or local user-action blockers;
- any app exits observed during sampling.

If one sample is ambiguous, run a focused retry that can answer the question. Do not claim no-crash evidence from a launch result alone.

## Private-Info Scan

Run private-info scans as category/path-only output. Do not print token, key, password, or secret values.

Acceptable report shape:

```text
no_credential_token_key_value_findings_identified
category_path_only_findings
path/to/file.swift: key-name only, no value printed or identified
```

## Closure Report

End each pilot with an AO report that covers:

- objective and scope;
- repos and HEADs before/after;
- selected workflow path;
- local commits created;
- failures reproduced;
- root causes and fixes;
- build/test/install/launch/runtime results;
- artifact and private-info guard results;
- public API and C ABI status;
- provided-library boundaries;
- forbidden actions not performed;
- remaining risks;
- next local AO Stack test step.

Use practical language. State what the evidence supports. Do not turn local private evidence into a public claim.
