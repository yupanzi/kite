export type ChatRole = 'user' | 'assistant' | 'tool'

export interface ChatInputOption {
  label: string
  value: string
  description?: string
}

export interface ChatInputField {
  name: string
  label: string
  type: 'text' | 'number' | 'textarea' | 'select' | 'switch'
  required?: boolean
  placeholder?: string
  description?: string
  defaultValue?: string
  options?: ChatInputOption[]
}

export interface ChatInputRequest {
  sessionId: string
  kind: 'choice' | 'form'
  name?: string
  title: string
  description?: string
  submitLabel?: string
  options?: ChatInputOption[]
  fields?: ChatInputField[]
}

export interface ChatPendingAction {
  sessionId: string
  tool: string
  args: Record<string, unknown>
}

export interface ChatMessage {
  id: string
  role: ChatRole
  content: string
  thinking?: string
  toolCallId?: string
  toolName?: string
  toolArgs?: Record<string, unknown>
  toolResult?: string
  inputRequest?: ChatInputRequest
  pendingAction?: ChatPendingAction
  actionStatus?: 'pending' | 'confirmed' | 'denied' | 'error'
}

export interface PageContext {
  page: string
  namespace: string
  resourceName: string
  resourceKind: string
}

export interface ChatSession {
  id: string
  title: string
  messages: ChatMessage[]
  createdAt: number
  updatedAt: number
  clusterName?: string
}

// Wire format sent to POST /api/v1/ai/chat. Tool turns are sent structurally
// (not flattened to "[Tool: ...]" text) so the backend can rebuild real
// tool_use / tool_result blocks — feeding tool calls back as plain text
// poisons the model into emitting textual/XML tool calls on later turns.
export type APIChatMessage =
  | { role: 'user' | 'assistant'; content: string }
  | {
      role: 'tool'
      tool_call_id: string
      tool_name: string
      tool_args?: Record<string, unknown>
      tool_result: string
      is_error?: boolean
    }

export interface AIChatState {
  messages: ChatMessage[]
  history: ChatSession[]
  currentSessionId: string | null
  isLoading: boolean
}
