# Signing and notarizing the macOS release

A downloaded app that is not notarized is blocked by macOS Gatekeeper the first time it is opened. On macOS 15,
right-click > Open no longer gets past it; the person must allow it in System Settings > Privacy & Security.

## Prerequisites

- An Apple Developer Program membership and a **Developer ID Application** certificate installed in your login
  keychain (or exported as a `.p12` and imported with `security import cert.p12 -k ~/Library/Keychains/login.keychain-db -T /usr/bin/codesign`).
- An Apple ID, 10-character Team ID, and app-specific password (from appleid.apple.com > Sign-In and Security),
  stored once in a `notarytool` keychain profile:

```bash
xcrun notarytool store-credentials "nalarupa-notary" \
  --apple-id "<apple-id@example.com>" \
  --team-id "<TEAM_ID>" \
  --password "<app-specific-password>"
```

Find your signing identity hash or common name:

```bash
SIGN_ID=$(security find-identity -v -p codesigning | awk '/Developer ID Application/ { print $2; exit }')
```

## 1. Sign the `.app` bundle

Build `out/Nalarupa.app` as documented in `README.md` (with `nalarupa` inside `out/Nalarupa.app/Contents/MacOS/`), then
sign any inner libraries (if present) followed by the executable and the bundle itself with the hardened runtime and
`packaging/macos/entitlements.plist`:

```bash
APP="out/Nalarupa.app"
for f in "$APP"/Contents/MacOS/*.dylib; do
  [ -f "$f" ] || continue
  codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$f"
done
codesign --force --timestamp --options runtime --entitlements packaging/macos/entitlements.plist --sign "$SIGN_ID" "$APP/Contents/MacOS/nalarupa"
codesign --force --timestamp --options runtime --entitlements packaging/macos/entitlements.plist --sign "$SIGN_ID" "$APP"
codesign --verify --deep --strict "$APP"
```

## 2. Package, sign, notarize, and staple the `.dmg`

```bash
VERSION="<X.Y.Z>"
DMG="out/Nalarupa-$VERSION-macos-arm64.dmg"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
cp -R "$APP" "$stage/"
ln -s /Applications "$stage/Applications"
hdiutil create -volname "Nalarupa $VERSION" -srcfolder "$stage" -format UDZO -ov "$DMG"
rm -rf "$stage"
trap - EXIT

codesign --force --timestamp --sign "$SIGN_ID" "$DMG"
xcrun notarytool submit "$DMG" --keychain-profile "nalarupa-notary" --wait
xcrun stapler staple "$DMG"
```

## 3. Verify Gatekeeper acceptance

Simulate a browser download by quarantining the `.dmg` and extracted `.app`, then running `spctl`:

```bash
Q="0083;$(printf %x "$(date +%s)");Safari;$(uuidgen)"
TEST_DIR=$(mktemp -d)
xattr -w com.apple.quarantine "$Q" "$DMG"
hdiutil attach -nobrowse -mountpoint "$TEST_DIR/dmg" "$DMG"
ditto "$TEST_DIR/dmg/Nalarupa.app" "$TEST_DIR/Nalarupa.app"
hdiutil detach "$TEST_DIR/dmg"
xattr -rw com.apple.quarantine "$Q" "$TEST_DIR/Nalarupa.app"

spctl --assess -vv --type execute "$TEST_DIR/Nalarupa.app"

# Remove the test quarantine attribute from the release DMG and clean up.
xattr -d com.apple.quarantine "$DMG"
rm -rf "$TEST_DIR"
```
