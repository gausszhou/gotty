<template>
  <Terminal ref="terminalRef" />
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import Terminal from './components/Terminal.vue'
import { WebTTY, protocols, type Terminal as ITerminal } from './utils/webtty'
import { ConnectionFactory } from './utils/websocket'

const terminalRef = ref<InstanceType<typeof Terminal>>()

interface SessionInfo {
  id: string
  state: string
  exited: boolean
}

// sessionId is the id of the session this page is bound to;
// it may change when a dead session is recreated.
let sessionId: string | null = new URLSearchParams(window.location.search).get('id')

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) {
    throw new Error(`request to ${url} failed with status ${res.status}`)
  }
  return res.json() as Promise<T>
}

// resolveSession returns the id of a living session: it reuses the current
// one while it exists, and creates a fresh session otherwise.
async function resolveSession(): Promise<string> {
  if (sessionId) {
    try {
      const session = await fetchJSON<SessionInfo>(`/api/sessions/${sessionId}`)
      if (session.state !== 'destroyed' && !session.exited) {
        return sessionId
      }
    } catch {
      // session is gone (404 or server restart) — fall through to create
    }
  }

  const session = await fetchJSON<SessionInfo>('/api/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ command: '', args: [] }),
  })
  sessionId = session.id

  const url = new URL(window.location.href)
  url.searchParams.set('id', session.id)
  window.history.replaceState(null, '', url)

  return session.id
}

onMounted(() => {
  const el = terminalRef.value!
  if (!el) return

  const termAdapter: ITerminal = {
    info: () => el.info(),
    output: (data: Uint8Array) => el.write(data),
    showMessage: (msg: string, timeout: number) => el.showMessage(msg, timeout),
    removeMessage: () => el.removeMessage(),
    setWindowTitle: (title: string) => el.setWindowTitle(title),
    setPreferences: (value: object) => el.setPreferences(value),
    onInput: (cb) => el.onInput(cb),
    onResize: (cb) => el.onResize(cb),
    reset: () => el.reset(),
    deactivate: () => el.deactivate(),
    close: () => el.close(),
  }

  const httpsEnabled = window.location.protocol === 'https:'
  const wsBase =
    (httpsEnabled ? 'wss://' : 'ws://') +
    window.location.host +
    '/ws'

  resolveSession()
    .then((id) => {
      const token = (window as any).gotty_auth_token || ''
      const wt = new WebTTY(
        termAdapter,
        new ConnectionFactory(wsBase, protocols),
        '', // Arguments (unused by the new session-based API)
        token,
        id,
        resolveSession,
      )
      const closer = wt.open()

      window.addEventListener('unload', () => {
        closer()
        termAdapter.close()
      })
    })
    .catch((err) => {
      console.error('Failed to resolve a session:', err)
      el.showMessage('Failed to connect to the server', 0)
    })
})
</script>

<style>
html, body, #app {
  margin: 0;
  padding: 0;
  height: 100%;
  width: 100%;
  background: black;
}
</style>