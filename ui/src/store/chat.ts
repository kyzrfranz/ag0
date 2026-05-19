import { defineStore } from 'pinia'
import { ref } from 'vue'
import * as api from '@/api/chat'
import type { HistoryMessage, IncomingFrame } from '@/api/chat'
import type {
  ActivityFrame,
  ActivityState,
  ChatStatus,
  ChunkFrame,
  ConnectionStatus,
  DoneFrame,
  ErrorFrame,
  Message,
  ReplayFrame,
  Role,
} from '@/types/chat'

function makeId(): string {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

const DEFAULT_SESSION_ID = 'coaching-session-1'
const INITIAL_SESSION_ID = import.meta.env.VITE_SESSION_ID ?? DEFAULT_SESSION_ID
const RECONNECT_DELAY_MS = 2000

export const useChatStore = defineStore('chat', () => {
  const messages = ref<Message[]>([])
  const status = ref<ChatStatus>('idle')
  const connection = ref<ConnectionStatus>('disconnected')
  const errorMessage = ref<string | null>(null)
  const sessionId = ref<string>(INITIAL_SESSION_ID)
  const streamingMessageId = ref<string | null>(null)
  const currentActivity = ref<ActivityState | null>(null)
  const lastAckedMsgId = ref<string | null>(null)

  let socket: WebSocket | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let pending: string | null = null
  let userClosed = false
  let streamingMsgId: string | null = null

  function ackMsg(msgId: string): void {
    lastAckedMsgId.value = msgId
    if (socket && socket.readyState === WebSocket.OPEN) {
      api.sendAck(socket, msgId)
    }
  }

  function setStatus(next: ChatStatus): void {
    status.value = next
  }

  function setConnection(next: ConnectionStatus): void {
    connection.value = next
  }

  function setSessionId(next: string): void {
    sessionId.value = next
  }

  function setActivity(next: ActivityState | null): void {
    currentActivity.value = next
  }

  function appendMessage(msg: Message): void {
    messages.value.push(msg)
  }

  function mapHistoryRole(role: string): Role | null {
    if (role === 'user' || role === 'assistant' || role === 'system') return role
    return null
  }

  function normalizeTimestamp(ts: number | undefined): number {
    if (typeof ts !== 'number' || !Number.isFinite(ts)) return Date.now()
    // Seconds-since-epoch values are < 1e12; convert to millis.
    return ts < 1e12 ? ts * 1000 : ts
  }

  function hydrateFromHistory(history: HistoryMessage[]): void {
    if (history.length === 0) return
    // Bail out if the user has typed something while we were fetching.
    if (messages.value.length !== 0) return
    const mapped: Message[] = []
    for (const h of history) {
      const role = mapHistoryRole(h.role)
      if (!role || typeof h.content !== 'string') continue
      mapped.push({
        id: makeId(),
        role,
        content: h.content,
        createdAt: normalizeTimestamp(h.created_at),
      })
    }
    if (mapped.length === 0) return
    messages.value = mapped
  }

  async function loadHistory(): Promise<void> {
    if (messages.value.length !== 0) return
    try {
      const history = await api.fetchHistory(sessionId.value)
      hydrateFromHistory(history)
    } catch (err) {
      // History is best-effort — don't surface as a connection error.
      console.warn('history fetch failed', err)
    }
  }

  function appendChunkToStreamingMessage(chunk: string): void {
    const id = streamingMessageId.value
    if (id === null) return
    const idx = messages.value.findIndex((m) => m.id === id)
    if (idx < 0) return
    const existing = messages.value[idx]
    messages.value.splice(idx, 1, {
      ...existing,
      content: existing.content + chunk,
    })
  }

  function handleChunk(frame: ChunkFrame): void {
    if (frame.msg_id) streamingMsgId = frame.msg_id
    if (streamingMessageId.value === null) {
      const id = makeId()
      streamingMessageId.value = id
      appendMessage({
        id,
        role: 'assistant',
        content: frame.text,
        createdAt: Date.now(),
      })
    } else {
      appendChunkToStreamingMessage(frame.text)
    }
    setStatus('receiving')
  }

  function handleReplay(frame: ReplayFrame): void {
    if (frame.msg_id === lastAckedMsgId.value) {
      // Already rendered before reconnect — just re-ack to clear server buffer.
      ackMsg(frame.msg_id)
      return
    }
    appendMessage({
      id: makeId(),
      role: 'assistant',
      content: frame.text,
      createdAt: Date.now(),
    })
    ackMsg(frame.msg_id)
  }

  function handleActivity(frame: ActivityFrame): void {
    setActivity({ event: frame.event, agent: frame.agent, tool: frame.tool })
  }

  function handleDone(frame: DoneFrame): void {
    const hadPending = pending !== null
    pending = null

    const streamId = streamingMessageId.value
    let emptyTurn = false

    if (streamId !== null) {
      const idx = messages.value.findIndex((m) => m.id === streamId)
      if (idx >= 0 && messages.value[idx].content.trim() === '') {
        // Drop the blank assistant bubble so we don't leave a ghost in the log.
        messages.value.splice(idx, 1)
        emptyTurn = true
      }
    } else if (hadPending) {
      // We were waiting on a reply and got `done` without ever opening a bubble.
      emptyTurn = true
    }

    const ackId = frame.msg_id ?? streamingMsgId
    if (ackId && !emptyTurn) ackMsg(ackId)
    streamingMsgId = null

    streamingMessageId.value = null
    setActivity(null)

    if (emptyTurn) {
      appendMessage({
        id: makeId(),
        role: 'system',
        content: 'No response — try again.',
        createdAt: Date.now(),
      })
      errorMessage.value = 'No response — try again'
    }

    setStatus('idle')
  }

  function handleError(frame: ErrorFrame): void {
    errorMessage.value = frame.error
    pending = null
    setStatus('error')
    setActivity(null)
  }

  function handleIncoming(incoming: IncomingFrame): void {
    if (incoming.sessionId) setSessionId(incoming.sessionId)
    switch (incoming.frame.type) {
      case 'chunk':
        handleChunk(incoming.frame)
        return
      case 'activity':
        handleActivity(incoming.frame)
        return
      case 'done':
        handleDone(incoming.frame)
        return
      case 'replay':
        handleReplay(incoming.frame)
        return
      case 'error':
        handleError(incoming.frame)
        return
    }
  }

  function scheduleReconnect(): void {
    if (userClosed || reconnectTimer !== null) return
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      setConnection('reconnecting')
      openSocket()
    }, RECONNECT_DELAY_MS)
  }

  function openSocket(): void {
    if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
      return
    }
    setConnection(connection.value === 'disconnected' ? 'connecting' : connection.value)

    let ws: WebSocket
    try {
      ws = api.openConnection(sessionId.value)
    } catch (err) {
      errorMessage.value = err instanceof Error ? err.message : 'connection failed'
      setConnection('disconnected')
      scheduleReconnect()
      return
    }
    socket = ws

    api.onMessage(ws, handleIncoming)

    ws.addEventListener('open', () => {
      setConnection('connected')
      errorMessage.value = null
      if (pending !== null) {
        api.sendMessage(ws, pending)
        setStatus('sending')
      }
      void loadHistory()
    })

    ws.addEventListener('error', () => {
      errorMessage.value = 'websocket error'
    })

    ws.addEventListener('close', () => {
      socket = null
      streamingMessageId.value = null
      streamingMsgId = null
      setActivity(null)
      if (userClosed) {
        setConnection('disconnected')
        return
      }
      setConnection('disconnected')
      if (status.value === 'sending' || status.value === 'receiving') {
        setStatus('idle')
      }
      scheduleReconnect()
    })
  }

  function connect(): void {
    userClosed = false
    openSocket()
  }

  function disconnect(): void {
    userClosed = true
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    setActivity(null)
    if (socket) api.closeConnection(socket)
    socket = null
    setConnection('disconnected')
  }

  function send(text: string): void {
    const trimmed = text.trim()
    if (!trimmed || status.value === 'sending' || status.value === 'receiving') return

    streamingMessageId.value = null
    setActivity(null)

    errorMessage.value = null
    appendMessage({
      id: makeId(),
      role: 'user',
      content: trimmed,
      createdAt: Date.now(),
    })
    pending = trimmed
    setStatus('sending')

    if (socket && socket.readyState === WebSocket.OPEN) {
      api.sendMessage(socket, trimmed)
    } else {
      openSocket()
    }
  }

  function clear(): void {
    streamingMessageId.value = null
    streamingMsgId = null
    messages.value = []
    errorMessage.value = null
    pending = null
    setActivity(null)
    setStatus('idle')
  }

  return {
    messages,
    status,
    connection,
    errorMessage,
    sessionId,
    streamingMessageId,
    currentActivity,
    setStatus,
    setSessionId,
    connect,
    disconnect,
    send,
    clear,
  }
})