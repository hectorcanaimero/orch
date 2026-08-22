# Orch Frontend (React SPA spike)

Vite + React 18 + TypeScript SPA for the orch dashboard. Consumes the FastAPI
backend's JSON endpoints (`/stakeholder/summary`, etc.) via `Authorization:
Bearer <token>`. Coexists with the current FastAPI + Jinja dashboard — the SPA
lives in this `frontend/` directory and does not touch anything under
`orchestrator/`.

## Stack

- pnpm (package manager)
- Vite 5 + React 18 + TypeScript 5 (strict mode)
- Tailwind CSS v4 (via `@tailwindcss/vite` plugin — CSS-first config)
- shadcn/ui-style primitives (hand-authored — see note below)
- TanStack Query v5 + React Router v6 + axios + lucide-react

### shadcn note

The spike ships hand-written shadcn/ui-compatible primitives under
`src/components/ui/` (`button`, `card`, `input`, `label`, `progress`, `alert`,
`skeleton`). They match the shadcn New York / zinc style and use the same CSS
variables so the `pnpx shadcn@latest add <component>` CLI can drop in more
components later — the `components.json` at the repo root is already set up.

## Prerequisites

- Node 20+ (tested on Node 22)
- pnpm 10+ (`brew install pnpm` or `npm install -g pnpm`)

## Setup

```bash
cd frontend
cp env.example .env      # copy defaults (VITE_API_BASE_URL=http://127.0.0.1:7420)
pnpm install
pnpm dev                 # opens on http://localhost:5173
```

> Note: the file ships as `env.example` (no leading dot) because the repo's
> permission sandbox blocks writes to `.env*` paths. Copy it to `.env` locally.

## Point at a different backend

Edit `.env`:

```bash
VITE_API_BASE_URL=http://127.0.0.1:7420
```

## Backend requirements

The FastAPI dashboard must be running with the stakeholder profile and CORS
enabled for `http://localhost:5173`:

```bash
ORCH_DASHBOARD_DEV_CORS=1 orch dashboard --profile stakeholder --token <your-token>
```

The `ORCH_DASHBOARD_DEV_CORS=1` env var (separate PR) allows the SPA on
`localhost:5173` to call the backend on `127.0.0.1:7420`. Without it, the
browser will block cross-origin requests.

## End-to-end test flow

1. Start the backend as above.
2. `pnpm dev` in this directory.
3. Open `http://localhost:5173` — you will be redirected to `/login`.
4. Paste the token you passed to `--token` and click "Enter".
5. You should land on the stakeholder summary page, auto-refreshing every N
   seconds (driven by the API's `refresh_interval_s` field).

## Build

```bash
pnpm build      # emits dist/
pnpm preview    # serves dist/ locally
```

## Layout

```
src/
├── main.tsx                     # entry
├── App.tsx                      # QueryClient + Router + routes
├── index.css                    # tailwind v4 + shadcn CSS variables
├── vite-env.d.ts                # env typing
├── lib/
│   ├── api.ts                   # axios instance + Bearer + 401 redirect
│   ├── types.ts                 # /stakeholder/summary types
│   └── utils.ts                 # cn() helper
├── hooks/
│   ├── useAuth.ts               # localStorage token store (useSyncExternalStore)
│   └── useStakeholderSummary.ts # TanStack Query hook, dynamic refetchInterval
├── components/
│   ├── AppLayout.tsx            # sidebar + main content
│   ├── ProtectedRoute.tsx       # redirect to /login when no token
│   └── ui/                      # shadcn primitives
└── pages/
    ├── LoginPage.tsx            # paste-a-token form
    └── StakeholderSummaryPage.tsx
```

## Non-goals for this spike

- No SSR
- No dark mode toggle (CSS variables are in place, no UI switch)
- No real auth (token is pasted, stored in `localStorage`)
- No tests yet — TypeScript strict + `pnpm build` is the guard for now.
