import type { ChatResponse, WsFrame } from '@/types/chat'

function resolveBaseUrl(): string {
  const configured = import.meta.env.VITE_AG0_URL
  if (configured && configured.length > 0) return configured
  // Empty/unset = same-origin: UI is embedded in the ag0 binary and served from
  // the same host that exposes /ws/chat. Resolve to window.location.origin so
  // the WebSocket constructor gets an absolute URL.
  if (typeof window !== 'undefined') return window.location.origin
  return ''
}

const BASE_URL = resolveBaseUrl()

function toWebSocketUrl(httpUrl: string, sessionId: string): string {
  const base = httpUrl.replace(/^http(s?):\/\//i, (_, s) => `ws${s}://`).replace(/\/+$/, '')
  const params = new URLSearchParams({ session_id: sessionId })
  return `${base}/ws/chat?${params.toString()}`
}

export function openConnection(sessionId: string): WebSocket {
  return new WebSocket(toWebSocketUrl(BASE_URL, sessionId))
}

export function sendMessage(ws: WebSocket, message: string): void {
  ws.send(JSON.stringify({ message }))
}

export function sendAck(ws: WebSocket, msgId: string): void {
  ws.send(JSON.stringify({ type: 'ack', msg_id: msgId }))
}

export interface IncomingFrame {
  frame: WsFrame
  sessionId: string | null
}

function parseFrame(data: string): IncomingFrame | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(data)
  } catch {
    return null
  }
  if (!parsed || typeof parsed !== 'object') return null
  const obj = parsed as Record<string, unknown>

  if (obj.type === 'chunk' && typeof obj.text === 'string') {
    return {
      frame: {
        type: 'chunk',
        text: obj.text,
        msg_id: typeof obj.msg_id === 'string' ? obj.msg_id : undefined,
      },
      sessionId: null,
    }
  }
  if (obj.type === 'activity' && typeof obj.event === 'string') {
    return {
      frame: {
        type: 'activity',
        event: obj.event,
        agent: typeof obj.agent === 'string' ? obj.agent : undefined,
        tool: typeof obj.tool === 'string' ? obj.tool : undefined,
      },
      sessionId: null,
    }
  }
  if (obj.type === 'done') {
    return {
      frame: {
        type: 'done',
        msg_id: typeof obj.msg_id === 'string' ? obj.msg_id : undefined,
      },
      sessionId: null,
    }
  }
  if (obj.type === 'replay' && typeof obj.msg_id === 'string' && typeof obj.text === 'string') {
    return {
      frame: { type: 'replay', msg_id: obj.msg_id, text: obj.text },
      sessionId: null,
    }
  }
  if (obj.type === 'error' && typeof obj.error === 'string') {
    return { frame: { type: 'error', error: obj.error }, sessionId: null }
  }

  // Backward compat: legacy shape { response, session_id? }
  const legacy = obj as Partial<ChatResponse>
  if (typeof legacy.response === 'string') {
    return {
      frame: { type: 'chunk', text: legacy.response },
      sessionId: typeof legacy.session_id === 'string' ? legacy.session_id : null,
    }
  }

  return null
}

export function onMessage(ws: WebSocket, callback: (incoming: IncomingFrame) => void): void {
  ws.addEventListener('message', (event: MessageEvent<string>) => {
    const incoming = parseFrame(event.data)
    // TODO: remove — debug log for ws frame protocol verification
    console.log('[ws]', event.data, '→', incoming?.frame.type ?? 'IGNORED')
    if (incoming) callback(incoming)
  })
}

export function closeConnection(ws: WebSocket): void {
  ws.close(1000, 'client closing')
}

export async function checkHealth(): Promise<boolean> {
  try {
    const res = await fetch(`${BASE_URL}/health`)
    return res.ok
  } catch {
    return false
  }
}

export interface HistoryMessage {
  role: string
  content: string
  created_at?: number
}

export async function fetchHistory(sessionId: string): Promise<HistoryMessage[]> {
  const params = new URLSearchParams({ session_id: sessionId })
  const res = await fetch(`${BASE_URL}/history?${params.toString()}`)
  if (!res.ok) throw new Error(`history request failed: ${res.status}`)
  const data: unknown = await res.json()
  if (Array.isArray(data)) return data as HistoryMessage[]
  if (data && typeof data === 'object' && Array.isArray((data as { messages?: unknown }).messages)) {
    return (data as { messages: HistoryMessage[] }).messages
  }
  return []
}