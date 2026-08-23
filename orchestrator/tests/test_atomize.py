"""Tests para ``orchestrator.atomize`` — parser + merge + diff.

Cobertura mínima MVP:
    - Parser: extrae fase/paquete/task correctamente.
    - ``parse_estimate``: unidades h/m/d, sin unidad, basura.
    - ``parse_deps``: split por coma con y sin espacios.
    - Files: inline con backticks + sub-bullets anidados.
    - Merge: preserva runtime, actualiza declarativos.
    - Merge: detecta huérfanas sin borrarlas.
    - Merge: convive con IDs legacy sin tocarlos.
    - Diff: no crashea con distintos buckets.
    - Roundtrip: parse → merge → serializar → ``load_tasks`` carga OK.

Cobertura orch-spec (issue #28):
    - ``TestOrchSpecOutputFormat``: valida el output EXACTO que genera
      ``orch-spec`` — frontmatter + labels acentuados ES (Estimación,
      Razón, Modelo) + sub-bullets Files + descripción multiline.
      Fixture: ``fixtures/atomize/orch_spec_output.md``.
"""

from __future__ import annotations

import io
import json
from pathlib import Path

import pytest

from orchestrator.atomize import (
    MergeDiff,
    ParseResult,
    ParsedTask,
    _iter_spec_files,
    load_raw_tasks_json,
    main as atomize_main,
    merge_tasks,
    parse_deps,
    parse_estimate,
    parse_spec_file,
    parse_specs,
    render_diff,
    render_list,
    to_json_row,
    write_tasks_json,
)
from orchestrator.state import load_tasks

from rich.console import Console


FIXTURE = Path(__file__).parent / "fixtures" / "atomize" / "sample_spec.md"


# ---- Primitives ----------------------------------------------------------


class TestParseEstimate:
    def test_hours_int(self) -> None:
        assert parse_estimate("8h") == 8.0

    def test_hours_float(self) -> None:
        assert parse_estimate("1.5h") == 1.5

    def test_minutes(self) -> None:
        assert parse_estimate("30m") == 0.5

    def test_days_to_workhours(self) -> None:
        # 2 días × 8h laborales = 16h
        assert parse_estimate("2d") == 16.0

    def test_no_unit_assumes_hours(self) -> None:
        assert parse_estimate("3") == 3.0

    def test_empty_returns_zero(self) -> None:
        assert parse_estimate("") == 0.0
        assert parse_estimate("   ") == 0.0

    def test_junk_returns_zero(self) -> None:
        assert parse_estimate("mucho") == 0.0

    def test_comma_decimal(self) -> None:
        # locale ES: "1,5h" también debería parsearse
        assert parse_estimate("1,5h") == 1.5


class TestParseDeps:
    def test_simple(self) -> None:
        assert parse_deps("F1.1.T8, F1.1.T5") == ["F1.1.T8", "F1.1.T5"]

    def test_no_spaces(self) -> None:
        assert parse_deps("A,B,C") == ["A", "B", "C"]

    def test_empty(self) -> None:
        assert parse_deps("") == []

    def test_trailing_comma(self) -> None:
        assert parse_deps("A, B,") == ["A", "B"]

    def test_semicolon_separator(self) -> None:
        assert parse_deps("A; B") == ["A", "B"]


# ---- Parser --------------------------------------------------------------


class TestParseSpecFile:
    def test_extracts_all_tasks(self) -> None:
        docs_root = FIXTURE.parent
        result = parse_spec_file(FIXTURE, docs_root)
        ids = [t.id for t in result.tasks]
        assert ids == ["F0.1.T1", "F1.1.T1", "F1.1.T9"]

    def test_phase_is_int(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F0.1.T1"].phase == 0
        assert by_id["F1.1.T1"].phase == 1
        assert by_id["F1.1.T9"].phase == 1

    def test_title_stripped(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F1.1.T9"].title == "Presentation: AuthSignInScreen"

    def test_estimate_days_parsed(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F0.1.T1"].estimate_hours == 16.0  # 2d

    def test_deps_parsed(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F1.1.T9"].dependencies == ["F1.1.T1", "F0.1.T1"]

    def test_files_sub_bullets(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F1.1.T9"].files == [
            "lib/features/auth/presentation/screens/auth_sign_in_screen.dart",
            "lib/features/auth/presentation/widgets/social_login_row.dart",
        ]

    def test_files_from_bootstrap_task(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F0.1.T1"].files == [
            "nx.json",
            "package.json",
            "apps/mobile/project.json",
        ]

    def test_description_captured(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        desc = by_id["F1.1.T9"].description
        assert "Como usuario" in desc
        assert "reset password" in desc
        # el bullet **Modelo** NO debe estar dentro de la descripción
        assert "**Modelo**" not in desc

    def test_spec_ref_auto(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F1.1.T9"].spec_ref == "sample_spec.md#F1.1.T9"

    def test_model_and_reason(self) -> None:
        result = parse_spec_file(FIXTURE, FIXTURE.parent)
        by_id = {t.id: t for t in result.tasks}
        assert by_id["F1.1.T1"].model == "claude-sonnet-4-6"
        assert by_id["F1.1.T1"].reason.startswith("Solo tipos")


class TestParserInlineFiles:
    def test_inline_single_file(self, tmp_path: Path) -> None:
        spec = tmp_path / "x.md"
        spec.write_text(
            "# F2 — X\n\n## F2.1 — pkg\n\n### F2.1.T1 — inline files\n\n"
            "- **Files**: `lib/x.dart`\n",
            encoding="utf-8",
        )
        r = parse_spec_file(spec, tmp_path)
        assert r.tasks[0].files == ["lib/x.dart"]


class TestParserUnknownField:
    def test_unknown_label_emits_warning(self, tmp_path: Path) -> None:
        spec = tmp_path / "x.md"
        spec.write_text(
            "# F1 — X\n### F1.1.T1 — Task\n"
            "- **Foo**: bar\n"
            "- **Modelo**: opus\n",
            encoding="utf-8",
        )
        r = parse_spec_file(spec, tmp_path)
        assert any("desconocido" in w for w in r.warnings)
        assert r.tasks[0].model == "opus"


# ---- Frontmatter YAML ----------------------------------------------------


class TestFrontmatterExtractor:
    def test_no_frontmatter_returns_full_text(self) -> None:
        from orchestrator.atomize import extract_frontmatter

        text = "# F1 — X\n### F1.1.T1 — Task\n"
        fm, body = extract_frontmatter(text)
        assert fm.present is False
        assert body == text  # body intacto — retrocompat total

    def test_valid_frontmatter_parsed(self) -> None:
        from orchestrator.atomize import extract_frontmatter

        text = (
            "---\n"
            "type: spec\n"
            "project_id: rupies\n"
            "phase: 1\n"
            "package: authentication\n"
            "generated_by: orch-spec\n"
            "consumed_by: [orch-atomizer]\n"
            "---\n"
            "# F1 — Auth\n"
        )
        fm, body = extract_frontmatter(text)
        assert fm.present is True
        assert fm.type == "spec"
        assert fm.project_id == "rupies"
        assert fm.phase == 1
        assert fm.package == "authentication"
        assert fm.generated_by == "orch-spec"
        assert fm.consumed_by == ["orch-atomizer"]
        assert body.startswith("# F1 — Auth")
        assert fm.warnings == []

    def test_malformed_yaml_treated_as_legacy(self) -> None:
        from orchestrator.atomize import extract_frontmatter

        # YAML roto: dos ``:`` sin escape, colon inesperado
        text = "---\ntype: spec\nphase: [unclosed\n---\n# F1 — X\n"
        fm, body = extract_frontmatter(text)
        assert fm.present is False
        assert any("malformado" in w for w in fm.warnings)
        # Body debe ser el TEXTO ENTERO (incluyendo el ``---``) para que
        # nada se pierda en modo fallback.
        assert body == text

    def test_invalid_type_warns(self) -> None:
        from orchestrator.atomize import extract_frontmatter

        text = "---\ntype: banana\n---\n# F1 — X\n"
        fm, _ = extract_frontmatter(text)
        assert fm.present is True
        assert fm.type == "banana"
        assert any("no es válido" in w for w in fm.warnings)

    def test_non_atomizer_type_warns(self) -> None:
        from orchestrator.atomize import extract_frontmatter

        text = "---\ntype: prd\n---\n# irrelevant\n"
        fm, _ = extract_frontmatter(text)
        assert fm.type == "prd"
        assert any("no es un tipo consumible" in w for w in fm.warnings)

    def test_consumed_by_without_atomizer_warns(self) -> None:
        from orchestrator.atomize import extract_frontmatter

        text = (
            "---\n"
            "type: spec\n"
            "consumed_by: [some-other-tool]\n"
            "---\n"
            "# F1 — X\n"
        )
        fm, _ = extract_frontmatter(text)
        assert any("orch-atomizer" in w for w in fm.warnings)


class TestParseSpecWithFrontmatter:
    def test_frontmatter_parsed_and_tasks_extracted(self, tmp_path: Path) -> None:
        spec = tmp_path / "f1-auth.md"
        spec.write_text(
            "---\n"
            "type: spec\n"
            "project_id: rupies\n"
            "phase: 1\n"
            "package: authentication\n"
            "generated_by: orch-spec\n"
            "consumed_by: [orch-atomizer]\n"
            "---\n"
            "# F1 — Auth\n\n"
            "## F1.1 — auth pkg\n\n"
            "### F1.1.T1 — Domain: Auth model\n\n"
            "- **Modelo**: claude-sonnet-4-6\n"
            "- **Estimación**: 4h\n",
            encoding="utf-8",
        )
        r = parse_spec_file(spec, tmp_path, expected_project_id="rupies")
        assert len(r.tasks) == 1
        assert r.tasks[0].id == "F1.1.T1"
        assert r.tasks[0].phase == 1
        assert r.tasks[0].model == "claude-sonnet-4-6"
        # No warnings esperados con frontmatter válido + project match
        assert r.warnings == []

    def test_frontmatter_phase_default_when_no_header(self, tmp_path: Path) -> None:
        # Sin header ``# F<n>``, sólo header de package + task
        spec = tmp_path / "f5-x.md"
        spec.write_text(
            "---\n"
            "type: spec\n"
            "phase: 5\n"
            "---\n"
            "## F5.1 — pkg\n\n"
            "### F5.1.T1 — Task\n"
            "- **Modelo**: opus\n",
            encoding="utf-8",
        )
        r = parse_spec_file(spec, tmp_path)
        # Como frontmatter declaró phase=5 y hay match con el header pkg F5.1,
        # NO debería warnear "sin header de fase previo".
        assert not any("sin header de fase previo" in w for w in r.warnings)
        assert r.tasks[0].phase == 5

    def test_project_id_mismatch_warns(self, tmp_path: Path) -> None:
        spec = tmp_path / "x.md"
        spec.write_text(
            "---\ntype: spec\nproject_id: other-project\n---\n"
            "# F1 — X\n### F1.1.T1 — Task\n- **Modelo**: opus\n",
            encoding="utf-8",
        )
        r = parse_spec_file(spec, tmp_path, expected_project_id="rupies")
        assert any(
            "no matchea con project activo 'rupies'" in w for w in r.warnings
        )
        # Aun así las tasks se extraen — es warning, no error.
        assert len(r.tasks) == 1

    def test_backcompat_no_frontmatter_still_works(self, tmp_path: Path) -> None:
        # Spec 100% legacy (idéntico al formato original, sin frontmatter)
        spec = tmp_path / "legacy.md"
        spec.write_text(
            "# F0 — Bootstrap\n\n"
            "## F0.1 — repo\n\n"
            "### F0.1.T1 — Task\n"
            "- **Modelo**: opus\n"
            "- **Estimación**: 8h\n",
            encoding="utf-8",
        )
        r = parse_spec_file(spec, tmp_path)
        assert len(r.tasks) == 1
        assert r.tasks[0].id == "F0.1.T1"
        assert r.tasks[0].estimate_hours == 8.0
        # Sin frontmatter no debe haber warnings
        assert r.warnings == []

    def test_malformed_frontmatter_parses_body_as_legacy(self, tmp_path: Path) -> None:
        spec = tmp_path / "broken.md"
        # YAML roto (bracket sin cerrar). El extractor NO matchea ``---...---``
        # completo cuando el YAML no lo puede parsear — el fallback es "no hay
        # frontmatter, tratá el archivo entero como body". Los headers ``---``
        # en la primera línea NO son válidos como spec, así que igual no
        # producen tasks fantasma.
        spec.write_text(
            "---\ntype: spec\nphase: [unclosed\n---\n"
            "# F2 — X\n### F2.1.T1 — Task\n- **Modelo**: opus\n",
            encoding="utf-8",
        )
        r = parse_spec_file(spec, tmp_path)
        assert any("malformado" in w for w in r.warnings)
        # Aun con el frontmatter roto, el body se puede parsear porque los
        # headers markdown válidos están abajo.
        assert len(r.tasks) == 1
        assert r.tasks[0].id == "F2.1.T1"


# ---- Merge ---------------------------------------------------------------


def _parsed(**overrides) -> ParsedTask:
    base = dict(
        id="F1.1.T1",
        phase=1,
        package=1,
        task_num=1,
        title="Auth domain",
        description="desc",
        model="claude-sonnet-4-6",
        reason="typed",
        estimate_hours=4.0,
        dependencies=[],
        files=[],
        spec_ref="x.md#F1.1.T1",
    )
    base.update(overrides)
    return ParsedTask(**base)


class TestMergePreservesRuntime:
    def test_status_preserved(self) -> None:
        existing = {
            "meta": {},
            "phases": [],
            "tasks": [
                {
                    "id": "F1.1.T1",
                    "phase": 1,
                    "title": "old title",
                    "description": "old",
                    "model": "old-model",
                    "reason": "old",
                    "status": "done",
                    "dependencies": [],
                    "estimateHours": 1.0,
                    "files": ["real/path.dart"],
                    "specRef": "old.md#F1.1.T1",
                    "comments": [{"note": "existing comment"}],
                }
            ],
        }
        result, diff = merge_tasks(existing, [_parsed()])
        row = result["tasks"][0]
        # runtime preservado
        assert row["status"] == "done"
        assert row["files"] == ["real/path.dart"]
        assert row["comments"] == [{"note": "existing comment"}]
        # declarativos actualizados
        assert row["title"] == "Auth domain"
        assert row["model"] == "claude-sonnet-4-6"
        assert row["estimateHours"] == 4.0
        # diff refleja
        assert len(diff.updated) == 1
        _, _, changed = diff.updated[0]
        assert "title" in changed and "model" in changed

    def test_new_task_uses_backlog_default(self) -> None:
        existing = {"meta": {}, "phases": [], "tasks": []}
        result, diff = merge_tasks(existing, [_parsed()])
        assert len(diff.new_tasks) == 1
        row = result["tasks"][0]
        assert row["status"] == "backlog"
        assert row["comments"] == []


class TestMergeNoDifference:
    def test_unchanged_bucket(self) -> None:
        existing = {
            "meta": {},
            "phases": [],
            "tasks": [
                {
                    "id": "F1.1.T1",
                    "phase": 1,
                    "title": "Auth domain",
                    "description": "desc",
                    "model": "claude-sonnet-4-6",
                    "reason": "typed",
                    "status": "in-progress",
                    "dependencies": [],
                    "estimateHours": 4.0,
                    "files": [],
                    "specRef": "x.md#F1.1.T1",
                    "comments": [],
                }
            ],
        }
        result, diff = merge_tasks(existing, [_parsed()])
        assert diff.updated == []
        assert len(diff.unchanged) == 1
        assert result["tasks"][0]["status"] == "in-progress"


class TestMergeOrphans:
    def test_orphan_not_removed(self) -> None:
        existing = {
            "meta": {"foo": "bar"},
            "phases": [{"id": 1}],
            "tasks": [
                {"id": "F9.9.T99", "phase": 9, "title": "orphan", "status": "done"},
            ],
        }
        result, diff = merge_tasks(existing, [_parsed()])
        assert any(r["id"] == "F9.9.T99" for r in result["tasks"])
        assert any(o["id"] == "F9.9.T99" for o in diff.orphans)

    def test_legacy_id_not_touched(self) -> None:
        existing = {
            "meta": {},
            "phases": [],
            "tasks": [
                {
                    "id": "R-001",
                    "phase": 0,
                    "title": "Monorepo bootstrap",
                    "status": "done",
                    "model": "legacy",
                    "dependencies": [],
                    "estimateHours": 0,
                    "files": [],
                    "specRef": "",
                    "comments": [],
                    "description": "",
                    "reason": "",
                },
            ],
        }
        result, diff = merge_tasks(existing, [_parsed()])
        # Row viejo intacto
        r001 = next(r for r in result["tasks"] if r["id"] == "R-001")
        assert r001["status"] == "done"
        assert r001["title"] == "Monorepo bootstrap"
        # Nueva task agregada al final (después de la vieja)
        assert result["tasks"][-1]["id"] == "F1.1.T1"

    def test_meta_and_phases_preserved(self) -> None:
        existing = {
            "meta": {"note": "custom"},
            "phases": [{"id": 1, "name": "test"}],
            "tasks": [],
        }
        result, _ = merge_tasks(existing, [_parsed()])
        assert result["meta"] == {"note": "custom"}
        assert result["phases"] == [{"id": 1, "name": "test"}]


class TestMergeDepValidation:
    def test_dep_warning_when_missing(self) -> None:
        existing = {"meta": {}, "phases": [], "tasks": []}
        p = _parsed(dependencies=["NON.EXISTENT.ID"])
        _, diff = merge_tasks(existing, [p])
        assert any("NON.EXISTENT.ID" in w for w in diff.dep_warnings)

    def test_no_warning_when_dep_exists(self) -> None:
        existing = {"meta": {}, "phases": [], "tasks": []}
        p1 = _parsed(id="F1.1.T1")
        p2 = _parsed(id="F1.1.T2", dependencies=["F1.1.T1"])
        _, diff = merge_tasks(existing, [p1, p2])
        assert diff.dep_warnings == []


# ---- Roundtrip -----------------------------------------------------------


class TestRoundtrip:
    def test_parse_merge_write_load(self, tmp_path: Path) -> None:
        # Copiamos el fixture a un tmp_path (simulando docs/)
        docs = tmp_path / "docs"
        docs.mkdir()
        (docs / "sample.md").write_text(FIXTURE.read_text(), encoding="utf-8")

        parse = parse_specs(_iter_spec_files(docs, None), docs)
        assert len(parse.tasks) == 3

        tasks_json = tmp_path / "tasks.json"
        existing = load_raw_tasks_json(tasks_json)
        merged, diff = merge_tasks(existing, parse.tasks)
        assert len(diff.new_tasks) == 3

        write_tasks_json(tasks_json, merged, make_backup=False)

        # Ahora ``load_tasks`` del state.py debe cargar sin explotar
        tasks = load_tasks(tasks_json)
        assert len(tasks) == 3
        by_id = {t.id: t for t in tasks}
        assert by_id["F1.1.T9"].dependencies == ["F1.1.T1", "F0.1.T1"]
        assert by_id["F1.1.T9"].estimate_hours == 8.0
        assert by_id["F1.1.T9"].status == "backlog"
        assert by_id["F1.1.T9"].files == [
            "lib/features/auth/presentation/screens/auth_sign_in_screen.dart",
            "lib/features/auth/presentation/widgets/social_login_row.dart",
        ]


# ---- Diff rendering ------------------------------------------------------


class TestRenderDiff:
    def test_renders_without_error(self) -> None:
        # Diff con todos los buckets poblados — no debe crashear.
        p = _parsed()
        existing = {
            "meta": {},
            "phases": [],
            "tasks": [
                {
                    "id": "F0.0.T0",
                    "phase": 0,
                    "title": "orphan",
                    "status": "done",
                    "model": "x",
                    "dependencies": [],
                    "estimateHours": 0,
                    "files": [],
                    "specRef": "",
                    "comments": [],
                    "description": "",
                    "reason": "",
                },
            ],
        }
        merged, diff = merge_tasks(existing, [p])
        buf = io.StringIO()
        console = Console(file=buf, force_terminal=False, width=120)
        parse = ParseResult(tasks=[p], files_scanned=[Path("x.md")])
        render_diff(diff, parse, console)
        out = buf.getvalue()
        assert "NUEVAS" in out
        assert "HUÉRFANAS" in out
        assert "F0.0.T0" in out  # orphan listada
        assert "F1.1.T1" in out  # nueva listada

    def test_render_list(self) -> None:
        p = _parsed()
        parse = ParseResult(tasks=[p], files_scanned=[Path("x.md")])
        buf = io.StringIO()
        console = Console(file=buf, force_terminal=False, width=120)
        render_list(parse, console)
        out = buf.getvalue()
        assert "F1.1.T1" in out
        assert "Auth domain" in out


# ---- CLI e2e mínimo ------------------------------------------------------


class TestCliMain:
    def test_diff_default_no_apply(self, tmp_path: Path, monkeypatch) -> None:
        # Simulamos proyecto: docs/ con un spec + tasks.json vacío
        docs = tmp_path / "docs"
        docs.mkdir()
        (docs / "sample.md").write_text(FIXTURE.read_text(), encoding="utf-8")
        tasks_json = tmp_path / "tasks.json"
        # Config.yaml stub (resolve_project_paths lo resuelve pero no lo lee)
        (tmp_path / "orchestrator").mkdir()
        (tmp_path / "orchestrator" / "config.yaml").write_text("{}", encoding="utf-8")
        # scripts stub para pasar ensure_valid si se llamara — atomize NO lo llama
        # así que no hace falta.

        rc = atomize_main(
            [
                "--project-root",
                str(tmp_path),
                "--specs-dir",
                str(docs),
            ]
        )
        assert rc == 0
        # NO debe haber escrito tasks.json
        assert not tasks_json.exists()

    def test_apply_writes_json(self, tmp_path: Path) -> None:
        docs = tmp_path / "docs"
        docs.mkdir()
        (docs / "sample.md").write_text(FIXTURE.read_text(), encoding="utf-8")
        tasks_json = tmp_path / "tasks.json"
        (tmp_path / "orchestrator").mkdir()
        (tmp_path / "orchestrator" / "config.yaml").write_text("{}", encoding="utf-8")

        rc = atomize_main(
            [
                "--project-root",
                str(tmp_path),
                "--specs-dir",
                str(docs),
                "--apply",
                "--no-backup",
            ]
        )
        assert rc == 0
        assert tasks_json.exists()
        data = json.loads(tasks_json.read_text())
        ids = [t["id"] for t in data["tasks"]]
        assert set(ids) == {"F0.1.T1", "F1.1.T1", "F1.1.T9"}

    def test_apply_creates_backup(self, tmp_path: Path) -> None:
        docs = tmp_path / "docs"
        docs.mkdir()
        (docs / "sample.md").write_text(FIXTURE.read_text(), encoding="utf-8")
        tasks_json = tmp_path / "tasks.json"
        # tasks.json preexistente
        tasks_json.write_text(
            json.dumps({"meta": {}, "phases": [], "tasks": []}), encoding="utf-8"
        )
        (tmp_path / "orchestrator").mkdir()
        (tmp_path / "orchestrator" / "config.yaml").write_text("{}", encoding="utf-8")

        rc = atomize_main(
            [
                "--project-root",
                str(tmp_path),
                "--specs-dir",
                str(docs),
                "--apply",
            ]
        )
        assert rc == 0
        backups = list(tmp_path.glob("tasks.json.bak-*"))
        assert len(backups) == 1

    def test_list_mode(self, tmp_path: Path) -> None:
        docs = tmp_path / "docs"
        docs.mkdir()
        (docs / "sample.md").write_text(FIXTURE.read_text(), encoding="utf-8")
        (tmp_path / "orchestrator").mkdir()
        (tmp_path / "orchestrator" / "config.yaml").write_text("{}", encoding="utf-8")

        rc = atomize_main(
            [
                "--project-root",
                str(tmp_path),
                "--specs-dir",
                str(docs),
                "--list",
            ]
        )
        assert rc == 0


# ---- orch-spec exact output format ---------------------------------------
# These tests use a fixture that mirrors exactly what the orch-spec skill
# generates: frontmatter + accented ES labels (Estimación, Razón, Modelo) +
# sub-bullet Files. Guards against regressions where label mapping, accent
# normalisation, or frontmatter parsing break the atomizer pipeline.

ORCH_SPEC_FIXTURE = (
    Path(__file__).parent / "fixtures" / "atomize" / "orch_spec_output.md"
)


class TestOrchSpecOutputFormat:
    """Parse a file that exactly mirrors orch-spec generated output."""

    def _result(self) -> ParseResult:
        return parse_spec_file(ORCH_SPEC_FIXTURE, ORCH_SPEC_FIXTURE.parent)

    def test_frontmatter_parsed_no_warnings(self) -> None:
        """Valid orch-spec frontmatter must not produce warnings."""
        r = self._result()
        # Only acceptable warning: consumed_by check passes because we include
        # orch-atomizer. There should be zero warnings on a clean spec.
        assert r.warnings == [], f"Unexpected warnings: {r.warnings}"

    def test_all_three_tasks_extracted(self) -> None:
        r = self._result()
        ids = [t.id for t in r.tasks]
        assert ids == ["F1.1.T1", "F1.1.T2", "F1.1.T3"]

    def test_accented_estimacion_label_parsed(self) -> None:
        """- **Estimación**: 4h must map to estimate_hours=4.0."""
        r = self._result()
        by_id = {t.id: t for t in r.tasks}
        assert by_id["F1.1.T1"].estimate_hours == 4.0
        assert by_id["F1.1.T2"].estimate_hours == 8.0

    def test_estimacion_days_converted(self) -> None:
        """- **Estimación**: 2d must map to estimate_hours=16.0 (2×8h)."""
        r = self._result()
        by_id = {t.id: t for t in r.tasks}
        assert by_id["F1.1.T3"].estimate_hours == 16.0

    def test_accented_razon_label_parsed(self) -> None:
        """- **Razón**: ... must map to reason field."""
        r = self._result()
        by_id = {t.id: t for t in r.tasks}
        assert by_id["F1.1.T1"].reason == "Solo tipos, Sonnet alcanza."

    def test_modelo_label_parsed(self) -> None:
        """- **Modelo**: ... must map to model field."""
        r = self._result()
        by_id = {t.id: t for t in r.tasks}
        assert by_id["F1.1.T1"].model == "claude-sonnet-4-6"
        assert by_id["F1.1.T3"].model == "claude-opus-4-7"

    def test_dependencies_parsed(self) -> None:
        r = self._result()
        by_id = {t.id: t for t in r.tasks}
        assert by_id["F1.1.T1"].dependencies == ["F0.1.T1"]
        assert by_id["F1.1.T3"].dependencies == ["F1.1.T2", "F1.1.T1"]

    def test_files_sub_bullets_parsed(self) -> None:
        r = self._result()
        by_id = {t.id: t for t in r.tasks}
        assert by_id["F1.1.T1"].files == [
            "lib/features/auth/domain/models/user.dart",
            "lib/features/auth/domain/models/session.dart",
        ]

    def test_description_multiline_captured(self) -> None:
        """Multiline description before first bullet must be captured."""
        r = self._result()
        by_id = {t.id: t for t in r.tasks}
        desc = by_id["F1.1.T3"].description
        assert "Como usuario" in desc
        assert "Happy path" in desc
        assert "red caída" in desc
        # Field bullets must NOT bleed into description
        assert "**Modelo**" not in desc

    def test_phase_set_from_header(self) -> None:
        r = self._result()
        for t in r.tasks:
            assert t.phase == 1

    def test_full_roundtrip_with_frontmatter(self, tmp_path: Path) -> None:
        """Full parse → merge → write → load roundtrip for orch-spec output."""
        docs = tmp_path / "docs"
        docs.mkdir()
        (docs / "spec.md").write_text(
            ORCH_SPEC_FIXTURE.read_text(encoding="utf-8"), encoding="utf-8"
        )

        parse = parse_specs(
            _iter_spec_files(docs, None),
            docs,
            expected_project_id="sample-project",
        )
        assert len(parse.tasks) == 3
        assert parse.warnings == []

        tasks_json = tmp_path / "tasks.json"
        existing = load_raw_tasks_json(tasks_json)
        merged, diff = merge_tasks(existing, parse.tasks)
        assert len(diff.new_tasks) == 3

        write_tasks_json(tasks_json, merged, make_backup=False)

        tasks = load_tasks(tasks_json)
        by_id = {t.id: t for t in tasks}
        assert by_id["F1.1.T3"].estimate_hours == 16.0
        assert by_id["F1.1.T3"].dependencies == ["F1.1.T2", "F1.1.T1"]
        assert by_id["F1.1.T3"].status == "backlog"
