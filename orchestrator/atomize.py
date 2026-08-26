"""Spec Atomizer — MVP.

Convierte specs markdown estructurados en filas de ``tasks.json``, con merge
NO destructivo contra el ``tasks.json`` existente. Read-only por default:
imprime un diff human-readable y sólo escribe cuando se pasa ``--apply``.

Alcance:
    - Parser markdown de headers ``# F<n>``, ``## F<n>.<pkg>``, ``### F<n>.<pkg>.T<n>``.
    - Bullets con formato ``- **<Campo>**: <valor>`` mapean a campos de ``Task``.
    - Merge que preserva ``status/comments/files`` de tasks existentes (runtime
      state) y actualiza los campos declarativos que vienen de la spec.
    - Diff coloreado via ``rich`` (ya está en deps).
    - IDs viejos (``R-001``, ``B-003``) coexisten sin ser tocados — el parser
      sólo genera IDs ``F<n>.<pkg>.T<n>``.
    - Backup automático al escribir: ``tasks.json.bak-<timestamp>``.

Explícitamente FUERA de scope (MVP):
    - Sin borrar tasks huérfanas (sólo se listan en el diff).
    - Sin validación estricta de dependencias — sólo warning si hay huérfanas.
    - Sin escritura de ``tasks.json`` vía shell scripts (C-1..C-3 aplica al
      runtime, no al atomizer que edita metadata declarativa).
    - Sin resolución de conflictos interactiva — el merge es determinístico.

CLI:
    python -m orchestrator.atomize                     # diff (default: <root>/docs)
    python -m orchestrator.atomize --specs-dir <path>  # override specs dir
    python -m orchestrator.atomize --project-root <p>  # override proyecto
    python -m orchestrator.atomize --file docs/x.md    # sólo un archivo
    python -m orchestrator.atomize --list              # sólo listar
    python -m orchestrator.atomize --apply             # escribir tasks.json

Formato de spec: ver ``docs/SPEC-FORMAT.md``.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

import yaml
from rich.console import Console
from rich.table import Table

from orchestrator.paths import resolve_project_paths


# ---- Frontmatter YAML ---------------------------------------------------


# Delimitador de frontmatter estilo Jekyll/Hugo: ``---\n...\n---\n`` al TOP del file.
_RE_FRONTMATTER = re.compile(r"^---\s*\n(.*?)\n---\s*\n?", re.DOTALL)

# Tipos válidos para el frontmatter — orch-plan usa un enum acotado.
# El atomizer sólo consume ``spec`` y ``task-batch``. Los demás (``prd``,
# ``arch``, ``poc``) son metadata de otros artifacts y NO deben terminar en
# tasks.json. Los aceptamos como warning (no fatal) para que el walk no
# muera si un `docs/prd/001-foo.md` se filtra por error.
_VALID_TYPES: frozenset[str] = frozenset(
    {"prd", "arch", "spec", "poc", "task-batch"}
)
_ATOMIZER_TYPES: frozenset[str] = frozenset({"spec", "task-batch"})


@dataclass
class Frontmatter:
    """Metadata parseada del frontmatter YAML de un spec.

    Todos los campos son opcionales — retrocompat con specs sin frontmatter.
    Cuando ``present=False``, el resto queda con defaults y el parser trata
    al file como legacy.

    Fields relevantes al atomizer:
        - ``type``: debe ser ``spec`` o ``task-batch`` para no warnear.
        - ``project_id``: si no matchea con el project activo → warning.
        - ``phase``: default para tasks sin fase declarada en headers.
        - ``package``: metadata pura, no altera parsing.
        - ``generated_by``: informativo (qué skill lo generó).
        - ``consumed_by``: si NO incluye ``orch-atomizer`` → warning.
    """

    present: bool = False
    type: str | None = None
    project_id: str | None = None
    phase: int | None = None
    package: str | None = None
    version: str | None = None
    depends_on: list[str] = field(default_factory=list)
    consumed_by: list[str] = field(default_factory=list)
    generated_by: str | None = None
    generated_at: str | None = None
    title: str | None = None
    raw: dict[str, Any] = field(default_factory=dict)
    warnings: list[str] = field(default_factory=list)


def extract_frontmatter(text: str) -> tuple[Frontmatter, str]:
    """Extraé frontmatter YAML del top de ``text``.

    Devuelve ``(Frontmatter, body)``. Si no hay frontmatter, retorna
    ``Frontmatter(present=False)`` y el texto entero como body — retrocompat
    total con specs viejos.

    Errores parseando el YAML NO son fatales: se registra warning y se sigue
    tratando al archivo como legacy (sin frontmatter). El body en ese caso
    incluye el bloque ``---`` intacto (para que headers en la primera línea
    no se pierdan).

    Reglas de validación (warning, no error):
        - ``type`` fuera de ``_VALID_TYPES`` → warning
        - ``type`` presente pero no es ``spec``/``task-batch`` → warning
          adicional recomendando NO alimentar al atomizer
        - ``consumed_by`` presente y NO incluye ``orch-atomizer`` → warning
        - ``phase`` no int → warning, se ignora el campo
    """
    m = _RE_FRONTMATTER.match(text)
    if not m:
        return Frontmatter(present=False), text

    raw_yaml = m.group(1)
    body = text[m.end():]
    fm = Frontmatter(present=True)

    try:
        data = yaml.safe_load(raw_yaml)
    except yaml.YAMLError as e:
        fm = Frontmatter(present=False)
        fm.warnings.append(f"frontmatter YAML malformado: {e}")
        # Cuando el YAML explota, tratamos al file entero como body legacy.
        return fm, text

    if data is None:
        # Frontmatter vacío (`---\n---\n`) — tolerado, tratamos como sin frontmatter.
        return Frontmatter(present=False), body
    if not isinstance(data, dict):
        fm.warnings.append(
            f"frontmatter debe ser un dict YAML, vino {type(data).__name__} — ignorado"
        )
        return Frontmatter(present=False), body

    fm.raw = data

    t = data.get("type")
    if isinstance(t, str):
        fm.type = t
        if t not in _VALID_TYPES:
            fm.warnings.append(
                f"frontmatter type='{t}' no es válido "
                f"(esperado uno de: {sorted(_VALID_TYPES)})"
            )
        elif t not in _ATOMIZER_TYPES:
            fm.warnings.append(
                f"frontmatter type='{t}' no es un tipo consumible por el atomizer "
                f"(esperado: spec | task-batch) — este archivo no debería estar en el walk"
            )
    elif t is not None:
        fm.warnings.append(f"frontmatter type debe ser string, vino {type(t).__name__}")

    pid = data.get("project_id")
    if isinstance(pid, str):
        fm.project_id = pid
    elif pid is not None:
        fm.warnings.append("frontmatter project_id debe ser string — ignorado")

    ph = data.get("phase")
    if isinstance(ph, int) and not isinstance(ph, bool):
        fm.phase = ph
    elif ph is not None:
        fm.warnings.append(
            f"frontmatter phase debe ser int, vino {type(ph).__name__} — ignorado"
        )

    pkg = data.get("package")
    if isinstance(pkg, str):
        fm.package = pkg
    elif pkg is not None:
        fm.warnings.append("frontmatter package debe ser string — ignorado")

    ver = data.get("version")
    if ver is not None:
        fm.version = str(ver)

    dep = data.get("depends_on")
    if isinstance(dep, list):
        fm.depends_on = [str(x) for x in dep]
    elif dep is not None:
        fm.warnings.append("frontmatter depends_on debe ser lista — ignorado")

    cby = data.get("consumed_by")
    if isinstance(cby, list):
        fm.consumed_by = [str(x) for x in cby]
        if fm.consumed_by and "orch-atomizer" not in fm.consumed_by:
            fm.warnings.append(
                f"frontmatter consumed_by={fm.consumed_by} no incluye 'orch-atomizer' "
                "— este archivo puede no estar destinado al atomizer"
            )
    elif cby is not None:
        fm.warnings.append("frontmatter consumed_by debe ser lista — ignorado")

    gby = data.get("generated_by")
    if isinstance(gby, str):
        fm.generated_by = gby

    gat = data.get("generated_at")
    if gat is not None:
        fm.generated_at = str(gat)

    ttl = data.get("title")
    if isinstance(ttl, str):
        fm.title = ttl

    return fm, body


# ---- Regex ---------------------------------------------------------------


# ``# F1 — Título``  (guión largo o corto tolerado)
_RE_PHASE = re.compile(r"^#\s+F(\d+)\s*[—\-–]\s*(.+?)\s*$")
# ``## F1.1 — Título``
_RE_PACKAGE = re.compile(r"^##\s+F(\d+)\.(\d+)\s*[—\-–]\s*(.+?)\s*$")
# ``### F1.1.T9 — Título``
_RE_TASK = re.compile(r"^###\s+F(\d+)\.(\d+)\.T(\d+)\s*[—\-–]\s*(.+?)\s*$")
# Bullet campo: ``- **Modelo**: valor`` (2-4 espacios tolerados como indent)
_RE_FIELD_BULLET = re.compile(r"^\s*[-*]\s+\*\*([^*]+)\*\*\s*:\s*(.*?)\s*$")
# Sub-bullet (files): ``  - `path/a/foo.dart``` o ``- `path```
_RE_FILE_BULLET = re.compile(r"^\s*[-*]\s+`([^`]+)`\s*$")

# Mapeo campo (label ES) → key snake_case en Task
_FIELD_MAP: dict[str, str] = {
    "modelo": "model",
    "model": "model",
    "estimación": "estimate_hours",
    "estimacion": "estimate_hours",
    "estimate": "estimate_hours",
    "estimation": "estimate_hours",
    "razón": "reason",
    "razon": "reason",
    "reason": "reason",
    "dependencies": "dependencies",
    "dependencias": "dependencies",
    "deps": "dependencies",
    "files": "files",
    "archivos": "files",
}


# ---- Parser primitives ---------------------------------------------------


def parse_estimate(raw: str) -> float:
    """Parseá una estimación tipo ``"8h"``, ``"1.5h"``, ``"30m"``, ``"2d"``.

    Reglas:
        - ``d`` → días × 8h laborales
        - ``h`` → horas (float)
        - ``m`` → minutos / 60
        - sin unidad → asume horas
        - vacío / basura → 0.0

    Case-insensitive. Tolera espacios.
    """
    if not raw:
        return 0.0
    s = raw.strip().lower().replace(",", ".")
    # patrones tipo "2d 4h" no soportados en MVP — tomamos el primer token
    m = re.match(r"^\s*([0-9]+(?:\.[0-9]+)?)\s*([dhm]?)", s)
    if not m:
        return 0.0
    val = float(m.group(1))
    unit = m.group(2)
    if unit == "d":
        return val * 8.0
    if unit == "m":
        return val / 60.0
    # "h" o sin unidad
    return val


def parse_deps(raw: str) -> list[str]:
    """Parseá ``"F1.1.T8, F1.1.T5"`` como lista de IDs.

    Tolera separadores ``,`` o ``;``. Filtra vacíos. NO valida existencia.
    """
    if not raw:
        return []
    parts = re.split(r"[,;]", raw)
    return [p.strip() for p in parts if p.strip()]


# ---- Dominio parser ------------------------------------------------------


@dataclass
class ParsedTask:
    """Task extraída de un spec markdown, previa a merge.

    Campos ``camelCase`` NO existen acá — trabajamos en snake_case tipo
    ``Task`` dataclass. La serialización a camelCase pasa en ``to_json_row``.

    ``source_file`` y ``line`` sirven para errores/diff.
    """

    id: str
    phase: int
    package: int
    task_num: int
    title: str
    description: str = ""
    model: str = ""
    reason: str = ""
    estimate_hours: float = 0.0
    dependencies: list[str] = field(default_factory=list)
    files: list[str] = field(default_factory=list)
    spec_ref: str = ""
    source_file: str = ""
    line: int = 0


@dataclass
class ParseResult:
    """Bundle de lo extraído por spec file — útil para tests y diff."""

    tasks: list[ParsedTask] = field(default_factory=list)
    files_scanned: list[Path] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)


def _iter_spec_files(root: Path, single_file: Path | None) -> list[Path]:
    """Enumerá los .md a escanear.

    - Si ``single_file`` está seteado, devuelve sólo ese path (resuelto).
    - Si no, walkeá ``root/**/*.md`` en orden lexicográfico (determinístico).
    """
    if single_file is not None:
        p = single_file.expanduser().resolve()
        if not p.exists():
            raise FileNotFoundError(f"spec file not found: {p}")
        return [p]
    if not root.exists():
        return []
    return sorted(root.rglob("*.md"))


def _relpath_for_spec_ref(spec_path: Path, docs_root: Path) -> str:
    """Devolvé el relpath desde ``docs_root`` (POSIX) para ``spec_ref``.

    Si el spec cae fuera de ``docs_root`` (edge case: ``--file /otro/lado.md``),
    caemos al ``.name`` a secas. Nunca devuelve rutas absolutas — el
    ``spec_ref`` debe quedar portable entre máquinas.
    """
    try:
        rel = spec_path.relative_to(docs_root)
        return rel.as_posix()
    except ValueError:
        return spec_path.name


def _finalize_description(buf: list[str]) -> str:
    """Colapsa el buffer de descripción respetando párrafos internos.

    Sacamos líneas en blanco al inicio/final pero preservamos los saltos de
    línea internos — la descripción puede ser multiline con listas o code
    blocks.
    """
    while buf and not buf[0].strip():
        buf.pop(0)
    while buf and not buf[-1].strip():
        buf.pop()
    return "\n".join(buf)


def parse_spec_file(
    path: Path,
    docs_root: Path,
    expected_project_id: str | None = None,
) -> ParseResult:
    """Parseá un archivo markdown y extraé ``ParsedTask`` de sus headers.

    Formato asumido (ver ``docs/SPEC-FORMAT.md``):
        - Frontmatter YAML opcional al top (``---\\n...\\n---\\n``). Ver
          ``FRONTMATTER-CONTRACT.md``. Retrocompat total: si no está, el
          archivo se procesa exactamente igual que antes.
        - ``# F<n> — <título>`` = fase (número entero → ``task.phase``).
        - ``## F<n>.<pkg> — <título>`` = paquete (agrupamiento visual).
        - ``### F<n>.<pkg>.T<n> — <título>`` = task (ID completo).
        - Bullets ``- **<Campo>**: <valor>`` mapean a campos declarativos.

    La descripción es TODO texto que aparece entre el header de task y el
    primer bullet ``- **Campo**`` (o el próximo header). Preservamos el
    formato multiline tal cual (útil para user stories que ocupan varios
    párrafos).

    ``expected_project_id`` (opcional): si se pasa, y el frontmatter declara
    otro ``project_id``, se emite warning (no fatal).

    Reglas frontmatter → parser:
        - Si frontmatter declara ``phase: N`` y el archivo NO tiene header
          ``# F<n>`` antes del primer task, se usa ``N`` como default.
        - Si el archivo tiene header propio, gana el header (el frontmatter
          es un fallback, no un override).

    Warnings (no fatal):
        - Task sin fase declarada previamente (header ``###`` antes de ``#``).
        - Números de fase/paquete que no matchean entre header y task ID.
        - Frontmatter con ``project_id`` distinto al esperado.
        - Frontmatter con ``type`` no consumible por el atomizer.
    """
    result = ParseResult(files_scanned=[path])
    rel_ref = _relpath_for_spec_ref(path, docs_root)

    raw_text = path.read_text(encoding="utf-8")
    fm, body = extract_frontmatter(raw_text)

    # Warnings del extractor → propagar con prefijo de archivo
    for w in fm.warnings:
        result.warnings.append(f"{path.name}: {w}")

    # Cross-check project_id contra el esperado
    if (
        fm.present
        and fm.project_id
        and expected_project_id
        and fm.project_id != expected_project_id
    ):
        result.warnings.append(
            f"{path.name}: frontmatter project_id='{fm.project_id}' "
            f"no matchea con project activo '{expected_project_id}'"
        )

    # Default de phase desde frontmatter (fallback, el header manda)
    current_phase: int | None = fm.phase if fm.present else None
    current_pkg: int | None = None  # sólo para validación cruzada
    current_task: ParsedTask | None = None
    # Estado del parser de task:
    #   "desc"   → todavía juntando líneas de description
    #   "fields" → ya arrancaron los bullets de campos
    #   "files"  → dentro de los sub-bullets de ``Files``
    task_state: str = "desc"
    desc_buf: list[str] = []

    def _flush_task() -> None:
        nonlocal current_task, desc_buf, task_state
        if current_task is None:
            return
        current_task.description = _finalize_description(desc_buf)
        result.tasks.append(current_task)
        current_task = None
        desc_buf = []
        task_state = "desc"

    # ``lineno`` acá refiere a la línea DENTRO del body (post frontmatter);
    # es suficiente para warnings/debug. No mantenemos offset absoluto para
    # simplificar — mostrar "body line N" es más limpio que "file line N ±".
    for lineno, line in enumerate(body.splitlines(keepends=False), start=1):
        # ---- Headers rompen cualquier estado de task ----
        m_phase = _RE_PHASE.match(line)
        if m_phase:
            _flush_task()
            current_phase = int(m_phase.group(1))
            current_pkg = None
            continue

        m_pkg = _RE_PACKAGE.match(line)
        if m_pkg:
            _flush_task()
            pkg_phase = int(m_pkg.group(1))
            current_pkg = int(m_pkg.group(2))
            if current_phase is None:
                current_phase = pkg_phase
                result.warnings.append(
                    f"{path.name}:{lineno}: package F{pkg_phase}.{current_pkg} "
                    "sin header de fase previo — asumiendo fase implícita"
                )
            elif pkg_phase != current_phase:
                result.warnings.append(
                    f"{path.name}:{lineno}: package F{pkg_phase}.{current_pkg} "
                    f"no matchea con fase actual F{current_phase}"
                )
                current_phase = pkg_phase
            continue

        m_task = _RE_TASK.match(line)
        if m_task:
            _flush_task()
            t_phase = int(m_task.group(1))
            t_pkg = int(m_task.group(2))
            t_num = int(m_task.group(3))
            title = m_task.group(4).strip()
            tid = f"F{t_phase}.{t_pkg}.T{t_num}"
            if current_phase is None:
                current_phase = t_phase
                result.warnings.append(
                    f"{path.name}:{lineno}: task {tid} sin fase previa"
                )
            current_task = ParsedTask(
                id=tid,
                phase=t_phase,
                package=t_pkg,
                task_num=t_num,
                title=title,
                spec_ref=f"{rel_ref}#{tid}",
                source_file=str(path),
                line=lineno,
            )
            task_state = "desc"
            continue

        # ---- Contenido de task ----
        if current_task is None:
            # Texto libre a nivel fase/paquete → ignorado (descripciones
            # de fase/paquete NO se guardan; no hay campo destino).
            continue

        m_field = _RE_FIELD_BULLET.match(line)
        if m_field:
            task_state = "fields"
            label = m_field.group(1).strip().lower()
            value = m_field.group(2).strip()
            key = _FIELD_MAP.get(label)
            if key is None:
                result.warnings.append(
                    f"{path.name}:{lineno}: campo desconocido "
                    f"'**{m_field.group(1)}**' en task {current_task.id}"
                )
                continue
            if key == "model":
                current_task.model = value
            elif key == "reason":
                current_task.reason = value
            elif key == "estimate_hours":
                current_task.estimate_hours = parse_estimate(value)
            elif key == "dependencies":
                current_task.dependencies = parse_deps(value)
            elif key == "files":
                # Si venía inline (``- **Files**: `path```) tratamos como
                # single-file. Si no, próximo estado espera sub-bullets.
                task_state = "files"
                inline = value.strip()
                if inline:
                    m_inline = re.findall(r"`([^`]+)`", inline)
                    current_task.files.extend(m_inline)
            continue

        # Sub-bullets bajo ``Files:``
        if task_state == "files":
            m_file = _RE_FILE_BULLET.match(line)
            if m_file:
                current_task.files.append(m_file.group(1))
                continue
            # Si no matchea file bullet Y no es línea vacía → salimos
            # de estado files. Línea vacía es tolerada.
            if line.strip():
                task_state = "fields"

        # Description buffer: sólo mientras estamos en "desc"
        if task_state == "desc":
            desc_buf.append(line)

    _flush_task()
    return result


def parse_specs(
    files: Iterable[Path],
    docs_root: Path,
    expected_project_id: str | None = None,
) -> ParseResult:
    """Agregá ``parse_spec_file`` sobre un iterable de paths.

    Duplicados de ID: gana el ÚLTIMO parseado (walking lexicográfico → dueño
    de la definición es siempre el archivo más reciente). Se emite warning.

    ``expected_project_id`` se propaga a cada ``parse_spec_file`` para
    cross-check contra el frontmatter YAML (si existe).
    """
    agg = ParseResult()
    seen: dict[str, str] = {}  # id → source_file
    for p in files:
        r = parse_spec_file(p, docs_root, expected_project_id=expected_project_id)
        agg.files_scanned.append(p)
        agg.warnings.extend(r.warnings)
        for t in r.tasks:
            if t.id in seen:
                agg.warnings.append(
                    f"task {t.id} duplicada — primero en {seen[t.id]}, "
                    f"ahora en {t.source_file} (gana el segundo)"
                )
                # remover la vieja
                agg.tasks = [x for x in agg.tasks if x.id != t.id]
            seen[t.id] = t.source_file
            agg.tasks.append(t)
    return agg


# ---- Merge ---------------------------------------------------------------


# Campos declarativos que vienen de la spec y OK sobrescribir:
_DECLARATIVE_FIELDS: tuple[str, ...] = (
    "title",
    "description",
    "model",
    "reason",
    "dependencies",
    "estimateHours",  # camelCase en json
    "specRef",  # camelCase en json
    "phase",
)

# Campos runtime — NUNCA se pisan al mergear:
_RUNTIME_FIELDS: tuple[str, ...] = ("status", "comments", "files")


def to_json_row(t: ParsedTask) -> dict[str, Any]:
    """Serializá un ``ParsedTask`` como fila de ``tasks.json`` (camelCase).

    Defaults para task NUEVA:
        - ``status = "backlog"``
        - ``files = []``
        - ``comments = []``

    NOTA: si la task ya existía, este dict se usa como fuente de campos
    declarativos (title/description/etc); los runtime del row viejo se
    preservan en ``merge_tasks``.
    """
    return {
        "id": t.id,
        "phase": t.phase,
        "title": t.title,
        "description": t.description,
        "model": t.model,
        "reason": t.reason,
        "status": "backlog",
        "dependencies": list(t.dependencies),
        "estimateHours": t.estimate_hours,
        "files": list(t.files),
        "specRef": t.spec_ref,
        "comments": [],
    }


@dataclass
class MergeDiff:
    """Resultado del merge: buckets para el diff report."""

    new_tasks: list[dict[str, Any]] = field(default_factory=list)
    # (row_final, row_previo, campos_cambiados)
    updated: list[tuple[dict[str, Any], dict[str, Any], list[str]]] = field(
        default_factory=list
    )
    unchanged: list[dict[str, Any]] = field(default_factory=list)
    orphans: list[dict[str, Any]] = field(default_factory=list)
    dep_warnings: list[str] = field(default_factory=list)


def load_raw_tasks_json(path: Path) -> dict[str, Any]:
    """Cargá ``tasks.json`` preservando ``meta`` y ``phases``.

    Retrocompat: si el archivo no existe, devolvemos estructura mínima. Si
    es un bare array (legacy), lo envolvemos.
    """
    if not path.exists():
        return {"meta": {}, "phases": [], "tasks": []}
    with path.open(encoding="utf-8") as fh:
        raw = json.load(fh)
    if isinstance(raw, list):
        return {"meta": {}, "phases": [], "tasks": list(raw)}
    if not isinstance(raw, dict):
        raise ValueError(
            f"{path}: esperaba top-level dict o list, vino {type(raw).__name__}"
        )
    raw.setdefault("meta", {})
    raw.setdefault("phases", [])
    raw.setdefault("tasks", [])
    return raw


def merge_tasks(
    existing: dict[str, Any],
    parsed: list[ParsedTask],
) -> tuple[dict[str, Any], MergeDiff]:
    """Aplicá el merge y devolvé (json_final, diff).

    Reglas (críticas — ver README):
        - Task NUEVA (ID no está en existing) → append al final.
        - Task EXISTENTE → actualiza campos declarativos, preserva runtime.
        - Task HUÉRFANA (existe pero no vino en el parse) → NO se borra;
          se lista en el diff.
        - Preserva orden: existentes mantienen su índice, nuevas al final.
        - Preserva ``meta`` y ``phases`` top-level.

    Validación post-merge: warning si una task depende de un ID inexistente.
    """
    diff = MergeDiff()
    rows: list[dict[str, Any]] = list(existing.get("tasks", []))
    by_id: dict[str, int] = {row["id"]: i for i, row in enumerate(rows) if "id" in row}
    parsed_ids: set[str] = {t.id for t in parsed}

    for pt in parsed:
        candidate = to_json_row(pt)
        if pt.id in by_id:
            idx = by_id[pt.id]
            old = rows[idx]
            merged = dict(old)  # base = row viejo
            changed: list[str] = []
            for f in _DECLARATIVE_FIELDS:
                new_val = candidate.get(f)
                old_val = old.get(f)
                if new_val != old_val:
                    changed.append(f)
                    merged[f] = new_val
            # runtime → preserva ``old``; si no estaba, default sano
            for f in _RUNTIME_FIELDS:
                if f in old:
                    merged[f] = old[f]
                else:
                    merged[f] = candidate.get(f, [] if f != "status" else "backlog")
            rows[idx] = merged
            if changed:
                diff.updated.append((merged, old, changed))
            else:
                diff.unchanged.append(merged)
        else:
            rows.append(candidate)
            diff.new_tasks.append(candidate)

    # Huérfanas: en existing, no en parsed_ids. NO borrar, sólo listar.
    for row in rows:
        rid = row.get("id")
        if rid and rid not in parsed_ids:
            # sólo listamos huérfanas que "parecen" venir de la spec (F<n>.<n>.T<n>);
            # los IDs viejos tipo R-001 / B-003 los reportamos como legacy separado.
            diff.orphans.append(row)

    # Validación de deps huérfanas
    all_ids = {row["id"] for row in rows if "id" in row}
    for row in rows:
        for dep in row.get("dependencies", []) or []:
            if dep not in all_ids:
                diff.dep_warnings.append(
                    f"task {row.get('id')} depende de '{dep}' que no existe"
                )

    result_json = dict(existing)
    result_json["tasks"] = rows
    return result_json, diff


# ---- Diff renderer -------------------------------------------------------


def _looks_new_format(tid: str) -> bool:
    """True si el ID matchea ``F<n>.<n>.T<n>`` (formato nuevo)."""
    return bool(re.match(r"^F\d+\.\d+\.T\d+$", tid or ""))


def render_diff(
    diff: MergeDiff,
    parse: ParseResult,
    console: Console,
) -> None:
    """Pintá el diff en consola usando ``rich``.

    Formato:
        === Atomizer diff ===
        Specs escaneados: N archivos, M tasks encontradas

        + NUEVAS (k):
          + F1.1.T9  — ...  model  Xh  deps: ...
        ...

        ~ ACTUALIZADAS (k):
          ~ F1.1.T1 — Auth domain model
            - model: X → Y
        ...

        = SIN CAMBIOS (k)

        ⚠ HUÉRFANAS (k):
          ? F1.2.T7 (status=done) — ...

    Los IDs legacy (R-001, etc) van en su propia sección para evitar ruido.
    """
    console.rule("[bold]Atomizer diff[/bold]")
    console.print(
        f"Specs escaneados: [bold]{len(parse.files_scanned)}[/bold] archivos, "
        f"[bold]{len(parse.tasks)}[/bold] tasks encontradas"
    )
    if parse.warnings:
        console.print(f"[yellow]Parser warnings: {len(parse.warnings)}[/yellow]")
        for w in parse.warnings[:10]:
            console.print(f"  [yellow]· {w}[/yellow]")
        if len(parse.warnings) > 10:
            console.print(f"  [yellow]... +{len(parse.warnings) - 10} más[/yellow]")

    # ---- Nuevas ----
    console.print(f"\n[green]+ NUEVAS ({len(diff.new_tasks)}):[/green]")
    for row in diff.new_tasks:
        deps = row.get("dependencies") or []
        deps_s = ", ".join(deps) if deps else "—"
        console.print(
            f"  [green]+ {row['id']:<14}[/green] — {row.get('title', ''):<45} "
            f"[dim]{row.get('model', ''):<24}[/dim] "
            f"[cyan]{row.get('estimateHours', 0)}h[/cyan]  "
            f"deps: [magenta]{deps_s}[/magenta]"
        )

    # ---- Actualizadas ----
    console.print(f"\n[yellow]~ ACTUALIZADAS ({len(diff.updated)}):[/yellow]")
    for new_row, old_row, changed in diff.updated:
        console.print(f"  [yellow]~ {new_row['id']}[/yellow] — {new_row.get('title', '')}")
        for f in changed:
            old_v = old_row.get(f)
            new_v = new_row.get(f)
            console.print(
                f"    [dim]- {f}:[/dim]  {_short(old_v)}  →  [bold]{_short(new_v)}[/bold]"
            )

    # ---- Sin cambios ----
    console.print(f"\n= SIN CAMBIOS ([bold]{len(diff.unchanged)}[/bold])")

    # ---- Huérfanas ----
    if diff.orphans:
        new_fmt = [r for r in diff.orphans if _looks_new_format(r.get("id", ""))]
        legacy = [r for r in diff.orphans if not _looks_new_format(r.get("id", ""))]
        console.print(
            f"\n[red]⚠ HUÉRFANAS en tasks.json[/red] "
            f"(no en specs actuales, NO se borran) "
            f"([bold]{len(diff.orphans)}[/bold]):"
        )
        for row in new_fmt:
            console.print(
                f"  [red]?[/red] {row.get('id'):<14} "
                f"[dim](status={row.get('status', '?')})[/dim] — {row.get('title', '')}"
            )
        for row in legacy:
            console.print(
                f"  [dim]?[/dim] {row.get('id'):<14} "
                f"[dim](status={row.get('status', '?')}) — {row.get('title', '')}"
                " [formato legacy, ok][/dim]"
            )

    # ---- Deps huérfanas ----
    if diff.dep_warnings:
        console.print(
            f"\n[red]⚠ DEPENDENCIAS HUÉRFANAS ({len(diff.dep_warnings)}):[/red]"
        )
        for w in diff.dep_warnings[:20]:
            console.print(f"  [red]· {w}[/red]")
        if len(diff.dep_warnings) > 20:
            console.print(f"  [red]... +{len(diff.dep_warnings) - 20} más[/red]")

    console.print()
    if diff.new_tasks or diff.updated:
        console.print("[bold]Para aplicar:[/bold] agregá [cyan]--apply[/cyan]")
    else:
        console.print("[dim]Nada que aplicar.[/dim]")


def _short(v: Any) -> str:
    """Representación compacta para el diff (evita spam de multilines)."""
    if v is None:
        return "∅"
    if isinstance(v, list):
        if not v:
            return "[]"
        return "[" + ", ".join(str(x) for x in v[:4]) + ("...]" if len(v) > 4 else "]")
    if isinstance(v, str):
        v2 = v.strip().replace("\n", " ")
        if len(v2) > 60:
            return v2[:57] + "..."
        return v2 or '""'
    return str(v)


def render_list(parse: ParseResult, console: Console) -> None:
    """Modo ``--list``: sólo mostrar lo que se parseó, sin merge/diff."""
    console.rule("[bold]Atomizer list[/bold]")
    console.print(
        f"Specs escaneados: [bold]{len(parse.files_scanned)}[/bold] archivos"
    )
    for p in parse.files_scanned:
        console.print(f"  [dim]· {p}[/dim]")
    if parse.warnings:
        console.print(f"[yellow]Warnings: {len(parse.warnings)}[/yellow]")
        for w in parse.warnings:
            console.print(f"  [yellow]· {w}[/yellow]")
    table = Table(show_header=True, header_style="bold")
    table.add_column("ID")
    table.add_column("Phase", justify="right")
    table.add_column("Title")
    table.add_column("Model")
    table.add_column("Est", justify="right")
    table.add_column("Deps")
    for t in parse.tasks:
        table.add_row(
            t.id,
            str(t.phase),
            t.title,
            t.model or "—",
            f"{t.estimate_hours}h",
            ", ".join(t.dependencies) if t.dependencies else "—",
        )
    console.print(table)


# ---- Escritura -----------------------------------------------------------


def _timestamp() -> str:
    """``YYYYMMDD-HHMMSS`` UTC — para nombres de backup."""
    return datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")


def write_tasks_json(
    path: Path, data: dict[str, Any], make_backup: bool = True
) -> Path | None:
    """Serializá ``data`` a ``path``. Devolvé el path del backup (o ``None``).

    - Si ``path`` existía y ``make_backup`` → copia a
      ``<path>.bak-<ts>`` antes de reescribir.
    - Escritura atómica: primero a ``<path>.tmp``, luego rename.
    - Retrocompat: si ``path`` no existía, crea sin backup.
    """
    backup_path: Path | None = None
    if path.exists() and make_backup:
        backup_path = path.with_suffix(path.suffix + f".bak-{_timestamp()}")
        backup_path.write_bytes(path.read_bytes())

    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.parent.mkdir(parents=True, exist_ok=True)
    with tmp.open("w", encoding="utf-8") as fh:
        json.dump(data, fh, indent=2, ensure_ascii=False)
        fh.write("\n")
    tmp.replace(path)
    return backup_path


# ---- CLI -----------------------------------------------------------------


def _build_argparser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="orch atomize",
        description=(
            "Spec Atomizer — parse markdown specs and merge into tasks.json. "
            "Read-only by default (shows a diff). Pass --apply to write."
        ),
    )
    p.add_argument(
        "--project-root",
        default=None,
        help="Root del proyecto (por default: cwd o $ORCH_PROJECT_ROOT).",
    )
    p.add_argument(
        "--project-id",
        default=None,
        help="ID de proyecto (por default: derivado del basename del root).",
    )
    p.add_argument(
        "--config",
        default=".orchestrator/config.yaml",
        help="Path a config.yaml (para compat con resolve_project_paths).",
    )
    p.add_argument(
        "--specs-dir",
        default=None,
        help="Directorio de specs .md (default: <project-root>/docs).",
    )
    p.add_argument(
        "--file",
        default=None,
        help="Sólo un archivo markdown (bypass del walk de --specs-dir).",
    )
    p.add_argument(
        "--tasks-json",
        default=None,
        help="Path a tasks.json (default: <project-root>/tasks.json).",
    )
    p.add_argument(
        "--list",
        action="store_true",
        help="Modo listar: sólo imprime lo parseado, sin merge/diff.",
    )
    p.add_argument(
        "--apply",
        action="store_true",
        help="Escribí tasks.json (con backup). Sin este flag es read-only.",
    )
    p.add_argument(
        "--no-backup",
        action="store_true",
        help="No crear backup .bak-<ts> al escribir (default: sí crea).",
    )
    return p


def main(argv: list[str] | None = None) -> int:
    args = _build_argparser().parse_args(argv)
    console = Console()

    paths = resolve_project_paths(
        project_root_arg=args.project_root,
        project_id_arg=args.project_id,
        config_arg=args.config,
    )

    specs_root = (
        Path(args.specs_dir).expanduser().resolve()
        if args.specs_dir
        else paths.project_root / "docs"
    )
    single_file = Path(args.file).expanduser().resolve() if args.file else None
    tasks_json = (
        Path(args.tasks_json).expanduser().resolve()
        if args.tasks_json
        else paths.tasks_json
    )

    try:
        spec_files = _iter_spec_files(specs_root, single_file)
    except FileNotFoundError as e:
        console.print(f"[red]ERROR:[/red] {e}")
        return 2

    if not spec_files:
        console.print(
            f"[yellow]No hay specs .md bajo {specs_root} "
            f"(y no se pasó --file). Nada que hacer.[/yellow]"
        )
        return 0

    parse = parse_specs(spec_files, specs_root, expected_project_id=paths.project_id)

    if args.list:
        render_list(parse, console)
        return 0

    existing = load_raw_tasks_json(tasks_json)
    merged, diff = merge_tasks(existing, parse.tasks)
    render_diff(diff, parse, console)

    if not args.apply:
        return 0

    if not (diff.new_tasks or diff.updated):
        console.print("[dim]--apply pasado pero no hay cambios que aplicar.[/dim]")
        return 0

    backup = write_tasks_json(tasks_json, merged, make_backup=not args.no_backup)
    console.print(f"[green]✓ Escrito:[/green] {tasks_json}")
    if backup:
        console.print(f"[dim]  backup: {backup}[/dim]")
    return 0


if __name__ == "__main__":
    sys.exit(main())
