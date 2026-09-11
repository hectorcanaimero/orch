---
type: spec
project_id: sample-project
phase: 1
package: authentication
version: 0.1
depends_on:
  - arch/001-auth.md
consumed_by:
  - orch-atomizer
generated_by: orch-spec
generated_at: 2026-08-23
title: authentication Package Spec (F1.1)
---

# F1 — Auth core

Fase 1: autenticación y gestión de sesión.

## F1.1 — Package: authentication

Todo lo relacionado con onboarding y sesión de usuario.

### F1.1.T1 — Domain: Auth model

Definir tipos base de auth (User, Session, AuthProvider).

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 4h
- **Razón**: Solo tipos, Sonnet alcanza.
- **Dependencies**: F0.1.T1
- **Files**:
  - `lib/features/auth/domain/models/user.dart`
  - `lib/features/auth/domain/models/session.dart`

### F1.1.T2 — Application: Auth use cases

Implementar use cases SignIn, SignOut, RefreshToken.

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 8h
- **Razón**: Orchestration con manejo de errores, Sonnet.
- **Dependencies**: F1.1.T1
- **Files**:
  - `lib/features/auth/application/use_cases/sign_in_use_case.dart`
  - `lib/features/auth/application/use_cases/sign_out_use_case.dart`

### F1.1.T3 — Presentation: Auth SignIn Screen

Como usuario quiero firmar con email+password y recibir un token.

Casos cubiertos:
- Happy path: credenciales válidas → token + navegación
- Edge: credenciales inválidas → error visible
- Edge: red caída → retry visible

- **Modelo**: claude-opus-4-7
- **Estimación**: 2d
- **Razón**: UI con lógica visual + manejo de errores complejos, Opus.
- **Dependencies**: F1.1.T2, F1.1.T1
- **Files**:
  - `lib/features/auth/presentation/screens/sign_in_screen.dart`
  - `lib/features/auth/presentation/widgets/auth_form.dart`
