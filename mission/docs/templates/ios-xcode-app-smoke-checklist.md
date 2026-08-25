# iOS/Xcode Local App Smoke Checklist

Use this checklist when a local private pilot needs app-level or device runtime evidence for an iOS target.

## Inputs

- Workspace: `<path/to/App.xcworkspace>`
- Project: `<path/to/App.xcodeproj>`
- Scheme: `<scheme>`
- Bundle ID: `<bundle id>`
- Device requirement: `<physical device / simulator / build only>`
- Selected physical device UDID: `<udid, if required>`
- Evidence directory: `<absolute local path>`

## Repository And Workspace Checks

- [ ] Save AO repo status and HEAD.
- [ ] Save target repo status and HEAD.
- [ ] Save app repo status and HEAD, if separate.
- [ ] If any repo is dirty, record exact paths before mutation.
- [ ] Confirm read-only repos will not be modified.
- [ ] List workspace schemes:

```sh
xcodebuild -list -workspace <workspace> -skipPackageUpdates
```

- [ ] List app project schemes:

```sh
xcodebuild -list -project <project> -skipPackageUpdates
```

## Device Selection

- [ ] List Xcode destinations:

```sh
xcodebuild -showdestinations -workspace <workspace> -scheme <scheme> -skipPackageUpdates
```

- [ ] List local devices:

```sh
xcrun devicectl list devices
```

- [ ] Confirm the selected physical device UDID appears in Xcode destinations.
- [ ] Confirm `devicectl` reports the device connected.
- [ ] Capture lock state when available:

```sh
xcrun devicectl device info lockState --device <udid>
```

## Build

Use a local DerivedData path under the evidence directory:

```sh
DERIVED_DATA=<evidence-dir>/deriveddata/app-smoke
```

- [ ] Build direct dependency framework targets first:

```sh
xcodebuild -workspace <workspace> -scheme <framework-scheme> -configuration Debug -sdk iphoneos -destination 'platform=iOS,id=<udid>' -derivedDataPath "$DERIVED_DATA" -skipPackageUpdates build
```

- [ ] Build the app:

```sh
xcodebuild -workspace <workspace> -scheme <scheme> -configuration Debug -sdk iphoneos -destination 'platform=iOS,id=<udid>' -derivedDataPath "$DERIVED_DATA" -skipPackageUpdates build
```

- [ ] Save full logs.
- [ ] Record built app path.

## Install

- [ ] Confirm app bundle path.
- [ ] Install locally:

```sh
xcrun devicectl device install app --device <udid> <path/to/App.app>
```

- [ ] Query installed app:

```sh
xcrun devicectl device info apps --device <udid> --bundle-id <bundle-id>
```

## Launch

- [ ] Launch the app:

```sh
xcrun devicectl device process launch --device <udid> <bundle-id> --terminate-existing --activate
```

- [ ] If launch is denied because the device is locked, save the exact log and stop unless the operator unlocks the device.
- [ ] If launch succeeds, record process ID and executable path from JSON output.

## Runtime Observation

- [ ] Capture immediate process evidence:

```sh
xcrun devicectl device info processes --device <udid>
```

- [ ] Observe for at least 30 seconds when tooling permits.
- [ ] Capture process evidence after the observation window.
- [ ] Query crash logs for the app name:

```sh
xcrun devicectl device info files --device <udid> --domain-type systemCrashLogs --recurse --filter "name CONTAINS[c] '<AppName>'"
```

- [ ] If the app exits before the observation window, retry once with a more focused command such as console-attached launch when appropriate:

```sh
xcrun devicectl device process launch --device <udid> <bundle-id> --terminate-existing --activate --console
```

## Local Permission Prompts

- [ ] Record camera/photo/location/local-network prompts as local permission observations.
- [ ] Do not automate external contact.
- [ ] If a prompt blocks the smoke, record the exact prompt and the local action needed.

## Guards

- [ ] Run `git diff --check` in each touched repo.
- [ ] Run artifact guard for forbidden staged/tracked artifacts.
- [ ] Run private-info scan as category/path-only output.
- [ ] Confirm named public API files are unchanged or approved.
- [ ] Confirm named public ABI headers are unchanged or approved.
- [ ] Confirm provided libraries and runtimes were not replaced.

## Report

The report should include:

- selected workspace/project/scheme;
- selected device and UDID;
- install result or reason install was not rerun;
- launch result;
- process evidence;
- crash-log result;
- 30-second observation result;
- guard results;
- public API/ABI status;
- final repo status;
- exact local blocker, if any;
- next AO Stack test step.
