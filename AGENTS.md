<!-- bmad:context -->

## nalarupa

AI design editor: a 100% Go desktop application combining the Go core (`core-go/`, linked directly in-process) and a GPU-native UI built on [MyGo](https://mygo.run/) (`app/`, `github.com/egoist/mygo/ui`). Windows is the primary release platform (with macOS and Linux builds). Work is tracked in GitHub issues.

## Policy

- Never push to `dev` or `main`; open a PR against `dev`. `main` is release-only and moved by the maintainer.
- Public repo: never name, quote, or copy code, assets, kits or text from other design tools. The `.pen` compatibility literals already in code (`pencil:` URL prefix, `"pencil:pending"`, the `pencil` script alias, `data-pen-node-clipboard`) are the only exception. Research notes stay outside the repo, never in `docs/`.
- Regenerate README screenshots only with `cd app && CGO_ENABLED=1 go run ./cmd/shots` (it uses a fresh `NALARUPA_DATA`, so no personal recents or chats leak).

## Where things are

- `core-go/guide/md/*.md` is runtime content compiled into the in-app agent (embedded via `core-go/guide/guide.go`), not instructions for you; edit them only to change what that agent is told.
- `app/` is the standalone Go + MyGo desktop application (`main.go`, `app_state.go`, `canvas.go`, `agent_layer.go`, `layers.go`, `inspector.go`, `agent_panel.go`, `agent_store.go`, `home.go`, `present.go`, `toolbar.go`, `widgets.go`, `bridge.go`) plus Go subcommands (`cmd/design-eval`, `cmd/shots`).

## Running and verifying

- Fetch the pinned `wgpu-native` static archive once in `core-go/`: `go generate ./shader/gpu`. Build the headless CLI if needed with `CGO_ENABLED=1 bash build.sh` (`build.ps1` on Windows, with MinGW-w64 gcc as `CC`).
- Run the desktop UI in dev mode from `app/`: `CGO_ENABLED=1 go tool mygo dev`.
- Build the standalone binary from `app/`: `bash build.sh` (`build.ps1` on Windows), which writes `app/dist/nalarupa` (`nalarupa.exe` on Windows) with `core-go` linked directly in-process. Set `NALARUPA_RELEASE=1` on Unix to strip the `MYGO_AUTOMATION` plugin (`-tags mygo_noinspector`).
- Go checks:
  - `cd core-go && CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./...`
  - `cd app && CGO_ENABLED=1 go tool mygo vet . && CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./...`
- On Linux run real windowed Wayland E2E checks from `app/` as `bash scripts/wl-run.sh env NALARUPA_E2E=1 CGO_ENABLED=1 go test -v -run TestRealE2E .` (starts headless `sway` 1920×1080 and drives `dist/nalarupa` over `MYGO_AUTOMATION` + HTTP MCP + PNG window captures).
- Windows-specific DWM/NSIS packaging checks are Windows-only; name any skipped platform-specific checks in the PR.

## Conventions that differ from defaults

- Never hold `engine` lock across slow work: render, poll and boxes share its mutex and the UI freezes; do heavy work after the guard drops (see `ToolOut.Finish`, `core-go/engine/toolout.go` and `Bridge.RenderInto` / `Bridge.ExecuteJS` in `app/bridge.go`).
- Follow MyGo UI lifetime rules (`go tool mygo vet .`): never store `ui.Element`, `*ui.Context`, or `*Parts` in structs or event closures (`HandleInput`, `Draw`, `After`, goroutines); put `.Key(...)` before `.Children(...)` / `.Clicked()` / `.Hovered()` / `.Dragged()`; keep persistent state (`ui.Handle`, `ui.ScrollState`, `ui.ListState`, `*ui.Bitmap`, `ui.Path`) on `NalarupaApp`.
- Never call `mygo.Theme.Preferences()` before `mygo.App.Run()` starts (it dispatches onto `mygo.onMain` and blocks when no main loop is running); read `c.Preferences().ReduceMotion` inside `View(c)` (which works in both real windows and `ui.Tester`).
- Every interactive control (`ButtonBase`, `TextInput`, `TextArea`, `RoleTab`, `RoleTreeItem`, `RoleCheckBox`) must have an accessible `.Label(...)`; `TestUIHomeTemplatesPresentLayersAndAccessibilityAudit` audits `tt.AccessAll()` across views and fails on any unnamed control.
- Match each file's line endings when patching by string; the repo mixes CRLF and LF.

<!-- /bmad:context -->

## Changing the design agent

Measure a change to what the design agent is told (`core-go/guide/md/*.md` and its instructions in `core-go/agent/agent.go`) with an eval run before and after: `cd app && CGO_ENABLED=1 go run ./cmd/design-eval run`, then `CGO_ENABLED=1 go run ./cmd/design-eval compare <before> <after>` (README Checks).

## Agent skills

### Issue tracker

Issues live in this repo's GitHub Issues (`gh` CLI). See `docs/agents/issue-tracker.md`.

### Triage labels

The five default triage labels (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.
