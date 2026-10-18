// Entry for the compiled binary. `bun build --compile` cannot embed GPUIX's native
// addon (napi loads it with a dynamic require), so dist/ ships it next to the exe
// and we point the napi loader at it before GPUIX is imported.
import { existsSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'

const dir = dirname(process.execPath)
const addon = existsSync(dir) ? readdirSync(dir).find((f) => f.startsWith('gpuix-native.') && f.endsWith('.node')) : undefined
if (addon && !process.env.NAPI_RS_NATIVE_LIBRARY_PATH) process.env.NAPI_RS_NATIVE_LIBRARY_PATH = join(dir, addon)

await import('./app.tsx')
