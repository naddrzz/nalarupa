import { basename } from 'node:path'
import { existsSync } from 'node:fs'
import { useGpuixRequired } from '@gpuix/react'
import * as Tooltip from '@gpuix/react/tooltip'
import { tryCall } from './core'
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

export function Home({ recent, onOpened, toast }: { recent: string[]; onOpened: () => void; toast: Toast }) {
  const renderer = useGpuixRequired()
  const create = (template: string) => {
    if (tryCall('new', { template }) !== null) onOpened()
  }
  const open = (path: string) => {
    if (tryCall('open', { path }) !== null) onOpened()
    else toast(`Unable to open ${basename(path)}. Check that the file still exists and is a .nala or .pen document.`, true)
  }
  const browse = async () => {
    const paths = await renderer.promptForPaths?.({ files: true, multiple: false, prompt: 'Open' })
    if (paths?.[0]) open(paths[0])
  }
  return (
    <div style={{ width: '100%', height: '100%', backgroundColor: C.bg, display: 'flex', flexDirection: 'column', alignItems: 'center', paddingTop: 72 }}>
      <div style={{ width: 760, display: 'flex', flexDirection: 'column', gap: 28 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 12 }}>
            <img src={LOGO} style={{ width: 40, height: 40, flexShrink: 0 }} />
            <text style={{ fontSize: 30, fontWeight: '700', color: C.text, fontFamily: FONT }}>Nalarupa</text>
          </div>
          <T size={14} color={C.muted}>Design on an infinite canvas with an AI agent. Rust core, GPU-native UI.</T>
        </div>
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
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <T size={12} color={C.muted} weight="600">Recent</T>
          {/* Long paths are cut at the row's end: the full path shows in a tooltip on hover, and in the row
              itself while it has keyboard focus. */}
          <Tooltip.Provider delayDuration={350}>
            {recent.filter((p) => existsSync(p)).slice(0, 8).map((p) => (
              <Tooltip.Root key={p} style={{ flexDirection: 'column', alignItems: 'stretch', minWidth: 0 }}>
                <Tooltip.Trigger asChild>
                  <Pressable testId={`recent-${basename(p)}`} label={`Open ${basename(p)}, ${p}`} onClick={() => open(p)} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 10, minHeight: 34, paddingLeft: 10, paddingRight: 10, borderRadius: 7, hover: { backgroundColor: C.panel } }}>
                    {(focused) => (
                      <>
                        <Icon name="frame" size={13} color={C.ghost} />
                        <T size={13}>{basename(p)}</T>
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
          {recent.length === 0 ? <T size={12} color={C.ghost}>No recent files yet.</T> : null}
        </div>
      </div>
    </div>
  )
}
