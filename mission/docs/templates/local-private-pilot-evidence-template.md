# Local Private Pilot Evidence Template

Evidence directory: `<absolute local path>`

## Objective

`<one sentence describing the local private pilot>`

## Scope

- AO supervisor repo: `<path>`
- Target repo or workspace: `<path>`
- Read-only repos: `<paths, if any>`
- Baseline commit or status: `<commit/status>`
- Prior evidence: `<path, if any>`

This evidence is internal AO Stack evidence. It is not a public claim.

## Hard Boundaries

- Local-only work.
- No fetch, pull, push, PR, tag, upload, deployment, or external contact unless explicitly authorized for this pilot.
- No provider pilots.
- No replacement of provided libraries or runtimes.
- No committed models, binaries, `.framework` directories, `.xcframework` directories, archives, generated artifacts, build outputs, or compiled libraries.
- Preserve named public API and ABI files unless the operator approves a change before it is made.

## Initial State

| Repo | HEAD Before | Status Before | Notes |
|---|---|---|---|
| `<repo>` | `<sha>` | `<status summary>` | `<clean / pre-existing dirty paths>` |

Saved snapshots:

- `snapshots/<repo>-status-before.txt`
- `snapshots/<repo>-head-before.txt`
- `snapshots/<repo>-diff-before.patch`

## Selected Local Path

`<workspace/project/scheme/device/app path selected>`

Reason:

`<why this is the smallest safe local path that exercises the target behavior>`

## Reproduced Failure Or Starting Checkpoint

Command:

```sh
<command>
```

Result:

`<exit code and short result>`

Evidence:

- `logs/<log-file>`

## Diagnosis

Root cause:

`<specific cause supported by logs and project/source inspection>`

Safe local paths evaluated:

- `<path evaluated>`
- `<path rejected or accepted, with reason>`

## Mutations

| Mutation | Files | Reason | Commit |
|---|---|---|---|
| `<none or summary>` | `<paths>` | `<why safe>` | `<sha or none>` |

Rules:

- Commit only source/docs/settings/project wiring changes.
- Do not commit provider artifacts or generated build outputs.
- Save diffs under `mutations/` or `snapshots/`.

## Verification

| Check | Command | Result | Evidence |
|---|---|---|---|
| Build | `<command>` | `<result>` | `logs/<file>` |
| Tests | `<command>` | `<result>` | `logs/<file>` |
| Install | `<command>` | `<result or not rerun>` | `logs/<file>` |
| Launch | `<command>` | `<result>` | `logs/<file>` |
| Runtime observation | `<command>` | `<result>` | `logs/<file>` |
| `git diff --check` | `<command>` | `<result>` | `logs/<file>` |
| Artifact guard | `<command>` | `<result>` | `logs/<file>` |
| Private-info scan | `<command>` | `<result>` | `logs/<file>` |
| API/ABI check | `<command>` | `<result>` | `snapshots/<file>` |

## Provided-Library Boundaries

State whether these stayed unchanged:

- OpenCV
- TensorFlowLite
- Paddle
- Torch
- OCR/barcode runtimes
- model runtimes
- provider frameworks
- binary frameworks

## Artifact Guard Result

`<no forbidden staged/tracked artifacts, or exact path-only finding>`

## Private-Info Result

`<no credential/token/key value findings, or category/path-only findings without values>`

## Public API And C ABI

- Public Swift API file changed: `<yes/no/not applicable>`
- Public C ABI header changed: `<yes/no/not applicable>`
- Approval required: `<yes/no>`

## Final State

| Repo | HEAD After | Status After | Notes |
|---|---|---|---|
| `<repo>` | `<sha>` | `<status summary>` | `<clean / preserved dirty paths>` |

## Result

`<what the evidence supports>`

## Remaining Risks

- `<risk>`

## Next Local AO Step

`<concrete next action>`

## Boundary Confirmation

Confirm no forbidden remote, upload, deployment, external contact, provider pilot, provided-library replacement, or forbidden artifact commit occurred.
