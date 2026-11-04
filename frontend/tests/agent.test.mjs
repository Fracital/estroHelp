import { test } from 'node:test';
import assert from 'node:assert/strict';
import { accumulateToolCall, finishedToolCalls, buildHistory, userContent, truncateOutput } from '../src/lib/agent.ts';
import { chunkEvents } from '../src/lib/api.ts';

test('tool-call deltas accumulate across chunks in index order', () => {
  const chunks = [
    { choices: [{ delta: { tool_calls: [{ index: 0, id: 'tool_A', type: 'function', function: { name: 'run_shell', arguments: '' } }] } }] },
    { choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: '{"command":"' } }] } }] },
    { choices: [{ delta: { tool_calls: [{ index: 1, id: 'tool_B', function: { name: 'read_file', arguments: '{"path":"a"}' } }] } }] },
    { choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: 'ls -la"}' } }] } }] },
    { choices: [{ delta: {}, finish_reason: 'tool_calls' }] },
  ];
  const acc = new Map();
  let finish = '';
  for (const c of chunks) for (const ev of chunkEvents(c)) { if (ev.toolCall) accumulateToolCall(acc, ev.toolCall); if (ev.finish) finish = ev.finish; }
  assert.equal(finish, 'tool_calls');
  assert.deepEqual(finishedToolCalls(acc), [
    { id: 'tool_A', name: 'run_shell', arguments: '{"command":"ls -la"}' },
    { id: 'tool_B', name: 'read_file', arguments: '{"path":"a"}' },
  ]);
});

test('chunkEvents separates text and reasoning', () => {
  const evs = chunkEvents({ choices: [{ delta: { reasoning_content: 'hmm', content: 'hi' }, finish_reason: null }] });
  assert.deepEqual(evs, [{ text: 'hi' }, { reasoning: 'hmm' }]);
});

test('buildHistory rebuilds tool rounds and attachments in OpenAI shape', () => {
  const png = 'data:image/png;base64,AAAA';
  const h = buildHistory([
    { seq: 1, role: 'user', content: 'look', data: { attachments: [{ kind: 'image', name: 'a.png', mime: 'image/png', size: 4, dataUrl: png }, { kind: 'file', name: 'x.txt', mime: 'text/plain', size: 2, text: 'hi' }] } },
    { seq: 2, role: 'assistant', content: '', data: { toolCalls: [{ id: 'tool_A', name: 'run_shell', arguments: '{"command":"ls"}' }] } },
    { seq: 3, role: 'tool', content: 'a b', data: { toolCallId: 'tool_A', toolName: 'run_shell', exitCode: 0 } },
    { seq: 4, role: 'assistant', content: 'two files', data: { reasoning: 'r', reasoningMs: 10 } },
  ]);
  assert.deepEqual(h, [
    { role: 'user', content: [{ type: 'text', text: 'look\n\n```x.txt\nhi\n```' }, { type: 'image_url', image_url: { url: png } }] },
    { role: 'assistant', content: null, tool_calls: [{ id: 'tool_A', type: 'function', function: { name: 'run_shell', arguments: '{"command":"ls"}' } }] },
    { role: 'tool', tool_call_id: 'tool_A', content: 'a b' },
    { role: 'assistant', content: 'two files' },
  ]);
});

test('plain user messages stay strings', () => {
  assert.equal(userContent('hello'), 'hello');
  assert.equal(userContent('hello', [{ kind: 'file', name: 'f.md', mime: 'text/markdown', size: 1, text: '# t' }]), 'hello\n\n```f.md\n# t\n```');
});

test('truncateOutput keeps head and tail', () => {
  const s = 'a'.repeat(30000) + 'END';
  const t = truncateOutput(s);
  assert.ok(t.length < 25000);
  assert.ok(t.endsWith('END'));
  assert.ok(t.includes('characters truncated'));
});
