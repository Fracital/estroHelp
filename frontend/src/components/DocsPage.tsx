import CodeTabs, { type CodeSample } from './CodeTabs';
import Integrations from './Integrations';
import '../styles/docs.css';

const BASE_URL = 'https://estro.help';

const AUTH_SAMPLES: CodeSample[] = [
  {
    id: 'curl', label: 'curl',
    code: `curl ${BASE_URL}/api/auth/login \\
  -H "Content-Type: application/json" \\
  -d '{"username":"you","password":"yourpassword"}'

# Response:
# {"token":"eyJ...","user":{"id":1,"username":"you",...}}`,
  },
  {
    id: 'python', label: 'Python',
    code: `import requests

resp = requests.post("${BASE_URL}/api/auth/login", json={
    "username": "you",
    "password": "yourpassword",
})
token = resp.json()["token"]
# Use as: Authorization: Bearer <token>`,
  },
  {
    id: 'node', label: 'Node.js',
    code: `const resp = await fetch("${BASE_URL}/api/auth/login", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ username: "you", password: "yourpassword" }),
});
const { token } = await resp.json();
// Use as: Authorization: Bearer <token>`,
  },
];

const CHAT_SAMPLES: CodeSample[] = [
  {
    id: 'curl', label: 'curl',
    code: `curl -N ${BASE_URL}/v1/chat/completions \\
  -H "Authorization: Bearer $ESTRO_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "kali-gamma",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Refactor this function and explain the tradeoffs."}
    ]
  }'`,
  },
  {
    id: 'python', label: 'Python',
    code: `from openai import OpenAI

client = OpenAI(
    base_url="${BASE_URL}/v1",
    api_key=ESTRO_TOKEN,
)

stream = client.chat.completions.create(
    model="kali-gamma",
    messages=[{"role": "user", "content": "Refactor this function and explain the tradeoffs."}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="", flush=True)`,
  },
  {
    id: 'node', label: 'Node.js',
    code: `import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "${BASE_URL}/v1",
  apiKey: ESTRO_TOKEN,
});

const stream = await client.chat.completions.create({
  model: "kali-gamma",
  messages: [{ role: "user", content: "Refactor this function and explain the tradeoffs." }],
  stream: true,
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content ?? "");
}`,
  },
];

const REASONING_SAMPLES: CodeSample[] = [
  {
    id: 'curl', label: 'curl',
    code: `# Reasoning deltas appear as "reasoning_content" in each chunk's delta.
# The full response includes a collapsible thought process.

curl -N ${BASE_URL}/v1/chat/completions \\
  -H "Authorization: Bearer $ESTRO_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "kali-beta",
    "stream": true,
    "messages": [{"role": "user", "content": "Prove that sqrt(2) is irrational."}]
  }'

# Stream events include:
# {"choices":[{"delta":{"reasoning_content":"Let me think..."}}]}
# {"choices":[{"delta":{"content":"Here is the proof..."}}]}`,
  },
  {
    id: 'python', label: 'Python',
    code: `from openai import OpenAI

client = OpenAI(base_url="${BASE_URL}/v1", api_key=ESTRO_TOKEN)

stream = client.chat.completions.create(
    model="kali-beta",
    messages=[{"role": "user", "content": "Prove that sqrt(2) is irrational."}],
    stream=True,
)
for chunk in stream:
    delta = chunk.choices[0].delta
    # Reasoning tokens come through before the answer
    if hasattr(delta, "reasoning_content") and delta.reasoning_content:
        print(f"[thinking] {delta.reasoning_content}", end="")
    elif delta.content:
        print(delta.content, end="")`,
  },
];

export default function DocsPage() {
  return (
    <div class="docs-root">
      {/* Hero */}
      <section class="docs-hero">
        <h1>API reference</h1>
        <p class="docs-lede">Estro speaks the OpenAI chat-completions dialect. Point any client, SDK, or IDE extension at it by changing the base URL and key.</p>
      </section>

      <section class="docs-section">
        <Integrations />
      </section>

      {/* Auth */}
      <section class="docs-section">
        <h2 id="auth">Authentication</h2>
        <p>Sign in to get a session token. Pass it as <code>Authorization: Bearer &lt;token&gt;</code> or <code>X-Api-Key</code>. Tokens last 30 days and can be revoked from your <a href="/account">account page</a>.</p>
        <CodeTabs samples={AUTH_SAMPLES} />
      </section>

      {/* Models */}
      <section class="docs-section">
        <h2 id="models">Models</h2>
        <p><code>GET /v1/models</code> returns this list live.</p>
        <div class="model-table">
          <table class="table">
            <thead><tr><th>Model ID</th><th>Name</th><th>Best for</th></tr></thead>
            <tbody>
              <tr><td><code>kali-gamma</code></td><td><span class="badge badge-gamma">Kali Gamma</span></td><td>Deep reasoning, multi-step work, large codebases</td></tr>
              <tr><td><code>kali-beta</code></td><td><span class="badge badge-beta">Kali Beta</span></td><td>Everyday coding, writing, extended context</td></tr>
              <tr><td><code>kali-epsilon</code></td><td><span class="badge badge-epsilon">Kali Epsilon</span></td><td>Fast iteration, quick answers</td></tr>
            </tbody>
          </table>
        </div>
      </section>

      {/* Chat completions */}
      <section class="docs-section">
        <h2 id="chat-completions">Chat completions</h2>
        <p><code>POST /v1/chat/completions</code> takes the standard OpenAI request shape. Set <code>stream: true</code> for server-sent events.</p>
        <CodeTabs samples={CHAT_SAMPLES} />

        <h3 id="reasoning">Reasoning / chain-of-thought</h3>
        <p>When a model thinks before answering, stream chunks include <code>reasoning_content</code> deltas before the answer content. The chat UI renders these as a collapsible "Thought process" block.</p>
        <CodeTabs samples={REASONING_SAMPLES} />
      </section>

      {/* Request / Response */}
      <section class="docs-section">
        <h2 id="request-format">Request format</h2>
        <div class="params-table">
          <table class="table">
            <thead><tr><th>Field</th><th>Type</th><th>Required</th><th>Description</th></tr></thead>
            <tbody>
              <tr><td><code>model</code></td><td>string</td><td>yes</td><td><code>kali-gamma</code>, <code>kali-beta</code>, or <code>kali-epsilon</code></td></tr>
              <tr><td><code>messages</code></td><td>array</td><td>yes</td><td>Array of <code>{'{role, content}'}</code> objects. Roles: <code>system</code>, <code>user</code>, <code>assistant</code></td></tr>
              <tr><td><code>stream</code></td><td>boolean</td><td>no</td><td>Default <code>false</code>. When <code>true</code>, response is SSE with <code>data:</code> lines</td></tr>
              <tr><td><code>temperature</code></td><td>number</td><td>no</td><td>Sampling temperature, 0 to 2. Passed through to the model</td></tr>
              <tr><td><code>max_tokens</code></td><td>integer</td><td>no</td><td>Maximum tokens in the response</td></tr>
              <tr><td><code>top_p</code></td><td>number</td><td>no</td><td>Nucleus sampling threshold</td></tr>
              <tr><td><code>stop</code></td><td>string | array</td><td>no</td><td>Stop sequences</td></tr>
            </tbody>
          </table>
        </div>
      </section>

      {/* Response format */}
      <section class="docs-section">
        <h2 id="response-format">Response format</h2>
        <p>Non-streaming responses return standard <code>chat.completion</code> JSON:</p>
        <CodeTabs samples={[{
          id: 'json', label: 'Response', code: `{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "model": "kali-gamma",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "Here is the refactored function...",
      "reasoning_content": "Let me analyze the code..."
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 42,
    "completion_tokens": 128,
    "total_tokens": 170
  }
}`,
        }, {
          id: 'stream', label: 'Stream chunk', code: `data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","model":"kali-gamma","choices":[{"index":0,"delta":{"content":"Here"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","model":"kali-gamma","choices":[{"index":0,"delta":{"content":" is"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","model":"kali-gamma","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{...}}

data: [DONE]`,
        }]} />
      </section>

      {/* Errors */}
      <section class="docs-section">
        <h2 id="errors">Errors &amp; limits</h2>
        <div class="params-table">
          <table class="table">
            <thead><tr><th>Status</th><th>Code</th><th>Meaning</th></tr></thead>
            <tbody>
              <tr><td><code>400</code></td><td><code>bad_request</code></td><td>Malformed JSON or empty messages</td></tr>
              <tr><td><code>401</code></td><td>none</td><td>Missing or expired session token</td></tr>
              <tr><td><code>403</code></td><td><code>email_unverified</code></td><td>Verify your email address first</td></tr>
              <tr><td><code>403</code></td><td><code>banned</code></td><td>Account suspended by admin</td></tr>
              <tr><td><code>404</code></td><td><code>model_not_found</code></td><td>Unknown model id</td></tr>
              <tr><td><code>429</code></td><td><code>rate_limit_exceeded</code></td><td>Daily request cap reached (resets at midnight UTC)</td></tr>
              <tr><td><code>502</code></td><td>none</td><td>The model provider is unavailable</td></tr>
            </tbody>
          </table>
        </div>
        <p>Error bodies follow the OpenAI shape: <code>{`{"error":{"message":"...","type":"invalid_request_error","code":"..."}}`}</code></p>
      </section>

      {/* Endpoints reference */}
      <section class="docs-section">
        <h2 id="endpoints">All endpoints</h2>
        <h3>Chat &amp; models</h3>
        <div class="params-table">
          <table class="table">
            <thead><tr><th>Method</th><th>Path</th><th>Auth</th><th>Description</th></tr></thead>
            <tbody>
              <tr><td><code>POST</code></td><td><code>/v1/chat/completions</code></td><td>required</td><td>Chat completion (stream or JSON)</td></tr>
              <tr><td><code>GET</code></td><td><code>/v1/models</code></td><td>no</td><td>List available models</td></tr>
              <tr><td><code>GET</code></td><td><code>/api/models</code></td><td>no</td><td>Models with availability status</td></tr>
              <tr><td><code>GET</code></td><td><code>/api/usage</code></td><td>required</td><td>Your daily usage + limit</td></tr>
            </tbody>
          </table>
        </div>
        <h3>Keys</h3>
        <p>Create a long-lived key from your <a href="/account">account page</a> or with these calls. A key works anywhere a session token does.</p>
        <div class="params-table">
          <table class="table">
            <thead><tr><th>Method</th><th>Path</th><th>Auth</th><th>Description</th></tr></thead>
            <tbody>
              <tr><td><code>POST</code></td><td><code>/api/auth/login</code></td><td>no</td><td>Sign in, returns a session token</td></tr>
              <tr><td><code>GET</code></td><td><code>/api/account/api-keys</code></td><td>required</td><td>List your keys (prefix only)</td></tr>
              <tr><td><code>POST</code></td><td><code>/api/account/api-keys</code></td><td>required</td><td>Create a key; the secret is returned once</td></tr>
              <tr><td><code>DELETE</code></td><td><code>/api/account/api-keys/:id</code></td><td>required</td><td>Revoke a key</td></tr>
            </tbody>
          </table>
        </div>
        <h3 id="rate-limits">Rate limits</h3>
        <p>Model endpoints allow 120 requests a minute per account with a burst of 120, which is plenty for agent loops that fire many calls in sequence. Sign-in and password endpoints allow 10 a minute per IP. Over the limit you get <code>429</code> with a <code>Retry-After</code> header; the daily request cap is separate and resets at midnight UTC.</p>
      </section>
    </div>
  );
}
