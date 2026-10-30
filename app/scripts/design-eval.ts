// Design agent eval (manual: every run spends model tokens, so it is not in CI).
//   bun scripts/design-eval.ts run [--backend claude-code|api] [--model id] [--only id,id] [--out dir]
//   bun scripts/design-eval.ts compare <runA> <runB> [--out page.html]
//   bun scripts/design-eval.ts summary <run>
// `run` sends every eval brief through the design agent headlessly and keeps an eval run folder: each brief's
// top-level frames as PNGs, its document, its transcript, and metrics.json + summary.md. `compare` writes one page
// with two eval runs side by side and B − A for every metric; `summary` reprints a run's summary.md. Both flag
// unrelated briefs whose results look alike (sameness is a defect): only briefs of one kind are compared, since a
// phone screen and a page differ by shape alone, and the existing-style brief is meant to look like its fixture.
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { dirname, join, relative, resolve } from 'node:path'
import { decodePng } from './png'

type Kind = 'page' | 'mobile' | 'slides' | 'existing'
type Brief = { id: string; kind: Kind; text: string }

// Written as a person would ask, with real subject matter and no look requested.
const BRIEFS: Brief[] = [
  { id: 'bakery', kind: 'page', text: 'Landing page for Sourdough & Co, a neighbourhood bakery in Leeds. It opens at 7am, bakes three loaves a day in a wood-fired oven, and takes pre-orders for Saturday pickup.' },
  { id: 'ledgerline', kind: 'page', text: 'Landing page for Ledgerline, accounting software for freelance translators: it tracks invoices in several currencies, sets tax aside every month, and exports everything for the annual return.' },
  { id: 'docfest', kind: 'page', text: 'One-page site for Olhar, a three-day documentary film festival in Lisbon (12–14 September): programme highlights, the three venues, and ticket prices.' },
  { id: 'climb', kind: 'mobile', text: 'Home screen of a mobile app for people learning to climb indoors: today\'s session plan, progress through the grades, and the next booked wall slot.' },
  { id: 'trains', kind: 'mobile', text: 'Mobile app screen that shows a commuter their next trains from Utrecht Centraal to Amsterdam Zuid, with delays and platforms, and a way to save the route.' },
  { id: 'roastery', kind: 'slides', text: 'A 5-slide deck for the quarterly review of Halden, a small coffee roastery: revenue for the quarter, the new wholesale clients, a green-coffee sourcing problem, and next quarter\'s plan.' },
  { id: 'bridges', kind: 'slides', text: 'A 5-slide deck for a 10-minute talk to secondary-school students on why bridges don\'t fall down.' },
  { id: 'fernway', kind: 'existing', text: 'Add a pricing page for Fernway next to the home page: three subscription plans and a short FAQ.' },
]

// The existing-style fixture: Fernway (a houseplant care subscription) with its own variables, a button component and a
// finished home page. Built through `execute` so it always matches the current schema.
const FIXTURE = String.raw`
SetVariables({ink: {type: "color", value: "#1F2A1C"}, paper: {type: "color", value: "#F4EFE6"}, moss: {type: "color", value: "#4F6B3A"}, clay: {type: "color", value: "#C8643B"}, space: {type: "number", value: 24}})
kit = Insert(document, {type: "frame", name: "Components", x: 0, y: -260, layout: "horizontal", gap: 24, padding: 32, fill: "#FFFFFF"})
btn = Insert(kit, {type: "frame", name: "Button", reusable: true, padding: [14, 26], cornerRadius: 999, fill: "$clay", alignItems: "center"})
btnLabel = Insert(btn, {type: "text", name: "Label", content: "Start my plan", fontFamily: "Work Sans", fontSize: 16, fontWeight: "600", fill: "$paper"})
home = Insert(document, {type: "frame", name: "Fernway Home", x: 0, y: 0, width: 1440, layout: "vertical", fill: "$paper", clip: true})
nav = Insert(home, {type: "frame", name: "Nav", width: "fill_container", padding: [28, 96], justifyContent: "space_between", alignItems: "center"})
Insert(nav, {type: "text", name: "Logo", content: "fernway", fontFamily: "Fraunces", fontSize: 30, fontWeight: "600", fill: "$moss"})
links = Insert(nav, {type: "frame", name: "Links", gap: 36, alignItems: "center"})
for (const l of ["How it works", "Plants", "Journal"]) Insert(links, {type: "text", name: l, content: l, fontFamily: "Work Sans", fontSize: 16, fill: "$ink"})
hero = Insert(home, {type: "frame", name: "Hero", width: "fill_container", padding: [96, 96, 120, 96], gap: 64, alignItems: "end"})
copy = Insert(hero, {type: "frame", name: "Copy", width: "fill_container", layout: "vertical", gap: "$space"})
Insert(copy, {type: "text", name: "Kicker", content: "PLANT CARE, POSTED MONTHLY", fontFamily: "Work Sans", fontSize: 14, fontWeight: "600", letterSpacing: 2, fill: "$moss"})
Insert(copy, {type: "text", name: "Headline", content: "Your plants, looked after by people who know them.", textGrowth: "fixed-width", width: "fill_container", fontFamily: "Fraunces", fontSize: 72, lineHeight: 1.05, fill: "$ink"})
Insert(copy, {type: "text", name: "Lede", content: "Every month a Fernway gardener checks your plants over video, then sends the feed, soil and pots they need.", textGrowth: "fixed-width", width: 520, fontFamily: "Work Sans", fontSize: 19, lineHeight: 1.5, fill: "$ink"})
Insert(copy, {type: "ref", ref: btn, name: "Hero CTA"})
Insert(hero, {type: "frame", name: "Photo", width: 520, height: 600, cornerRadius: [260, 260, 0, 0], fill: "$moss"})
steps = Insert(home, {type: "frame", name: "How it works", width: "fill_container", padding: [96, 96], gap: 48, fill: "#EAE3D6"})
for (const [n, t, d] of [["01", "Tell us your plants", "Snap a photo of each one; we identify it and its light."], ["02", "Meet your gardener", "A monthly video check-in with the same person every time."], ["03", "Get the right kit", "Feed, soil and pots arrive sized for your plants."]]) {
  const s = Insert(steps, {type: "frame", name: "Step " + n, width: "fill_container", layout: "vertical", gap: 12})
  Insert(s, {type: "text", name: "Number", content: n, fontFamily: "Fraunces", fontSize: 40, fill: "$clay"})
  Insert(s, {type: "text", name: "Title", content: t, fontFamily: "Work Sans", fontSize: 22, fontWeight: "600", fill: "$ink"})
  Insert(s, {type: "text", name: "Body", content: d, textGrowth: "fixed-width", width: "fill_container", fontFamily: "Work Sans", fontSize: 17, lineHeight: 1.5, fill: "$ink"})
}
`

const BRIEF_TIMEOUT = 30 * 60_000
// Web fonts download in the background after a node first asks for them.
const FONT_WAIT = 8000
// Similarity at or above which two unrelated briefs count as looking alike (calibrated on the baseline eval run).
const SAME = { colour: 0.93, layout: 0.85 }
const ASPECT: Record<Kind, number> = { page: 0.625, mobile: 2.16, slides: 0.5625, existing: 0.625 }

const args = process.argv.slice(2)
const flag = (name: string) => {
  const i = args.indexOf(`--${name}`)
  return i >= 0 ? args[i + 1] : undefined
}

/** One grid of colours sampled over a frame's first screen, for comparing how alike two results look. */
type Thumb = { w: number; h: number; px: number[] }
const thumb = (file: string, kind: Kind): Thumb => {
  const img = decodePng(file)
  const cropH = Math.min(img.h, img.w * ASPECT[kind])
  const w = 16
  const h = Math.round(16 * ASPECT[kind])
  const px: number[] = []
  for (let gy = 0; gy < h; gy++)
    for (let gx = 0; gx < w; gx++) {
      const sum = [0, 0, 0]
      let n = 0
      for (let y = Math.floor((gy * cropH) / h); y < Math.floor(((gy + 1) * cropH) / h); y++)
        for (let x = Math.floor((gx * img.w) / w); x < Math.floor(((gx + 1) * img.w) / w); x++) {
          const [r, g, b, a] = img.at(x, y)
          const k = (a ?? 255) / 255
          sum[0] += r * k + 255 * (1 - k)
          sum[1] += g * k + 255 * (1 - k)
          sum[2] += b * k + 255 * (1 - k)
          n++
        }
      px.push(...sum.map((v) => v / Math.max(1, n)))
    }
  return { w, h, px }
}
/** Colour: 1 = identical colour grids, 0 = opposite. Layout: correlation of the two lightness grids, so the same
 * structure in another palette still scores high (1 = same structure, 0 = unrelated). */
const similarity = (a: Thumb, b: Thumb) => {
  const colour = 1 - a.px.reduce((s, v, i) => s + Math.abs(v - b.px[i]), 0) / a.px.length / 255
  const light = (t: Thumb) => t.px.filter((_, i) => i % 3 === 0).map((r, i) => (r + t.px[i * 3 + 1] + t.px[i * 3 + 2]) / 3)
  const [x, y] = [light(a), light(b)]
  const mean = (v: number[]) => v.reduce((s, n) => s + n, 0) / v.length
  const [mx, my] = [mean(x), mean(y)]
  let [xy, xx, yy] = [0, 0, 0]
  for (let i = 0; i < x.length; i++) {
    xy += (x[i] - mx) * (y[i] - my)
    xx += (x[i] - mx) ** 2
    yy += (y[i] - my) ** 2
  }
  return { colour, layout: xx && yy ? Math.max(0, xy / Math.sqrt(xx * yy)) : 0 }
}

type Shot = { file: string; name: string; w: number; h: number }
type BriefResult = {
  id: string; kind: Kind; brief: string; ok: boolean; error?: string; wallMs: number
  turns: number; tokens: { input: number; output: number; cacheRead: number; cacheWrite: number }; costUsd: number
  executeCalls: number; failedExecutes: number; repairs: number; issues: string[]
  instructionsChars: number; guideReads: { tool: string; arg: string; chars: number }[]; guideChars: number
  firstMessage: string; deniedTools: string[]; shots: Shot[]
}
type Run = { meta: { sha: string; backend: string; model: string | null; date: string }; briefs: BriefResult[] }
type Pair = { a: string; b: string; colour: number; layout: number }
const alike = (p: Pair) => p.colour >= SAME.colour || p.layout >= SAME.layout
const minutes = (ms: number) => (ms / 60_000).toFixed(1)

const isTool = (name: string, tool: string) => name === tool || name.endsWith(`__${tool}`)

/** Pairs of different briefs of the same kind, most alike first; the existing-style brief should look like its fixture, so it is left out. */
const sameness = (dir: string, run: Run): Pair[] => {
  const thumbs = run.briefs.filter((r) => r.shots[0] && r.kind !== 'existing').map((r) => ({ r, t: thumb(join(dir, r.shots[0].file), r.kind) }))
  const pairs: Pair[] = []
  for (let i = 0; i < thumbs.length; i++)
    for (let j = i + 1; j < thumbs.length; j++)
      if (thumbs[i].r.kind === thumbs[j].r.kind) pairs.push({ a: thumbs[i].r.id, b: thumbs[j].r.id, ...similarity(thumbs[i].t, thumbs[j].t) })
  return pairs.sort((x, y) => Math.max(y.colour, y.layout) - Math.max(x.colour, x.layout))
}

const totals = (run: Run) => {
  const sum = (f: (r: BriefResult) => number) => run.briefs.reduce((s, r) => s + f(r), 0)
  return {
    briefs: `${run.briefs.filter((r) => r.ok).length}/${run.briefs.length}`,
    issues: sum((r) => r.issues.length),
    failedExecutes: sum((r) => r.failedExecutes),
    repairs: sum((r) => r.repairs),
    executeCalls: sum((r) => r.executeCalls),
    turns: sum((r) => r.turns),
    minutes: +minutes(sum((r) => r.wallMs)),
    costUsd: +sum((r) => r.costUsd).toFixed(2),
    instructionsChars: run.briefs[0]?.instructionsChars ?? 0,
    guideChars: Math.round(sum((r) => r.guideChars) / Math.max(1, run.briefs.length)),
  }
}

const summary = (dir: string, run: Run) => {
  const rows = run.briefs.map((r) => `| ${r.id} | ${r.ok ? 'ok' : `**${r.error}**`} | ${r.issues.length} | ${r.failedExecutes} | ${r.repairs} | ${r.executeCalls} | ${r.turns} | ${minutes(r.wallMs)} | ${r.guideChars} |`)
  const t = totals(run)
  const pairs = sameness(dir, run)
  return [
    `# Eval run ${run.meta.date} (${run.meta.sha}, ${run.meta.backend}${run.meta.model ? `, ${run.meta.model}` : ''})`,
    '',
    '| Brief | Result | Issues detected | Failed executes | Repair attempts | Executes | Turns | Minutes | Guide chars read |',
    '|---|---|---|---|---|---|---|---|---|',
    ...rows,
    `| **Total** | ${t.briefs} | ${t.issues} | ${t.failedExecutes} | ${t.repairs} | ${t.executeCalls} | ${t.turns} | ${t.minutes} | ${t.guideChars} avg |`,
    '',
    `Always-loaded instructions: ${t.instructionsChars} chars. Cost: $${t.costUsd}.`,
    '',
    '## Sameness (unrelated briefs of one kind)',
    '',
    ...pairs.map((p) => `- ${p.a} / ${p.b}: colour ${p.colour.toFixed(3)}, layout ${p.layout.toFixed(3)}${alike(p) ? ' **looks alike**' : ''}`),
    '',
  ].join('\n')
}

const page = (out: string, runs: [string, Run][]) => {
  const esc = (s: string) => s.replace(/[&<>"]/g, (c) => `&#${c.charCodeAt(0)};`)
  const ts = runs.map(([d, r]) => [d, r, totals(r), sameness(d, r)] as const)
  const keys = Object.keys(ts[0][2]) as (keyof ReturnType<typeof totals>)[]
  const head = `<tr><th></th>${ts.map(([d, r]) => `<th>${esc(r.meta.date)}<br><small>${esc(r.meta.sha)} · ${esc(r.meta.backend)} · ${esc(relative(dirname(out), d))}</small></th>`).join('')}<th>B − A</th></tr>`
  const delta = (k: (typeof keys)[number]) => {
    const [a, b] = [ts[0][2][k], ts[1][2][k]]
    return typeof a === 'number' && typeof b === 'number' ? `${b - a > 0 ? '+' : ''}${+(b - a).toFixed(2)}` : ''
  }
  const metric = keys.map((k) => `<tr><td>${k}</td>${ts.map(([, , t]) => `<td>${t[k]}</td>`).join('')}<td>${delta(k)}</td></tr>`).join('')
  const same = `<tr><td>looks alike</td>${ts.map(([, , , p]) => `<td>${p.filter(alike).map((x) => `${x.a}/${x.b} colour ${x.colour.toFixed(2)} layout ${x.layout.toFixed(2)}`).join('<br>') || 'none'}</td>`).join('')}<td></td></tr>`
  const ids = [...new Set(runs.flatMap(([, r]) => r.briefs.map((b) => b.id)))]
  const briefs = ids.map((id) => {
    const cells = ts.map(([d, r]) => {
      const b = r.briefs.find((x) => x.id === id)
      if (!b) return '<td>not run</td>'
      const imgs = b.shots.map((s) => [esc(relative(dirname(out), join(d, s.file))), esc(s.name)]).map(([src, name]) => `<a href="${src}"><img src="${src}" title="${name}"></a>`).join('')
      return `<td><p class="m">${b.ok ? '' : `<b>${esc(b.error ?? '')}</b> · `}issues ${b.issues.length} · failed ${b.failedExecutes} · repair attempts ${b.repairs} · turns ${b.turns} · ${minutes(b.wallMs)} min</p><blockquote>${esc(b.firstMessage || '(no message before the first execute)')}</blockquote><div class="shots">${imgs}</div></td>`
    })
    const [a, b] = runs.map(([, r]) => r.briefs.find((x) => x.id === id))
    const d = (f: (r: BriefResult) => number) => (a && b ? `${f(b) - f(a) > 0 ? '+' : ''}${+(f(b) - f(a)).toFixed(1)}` : '–')
    const diff = `<p class="m">B − A: issues ${d((r) => r.issues.length)} · failed ${d((r) => r.failedExecutes)} · repair attempts ${d((r) => r.repairs)} · turns ${d((r) => r.turns)} · minutes ${d((r) => r.wallMs / 60_000)}</p>`
    const text = (a ?? b)?.brief ?? ''
    return `<h2>${esc(id)}</h2><p>${esc(text)}</p>${diff}<table class="side"><tr>${cells.join('')}</tr></table>`
  })
  writeFileSync(out, `<!doctype html><meta charset="utf-8"><title>Design eval</title><style>
body{font:14px/1.45 system-ui,sans-serif;margin:24px;color:#222;background:#fff}table{border-collapse:collapse}td,th{border:1px solid #ddd;padding:6px 10px;vertical-align:top;text-align:left}
.side{width:100%;table-layout:fixed}.shots{display:flex;flex-wrap:wrap;gap:8px}.shots img{max-width:100%;max-height:900px;border:1px solid #ccc}.m{color:#555;margin:0 0 6px}blockquote{margin:0 0 8px;padding-left:10px;border-left:3px solid #ccc;color:#444;white-space:pre-wrap}
</style><h1>Design eval</h1><table>${head}${metric}${same}</table>${briefs.join('')}`)
}

const run = async () => {
  const { call } = await import('../src/core')
  const out = resolve(process.env.EVAL_OUT!)
  const backend = flag('backend') ?? 'claude-code'
  const model = flag('model') ?? null
  const only = flag('only')?.split(',')
  const briefs = BRIEFS.filter((b) => !only || only.includes(b.id))
  const { mcpPort } = call('init')
  const tool = async (name: string, a: object) => {
    const r = await fetch(`http://127.0.0.1:${mcpPort}/mcp`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: a } }) })
    const res = (await r.json()).result
    return { text: res.content.map((c: any) => c.text ?? '').join(''), isError: !!res.isError }
  }
  call('settings_set', { key: 'agent_backend', value: backend })
  call('settings_set', { key: 'agent_model', value: model })
  const sha = Bun.spawnSync(['git', 'rev-parse', '--short', 'HEAD']).stdout.toString().trim()
  const result: Run = { meta: { sha, backend, model, date: new Date().toISOString().slice(0, 16).replace('T', ' ') }, briefs: [] }
  const instructionsChars = call<string>('agent_instructions', { backend }).length
  for (const b of briefs) {
    const dir = join(out, b.id)
    mkdirSync(dir, { recursive: true })
    call('agent_reset')
    call('new', { template: 'empty' })
    if (b.kind === 'existing') {
      const f = await tool('execute', { input: FIXTURE })
      if (f.isError) throw new Error(`the existing-style fixture failed: ${f.text}`)
      await Bun.sleep(FONT_WAIT)
    }
    const before = new Set(call<any[]>('frames').map((f) => f.id))
    const r: BriefResult = {
      id: b.id, kind: b.kind, brief: b.text, ok: true, wallMs: 0, turns: 0, tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, costUsd: 0,
      executeCalls: 0, failedExecutes: 0, repairs: 0, issues: [], instructionsChars, guideReads: [], guideChars: 0, firstMessage: '', deniedTools: [], shots: [],
    }
    const transcript: string[] = [`# ${b.id}\n\n> ${b.text}\n`]
    const text = new Map<string, string>()
    let executed = false
    console.log(`== ${b.id}`)
    call('agent_send', { text: b.text })
    const started = Date.now()
    for (;;) {
      await Bun.sleep(1000)
      const { state, events } = call('poll')
      for (const e of events) {
        if (e.type !== 'agent') continue
        const who = e.agent?.name ?? e.agent?.id ?? 'main'
        if (e.kind === 'text_delta') text.set(who, (text.get(who) ?? '') + e.text)
        if (e.kind === 'message_end' || e.kind === 'tool_start') {
          const t = text.get(who)?.trim()
          if (t) {
            transcript.push(`**${who}:** ${t}\n`)
            if (!executed && !r.firstMessage) r.firstMessage = t
          }
          text.delete(who)
        }
        if (e.kind === 'tool_start' && isTool(e.name, 'execute')) executed = true
        if (e.kind === 'tool_done') {
          transcript.push(`- \`${e.name}\`${e.error ? ' **failed**' : ''}${e.issues?.length ? `, ${e.issues.length} issues` : ''}`)
          if (isTool(e.name, 'execute')) {
            r.executeCalls++
            if (e.error) r.failedExecutes++
            if (e.input?.editId) r.repairs++
            r.issues.push(...(e.issues ?? []))
          }
          for (const t of ['read_guide', 'get_style'])
            if (isTool(e.name, t)) {
              const arg = String(e.input?.topic ?? e.input?.name ?? '')
              const chars = (await tool(t, t === 'read_guide' ? { topic: arg } : { name: arg })).text.length
              r.guideReads.push({ tool: t, arg, chars })
              r.guideChars += chars
            }
        }
        if (e.kind === 'permission') {
          r.deniedTools.push(e.tool)
          call('agent_permission', { id: e.id, answer: 'deny' })
        }
        if (e.kind === 'error') {
          r.ok = false
          r.error = String(e.text).slice(0, 200)
        }
        if (e.kind === 'done') {
          const u = e.usage ?? {}
          r.turns += Number(u.turns ?? 0)
          r.costUsd += Number(u.costUsd ?? 0)
          for (const k of Object.keys(r.tokens) as (keyof BriefResult['tokens'])[]) r.tokens[k] += Number(u[k] ?? 0)
        }
      }
      if (!state.agentRunning && Date.now() - started > 3000) break
      if (Date.now() - started > BRIEF_TIMEOUT) {
        call('agent_stop')
        r.ok = false
        r.error = 'timeout'
        break
      }
    }
    r.wallMs = Date.now() - started
    await Bun.sleep(FONT_WAIT)
    const frames = call<any[]>('frames').filter((f) => f.depth === 0 && f.type === 'frame' && !before.has(f.id))
    // Slides keep document order; a page or screen leads with its largest frame.
    if (b.kind !== 'slides') frames.sort((x, y) => y.w * y.h - x.w * x.h)
    for (const [i, f] of frames.entries()) {
      const file = join(b.id, `${i + 1}-${String(f.name).replace(/[^\w-]+/g, '_').slice(0, 40)}.png`)
      writeFileSync(join(out, file), Buffer.from(call<string>('png', { id: f.id, scale: Math.min(1, 1600 / f.w) }), 'base64'))
      r.shots.push({ file, name: f.name, w: Math.round(f.w), h: Math.round(f.h) })
    }
    if (!frames.length && r.ok) {
      r.ok = false
      r.error = 'no frames'
    }
    call('save', { path: join(dir, `${b.id}.pen`) })
    writeFileSync(join(dir, 'transcript.md'), transcript.join('\n') + '\n')
    result.briefs.push(r)
    console.log(`   ${r.ok ? 'ok' : r.error} · issues ${r.issues.length} · failed ${r.failedExecutes} · turns ${r.turns} · ${minutes(r.wallMs)} min`)
  }
  writeFileSync(join(out, 'metrics.json'), JSON.stringify(result, null, 2))
  const md = summary(out, result)
  writeFileSync(join(out, 'summary.md'), md)
  console.log(`\n${md}\nEval run: ${out}`)
  process.exit(0)
}

const load = (dir: string): Run => JSON.parse(readFileSync(join(dir, 'metrics.json'), 'utf8'))

if (process.env.EVAL_OUT) await run()
else if (args[0] === 'run') {
  // A fresh data folder keeps the eval out of the person's settings and chat history (the core reads
  // NALARUPA_DATA through libc, so it is set for a child process).
  const stamp = new Date().toISOString().slice(0, 16).replace(/[:T]/g, '-')
  const out = resolve(flag('out') ?? join(homedir(), 'nalarupa-eval', stamp))
  mkdirSync(out, { recursive: true })
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-eval-'))
  const child = Bun.spawnSync([process.execPath, import.meta.path, ...args], { env: { ...process.env, NALARUPA_DATA: data, EVAL_OUT: out }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(child.exitCode ?? 1)
} else if (args[0] === 'compare' && args[1] && args[2]) {
  const dirs = [resolve(args[1]), resolve(args[2])]
  const out = resolve(flag('out') ?? join(dirs[1], 'compare.html'))
  page(out, dirs.map((d) => [d, load(d)] as [string, Run]))
  console.log(out)
} else if (args[0] === 'summary' && args[1]) {
  console.log(summary(resolve(args[1]), load(resolve(args[1]))))
} else {
  console.error('usage: design-eval.ts run [--backend claude-code|api] [--model id] [--only id,id] [--out dir]\n       design-eval.ts compare <runA> <runB> [--out page.html]\n       design-eval.ts summary <run>')
  process.exit(2)
}
