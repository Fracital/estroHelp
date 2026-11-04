import type { ToolDef } from './workspace-contract';

export class ApiError extends Error {
  status: number;
  code?: string;
  data?: any;
  constructor(status: number, message: string, data?: any) {
    super(message);
    this.status = status;
    this.data = data;
    this.code = data?.error?.code;
  }
}

export async function api<T = any>(path: string, init: { method?: string; body?: unknown } = {}): Promise<T> {
  const res = await fetch(path, {
    method: init.method ?? (init.body !== undefined ? 'POST' : 'GET'),
    headers: { 'Content-Type': 'application/json' },
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
    credentials: 'same-origin',
  });
  const text = await res.text();
  let data: any = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
  if (!res.ok) {
    const msg = typeof data?.error === 'string' ? data.error : data?.error?.message ?? `request failed (${res.status})`;
    throw new ApiError(res.status, msg, data);
  }
  return data as T;
}

export interface ChatRequest {
  model: string;
  /** OpenAI-shaped messages (strings or content-part arrays, tool rounds included). */
  messages: unknown[];
  tools?: ToolDef[];
}

export interface ToolCallDelta {
  index: number;
  id?: string;
  name?: string;
  /** Fragment of the JSON arguments string. */
  args?: string;
}

export interface StreamEvent {
  text?: string;
  reasoning?: string;
  toolCall?: ToolCallDelta;
  finish?: string;
}

/** Streams an OpenAI-style chat completion, yielding content, reasoning and tool-call deltas. */
export async function* streamChat(req: ChatRequest, signal?: AbortSignal): AsyncGenerator<StreamEvent> {
  const res = await fetch('/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
    body: JSON.stringify({ ...req, stream: true }),
    credentials: 'same-origin',
    signal,
  });
  if (!res.ok) {
    let data: any = null;
    try { data = await res.json(); } catch {}
    const msg = typeof data?.error === 'string' ? data.error : data?.error?.message ?? `request failed (${res.status})`;
    throw new ApiError(res.status, msg, data);
  }
  if (!res.body) throw new ApiError(502, 'empty response');

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let idx: number;
    while ((idx = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, idx).replace(/\r$/, '');
      buffer = buffer.slice(idx + 1);
      if (!line.startsWith('data:')) continue;
      const payload = line.slice(5).trim();
      if (!payload || payload === '[DONE]') continue;
      let chunk: any;
      try { chunk = JSON.parse(payload); } catch { continue; }
      if (chunk.error) {
        throw new ApiError(502, typeof chunk.error === 'string' ? chunk.error : chunk.error.message ?? 'upstream error', chunk);
      }
      for (const ev of chunkEvents(chunk)) yield ev;
    }
  }
}

/** Turns one chat.completion.chunk into zero or more StreamEvents. Exported for tests. */
export function chunkEvents(chunk: any): StreamEvent[] {
  const out: StreamEvent[] = [];
  const choice = chunk.choices?.[0];
  if (!choice) return out;
  const delta = choice.delta ?? {};
  if (typeof delta.content === 'string' && delta.content) out.push({ text: delta.content });
  const r = delta.reasoning_content ?? delta.reasoning;
  if (typeof r === 'string' && r) out.push({ reasoning: r });
  if (Array.isArray(delta.tool_calls)) {
    for (const tc of delta.tool_calls) {
      const ev: ToolCallDelta = { index: typeof tc.index === 'number' ? tc.index : 0 };
      if (typeof tc.id === 'string' && tc.id) ev.id = tc.id;
      if (typeof tc.function?.name === 'string' && tc.function.name) ev.name = tc.function.name;
      if (typeof tc.function?.arguments === 'string' && tc.function.arguments) ev.args = tc.function.arguments;
      out.push({ toolCall: ev });
    }
  }
  if (typeof choice.finish_reason === 'string' && choice.finish_reason) out.push({ finish: choice.finish_reason });
  return out;
}
