import { existsSync, mkdirSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

// A free `<title>.nala` in `dir`: an existing document is never replaced, the name gets -1, -2, …
export function defaultSavePath(title: string, dir = join(homedir(), 'Documents', 'Nalarupa')): string {
  mkdirSync(dir, { recursive: true })
  const base = title.replace(/[\\/:*?"<>|]/g, '').trim() || 'Untitled'
  for (let i = 0; ; i++) {
    const p = join(dir, `${base}${i ? `-${i}` : ''}.nala`)
    if (!existsSync(p)) return p
  }
}
