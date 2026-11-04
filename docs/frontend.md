# Frontend

`frontend/` is an Astro 7 site with Preact islands, built to static HTML and served by the Go binary. There is no server-side rendering at runtime.

## Pages and layout

Every page uses `layouts/Base.astro` (head, fonts, the inline theme script, the animated background island, the client router) and `layouts/Shell.astro` (the icon rail on the left, the content column, the shell effects island). Astro's client router turns link clicks into client-side swaps: the rail and the background are persisted islands and stay mounted; only the content column is replaced, with a short fade.

Pages: `/` (landing), `/chat`, `/docs`, `/account`, `/settings`, `/admin`, `/login`, `/register`, `/verify`, `/forgot`, `/privacy`, `/terms`, `404`. The app pages (`chat`, `account`, `settings`, `admin`, auth) are `client:only` Preact components inside the layout with a server-rendered placeholder, so the frame is visible before JavaScript runs.

To add a page: create `src/pages/<name>.astro`, wrap it in `Base` and `Shell` with the right `active` rail item, and either write static markup or mount a Preact component. Add a rail entry in `components/Rail.tsx` if it deserves one.

## Theme and preferences

`lib/theme.ts` holds the theme presets (`estro` is the default; `christmas` and about thirty more are selectable) and resolves them to CSS variables (`--theme-*`, `--shell-*`, `--ui-*`, `--accent`). `lib/preferences.ts` stores the user's choices in `localStorage` under `kali:prefs` (theme, animated background, dim, blur, performance tier, reduced motion, custom accent) and exports `HEAD_SCRIPT`, an inline script in `Base.astro` that applies them to `<html>` before first paint. The settings page is the UI for all of it. Backgrounds live in `components/fx`, one lazy chunk each, tinted from the theme accent.

Styling is plain CSS in `src/styles`: `theme.css` (tokens, fonts), `shell.css` (rail and frame), `ai.css` (the chat app, ported from the reference product with `kali-` class prefixes), `chat-extra.css`, `account.css`, `settings.css`, `docs.css`, `integrations.css`, `fx.css`. Code identifiers such as `kali-` classes, `kali:` storage keys and the `kali_session` cookie are internal names and are not shown to users.

## The chat app

`components/ChatApp.tsx` owns the chat page: the thread list (search, date buckets, inline rename, delete, open in a window), the head (title, usage, Files and Shell toggles, popout), the message column, the composer, the workspace pane and the popout windows.

- **Sessions.** `lib/thread-session.ts` wraps one thread's state and the agent loop (`lib/agent.ts`): send, stream, accumulate reasoning and tool calls, run tools through the workspace, persist every turn through `/api/threads`, stop. The main column and each popout have their own session, so they never share state.
- **Popouts.** `components/chat/Popout.tsx` opens a thread in a Document Picture-in-Picture window, or a sized popup when that API is missing, and portals a compact view into it. Popping out hands the main column a fresh chat; the thread stays in the list marked as popped, with a dock-back action that closes the window and restores it. A thread created for a popout and closed unused is deleted.
- **Composer.** `components/Composer.tsx`: attachments (paperclip, drop, paste; images become image parts, text files are inlined), the model menu (`components/chat/ModelMenu.tsx`), the Configure Tools menu (`components/chat/ToolsMenu.tsx`, which decides which workspace tools go out as `tools`; the unchecked set is stored under `kali:tools-off`), send and stop.
- **Messages.** `components/chat/Messages.tsx` and `Trace.tsx` render user and assistant turns, the thought block with its duration, tool cards with live shell output, attachments and the lightbox. Markdown goes through `lib/markdown.ts` (marked, highlight.js, KaTeX, DOMPurify).
- **Workspace.** `components/workspace/` boots the runtime lazily, exposes the `workspace` API from `lib/workspace-contract.ts` (`isSupported`, `ready`, `tools`, `execute`) and renders `WorkspacePane.tsx` with the Files tab (browse, edit, upload, download, delete) and the Shell tab (xterm sessions). The pane and the thread list are resizable; widths persist under `kali:workspace-width` and `kali:sidebar-width`.
- **Run from docs.** Code samples on the docs and landing pages have a Run button that opens `/chat?run=...`. The chat page creates a fresh chat, opens the shell, makes sure an API key named `Shell` exists (cached under `kali:shell-key`), types a prelude and the sample with the real origin and key substituted, and runs it.

## Other islands

`Account.tsx` (gate when signed out; profile, usage, API keys, security, sessions, danger zone, and the admin card for admins), `SettingsPage.tsx`, `AdminPanel.tsx`, `GatePage.tsx` and `AuthGate.tsx` (login, register, verify, reset), `DocsPage.tsx` with `CodeTabs.tsx` and `Integrations.tsx` (the platform tiles and per-tool configs from `lib/integrations.ts`).

See also: [Architecture](architecture.md), [Development](development.md).
