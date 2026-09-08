# ongrid web

Single-page React + TypeScript app for ongrid AIOps. Production build is a static
`dist/` that nginx serves with SPA fallback to `index.html`.

## Stack

- Vite 5 + React 18 + TypeScript 5 (strict)
- Tailwind CSS v3 (light/dark theme tokens)
- React Router v6
- Zustand (auth + UI state, persisted to localStorage)
- Native `fetch` (no axios)
- `lucide-react` icons, `recharts` charts
- `react-markdown` + `remark-gfm` for assistant messages

## Frontend design

Read the [frontend design language and development guide](../docs/design/frontend-design-language.md)
before adding or changing UI. It covers page structure, theme tokens, shared controls,
form and filter layouts, accessibility, localization, and review checks.
Shared controls live in [src/components/ui](src/components/ui/); complex interactions
use Base UI with shadcn-style presentation adapted to Ongrid.

## Build

```bash
cd web
npm ci          # or `npm install`
npm run build   # produces dist/
```

Operators run `npm ci && npm run build` in the build pipeline; nginx serves the
resulting `dist/` directly with `try_files $uri /index.html;` for SPA fallback.

## Dev

```bash
npm run dev     # http://localhost:5173, proxies /api → http://localhost:8090
```

The Vite dev server proxies `/api/*` to the local manager via `vite.config.ts`
(`server.proxy`). In production, nginx forwards `/api/*` to the manager.

## Lint

```bash
npm run lint
```

## API contract

All requests are relative (`/api/v1/...`):

- Auth
  - `POST /api/v1/auth/login` → `{ access_token, refresh_token?, user: { email, role } }`
  - `POST /api/v1/auth/refresh`
  - `GET  /api/v1/auth/self`
- Edges
  - `GET    /api/v1/edges` → `{ edges: Edge[] }`
  - `GET    /api/v1/edges/:id`
  - `POST   /api/v1/edges` → returns one-time `secret_key`
  - `DELETE /api/v1/edges/:id`
  - `POST   /api/v1/edges/:id/rotate-secret`
  - `GET    /api/v1/edges/:id/metrics?from&to&resolution`
- Chat
  - `GET  /api/v1/chat/sessions`
  - `POST /api/v1/chat/sessions` → `{ id, title, ... }`
  - `GET  /api/v1/chat/sessions/:id/messages`
  - `POST /api/v1/chat/sessions/:id/messages` → `{ assistant_message, user_message? }`
  - `POST /api/v1/chat/sessions/:id/close`

The auth token is sent as `Authorization: Bearer <jwt>` and persisted in
localStorage under `ongrid.auth`. On any 401, the client clears the session and
the router redirects to `/login`.

## Routes

- `/login`            — login form
- `/`                 — home with chat input + sample prompts
- `/chat/:sessionId`  — chat thread
- `/edges`            — edge list (auto-refresh 10s)
- `/edges/:edgeId`    — edge detail (metrics tab auto-refresh 30s)
