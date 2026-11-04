// Setup snippets for every platform Estro plugs into. Snippets are data: the
// base URL and key are placeholders the reader swaps for their own host/token.

export const BASE_HOST = 'https://estro.help';
export const BASE_URL = `${BASE_HOST}/v1`;
export const KEY = 'estro_YOUR_TOKEN';
export const KEY_ENV = 'ESTRO_TOKEN';

export interface ModelRow { id: string; name: string }
export const MODELS: ModelRow[] = [
  { id: 'kali-gamma', name: 'Kali Gamma' },
  { id: 'kali-beta', name: 'Kali Beta' },
  { id: 'kali-epsilon', name: 'Kali Epsilon' },
];
const first = MODELS[0].id;
const idList = MODELS.map((m) => m.id).join('\n');

export interface IntegrationConfig { title: string; language: string; code: string }
export interface Integration { id: string; label: string; icon: string; description: string; configs: IntegrationConfig[] }

const vscodeChatLanguageModels = JSON.stringify([{
  name: 'Estro', vendor: 'customendpoint', apiType: 'responses', apiKey: KEY,
  models: MODELS.map((m) => ({ id: m.id, name: m.name, url: `${BASE_URL}/responses`, toolCalling: true, vision: true, reasoningEffortFormat: 'responses', supportsReasoningEffort: ['low', 'medium', 'high'] })),
}], null, 2);

const opencodeProvider = JSON.stringify({
  $schema: 'https://opencode.ai/config.json',
  provider: { estro: { npm: '@ai-sdk/openai', name: 'Estro', options: { baseURL: BASE_URL, apiKey: KEY }, models: Object.fromEntries(MODELS.map((m) => [m.id, { name: m.name, tool_call: true, reasoning: true, attachment: true }])) } },
  model: `kali/${first}`,
}, null, 2);

const cursorByokConfig = JSON.stringify({
  baseURL: 'https://api2.cursor.sh',
  modelAdapters: MODELS.map((m) => ({ displayName: m.name, type: 'openai', baseURL: BASE_URL, apiKey: KEY, modelID: m.id, supportedThinkingLevels: ['low', 'medium', 'high'] })),
}, null, 2);

const continueYaml = `name: Estro\nversion: 0.0.1\nschema: v1\nmodels:\n` + MODELS.map((m) => `  - name: "${m.name}"
    provider: openai
    model: "${m.id}"
    apiBase: ${BASE_URL}/
    apiKey: "${KEY}"
    roles:
      - chat
      - edit
      - apply
    capabilities:
      - tool_use
      - image_input`).join('\n');

const kiloConfig = JSON.stringify({
  provider: { estro: { npm: '@ai-sdk/anthropic', name: 'Estro', options: { baseURL: BASE_URL, apiKey: KEY }, models: Object.fromEntries(MODELS.map((m) => [m.id, { name: m.name }])) } },
  model: `kali/${first}`,
}, null, 2);

const zedSettings = JSON.stringify({
  language_models: { openai_compatible: { kali: { api_url: BASE_URL, available_models: MODELS.map((m) => ({ name: m.id, display_name: m.name, max_tokens: 128000, capabilities: { chat_completions: false } })) } } },
}, null, 2);

const openclawSetup = `openclaw config set models.providers.kali \\
  '{"baseUrl":"${BASE_URL}","apiKey":"${KEY}","api":"openai-responses"}' \\
  --strict-json --merge

openclaw config set models.providers.kali.models \\
  '${JSON.stringify(MODELS.map((m) => ({ id: m.id, name: m.name })))}' \\
  --strict-json --merge

openclaw models set kali/${first}`;

const anthropicPanel = (title: string) => ({ title, language: 'text', code: `base url:  ${BASE_HOST}\napi key:   ${KEY}\nmodel id:  ${first}` });

export const INTEGRATIONS: Integration[] = [
  { id: 'opencode', label: 'opencode', icon: 'opencode', description: 'Adds Estro as an OpenAI-compatible provider in opencode.json.',
    configs: [{ title: '~/.config/opencode/opencode.json', language: 'json', code: opencodeProvider }] },
  { id: 'continue', label: 'continue', icon: 'continue', description: 'The YAML file is the current schema. VS Code and JetBrains read the same file.',
    configs: [{ title: '~/.continue/config.yaml', language: 'yaml', code: continueYaml }] },
  { id: 'vscode', label: 'vs code', icon: 'vscode', description: 'Native BYOK through Chat: Manage Language Models. No extension and no GitHub sign-in needed.',
    configs: [
      { title: 'chatLanguageModels.json', language: 'json', code: vscodeChatLanguageModels },
      { title: 'settings.json (only if signed out of GitHub)', language: 'json', code: `"chat.utilityModel": "${first}",\n"chat.utilitySmallModel": "kali-epsilon"` },
    ] },
  { id: 'cursor', label: 'cursor', icon: 'cursor', description: "Chat models through Cursor's OpenAI API key override. Tab completion stays on Cursor's own models.",
    configs: [
      { title: 'Settings → Models → API Keys', language: 'text', code: `openai api key:  ${KEY}\nbase url:        ${BASE_URL}` },
      { title: 'Settings → Models: add each model and leave its toggle on', language: 'text', code: idList },
    ] },
  { id: 'cursorbyok', label: 'cursor-byok', icon: 'cursorbyok', description: 'Registers each model as its own adapter in the cursor-byok config file, so every model shows in the picker.',
    configs: [{ title: '~/.config/cursor-byok/config.json', language: 'json', code: cursorByokConfig }] },
  { id: 'windsurf', label: 'windsurf', icon: 'windsurf', description: "Cascade's OpenAI-compatible provider. If newer builds hide this panel, use Continue or Cline.",
    configs: [{ title: 'Settings → Cascade → Model providers → OpenAI compatible', language: 'text', code: `base url:  ${BASE_URL}\napi key:   ${KEY}\nmodel:     ${first}` }] },
  { id: 'cline', label: 'cline', icon: 'cline', description: 'Anthropic provider with a custom base URL. The IDE, CLI and SDK share ~/.cline/data/settings/providers.json.',
    configs: [anthropicPanel('Settings → API Provider → Anthropic, then tick Use custom base URL'), { title: 'Models: add each model as its own entry', language: 'text', code: idList }] },
  { id: 'roo', label: 'roo', icon: 'roo', description: 'Roo Code and its community fork Zoo Code take the same settings.',
    configs: [anthropicPanel('Settings → Providers → Anthropic, then tick Use custom base URL'), { title: 'Model configuration: add each model', language: 'text', code: idList }] },
  { id: 'kilo', label: 'kilo', icon: 'kilo', description: 'Global config at ~/.config/kilo/kilo.jsonc, read by the CLI, VS Code and JetBrains. Keep the key there, not in a committed project file.',
    configs: [{ title: '~/.config/kilo/kilo.jsonc', language: 'json', code: kiloConfig }] },
  { id: 'claudecode', label: 'claude', icon: 'claudecode', description: 'Anthropic-compatible endpoint. Claude Code adds /v1/messages itself, so use the bare host. ANTHROPIC_API_KEY works in place of ANTHROPIC_AUTH_TOKEN.',
    configs: [
      { title: 'Shell', language: 'bash', code: `export ANTHROPIC_BASE_URL=${BASE_HOST}\nexport ANTHROPIC_AUTH_TOKEN="$${KEY_ENV}"\nclaude --model ${first}` },
      { title: '~/.claude/settings.json (applies however claude is launched)', language: 'json', code: `{\n  "env": {\n    "ANTHROPIC_BASE_URL": "${BASE_HOST}",\n    "ANTHROPIC_AUTH_TOKEN": "${KEY}"\n  },\n  "model": "${first}"\n}` },
    ] },
  { id: 'codex', label: 'codex', icon: 'codex', description: `Set the provider in config.toml. The key is read from the ${KEY_ENV} environment variable and never written to the file.`,
    configs: [
      { title: '~/.codex/config.toml', language: 'toml', code: `model = "${first}"\nmodel_provider = "estro"\nmodel_reasoning_effort = "high"\nmodel_verbosity = "medium"\n\n[model_providers.estro]\nname = "Estro"\nbase_url = "${BASE_URL}"\nenv_key = "${KEY_ENV}"` },
      { title: 'Shell', language: 'bash', code: `export ${KEY_ENV}="${KEY}"` },
    ] },
  { id: 'hermes', label: 'hermes', icon: 'hermes', description: 'Run the setup wizard, or write the files by hand. The key lives in ~/.hermes/.env, not in config.yaml.',
    configs: [
      { title: 'Setup wizard: choose custom endpoint', language: 'text', code: `hermes setup\n\nbase url:       ${BASE_URL}\napi key:        ${KEY}\ndefault model:  ${first}` },
      { title: '~/.hermes/.env (owner-only, chmod 0600)', language: 'env', code: `${KEY_ENV}=${KEY}` },
      { title: '~/.hermes/config.yaml', language: 'yaml', code: `model:\n  provider: kali\n  default: ${first}\n  base_url: ${BASE_URL}\n  api_mode: anthropic_messages\n\nproviders:\n  kali:\n    api: ${BASE_URL}\n    api_key: \${${KEY_ENV}}\n    transport: anthropic_messages` },
      { title: 'OpenAI-compatible route (swap both fields)', language: 'yaml', code: `model:\n  provider: kali\n  default: ${first}\n  base_url: ${BASE_URL}\n  api_mode: chat_completions\n\nproviders:\n  kali:\n    api: ${BASE_URL}\n    api_key: \${${KEY_ENV}}\n    transport: openai_chat` },
      { title: 'Switch model for one run, or inside a session', language: 'text', code: `hermes chat --model ${first}\n/model kali-beta --provider kali\n/model kali-beta --provider kali --global   (persists)\n/model kali-beta --provider kali --once     (reverts after one turn)\n\nCLI flags beat config.yaml, which beats .env.` },
    ] },
  { id: 'openclaw', label: 'openclaw', icon: 'openclaw', description: 'Two commands register the provider and its models. Validate the config before you start the gateway.',
    configs: [
      { title: 'Register the provider and models', language: 'bash', code: openclawSetup },
      { title: 'Check before you start the gateway', language: 'bash', code: `openclaw config validate\nopenclaw models list --provider kali\nopenclaw gateway run` },
    ] },
  { id: 'aider', label: 'aider', icon: 'aider', description: 'Environment variables for one session, or a persistent YAML config that also declares reasoning effort.',
    configs: [
      { title: 'Shell', language: 'bash', code: `export OPENAI_API_BASE=${BASE_URL}\nexport OPENAI_API_KEY="$${KEY_ENV}"\naider --model openai/${first}` },
      { title: '~/.aider.conf.yml', language: 'yaml', code: `openai-api-base: ${BASE_URL}\nopenai-api-key: ${KEY}\nmodel: openai/${first}\nreasoning-effort: high` },
      { title: '.aider.model.settings.yml', language: 'yaml', code: `- name: openai/${first}\n  accepts_settings: ["reasoning_effort"]` },
    ] },
  { id: 'zed', label: 'zed', icon: 'zed', description: `Uses the Responses API. Zed reads the key from the system keychain or ${KEY_ENV}, not from settings.json.`,
    configs: [{ title: '~/.config/zed/settings.json', language: 'json', code: zedSettings }] },
  { id: 'jetbrains', label: 'jetbrains', icon: 'jetbrains', description: 'AI Assistant has a built-in OpenAI-compatible provider (2026.2+). If the test fails, try the base URL without /v1.',
    configs: [
      { title: 'AI Assistant → Third-party AI providers', language: 'text', code: `provider:  OpenAI-compatible\nbase url:  ${BASE_URL}\napi key:   ${KEY}` },
      { title: 'Any other OpenAI-compatible plugin', language: 'text', code: `base url:  ${BASE_URL}\napi key:   ${KEY}\nmodel:     ${first}` },
    ] },
  { id: 'neovim', label: 'neovim', icon: 'neovim', description: 'Each plugin wants a different URL shape. Export the key once, then point each plugin at it.',
    configs: [
      { title: 'Shell', language: 'bash', code: `export ${KEY_ENV}="${KEY}"` },
      { title: 'avante.nvim (endpoint keeps /v1)', language: 'lua', code: `require("avante").setup({\n  provider = "kali",\n  providers = {\n    kali = {\n      __inherited_from = "openai",\n      endpoint = "${BASE_URL}",\n      model = "${first}",\n      api_key_name = "${KEY_ENV}",\n      extra_request_body = {\n        reasoning_effort = "medium"\n      }\n    },\n    ["kali-claude"] = {\n      __inherited_from = "claude",\n      endpoint = "${BASE_HOST}",\n      model = "${first}",\n      api_key_name = "${KEY_ENV}"\n    }\n  }\n})` },
      { title: 'codecompanion.nvim (bare host, path supplied separately)', language: 'lua', code: `require("codecompanion").setup({\n  adapters = {\n    http = {\n      kali = function()\n        return require("codecompanion.adapters").extend("openai_compatible", {\n          env = {\n            url = "${BASE_HOST}",\n            api_key = "${KEY_ENV}",\n            chat_url = "/v1/chat/completions"\n          },\n          schema = { model = { default = "${first}" } }\n        })\n      end\n    }\n  },\n  interactions = {\n    chat = { adapter = "kali" },\n    inline = { adapter = "kali" }\n  }\n})` },
      { title: 'gp.nvim (full endpoint URLs)', language: 'lua', code: `require("gp").setup({\n  providers = {\n    openai = {\n      endpoint = "${BASE_URL}/chat/completions",\n      secret = os.getenv("${KEY_ENV}")\n    },\n    anthropic = {\n      endpoint = "${BASE_URL}/messages",\n      secret = os.getenv("${KEY_ENV}")\n    }\n  },\n  agents = {\n    {\n      name = "Estro",\n      provider = "openai",\n      chat = true,\n      command = true,\n      model = { model = "${first}" },\n      system_prompt = "You are a helpful coding assistant."\n    }\n  }\n})` },
    ] },
  { id: 'librechat', label: 'librechat', icon: 'librechat', description: 'A custom endpoint in librechat.yaml. The OpenAI format comes first, with an Anthropic-format entry after it.',
    configs: [
      { title: 'librechat.yaml (OpenAI format)', language: 'yaml', code: `endpoints:\n  custom:\n    - name: "Estro"\n      apiKey: "${KEY}"\n      baseURL: "${BASE_URL}"\n      models:\n        default: ["${first}"]\n        fetch: true\n      titleConvo: true\n      titleModel: "kali-epsilon"\n      customParams:\n        reasoningFormat: reasoning_effort\n        reasoningKey: reasoning_content\n        includeReasoningContent: true` },
      { title: `librechat.yaml (key from the ${KEY_ENV} variable)`, language: 'yaml', code: `endpoints:\n  custom:\n    - name: "Estro"\n      apiKey: "\${${KEY_ENV}}"\n      baseURL: "${BASE_URL}"\n      models:\n        default: ["${first}"]\n        fetch: true\n      titleConvo: true\n      titleTiming: "immediate"\n      titleModel: "current_model"\n      modelDisplayLabel: "Estro"` },
      { title: 'librechat.yaml (Anthropic format, add under custom)', language: 'yaml', code: `    - name: "Estro (anthropic)"\n      provider: "anthropic"\n      apiKey: "\${${KEY_ENV}}"\n      baseURL: "${BASE_HOST}"\n      models:\n        default: ["${first}"]\n        fetch: false\n      titleConvo: true\n      modelDisplayLabel: "Estro (anthropic)"` },
    ] },
  { id: 'openwebui', label: 'open webui', icon: 'openwebui', description: 'Set the connection in the admin panel, or set the same values at deploy time. Models are discovered from /v1/models.',
    configs: [
      { title: 'Admin Panel → Settings → Connections → OpenAI API', language: 'text', code: `api base url:  ${BASE_URL}\napi key:       ${KEY}` },
      { title: 'Deploy-time environment variables', language: 'env', code: `ENABLE_OPENAI_API=true\nOPENAI_API_BASE_URL=${BASE_URL}\nOPENAI_API_KEY=\${${KEY_ENV}}` },
      { title: 'Several backends (semicolon-separated, in matching order)', language: 'env', code: `OPENAI_API_BASE_URLS=${BASE_URL};http://other-backend:8000/v1\nOPENAI_API_KEYS=\${${KEY_ENV}};key-for-other-backend` },
    ] },
  { id: 'chatbox', label: 'chatbox', icon: 'chatbox', description: 'Add a custom OpenAI API Compatible provider, or an Anthropic provider.',
    configs: [
      { title: 'Settings → Model provider → Add → OpenAI API Compatible', language: 'text', code: `api host:   ${BASE_URL}/\napi path:   /chat/completions\napi key:    ${KEY}` },
      { title: 'Models → New: model id', language: 'text', code: `model id:  ${first}` },
      { title: 'Anthropic provider instead', language: 'text', code: `api host:   ${BASE_HOST}\napi key:    ${KEY}` },
    ] },
  { id: 'sdk', label: 'sdks', icon: 'sdk', description: 'OpenAI SDK settings with the Responses API preferred, plus the environment for any Anthropic SDK.',
    configs: [
      { title: 'OpenAI SDKs: environment', language: 'bash', code: `export OPENAI_BASE_URL=${BASE_URL}\nexport OPENAI_API_KEY="$${KEY_ENV}"` },
      { title: 'Python (OpenAI SDK)', language: 'python', code: `from openai import OpenAI\n\nclient = OpenAI()  # reads OPENAI_BASE_URL / OPENAI_API_KEY\nresp = client.responses.create(model="${first}", input="hi")\nprint(resp.output_text)` },
      { title: 'Go (openai-go)', language: 'go', code: `client := openai.NewClient()  // reads OPENAI_BASE_URL / OPENAI_API_KEY\nresp, err := client.Responses.New(ctx, responses.ResponseNewParams{\n    Model: "${first}",\n    Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("hi")},\n})` },
      { title: 'Any Anthropic SDK: environment', language: 'bash', code: `export ANTHROPIC_BASE_URL=${BASE_HOST}\nexport ANTHROPIC_API_KEY="$${KEY_ENV}"` },
    ] },
  { id: 'other', label: 'other', icon: 'openai', description: 'Endpoint reference and curl examples for each wire format.',
    configs: [
      { title: 'Endpoint reference', language: 'text', code: `openai base url:     ${BASE_URL}\nanthropic base url:  ${BASE_HOST}\napi key:             ${KEY}\nmodel:               ${first}` },
      { title: 'curl: OpenAI Responses', language: 'bash', code: `curl ${BASE_URL}/responses \\\n  -H "Authorization: Bearer $${KEY_ENV}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${first}","input":"hi"}'` },
      { title: 'curl: OpenAI chat completions', language: 'bash', code: `curl ${BASE_URL}/chat/completions \\\n  -H "Authorization: Bearer $${KEY_ENV}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${first}","messages":[{"role":"user","content":"hi"}]}'` },
      { title: 'curl: Anthropic Messages', language: 'bash', code: `curl ${BASE_URL}/messages \\\n  -H "x-api-key: $${KEY_ENV}" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${first}","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}'` },
    ] },
];

export const iconUrl = (i: Integration) => `/icons/ide/${i.icon}.svg`;
export const anchorFor = (id: string) => `integrations-${id}`;
