// Shared contract between the chat (agent loop, tool cards), the workspace
// (Dusk runtime, files, shell) and the backend's thread storage. Keep these
// shapes stable; several components are built against them in parallel.

/** OpenAI function-tool schema sent in the `tools` field of a chat request. */
export interface ToolDef {
  type: 'function';
  function: { name: string; description: string; parameters: Record<string, unknown> };
}

/** One tool call emitted by the model (arguments is the raw JSON string). */
export interface ToolCall {
  id: string;
  name: string;
  arguments: string;
}

export interface ToolResult {
  ok: boolean;
  /** Text returned to the model as the `tool` message content. Truncate large output before returning. */
  output: string;
  exitCode?: number;
}

export interface ExecuteOptions {
  /** Live output (shell stdout/stderr) for the terminal card; may be called many times. */
  onOutput?: (chunk: string) => void;
  signal?: AbortSignal;
}

/** Implemented by components/workspace/index.ts. */
export interface WorkspaceApi {
  /** True when the page can run Dusk (crossOriginIsolated, OPFS, SharedArrayBuffer). */
  isSupported(): boolean;
  /** Boots the runtime lazily; resolves once the shell and filesystem are usable. Idempotent. */
  ready(): Promise<void>;
  /** Tool schemas to send to the model when workspace tools are enabled. */
  tools(): ToolDef[];
  /** Runs one tool call inside the workspace. Must never throw; report failures via ok=false. */
  execute(call: ToolCall, opts?: ExecuteOptions): Promise<ToolResult>;
}

/** Attachment captured by the composer and stored in the message's data. */
export interface Attachment {
  kind: 'image' | 'file';
  name: string;
  mime: string;
  size: number;
  /** data: URL for images (sent to the model as an image_url part). */
  dataUrl?: string;
  /** Decoded text for files (sent to the model inline as a fenced block). */
  text?: string;
}

/** Extra per-message fields persisted in thread_message.data (JSONB). */
export interface MessageData {
  reasoning?: string;
  reasoningMs?: number;
  toolCalls?: ToolCall[];
  /** For role=tool: the call this message answers. */
  toolCallId?: string;
  toolName?: string;
  exitCode?: number;
  attachments?: Attachment[];
}

export type MessageRole = 'user' | 'assistant' | 'tool';

/** Props of components/workspace/WorkspacePane.tsx (default export). */
export interface WorkspacePaneProps {
  tab: 'files' | 'shell';
  onTabChange: (tab: 'files' | 'shell') => void;
  onClose: () => void;
}

