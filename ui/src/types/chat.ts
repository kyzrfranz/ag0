export type Role = 'user' | 'assistant' | 'system'

export type ChatStatus = 'idle' | 'sending' | 'receiving' | 'error'

export type ConnectionStatus = 'connecting' | 'connected' | 'reconnecting' | 'disconnected'

export interface Message {
  id: string
  role: Role
  content: string
  createdAt: number
}

export interface ChatRequest {
  message: string
}

export interface ChatResponse {
  response: string
  session_id?: string
}

export type ActivityEvent =
  | 'routing'
  | 'agent_start'
  | 'tool_call'
  | 'agent_done'
  | 'synthesizing'

export interface ActivityState {
  event: ActivityEvent | string
  agent?: string
  tool?: string
}

export interface ChunkFrame {
  type: 'chunk'
  text: string
  msg_id?: string
}

export interface ActivityFrame {
  type: 'activity'
  event: ActivityEvent | string
  agent?: string
  tool?: string
}

export interface DoneFrame {
  type: 'done'
  msg_id?: string
}

export interface ReplayFrame {
  type: 'replay'
  msg_id: string
  text: string
}

export interface ErrorFrame {
  type: 'error'
  error: string
}

export interface AckFrame {
  type: 'ack'
  msg_id: string
}

export type WsFrame = ChunkFrame | ActivityFrame | DoneFrame | ReplayFrame | ErrorFrame