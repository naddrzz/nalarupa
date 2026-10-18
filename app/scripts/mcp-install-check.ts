// "Connect an agent" through the core (no window): `bun scripts/mcp-install-check.ts`
// `mcp_install` writes the MCP server entry into Cursor's and Codex's config files and registers it with the
// Claude Code CLI. Runs against a throwaway HOME and a stand-in `claude`, so no real client config is touched.
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`mcp-install-check failed: ${msg}`)
}
const throws = (fn: () => unknown, re: RegExp, msg: string) => {
  try {
    fn()
  } catch (e) {
    return assert(re.test((e as Error).message), `${msg}: wrong error "${(e as Error).message}"`)
  }
  assert(false, `${msg}: did not throw`)
}
// The core reads HOME and NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a
// child with a throwaway home and a stand-in `claude` that records its arguments and answers as $HOME/claude-mode says.
if (process.platform === 'win32') {
  console.log('mcp-install-check skipped on Windows (the stand-in claude is a shell script)')
  process.exit(0)
}
if (!process.env.NALARUPA_CHECK_CHILD) {
  const root = mkdtempSync(join(tmpdir(), 'nalarupa-install-'))
  const home = join(root, 'home')
  const bin = join(root, 'bin')
  mkdirSync(home)
  mkdirSync(bin)
  const script = '#!/bin/sh\necho "$@" > "$HOME/claude-args"\ncase "$(cat "$HOME/claude-mode" 2>/dev/null)" in\n  exists) echo "MCP server nalarupa already exists in user config" >&2; exit 1 ;;\n  fail) echo "boom: not logged in" >&2; exit 1 ;;\nesac\n'
  writeFileSync(join(bin, 'claude'), script, { mode: 0o755 })
  const env = { ...process.env, HOME: home, NALARUPA_DATA: join(root, 'data'), PATH: `${bin}${delimiter}${process.env.PATH ?? ''}`, NALARUPA_CHECK_CHILD: '1' }
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(root, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const home = process.env.HOME!
const { mcpUrl } = call('init')
assert(/^http:\/\/127\.0\.0\.1:\d+\/mcp$/.test(mcpUrl), `the server URL: ${mcpUrl}`)
const install = (client: string) => call<string>('mcp_install', { client })
const cursor = join(home, '.cursor', 'mcp.json')
const codex = join(home, '.codex', 'config.toml')

// Cursor: creates the file, then merges into it without touching the user's other servers.
assert(install('cursor').includes(cursor), 'Cursor install names the file it wrote')
assert(JSON.parse(readFileSync(cursor, 'utf8')).mcpServers.nalarupa.url === mcpUrl, 'Cursor config points at the MCP server')
writeFileSync(cursor, JSON.stringify({ theme: 'dark', mcpServers: { other: { url: 'http://example.test/mcp' }, nalarupa: { url: 'http://stale' } } }))
install('cursor')
const merged = JSON.parse(readFileSync(cursor, 'utf8'))
assert(merged.theme === 'dark' && merged.mcpServers.other.url === 'http://example.test/mcp', 'other settings and servers survive')
assert(merged.mcpServers.nalarupa.url === mcpUrl, 'a stale nalarupa entry is refreshed')
// A config that is not a JSON object is refused, not overwritten.
writeFileSync(cursor, '[1,2]')
throws(() => install('cursor'), /not a JSON object/, 'Cursor config that is an array')
assert(readFileSync(cursor, 'utf8') === '[1,2]', 'the refused file is left untouched')

// Codex: appends a TOML table once; running it again does not duplicate it.
mkdirSync(join(home, '.codex'))
writeFileSync(codex, 'model = "gpt-5"\n')
assert(install('codex').includes(codex), 'Codex install names the file it wrote')
const toml = readFileSync(codex, 'utf8')
assert(toml.startsWith('model = "gpt-5"\n') && toml.includes(`[mcp_servers.nalarupa]\nurl = "${mcpUrl}"`), `Codex config keeps the user's keys and adds the server: ${toml}`)
assert(/already configured/.test(install('codex')) && readFileSync(codex, 'utf8') === toml, 'a second Codex install changes nothing')

// Claude Code: registers over HTTP at user scope through the CLI; "already exists" counts as installed.
const args = () => readFileSync(join(home, 'claude-args'), 'utf8').trim()
assert(install('claude-code').includes(mcpUrl), 'Claude Code install reports the URL')
assert(args() === `mcp add --transport http --scope user nalarupa ${mcpUrl}`, `claude is called with: ${args()}`)

// The CLI saying the server exists already is a success; any other failure surfaces its message.
writeFileSync(join(home, 'claude-mode'), 'exists')
assert(install('claude-code').includes(mcpUrl), 'an existing registration counts as installed')
writeFileSync(join(home, 'claude-mode'), 'fail')
throws(() => install('claude-code'), /boom: not logged in/, 'the CLI failing')

// Errors: unknown client.
throws(() => install('vscode'), /unknown client 'vscode'/, 'unknown client')
throws(() => install(''), /unknown client ''/, 'missing client')
assert(!existsSync(join(home, '.vscode')), 'an unknown client writes nothing')
console.log('mcp-install-check ok')
process.exit(0)
