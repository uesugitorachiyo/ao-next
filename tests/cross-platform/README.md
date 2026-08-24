# Physical Windows NTFS Qualification

Run this provider-free procedure on a physical Windows host after changes that would publish AO Next. It supplements the hosted Windows matrix; it does not authorize publication.

1. Create a new empty NTFS root with a path containing spaces, for example `D:\AO Next NTFS Qualification`. Clone the checkout below that root. Keep the result JSON outside the checkout, for example `D:\AO Next NTFS Qualification\result.json`.
2. Confirm `AO_NEXT_LIVE_PROVIDER_CALLS` is absent. Do not set a provider gate or provider-program override.
3. From the checkout, run this PowerShell block:

```powershell
cargo test -p ao-next-core --test capture_store -- --nocapture
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo test -p ao-next-core --test evidence_recovery provider_ -- --nocapture
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo test -p ao-next-cli --test cli recover_live -- --nocapture
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo test --workspace
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo clippy --workspace --all-targets -- -D warnings
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
cargo build --workspace --release
exit $LASTEXITCODE
```

4. Write the result JSON outside the checkout. Record the Windows OS build, NTFS filesystem, source `HEAD`, each command and exit code, final and incomplete capture inventory, provider process count (`0`), target-directory hash, and cleanup result. Remove the qualification root and record whether cleanup completed.

Live-provider tests remain intentionally ignored. Do not treat this procedure as a provider call, deployment, release, or Stage-0 closure.
