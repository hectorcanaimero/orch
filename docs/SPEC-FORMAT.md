# Spec Format — Atomizer

Este documento describe el formato de specs markdown que el módulo
`orchestrator.atomize` sabe parsear para generar / actualizar `tasks.json`.

El parser vive en `atomize.py::parse_spec_file`. Regla de oro: **si algo no
matchea el formato, se ignora silenciosamente** (excepto los campos con label
desconocido dentro de una task, que emiten warning).

---

## Jerarquía de headers

Tres niveles obligatorios: **fase → paquete → task**.

| Nivel   | Sintaxis                          | Ejemplo                                  | Efecto                                   |
| ------- | --------------------------------- | ---------------------------------------- | ---------------------------------------- |
| Fase    | `# F<n> — <título>`               | `# F1 — Auth + Marketplace core`         | Setea `task.phase = <n>` para las tasks |
| Paquete | `## F<n>.<pkg> — <título>`        | `## F1.1 — Package: authentication`      | Sólo agrupamiento visual                 |
| Task    | `### F<n>.<pkg>.T<n> — <título>`  | `### F1.1.T9 — Presentation: SignIn`     | Crea una task. ID = `F<n>.<pkg>.T<n>`   |

Reglas:

- El separador entre `F<n>...` y el título puede ser `—` (guión largo), `–`
  o `-` (guión corto). Todos los espacios alrededor son tolerados.
- Los números son enteros (`F1`, no `F01`). Un `phase` es un `int`.
- El `title` de una task es todo lo que va después del `—`.

## Campos declarativos (bullets)

Dentro de una task, cada campo va como bullet:

```markdown
- **Modelo**: claude-sonnet-4-6
- **Estimación**: 8h
- **Razón**: Explicación corta del "por qué este modelo".
- **Dependencies**: F1.1.T8, F1.1.T5
- **Files**:
  - `lib/features/auth/presentation/screens/auth_sign_in_screen.dart`
  - `lib/features/auth/presentation/widgets/social_login_row.dart`
```

### Labels aceptados (case-insensitive)

| Label ES        | Label EN       | Campo destino     |
| --------------- | -------------- | ----------------- |
| `Modelo`        | `Model`        | `model`           |
| `Estimación`    | `Estimate`     | `estimateHours`   |
| `Razón`         | `Reason`       | `reason`          |
| `Dependencies`  | `Deps`         | `dependencies`    |
| `Files`         | `Archivos`     | `files`           |

Cualquier otro label emite warning y se ignora.

### Parseo de valores

- **Estimación**: soporta `8h`, `1.5h`, `30m`, `2d` (donde `d` = 8h laborales).
  Sin unidad se asume horas. Basura → `0.0`.
- **Dependencies**: lista separada por `,` o `;`. NO se valida que existan.
- **Files**: dos formatos:
  - Inline: `- **Files**: \`lib/x.dart\``
  - Sub-bullets: bullet parent sin valor, seguido de bullets anidados con
    backticks alrededor del path. Los sub-bullets siguen hasta que aparece
    otro bullet de campo o un header.

## Descripción

Todo texto entre el header de task y el primer bullet `- **Campo**` (o el
próximo header) es la `description`. Se preserva multiline, útil para user
stories:

```markdown
### F1.1.T9 — Presentation: AuthSignInScreen

Como usuario, quiero una pantalla de login clara con email/password
+ Apple/Google buttons + link a reset password + link a signup.

- **Modelo**: claude-sonnet-4-6
```

## `spec_ref` auto-generado

El parser calcula `specRef = "<relpath desde docs/>#<task-id>"`.

Ejemplo: si el spec vive en `/proyecto/docs/f1-auth.md` y `--specs-dir` es
`/proyecto/docs`, entonces:

```
specRef = "f1-auth.md#F1.1.T9"
```

Si el spec cae fuera de `--specs-dir` (edge: `--file /otro/lado.md`), se usa
sólo el basename.

## Defaults para tasks nuevas

Cuando el atomizer crea una task NUEVA (ID no existe en `tasks.json`):

| Campo      | Default           |
| ---------- | ----------------- |
| `status`   | `"backlog"`       |
| `files`    | `[]` (o lo parseado del spec, si vino)   |
| `comments` | `[]`              |

Los defaults de `files` y `comments` para tasks NUEVAS incluyen lo que venga
del spec. Para tasks EXISTENTES, `files` y `comments` **NUNCA se pisan** —
son runtime state.

## Regla de merge (crítica)

Al mergear contra `tasks.json` existente:

- **Task nueva** (ID no está en `tasks.json`) → append al final.
- **Task existente** (ID ya está):
  - Se **actualizan**: `title`, `description`, `model`, `reason`,
    `dependencies`, `estimateHours`, `specRef`, `phase`.
  - Se **preservan sin tocar**: `status`, `comments`, `files`.
- **Task huérfana** (existe en `tasks.json` pero no en specs actuales):
  - **NUNCA se borra**. Se lista en el diff como "huérfana".
  - Puede ser una task removida de la spec (limpiala a mano si querés) o
    simplemente no está incluida en este scan (ej: pasaste `--file` con un
    solo archivo).
- **IDs viejos** (`R-001`, `B-003`, formato pre-atomizer) coexisten con los
  nuevos. El parser sólo genera IDs `F<n>.<pkg>.T<n>` — nunca los toca.

## Convivencia con IDs legacy

Tasks preexistentes con IDs tipo `R-001` (formato viejo de rupies) NUNCA son
tocadas por el atomizer:

- No se detectan como duplicadas (el parser sólo genera `F<n>.<pkg>.T<n>`).
- Se listan como huérfanas de formato legacy en el diff (sección separada
  para no ensuciar el output).
- Sus campos (`status`, deps, files, etc) quedan tal cual.

## Ejemplo completo

```markdown
# F1 — Auth + Marketplace core

Fase inicial: auth + core del marketplace.

## F1.1 — Package: authentication

Todo lo relacionado con onboarding y sesión de usuario.

### F1.1.T9 — Presentation: AuthSignInScreen

Como usuario, quiero una pantalla de login clara con email/password
+ Apple/Google buttons + link a reset password + link a signup.

Debe cumplir la guía visual del design system y ser accesible (WCAG AA).

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 8h
- **Razón**: UI con lógica visual, Sonnet.
- **Dependencies**: F1.1.T8, F1.1.T5
- **Files**:
  - `lib/features/auth/presentation/screens/auth_sign_in_screen.dart`
  - `lib/features/auth/presentation/widgets/social_login_row.dart`

### F1.1.T10 — Presentation: AuthSignUpScreen

Similar a T9, pero flujo de signup.

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 6h
- **Razón**: UI signup.
- **Dependencies**: F1.1.T8
```

Este spec produce dos tasks (`F1.1.T9`, `F1.1.T10`), ambas en `phase = 1`,
con `specRef = "<file>.md#<id>"`.

## Limitaciones conocidas del MVP

- No soporta multi-unidad en estimación (`2d 4h` → toma sólo `2d`).
- No soporta headers de nivel > 3 dentro de una task (H4 se traga como
  parte de la description o queda ignorado según posición).
- No soporta parseo de fields dentro de code blocks (`triple backticks`
  con `- **Campo**` adentro se parsean igual — evitalo).
- El parser es tolerante: labels desconocidos → warning + skip. NO falla.
- Si dos specs definen la misma task ID, gana el que se procesó último
  (walking lexicográfico → el "más nuevo" en orden alfabético).
