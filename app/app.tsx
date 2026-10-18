/**
 * Nalarupa — AI design editor. Rust core (core/) rendered through GPUIX (GPUI + React).
 *   bun run dev     hot-reloading window
 *   bun run build   standalone binary in dist/ (ship nalarupa_core.dll next to it)
 */
import { render } from '@gpuix/react'
import { App } from './src/App'
import { decorateWindow, windowSize } from './src/theme'
import { setKeyboardFocus } from './src/ui'

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
      if (event.modifiers?.shift) renderer.focusPrevious?.()
      else renderer.focusNext?.()
      setTimeout(() => setKeyboardFocus(renderer.getFocusedElementId?.() ?? null), 30)
    },
  },
)
// Native frame: Nalarupa icon, caption and border in the app colours (the border is the OS's, so it hides
// when maximised).
decorateWindow()
