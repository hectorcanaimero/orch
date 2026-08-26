"""Project path resolution — Fase 1 + 2 del refactor multi-proyecto.

Antes: todo el orquestador asumía `pwd == v2/` (rupies). Los paths a
`tasks.json`, `config.yaml`, `orchestrator/model_router.yaml`, `state/` y
`scripts/task-*.sh` se resolvían relativos a `Path.cwd()` de forma implícita.

Ahora: `ProjectPaths` centraliza la resolución. El CLI puede pasar
`--project-root` (o setear `ORCH_PROJECT_ROOT`) y todos los paths se derivan
de ahí. Si NO se pasa nada, se cae al comportamiento histórico
(`Path.cwd()`) — retrocompatible con rupies sin cambios.

Este módulo NO toca disco por sí solo salvo para chequear existencia en
`ProjectPaths.ensure_valid()`. No lee YAML/JSON.

Fase 1 alcance:
    - `project_root`: raíz del proyecto (donde vive `tasks.json` +
      `scripts/task-start.sh`).
    - `project_id`: identificador humano/telemetría (default = basename del
      root, con heurística para saltar `v2/`, `app/`, etc.).
    - Rutas derivadas (`tasks_json`, `config_yaml`, `router_yaml`,
      `state_dir`).

Fase 2 alcance (este archivo):
    - `state_layout`: `"legacy"` (rupies cwd fallback) vs `"namespaced"`
      (`--project-root` o env explícito). `state_dir` se computa según el
      layout: legacy usa `<root>/.orchestrator/state/`, namespaced usa
      `<root>/.orchestrator/state/<project_id>/`. NO auto-migra archivos.
    - `explicit_root`: expuesto para que sitios río abajo puedan decidir
      qué layout aplicar (por ejemplo, para skips de guards históricos).

Lo que NO hace este módulo (Fase 3):
    - `_SPEC_ROOT` sigue hardcodeado en `prompt_builder.py` — Fase 3.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Literal


StateLayout = Literal["legacy", "namespaced"]


# Basenames "genéricos" que NO sirven como `project_id`: si el project_root
# termina con uno de estos, el default toma el basename del PADRE. Cubre
# layouts tipo `<proyecto>/v2/`, `<proyecto>/app/`, `<proyecto>/src/`.
_GENERIC_BASENAMES: frozenset[str] = frozenset({
    "v2",
    "v3",
    "app",
    "apps",
    "src",
    "workspace",
    "repo",
    "root",
})


def _default_project_id(root: Path) -> str:
    """Derivá un `project_id` razonable del basename de `root`.

    Si el basename cae en `_GENERIC_BASENAMES`, subimos un nivel para
    encontrar algo más descriptivo. Ejemplo real: `/rupies/v2` → `rupies`.

    Si `root` es la raíz del filesystem (sin padre útil), devolvemos
    literalmente `"unknown"` — algo tiene que quedar loggeable.
    """
    root = root.resolve()
    name = root.name
    if name in _GENERIC_BASENAMES and root.parent != root:
        parent_name = root.parent.name
        if parent_name:
            return parent_name
    return name or "unknown"


@dataclass(frozen=True)
class ProjectPaths:
    """Snapshot inmutable de los paths que el orquestador necesita resolver.

    Se construye una sola vez al inicio de `main()` y se pasa por argumento
    a los sitios que lo requieran (no vive como singleton — testabilidad).

    Fase 2:
        - `explicit_root`: True si el usuario indicó `--project-root` (flag)
          o `ORCH_PROJECT_ROOT` (env). False si el root vino del fallback a
          `Path.cwd()` (modo rupies clásico).
        - `state_layout`: "legacy" | "namespaced" — derivado de `explicit_root`.
          Determina la forma de `state_dir`:
            legacy      → `<root>/.orchestrator/state/`      (retrocompat)
            namespaced  → `<root>/.orchestrator/state/<project_id>/`

    Sprint F-2 — Worktree isolation:
        - `override_root`: when set to a worktree path, project files
          (tasks_json, scripts_dir, router_yaml) resolve inside the worktree.
          state_dir ALWAYS uses project_root (shared across worktrees).
          None (default) → backward compat, all paths use project_root.
    """

    project_root: Path
    project_id: str
    # Path al config.yaml — puede quedar afuera de `project_root` si el usuario
    # pasa `--config /otro/lado.yaml`. Guardamos el que EFECTIVAMENTE se usó.
    config_yaml: Path
    explicit_root: bool = False
    state_layout: StateLayout = "legacy"
    override_root: Path | None = None

    @property
    def tasks_json(self) -> Path:
        return (self.override_root or self.project_root) / "tasks.json"

    @property
    def router_yaml(self) -> Path:
        return (self.override_root or self.project_root) / ".orchestrator" / "model_router.yaml"

    @property
    def state_dir(self) -> Path:
        """State dir según layout (Fase 2).

        - `legacy` → `<root>/.orchestrator/state/` (comportamiento histórico
          rupies — cuando NO se pasó `--project-root`/`ORCH_PROJECT_ROOT`).
        - `namespaced` → `<root>/.orchestrator/state/<project_id>/` (cuando
          el root fue explícito — permite multi-project sin colisiones de
          `run-*.json`, `events-*.jsonl`, `.lock`, etc.).

        NUNCA migra archivos: si un proyecto arrancó en `legacy` y luego
        pasás `--project-root`, los archivos viejos quedan donde estaban.
        """
        base = self.project_root / ".orchestrator" / "state"
        if self.state_layout == "namespaced":
            return base / self.project_id
        return base

    @property
    def scripts_dir(self) -> Path:
        return (self.override_root or self.project_root) / "scripts"

    def ensure_valid(self) -> None:
        """Chequeo defensivo: `tasks.json` y `scripts/task-start.sh` presentes.

        No valida el config_yaml — su presencia ya se chequea en `_load_config`
        con un mensaje de error dedicado. Este método imita el guard histórico
        `_ensure_v2_cwd()` pero contra el `project_root` explícito.

        Fase 2: si el layout es `namespaced`, creamos el `state_dir` al vuelo
        (`mkdir -p`) — así los primeros writes de `EventLog` / `SpendLog` /
        `RunFile` no fallan por `FileNotFoundError` en un proyecto nuevo.
        NO tocamos el layout legacy (ya lo garantiza `RunFile.create`).
        """
        from orchestrator.state import CwdViolationError

        if not self.tasks_json.exists() or not (self.scripts_dir / "task-start.sh").exists():
            raise CwdViolationError(
                f"orchestrator project_root={self.project_root} is missing "
                "tasks.json or scripts/task-start.sh"
            )

        if self.state_layout == "namespaced":
            self.state_dir.mkdir(parents=True, exist_ok=True)


def resolve_project_paths(
    project_root_arg: str | None,
    project_id_arg: str | None,
    config_arg: str | Path,
) -> ProjectPaths:
    """Materializá un `ProjectPaths` a partir del CLI/env.

    Orden de precedencia para `project_root`:
        1. `--project-root` (flag)
        2. `ORCH_PROJECT_ROOT` (env)
        3. `Path.cwd()` (default histórico — retrocompatible con rupies)

    Idem para `project_id`, con fallback a `_default_project_id(root)`.

    `config_arg` se resuelve como sigue:
        - si es absoluto → se usa tal cual
        - si es relativo → se resuelve contra `project_root`

    Fase 2 — `explicit_root` + `state_layout`:
        - Cuando el root vino de flag o env, `explicit_root=True` y el layout
          es `"namespaced"` (usa `state/<project_id>/`).
        - Cuando cae al cwd, `explicit_root=False` y el layout es `"legacy"`
          (usa `state/` a secas — mismo path que la Fase 1 y que rupies).
    """
    root_raw = (
        project_root_arg
        or os.environ.get("ORCH_PROJECT_ROOT")
        or None
    )
    explicit_root = root_raw is not None
    if root_raw:
        root = Path(root_raw).expanduser().resolve()
    else:
        root = Path.cwd().resolve()

    pid = (
        project_id_arg
        or os.environ.get("ORCH_PROJECT_ID")
        or _default_project_id(root)
    )

    cfg_path = Path(config_arg)
    if not cfg_path.is_absolute():
        cfg_path = (root / cfg_path).resolve()

    state_layout: StateLayout = "namespaced" if explicit_root else "legacy"

    # Auto-detect namespaced layout when the legacy default is chosen but
    # the project has already been run with --project-root (which creates
    # `.orchestrator/state/<project_id>/`). Without this, `orch dashboard`
    # started from the project dir without --project-root reads the empty
    # legacy DB instead of the real namespaced one.
    if state_layout == "legacy":
        namespaced_state = root / ".orchestrator" / "state" / pid
        if namespaced_state.is_dir() and (
            (namespaced_state / "orch.db").exists()
            or any(namespaced_state.glob("spend-*.jsonl"))
            or any(namespaced_state.glob("events-*.jsonl"))
        ):
            state_layout = "namespaced"

    return ProjectPaths(
        project_root=root,
        project_id=pid,
        config_yaml=cfg_path,
        explicit_root=explicit_root,
        state_layout=state_layout,
    )
