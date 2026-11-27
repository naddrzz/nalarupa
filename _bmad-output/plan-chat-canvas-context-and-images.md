---
title: 'Agent Chat Canvas Context & Multi-Image Attachments'
type: feature
ticket: ''
created: '2026-11-27'
status: built
baseline_revision: '78555d7e86f77f2bd4147d76ad8a7c710b7a7801'
route: full
route_source: auto
risk: medium
review: quick
review_source: pinned
lenses_ran: [quick]
review_loop_iteration: 0
context: []
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** The agent chat composer only accepts plain text; users cannot explicitly attach canvas context from the canvas screen or send multiple reference images with their brief.

**Approach:** Add a composer attachment tray, canvas-screen attach entrypoints, and an end-to-end multimodal send pipeline so users can attach pinned canvas context and multiple images to Design agent turns, rendered in user bubbles and preserved in chat history.

**Human Decisions:**
- **Canvas attach entrypoints (Q1 = Option A):** Floating **"+ Chat"** action pill near selected node(s) on canvas + **right-click context menu** on canvas (*"Attach to Agent Chat"* for node/selection, *"Attach viewport screen"* on empty canvas) + shortcut **`Ctrl/Cmd+L`** + composer attach controls (*"Current selection"*, *"Pick from canvas"* click-to-attach mode, *"Canvas viewport"*).
- **Canvas context payload (Q2 = Option A):** Send pinned node metadata & compact JSON/subtree summary (or viewport bounds + visible top-level nodes) inside an explicit `[Attached canvas context]` block **plus** an auto-rendered PNG snapshot of each attached node/frame (or viewport screen) when the active model supports vision.
- **Multi-image input & backends (Q3 = Option A):** Support multi-select file picker (`Multiple: true`), clipboard paste (`Ctrl/Cmd+V` for images & image file lists), and multi-file drag-and-drop onto the chat composer/panel (PNG/JPEG/WebP/GIF, capped at 10 images/turn, auto-downscaled when oversized). Deliver native image blocks to **API/BYOK vision models** AND **Claude Code CLI** (via `--input-format stream-json`), and show a clear non-blocking warning when the active BYOK provider has `vision: false`.

## Boundaries & Constraints

**Always:**
- Keep `agent_send` and v1 `sessions/*.json` backward-compatible for text-only callers (`design-eval`, tests, old chats) and keep plain-text stdin on `claude-code` when no images are attached.
- Keep user `content[0]` as `"type":"text"` (with `"__prompt_len"` stripped by `apiWireMessages`) so `context.go` `editorStateNoteIndex` and context-guard prefixing stay valid.
- Follow engine lock discipline: never hold `Engine.Lock()` across file I/O, image decode/encode, rasterization, or dialogs (`mygo.Dialog.Open` off-main-thread → `win.Update`).
- Follow MyGo UI lifetime & a11y rules: pass `go tool mygo vet .`, set `.Label(...)` on every interactive control, release evicted/removed `*ui.Bitmap` via `ReleasePixels()`, keep `composer-bottom` single-row at `1280×800` (`RightW = 300`), and preserve CRLF line endings.

**Never:**
- Never break `model.Provider` JSON key order (`openai_chat_test.go`, `openai_responses_test.go`) or `EnforceContextWindow` role/tool pairing.
- Never call `mygo.Theme.Preferences()` before `App.Run()`; never touch unrelated dirty files (`AGENTS.md`, `_bmad/*`).

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Canvas attach (selection / context menu / `Cmd+L` / pick mode) | Node(s) selected or picked on canvas, or empty canvas viewport | Switches to Agent tab (guarding `onSelectionChanged` auto-switch), adds deduped removable chip(s), focuses composer | Toast if node deleted before send; omit stale node cleanly |
| Multi-image attach | File picker (`Multiple: true`), `OnPaste` (`PNG`/`FileList`), or `DroppedFiles` (PNG/JPEG/WebP/GIF) | Appends valid images (auto-downscaled, max 10/turn) as removable thumbnail chips | Toast invalid/unreadable/over-limit files; keep valid ones |
| Send text +/or attachments | Prompt and/or canvas context and/or N images | Sends via `agent_send`; vision models get PNGs + structured context; renders user bubble badges + thumbnails; persists in `sessions/*.json` | On send error, toast and keep draft + attachments; non-vision BYOK shows warning |
| `claude-code` with images | User attaches 1+ images (or vision canvas snapshot) on `claude-code` backend | Invokes `claude` with `--input-format stream-json` and Claude-format `content` array on stdin | Plain text stdin unchanged when 0 images |
| Queue while agent running | Send with attachments while `Agent.Running` | Queues prompt + contexts + images; coalesced dequeue preserves all in next turn | `Stop` clears queue and emits `dequeued` |
| History reload | `chat_load` on saved chat with attachments | `RestoreChat` restores user bubble text, context chips, and image thumbnails | Old v1 sessions without attachment fields load cleanly |
| Context compaction | History nears `contextLimit` with earlier user images | `EnforceContextWindow` strips stale older user images in Pass 1/Pass 4 while preserving alternation & index-0 text | Keep latest human turn's images until Pass 4 |

</frozen-after-approval>

## Code Map

- `core-go/engine/agent.go` -- `AgentState` (`L89-147`), `Queue` (`L103`), `Record` (`L168-201`), `Clear`/`Restore` (`L248-280`). Add `AttachmentImage` / `CanvasContextRef` types and extend `Queue` entries to preserve attachments.
- `core-go/engine/tools.go`, `replay.go`, `sync.go` -- `EditorState()` (`tools.go:56-259`), unlocked `"png"` (`replay.go:276-305`, `NodeTreeAt` + off-lock `EncodeTree`), `SnapshotForRender` (`sync.go:522-542`). Reuse for pinned node JSON/metadata formatting and off-lock node/viewport PNG capture.
- `core-go/agent/commands.go`, `agent.go` -- `agent_send` (`commands.go:49-52`), `Send`/`begin`/`startTurn`/`run`/`takeQueue` (`agent.go:133-264, 333-443, 497-516`). Parse optional `contexts` & `images`, resolve pinned snapshots off-lock, thread through queue, events, and backends.
- `core-go/agent/api.go`, `claudecode.go`, `context.go` -- `runAPI` (`api.go:47-105`), `apiWireMessages` (`api.go:303-319`), `runClaudeCodeStep` (`claudecode.go:158-265`), `stripStaleImagesAndEditorState`/`compactOversizedTurnBlocks` (`context.go:124-184, 260-308`). Append user `"image"` blocks after index-0 text; support `--input-format stream-json` on `claude-code`; compact stale user images.
- `core-go/model/claude.go`, `openai_chat.go` (`L349-359`), `openai_responses.go` (`L292-298`), `sse.go` (`L54-81`) -- Top-level user `"image"` blocks & stall bump already supported. Do NOT change wire key order; prevent silent message drop when `vision: false` and text is empty.
- `app/agent_store.go` -- `ChatItem` (`L111-148`, has `Images`), `ApplyEvents` (`L680-697`), `RestoreChat` (`L1139-1204`), `ImageDataURI`/`DecodeBase64Image` (`L1207-1223`). Add `Contexts` metadata, populate `Images` + `Contexts` on `"user"`/`"queued"`, update queued-item dedup.
- `app/agent_panel.go`, `agent_skills_ui.go` -- `AgentPanelState` (`L80-177`), `submitAgentPrompt` (`L512-541`), `viewChatItem` (`L1809-1826, 2021-2059`), `viewAgentComposer` (`L2458-2788`), `ImageCache` (`L72-77, 214-225`), pill pattern (`agent_skills_ui.go:397-432`). Reuse `ui.DecodeBitmapScaled`, `IconSVG("image"|"paperclip"|"x"|"plus")`.
- `app/canvas.go`, `app_state.go` -- `buildCanvasChildren` (`canvas.go:1320-1491`), `onCanvasPointerDown` (`canvas.go:1760-1825`), `onSelectionChanged` (`app_state.go:381-399`, note auto-switch to `"design"`), `TriggerOpenDialog` (`L470-514`), `HandleEditorShortcut` (`L918-1159`, `Cmd/Ctrl+L` free).
- `app/ui_test.go` (`L31-55`), `round4_perf_test.go` (`L70-123`) -- Accessibility & single-row `composer-bottom` assertions.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/engine/agent.go`, `core-go/engine/tools.go` -- Define canvas-context & image attachment structs, extend `AgentState.Queue` to retain attachments, and add pinned node/viewport metadata + compact JSON formatter and off-lock PNG snapshot capture -- Centralizes queue coalescing and off-lock canvas context capture.
- [x] `core-go/agent/commands.go`, `agent.go`, `api.go`, `claudecode.go`, `context.go` -- Parse optional `contexts`/`images` in `agent_send`, capture pinned context snapshots (with PNG when backend supports vision), emit `contexts`/`images` on `user`/`queued` events, append `"image"` blocks in `runAPI`, emit `--input-format stream-json` in `runClaudeCodeStep` when images exist, and strip stale older user images in `context.go` -- Completes multimodal & canvas-context backend pipeline with full persistence.
- [x] `core-go/model/openai_chat.go`, `openai_responses.go` -- Ensure `vision: false` user messages with images still emit fallback text instead of dropping the message -- Prevents invalid provider requests.
- [x] `app/agent_store.go` -- Add `Contexts` slice on `ChatItem`, parse `images`/`contexts` on `"user"`/`"queued"` events, and extend queued coalescing/dedup -- Keeps live and restored chat transcripts accurate.
- [x] `app/agent_attachments.go` (new), `app/agent_panel.go` -- Implement pending canvas contexts & multi-image attachments (file picker `Multiple: true`, `OnPaste` for `PNG`/`FileList`, `DroppedFiles`/`FileDragOver`, downscale/cap validation, non-vision warning, removable chips/thumbnails, composer attach menu with Current selection / Pick from canvas / Canvas viewport, user-bubble rendering, and `submitAgentPrompt`) -- Delivers composer attachment UX without wrapping `composer-bottom`.
- [x] `app/canvas.go`, `app/app_state.go` -- Add floating `"+ Chat"` pill on canvas selection (clear of `frame-label` and `size-label`), right-click canvas context menu (*Attach to Agent Chat* / *Attach viewport screen*), pick-from-canvas cursor/click mode (`Esc` cancels), and `Ctrl/Cmd+L` shortcut that opens Agent tab and focuses composer without `onSelectionChanged` reverting to Design tab -- Delivers all canvas-screen attach entrypoints.
- [x] `core-go/agent/attachments_test.go` (new), `app/agent_attachments_test.go` (new) -- Unit & headless UI tests covering I/O matrix, multi-image picker/paste/drop, canvas attach entrypoints & pick mode, queue coalescing, `claude-code` stream-json stdin, history reload, context compaction, single-row `composer-bottom`, and `assertAllInteractiveControlsNamed` -- Verifies all constraints and ACs.

**Acceptance Criteria:**
- Given a document on the canvas screen, when the user triggers canvas attach via the floating `"+ Chat"` pill, right-click menu, `Ctrl/Cmd+L`, or composer *"Pick from canvas"* / *"Canvas viewport"*, then the Agent panel opens with removable context chip(s) and focused input, and sending delivers the pinned node metadata/JSON (plus PNG snapshot on vision models) even if selection changes beforehand.
- Given the Agent chat is open, when the user attaches multiple images (picker, paste, or drag-and-drop), removes one, and sends, then only remaining images reach the model (API/BYOK or `claude-code`) and render as thumbnails in the user bubble.
- Given a chat turn with canvas context and multiple images, when reloaded via History, then text, context chips, and image thumbnails restore intact.
- Given all new UI controls, when running `TestUIHomeTemplatesPresentLayersAndAccessibilityAudit` and `TestComposerBottomButtonsStayOnSingleRowWithQwen4Max`, then both pass.

## Implementation Notes

- Implemented end-to-end across `core-go` (engine/agent/model) and `app` (attachments/composer/store/canvas/shortcuts), preserving CRLF endings and keeping unrelated working-tree changes (`AGENTS.md`, `_bmad/*`) untouched.
- Addressed all 13 findings from `quick` review pass via targeted patches (off-UI-thread PNG snapshot capture in `go run()`, quit-deadlock fix on `TriggerAttachImagesDialog`, clean abort on deleted-only empty turn, queue 10-image cap check, `vision: false` OpenAI/Anthropic wire hygiene, hand-tool pick precedence, context-menu reset, viewport aspect/slide-expiry capture, component descendant/inherited JSON, budget-safe header/image alignment, >4 MB JPEG compression fallback, mutex-guarded attach loader, and `ui.Cmd` macOS shortcut test).
- Post-patch full verification (`core-go` `go vet` + `go test -count=1 ./...`, `app` `mygo vet` + `go vet` + `go test -count=1 ./...`) passed with exit 0.

## Plan Change Log

## Review Triage Log

### Pass 1 (`quick`) — Verdicts: 1 high, 12 medium, 0 low, 0 false, 0 maybe-false

| # | Location | Verdict | Route | Evidence & Action |
|---|----------|---------|-------|-------------------|
| 1 | `app/agent_panel.go:579`, `core-go/agent/agent.go:189-253` | `medium` | `patch` | Verified: `Bridge.Call("agent_send")` runs `begin()` inline on the UI thread before `go run()`, executing synchronous PNG rasterization/encoding in `CaptureCanvasContexts`. Patch: keep fast metadata/existence check in `begin()` (`includePNG=false`) and run PNG snapshot capture inside `go run()` off the UI thread. |
| 2 | `app/agent_attachments.go:293-309`, `app/app_state.go:143` | `high` | `patch` | Verified: `TriggerAttachImagesDialog` holds `a.bgWG` across `mygo.Dialog.Open` (which dispatches to the main thread), deadlocking `mygo.App.OnQuit` -> `app.Close()` -> `a.bgWG.Wait()` if quit occurs while the dialog is open. Patch: remove `a.bgWG` around `Dialog.Open` (matching `TriggerOpenDialog`), guarding `!win.IsDestroyed()`. |
| 3 | `core-go/agent/agent.go:231-266` | `medium` | `patch` | Verified: sending an attachment-only turn after all pinned nodes are deleted leaves `expanded`, `resolvedContexts`, and `modelImages` all empty, yet still calls `startTurn` with `""`. Patch: abort cleanly in `begin()` (releasing `Running` or draining next queue item) after emitting the skip toast. |
| 4 | `app/canvas.go:247-250, 296-330, 463-483, 1913-1927` | `medium` | `patch` | Verified: right-click target fields persist forever across doc switches, selection changes, left-clicks, and keyboard context menus (`Shift+F10`). Patch: clear context-menu target fields in `resetCanvasForNewDoc()`, `onSelectionChanged()`, left-click, and after menu invocation. |
| 5 | `app/canvas.go:940-945, 1903-1948` | `medium` | `patch` | Verified: `a.Tool == "hand"` is checked before `a.AgentUI.PickingCanvasCtx` in both `computeCanvasCursor` and `onCanvasPointerDown`, blocking pick mode when Hand tool is active. Patch: evaluate `PickingCanvasCtx` before `a.Tool == "hand"` (preserving middle-click and Space+Left pan). |
| 6 | `core-go/engine/tools.go:307-340` | `medium` | `patch` | Verified: overflowing `maxContextTotalTextBytes` omits a context's entire text section while still appending its PNG snapshot and chip, misaligning `image #N` indices. Patch: always include every resolved context's metadata + `image #N` header line and trim/omit only its JSON body when budget is exhausted. |
| 7 | `core-go/engine/tools.go:361-399, 417-428` | `medium` | `patch` | Verified: clamping `pw` to 64 while computing `zoom = pw / vp[2]` crops tall narrow viewports, and `captureViewportContext` skips `e.ExpireSlides()` unlike `SnapshotForRender`. Patch: call `e.ExpireSlides()` before `e.Sync()`, clamp `pw`/`ph` to `max(1, ...)`, and use `min(pw/vp[2], ph/vp[3])` for `zoom`. |
| 8 | `core-go/agent/api.go:75-98, 128-140, 297` | `medium` | `patch` | Verified: on `vision: false` BYOK providers, OpenAI formats still store/token-count raw image blocks in `e.Agent.Messages`, and Anthropic-format `vision: false` providers still receive prior-turn or tool-result `"image"` blocks. Patch: replace user images with blind-vision text note for all `!supportsVision` backends, and strip remaining `"image"` blocks in `apiWireMessages` when `!supportsVision`. |
| 9 | `core-go/agent/agent.go:160-184, 650-654` | `medium` | `patch` | Verified: queueing multiple turns whose combined images exceed 10 succeeds and renders queued bubbles, then `takeQueueEntry` silently drops excess images. Patch: enforce total queued images `<= MaxAttachedImages` in `SendWithAttachments` under lock, returning a clear error when exceeded. |
| 10 | `app/agent_attachments_test.go:137, 141, 149` | `medium` | `patch` | Verified: test sends `ui.Ctrl` while `agent-input` is focused (`takesText == true`), whereas `OnWindowShortcut` registers `ui.Cmd` (`ui.Super` on macOS). Patch: use `ui.Cmd` instead of `ui.Ctrl` in the test. |
| 11 | `app/agent_attachments.go:292-385`, `app/agent_panel.go:209` | `medium` | `patch` | Verified: single `AttachLoading` bool clears early on overlapping loads, `TriggerAttachImagesDialog` never sets loading during decode, and non-blocking send to cap-8 `attachDone` silently drops burst batches. Patch: replace channel + bool with a mutex-guarded result slice and `attachPending int` counter. |
| 12 | `core-go/engine/tools.go:473-486, 573-579` | `medium` | `patch` | Verified: slash-path component descendants (`"inst/child"`) have `e.Doc.Find(id) == nil` so JSON summary is omitted, and `"ref"` instances omit inherited props. Patch: resolve slash-path descendants via `res.Node`/`res.Resolve`/`Find` and include `res.Inherited(raw)` for `"ref"` nodes using existing exported `resolve` APIs. |
| 13 | `app/agent_attachments.go:545-564` | `medium` | `patch` | Verified: oversized non-JPEG images (`> 4 MB`) within `1568px` re-encode as lossless PNG with no post-encode size check, allowing payloads > 4 MB / Anthropic's 5 MB API limit. Patch: if encoded bytes still exceed `maxEncodedImageBytes`, convert to JPEG stepping down quality and dimensions until `<= maxEncodedImageBytes`. |

## Design Notes

- **Composer layout & width budget:** Place the compact attachment trigger strip (`+ Canvas` popover + `+ Image` button + non-vision warning) and removable attachment preview tray directly above `agent-input` (alongside active skill chips), leaving `composer-bottom` untouched so `TestComposerBottomButtonsStayOnSingleRowWithQwen4Max` stays single-row at `RightW = 300`.
- **Off-lock capture & downscaling:** Image file reads/decodes and downscaling (max 1568px long side, PNG/JPEG) run off the UI thread / outside `Engine.Lock()`. Canvas node/viewport PNG capture snapshots under a brief lock and rasterizes off-lock before building the user message.
- **Claude Code multimodal stdin:** When `len(images) == 0`, keep existing plain-text stdin untouched; when `len(images) > 0`, add `--input-format stream-json` and write one JSON line `{"type":"user","message":{"role":"user","content":[...]}}\n`.

## Verification

**Commands:**
- `cd core-go && CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./...` -- expected: exit 0, all core-go & new attachment/multimodal/compaction tests pass
- `cd app && CGO_ENABLED=1 go tool mygo vet . && CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./...` -- expected: exit 0, `mygo vet`, a11y audit, single-row composer, and UI attachment tests pass

**Manual checks (if no CLI):**
- Launch `cd app && CGO_ENABLED=1 go tool mygo dev`, select a frame on canvas, test floating `"+ Chat"`, right-click menu, `Ctrl/Cmd+L`, and *"Pick from canvas"*; attach 2+ images via picker/paste/drag-and-drop; send a turn, reload from History, and verify chips, thumbnails, and agent output.
