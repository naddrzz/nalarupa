# Test Automation Summary: MCP API, documents/export, save & reopen

Date: 2026-10-18 · Branch: `qa/automated-checks` · Framework: Bun scripts with `assert` (project convention, no test runner), GPUIX automation for the window.

## Generated Tests

### API Tests
- [x] `app/scripts/mcp-check.ts`: MCP HTTP contract. 404 on unknown paths, 405 + `Allow: POST` on GET, 403 for a foreign `Origin` (DNS-rebinding guard) while localhost is allowed, 202 for notifications, JSON-RPC -32700 / -32601, batches, `initialize`, per-agent tool lists, and happy path + error for every tool (`get_editor_state`, `read_guide`, `get_style`, `execute` incl. rollback on failure, `open_document`, `spawn_agents` refused for external agents).
- [x] `app/scripts/doc-check.ts`: core commands through FFI. Save → open → save is byte-identical (unicode names included); the dirty flag; open failures (missing file, invalid JSON, no path) keep the current document; select / move_by / undo / redo / reorder front+back / delete; variables (set, undo, missing name); export png / jpg / webp / svg / pdf (page count) / html-css plus unknown format, missing node and empty-deck errors; `png` command; settings persisted, API key masked, null removes, missing key rejected.

### E2E Tests
- [x] `app/scripts/file-e2e.ts`: through the window. Draw → toolbar Save writes `Documents/Nalarupa/Untitled.nala` → second Save after a paste updates the same file (no numbered copy) → Home lists it under Recent → reopening shows both rectangles → a recent file deleted from disk is no longer offered.

All three use a throwaway `NALARUPA_DATA` folder (the API checks re-run themselves in a child, because `process.env` writes don't reach the core's `getenv`) and clean up what they write.

## Bugs found and fixed (`app/src/App.tsx`)
1. **Home went blank while a toast was showing**, e.g. right after Save → Home. Home and the toast were two window-level siblings; they now share one root, like the editor.
2. **Recent never showed files saved or opened during the session.** Home got the list from the startup `init` snapshot; it now reads it fresh.

`file-e2e` fails on the old `App.tsx` (verified) and passes 3/3 with the fix.

## Coverage
- MCP endpoints/methods: 5/5 tools, 4/4 JSON-RPC methods, the HTTP error statuses 403, 404 and 405 (the 400 for an unreadable body is not exercised; previously 2 tools, no error paths).
- Core FFI commands not exercised by any script: `bounds`, `live`, `viewport`, `chat_clear`. `drag_begin`, `drag_end` and `chat_load` are covered only through UI flows in `editor-e2e`/`e2e`.
- UI flows: Save / Recent / reopen now covered; the Open… and Save-as dialogs (OS file pickers) are not automatable and stay manual.

## Verification
- On the VPS runner (Ubuntu, headless sway + lavapipe): `mcp-check`, `doc-check`, `file-e2e` (×3) pass; `bun run typecheck` passes. A mutation run confirmed `doc-check` assertions fail when the expected value is wrong.
- Not run on Windows yet. The `App.tsx` fixes are platform-independent React changes.

## Next Steps
- CI (`ci.yml`) runs the new checks on every PR; nightly QA and the Claude dev workflow include them too.
- Add `bounds`/`viewport`/`live` to `doc-check` if those commands start changing.
