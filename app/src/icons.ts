import cursor from '../assets/icons/mouse-pointer-2.svg' with { type: 'text' }
import frame from '../assets/icons/frame.svg' with { type: 'text' }
import square from '../assets/icons/square.svg' with { type: 'text' }
import circle from '../assets/icons/circle.svg' with { type: 'text' }
import type from '../assets/icons/type.svg' with { type: 'text' }
import hand from '../assets/icons/hand.svg' with { type: 'text' }
import play from '../assets/icons/play.svg' with { type: 'text' }
import save from '../assets/icons/save.svg' with { type: 'text' }
import open from '../assets/icons/folder-open.svg' with { type: 'text' }
import filePlus from '../assets/icons/file-plus.svg' with { type: 'text' }
import undo from '../assets/icons/undo-2.svg' with { type: 'text' }
import redo from '../assets/icons/redo-2.svg' with { type: 'text' }
import download from '../assets/icons/download.svg' with { type: 'text' }
import sparkles from '../assets/icons/sparkles.svg' with { type: 'text' }
import layers from '../assets/icons/layers.svg' with { type: 'text' }
import presentation from '../assets/icons/presentation.svg' with { type: 'text' }
import settings from '../assets/icons/settings.svg' with { type: 'text' }
import trash from '../assets/icons/trash-2.svg' with { type: 'text' }
import copy from '../assets/icons/copy.svg' with { type: 'text' }
import x from '../assets/icons/x.svg' with { type: 'text' }
import alert from '../assets/icons/circle-alert.svg' with { type: 'text' }
import send from '../assets/icons/send.svg' with { type: 'text' }
import stop from '../assets/icons/circle-stop.svg' with { type: 'text' }
import plus from '../assets/icons/plus.svg' with { type: 'text' }
import minus from '../assets/icons/minus.svg' with { type: 'text' }
import maximize from '../assets/icons/maximize.svg' with { type: 'text' }
import chevronRight from '../assets/icons/chevron-right.svg' with { type: 'text' }
import chevronDown from '../assets/icons/chevron-down.svg' with { type: 'text' }
import image from '../assets/icons/image.svg' with { type: 'text' }
import plug from '../assets/icons/plug.svg' with { type: 'text' }
import home from '../assets/icons/house.svg' with { type: 'text' }
import component from '../assets/icons/component.svg' with { type: 'text' }
import hidden from '../assets/icons/eye-off.svg' with { type: 'text' }
import pen from '../assets/icons/pen-tool.svg' with { type: 'text' }
import smile from '../assets/icons/smile.svg' with { type: 'text' }
import history from '../assets/icons/history.svg' with { type: 'text' }

// Alignment glyphs and the visibility eye, drawn in the same 24px stroke style.
const glyph = (body: string) =>
  `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`
const alignLeft = glyph('<path d="M4 3v18"/><rect x="8" y="6" width="12" height="4" rx="1"/><rect x="8" y="14" width="7" height="4" rx="1"/>')
const alignHCenter = glyph('<path d="M12 3v18"/><rect x="5" y="6" width="14" height="4" rx="1"/><rect x="8" y="14" width="8" height="4" rx="1"/>')
const alignRight = glyph('<path d="M20 3v18"/><rect x="4" y="6" width="12" height="4" rx="1"/><rect x="9" y="14" width="7" height="4" rx="1"/>')
const alignTop = glyph('<path d="M3 4h18"/><rect x="6" y="8" width="4" height="12" rx="1"/><rect x="14" y="8" width="4" height="7" rx="1"/>')
const alignVCenter = glyph('<path d="M3 12h18"/><rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="8" width="4" height="8" rx="1"/>')
const alignBottom = glyph('<path d="M3 20h18"/><rect x="6" y="4" width="4" height="12" rx="1"/><rect x="14" y="9" width="4" height="7" rx="1"/>')
const note = glyph('<path d="M15 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h8l6-6V5a2 2 0 0 0-2-2z"/><path d="M14 21v-5a2 2 0 0 1 2-2h5"/>')
const eye = glyph('<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>')

export const ICONS = {
  cursor, frame, square, circle, type, hand, play, save, open, filePlus, undo, redo, download, sparkles, layers,
  presentation, settings, trash, copy, x, alert, send, stop, plus, minus, maximize, chevronRight, chevronDown, image, plug,
  home, component, hidden, pen, smile, history, eye, note, alignLeft, alignHCenter, alignRight, alignTop, alignVCenter, alignBottom,
}
export type IconName = keyof typeof ICONS

export function nodeIcon(type: string, reusable = false): IconName {
  if (reusable) return 'component'
  switch (type) {
    case 'frame': return 'frame'
    case 'group': return 'layers'
    case 'rectangle': return 'square'
    case 'ellipse': return 'circle'
    case 'text': return 'type'
    case 'icon': return 'smile'
    case 'path':
    case 'polygon': return 'pen'
    case 'line': return 'minus'
    case 'note': return 'note'
    case 'prompt': return 'sparkles'
    case 'ref': return 'component'
    default: return 'square'
  }
}
