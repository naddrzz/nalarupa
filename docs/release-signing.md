# Signing and notarizing the macOS release

A downloaded app that is not notarized is blocked by macOS Gatekeeper the first time it is opened. On macOS 15,
right-click > Open no longer gets past it; the person must allow it in System Settings > Privacy & Security.
The release workflow (`.github/workflows/release.yml`) signs with a Developer ID and notarizes the disk image
whenever these repository secrets exist. Without them it ships an ad-hoc signed build, and the release notes say how
to allow it.

| Secret | What it is |
| --- | --- |
| `MACOS_CERTIFICATE` | Your *Developer ID Application* certificate and private key, exported from Keychain Access as a `.p12`, then base64-encoded: `base64 -i cert.p12 \| pbcopy` |
| `MACOS_CERTIFICATE_PASSWORD` | The password you set when exporting the `.p12` |
| `APPLE_ID` | The Apple ID email of the Apple Developer Program account |
| `APPLE_APP_PASSWORD` | An app-specific password for that Apple ID (appleid.apple.com > Sign-In and Security) |
| `APPLE_TEAM_ID` | The 10-character Team ID (developer.apple.com > Membership) |

Set them with `gh secret set <NAME>` or under Settings > Secrets and variables > Actions. A Developer ID certificate
needs an Apple Developer Program membership.

What the workflow does with them:

1. Signs `libnalarupa_core.dylib` and the GPUIX addon, then the `nalarupa` executable and the bundle, with the
   hardened runtime and `packaging/macos/entitlements.plist` (the exceptions a Bun-compiled executable needs).
2. Signs the `.dmg`, submits it to Apple's notary service, waits, and staples the ticket.
3. Quarantines a copy of the app as a browser download would and fails the job if Gatekeeper rejects a notarized
   build.

To check a published release on real Macs, run the **mac-smoke** workflow (Actions > mac-smoke > Run workflow) with
its tag: it downloads the `.dmg`, quarantines it, opens the app with a double-click's path, and runs it.
