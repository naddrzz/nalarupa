import { call } from '../src/core'
declare var self: Worker
// A stand-in for the UI thread (a Bun worker shares the process-wide engine): poll the core every 20 ms and record the slowest answer.
let max = 0
const iv = setInterval(() => {
  const t = performance.now()
  call('state')
  max = Math.max(max, performance.now() - t)
}, 20)
self.onmessage = () => {
  clearInterval(iv)
  postMessage({ max: Math.round(max) })
}
