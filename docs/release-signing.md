# Signing and notarizing the macOS release

A downloaded app that is not notarized is blocked by macOS Gatekeeper the first time it is opened. On macOS 15,
right-click > Open no longer gets past it; the person must allow it in System Settings > Privacy & Security.
To sign a release with a Developer ID and notarize the disk image, prepare **all five** of these credentials:

| Credential | What it is |
| --- | --- |
| `MACOS_CERTIFICATE` | Your *Developer ID Application* certificate and private key, exported from Keychain Access as a `.p12`, then base64-encoded into a file: `base64 -i cert.p12 -o cert.txt` |
| `MACOS_CERTIFICATE_PASSWORD` | The password you set when exporting the `.p12` |
| `APPLE_ID` | The Apple ID email of the Apple Developer Program account |
| `APPLE_APP_PASSWORD` | An app-specific password for that Apple ID (appleid.apple.com > Sign-In and Security) |
| `APPLE_TEAM_ID` | The 10-character Team ID (developer.apple.com > Membership) |

A Developer ID certificate needs an Apple Developer Program membership.

Signing steps:

1. Sign `libnalarupa_core.dylib` (the Go core, built by `core-go/build.sh`) and the GPUIX addon, then the `nalarupa`
   executable and the bundle, with the hardened runtime and `packaging/macos/entitlements.plist` (the exceptions a
   Bun-compiled executable needs).
2. Sign the `.dmg`, submit it to Apple's notary service (`xcrun notarytool submit --wait`), and staple the ticket
   (`xcrun stapler staple`).
3. Quarantine a copy of the app as a browser download would and verify that Gatekeeper accepts the notarized build
   (`spctl --assess -vv --type execute`).
