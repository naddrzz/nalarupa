/**
 * Nalarupa — AI design editor. Rust core (core/) rendered through GPUIX (GPUI + React).
 *   bun run dev     hot-reloading window
 *   bun run build   standalone binary in dist/ (ship nalarupa_core.dll next to it)
 */
import { render } from '@gpuix/react'
import { call } from './src/core'
import { App } from './src/App'
import { decorateWindow, windowSize } from './src/theme'
import { backStopOf, setKeyboardFocus } from './src/ui'

render(
  <App />,
  {
    title: 'Nalarupa',
    appName: 'Nalarupa',
    // Fit the display: a fixed 1440×900 overflowed a 1920×1080 screen at 125 % and pushed the title bar off it.
    ...windowSize(),
    // Agent/automation runs keep the window behind the user's editor.
    focus: process.env.GPUIX_BACKGROUND !== '1',
    // GPUIX binds no Tab: walk GPUI's tab stops. Focus lands on the UI thread a moment later, then the
    // focused element gets its keyboard focus ring.
    onKeyDown(event, renderer) {
      if ((event.key ?? '').toLowerCase() !== 'tab') return
      const sync = () => setTimeout(() => setKeyboardFocus(renderer.getFocusedElementId?.() ?? null), 30)
      const back = event.modifiers?.shift ? backStopOf(renderer.getFocusedElementId?.()) : undefined
      if (back !== undefined) {
        // Out of a text field backwards: see `backStops` in ui.tsx. The focus request lands with a later
        // frame (slow ones included), so walk back from the stop only once it holds focus.
        renderer.focusElement?.(back)
        const step = (tries: number) => {
          if (renderer.getFocusedElementId?.() === back) { renderer.focusPrevious?.(); sync() }
          else if (tries > 0) setTimeout(() => step(tries - 1), 40)
        }
        setTimeout(() => step(25), 40)
        return
      }
      if (event.modifiers?.shift) renderer.focusPrevious?.()
      else renderer.focusNext?.()
      sync()
    },
  },
)
// Native frame: Nalarupa icon, caption and border in the app colours (the border is the OS's, so it hides
// when maximised).
decorateWindow()
// The core writes chats on a background thread: let the last save reach the disk before the process ends.
process.on('exit', () => {
  try {
    call('chat_flush')
  } catch (err) {
    console.error(`Could not save the chat: ${err instanceof Error ? err.message : err}`)
  }
})
