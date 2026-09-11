# F0 — Bootstrap

Fase 0: cimientos del proyecto.

## F0.1 — Package: repo-scaffold

Setup inicial del monorepo.

### F0.1.T1 — Bootstrap: monorepo + Nx

Crear estructura monorepo con Nx workspace, dos apps stub (mobile, web) y
librería compartida.

Debe quedar el CI mínimo funcionando (lint + typecheck).

- **Modelo**: claude-opus-4-7
- **Estimación**: 2d
- **Razón**: Setup crítico, Opus para razonar sobre convenciones.
- **Files**:
  - `nx.json`
  - `package.json`
  - `apps/mobile/project.json`

# F1 — Auth + Marketplace

Fase 1: auth + core del marketplace.

## F1.1 — Package: authentication

### F1.1.T1 — Domain: Auth model

Definir tipos base de auth (User, Session, AuthProvider).

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 4h
- **Razón**: Solo tipos, Sonnet alcanza.
- **Dependencies**: F0.1.T1

### F1.1.T9 — Presentation: AuthSignInScreen

Como usuario, quiero una pantalla de login clara con email/password
+ Apple/Google buttons + link a reset password + link a signup.

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 8h
- **Razón**: UI con lógica visual, Sonnet.
- **Dependencies**: F1.1.T1, F0.1.T1
- **Files**:
  - `lib/features/auth/presentation/screens/auth_sign_in_screen.dart`
  - `lib/features/auth/presentation/widgets/social_login_row.dart`
