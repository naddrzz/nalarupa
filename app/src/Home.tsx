import { useState } from 'react'
import { basename } from 'node:path'
import { existsSync } from 'node:fs'
import { useGpuixRequired } from '@gpuix/react'
import * as Tooltip from '@gpuix/react/tooltip'
import { call } from './core'
import { C, FONT } from './theme'
import { Button, Icon, Pressable, T, type Toast } from './ui'
import type { IconName } from './icons'
import logo from '../assets/brand/nalarupa.svg' with { type: 'text' }

// A data URL, so the compiled exe (whose bundled files are not on disk) paints it too.
const LOGO = `data:image/svg+xml;base64,${Buffer.from(logo).toString('base64')}`

const TEMPLATES: { id: string; title: string; desc: string; icon: IconName }[] = [
  { id: 'blank', title: 'Design', desc: 'A 1440 desktop frame', icon: 'frame' },
  { id: 'slides', title: 'Slide deck', desc: '16:9 slides with tokens', icon: 'presentation' },
  { id: 'mobile', title: 'Mobile app', desc: 'A 393×852 phone frame', icon: 'square' },
  { id: 'empty', title: 'Empty canvas', desc: 'Start from nothing', icon: 'plus' },
]

/** `dirty` documents ask Save / Don't save / Cancel before a new or opened one replaces them (unless `busy`: the core refuses that switch outright); `back` returns to the open document. */
export function Home({ recent, dirty, busy, title, save, back, onOpened, toast }: {
  recent: string[]; dirty: () => boolean; busy: () => boolean; title: string; save: () => boolean; back?: () => void; onOpened: () => void; toast: Toast
}) {
  const renderer = useGpuixRequired()
  const [pending, setPending] = useState<(() => void) | null>(null)
  // The core refuses a switch while the agent runs; its message says what to do, so toast it as is.
  const switchTo = (cmd: string, args: Record<string, unknown>, failed: string) => {
    try {
      call(cmd, args)
      onOpened()
    } catch (e) {
      const msg = (e as Error).message
      toast(msg.startsWith('Stop the agent') ? msg : failed, true)
    }
  }
  // A running agent blocks the switch whatever the document holds, so the core's refusal comes before any Save prompt.
  const guarded = (go: () => void) => (dirty() && !busy() ? setPending(() => go) : go())
  const create = (template: string) => guarded(() => switchTo('new', { template }, 'Unable to create that document.'))
  const open = (path: string) => guarded(() => switchTo('open', { path }, `Unable to open ${basename(path)}. Check that the file still exists and is a .nala or .pen document.`))
  const proceed = (saveFirst: boolean) => {
    if (saveFirst && !save()) return
    const go = pending
    setPending(null)
    go?.()
  }
  const browse = async () => {
    const paths = await renderer.promptForPaths?.({ files: true, multiple: false, prompt: 'Open' })
    if (paths?.[0]) open(paths[0])
  }
  const shown = recent.filter((p) => existsSync(p)).slice(0, 8)
  return (
    <div testId="home-scroll" style={{ width: '100%', height: '100%', backgroundColor: C.bg, display: 'flex', flexDirection: 'column', alignItems: 'center', overflowY: 'scroll' }}>
      {/* Scrolls when Recent runs past a short window; the column keeps its height instead of squeezing. */}
      <div style={{ width: 760, flexShrink: 0, display: 'flex', flexDirection: 'column', gap: 28, paddingTop: 72, paddingBottom: 40 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 12 }}>
            <img src={LOGO} style={{ width: 40, height: 40, flexShrink: 0 }} />
            <text style={{ fontSize: 30, fontWeight: '700', color: C.text, fontFamily: FONT }}>Nalarupa</text>
          </div>
          <T size={14} color={C.muted}>Design on an infinite canvas with an AI agent. Rust core, GPU-native UI.</T>
        </div>
        {pending ? (
          <div
            testId="unsaved-prompt"
            role="dialog"
            aria-label="Unsaved changes"
            tabIndex={0}
            autoFocus
            onKeyDown={(e) => { if (e.key === 'escape') setPending(null) }}
            style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 8, padding: 12, borderRadius: 10, backgroundColor: C.panel2, borderWidth: 1, borderColor: C.border }}
          >
            <T size={13} grow style={{ minWidth: 0 }}>{`Save changes to ${title} first? Without saving they are lost.`}</T>
            <Button onClick={() => setPending(null)} testId="unsaved-cancel">Cancel</Button>
            <Button danger onClick={() => proceed(false)} testId="unsaved-discard">Don't save</Button>
            <Button primary onClick={() => proceed(true)} testId="unsaved-save">Save</Button>
          </div>
        ) : null}
        <div style={{ display: 'flex', flexDirection: 'row', gap: 12 }}>
          {TEMPLATES.map((t) => (
            <Pressable
              key={t.id}
              testId={`template-${t.id}`}
              label={`New ${t.title.toLowerCase()}: ${t.desc}`}
              onClick={() => create(t.id)}
              style={{
                flexGrow: 1, flexBasis: 0, height: 130, borderRadius: 12, padding: 16, display: 'flex', flexDirection: 'column', gap: 6,
                justifyContent: 'end', backgroundColor: C.panel, borderWidth: 1, borderColor: C.border,
                hover: { borderColor: C.accent, backgroundColor: C.panel2 },
              }}
            >
              <Icon name={t.icon} size={20} color={C.accent} />
              <T size={14} weight="600">{t.title}</T>
              <T size={12} color={C.ghost}>{t.desc}</T>
            </Pressable>
          ))}
        </div>
        <div style={{ display: 'flex', flexDirection: 'row', gap: 8 }}>
          <Button icon="open" onClick={browse} testId="open-file">Open .nala / .pen…</Button>
          {back ? <Button icon="frame" onClick={back} testId="home-back">{`Back to ${title}`}</Button> : null}
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <T size={12} color={C.muted} weight="600">Recent</T>
          {/* Long names and paths are cut with an ellipsis: the full path shows in a tooltip on hover, and in the
              row itself while it has keyboard focus. */}
          <Tooltip.Provider delayDuration={350}>
            {shown.map((p) => (
              <Tooltip.Root key={p} style={{ flexDirection: 'column', alignItems: 'stretch', minWidth: 0 }}>
                <Tooltip.Trigger asChild>
                  <Pressable testId={`recent-${basename(p)}`} label={`Open ${basename(p)}, ${p}`} onClick={() => open(p)} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 10, minHeight: 34, paddingLeft: 10, paddingRight: 10, borderRadius: 7, hover: { backgroundColor: C.panel } }}>
                    {(focused) => (
                      <>
                        <Icon name="frame" size={13} color={C.ghost} />
                        <T size={13} style={{ flexShrink: 0, maxWidth: 440, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{basename(p)}</T>
                        <T size={12} color={C.ghost} grow style={focused ? { minWidth: 0 } : { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{p}</T>
                      </>
                    )}
                  </Pressable>
                </Tooltip.Trigger>
                <Tooltip.Content side="bottom" align="start" sideOffset={4} style={{ maxWidth: 760, paddingLeft: 8, paddingRight: 8, paddingTop: 5, paddingBottom: 5, borderRadius: 6, backgroundColor: C.raised, borderWidth: 1, borderColor: C.border }}>
                  <T size={12} color={C.text}>{p}</T>
                </Tooltip.Content>
              </Tooltip.Root>
            ))}
          </Tooltip.Provider>
          {shown.length === 0 ? <T size={12} color={C.ghost}>No recent files yet.</T> : null}
        </div>
      </div>
    </div>
  )
}
