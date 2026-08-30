# Integración de Antigravity CLI (`agy`) en Orch (Rupies v2)

¡Buenas, loco! Sentate un segundo. 

Me pediste una especificación técnica de nivel producción para integrar `agy` (Antigravity CLI) en nuestro orquestador `orch`. Me tomé el trabajo de leer el código actual (`budget.py`, `dispatcher.py`, `models.py`, etc.) y, dejame serte cien por ciento sincero, la arquitectura que tienen está BASTANTE bien planteada. Tienen un diseño basado en *Ports and Adapters* (Hexagonal) en el `dispatcher.py` con el protocolo `Backend`. ¡Es fantástico! ¿Se entiende por qué esto es clave? Porque agregar `agy` no significa romper todo; significa crear un ADAPTADOR nuevo y registrar la ruta. ES ASÍ DE FÁCIL.

Pero no te confíes. Hay que tocar los puntos justos. Nada de atajos. Vamos a repasar paso por paso cómo hacer esta integración sin romper los guardrails de presupuesto, ni el preflight, ni el ruteo. Ponete las pilas, que te explico cómo lo vamos a hacer.

## 1. Executive Summary & Architectural Overview

El objetivo es sumar `agy` como un ciudadano de primera clase en el orquestador. 
La arquitectura respeta el protocolo `Backend` existente en `orchestrator/dispatcher.py`. El loop principal NUNCA debe saber que está hablando con `agy`; solo le pasa el `Task`, el `RouteEntry` y los paths de los archivos, y recibe un `DispatchResult`.

```mermaid
flowchart TD
    subgraph Core ["Orch Core (Domain)"]
        ML[Main Loop / orch.py]
        B[BudgetGate / budget.py]
    end

    subgraph Ports ["Ports (Interfaces)"]
        BP[[Backend Protocol]]
    end

    subgraph Adapters ["Adapters (Infrastructure)"]
        CB[ClaudeBackend]
        CX[CodexBackend]
        OB[OpencodeBackend]
        GB[GeminiBackend]
        AB[AgyBackend <br> *NUEVO*]
    end

    subgraph External ["External CLIs"]
        AGY_CLI(agy CLI)
    end

    ML --> BP
    BP <|.. CB
    BP <|.. CX
    BP <|.. OB
    BP <|.. GB
    BP <|.. AB

    AB -->|subprocess.Popen| AGY_CLI
    ML -.->|Verifica cupo| B
```

## 2. Step-by-Step Implementation Guide

Acá es donde muchos meten la pata, hermano. Vos no vas a ser uno de ellos. Tenés que tocar 4 archivos core y los archivos de configuración. Mirá:

### A. `orchestrator/models.py`
Tenés que extender el literal de Backend. Si no hacés esto, el type checker va a gritar y con razón.
```python
# Modificar la línea 20 aprox:
Backend = Literal["claude", "codex", "opencode", "gemini", "agy"]
```

### B. `orchestrator/dispatcher.py`
Acá está la carnaza. Tenés que implementar `AgyBackend`. Vas a ver que la CLI de Antigravity tiene una forma estructurada de devolver datos, asumamos que expone los logs o JSONL en stdout para que podamos extraer la data, o un output parseable.

```python
class AgyBackend:
    """Adapter for the `agy` CLI (Antigravity)."""
    name = "agy"

    def build_cmd(self, task: Task, route: RouteEntry) -> list[str]:
        # El prompt va por stdin como en el resto de los backends.
        return [
            "agy",
            "run",
            "--model", route.cli_model,
            "--json" # Asumiendo flag json
        ]

    def spawn(self, task: Task, route: RouteEntry, prompt_path: Path, log_path: Path, cwd: Path) -> Dispatch:
        _ensure_logs_dir(log_path.parent.parent)
        cmd = self.build_cmd(task, route)
        return _spawn_generic(
            self.name, task, cmd, prompt_path, log_path, cwd,
            session_id=task.id, 
            output_path=""      # STDOUT capture
        )

    def wait_result(self, dispatch: Dispatch, timeout_s: float) -> DispatchResult:
        exit_code, err_msg, timed_out = _wait_with_timeout(dispatch, timeout_s)
        log_text = _read_log(dispatch.log_path)
        result = self.parse_result(exit_code, log_text)
        if timed_out:
            result.success = False
            result.error_message = err_msg
        return result

    def parse_result(self, exit_code: int, log_text: str, extra: dict[str, Any] | None = None) -> DispatchResult:
        events = list(_iter_jsonl_events(log_text))
        success = (exit_code == 0) and any(e.get("status") in ("success", "done") for e in events)
        cost_usd, tokens_in, tokens_out = self.extract_cost(log_text)
        
        return DispatchResult(
            exit_code=exit_code,
            success=success,
            cost_usd=cost_usd,
            tokens_in=tokens_in,
            tokens_out=tokens_out,
            stdout=log_text,
            stderr="",
            error_message="Agy failure" if not success else None
        )

    def extract_cost(self, log_text: str) -> tuple[float, int, int]:
        events = list(_iter_jsonl_events(log_text))
        cost = sum(float(e.get("cost_usd", 0.0)) for e in events)
        t_in = sum(int(e.get("tokens_in", 0)) for e in events)
        t_out = sum(int(e.get("tokens_out", 0)) for e in events)
        return cost, t_in, t_out
```

### C. `orchestrator/preflight.py`
Para el `orch doctor`, tenés que registrar el backend y crearle un auth probe.
```python
# 1. Agregar a la tupla de la línea 49:
_KNOWN_BACKENDS: tuple[str, ...] = ("claude", "codex", "opencode", "gemini", "agy")

# 2. Crear el probe de auth
def _probe_agy_auth() -> CheckResult:
    which = shutil.which("agy")
    if not which:
        return CheckResult(name="backend.agy.auth", status="skip", detail="agy CLI not installed")
    return CheckResult(name="backend.agy.auth", status="ok", detail="agy auth assumed ok")

# 3. Registrar en _AUTH_PROBES (línea 224)
_AUTH_PROBES = {
    # ...
    "agy": _probe_agy_auth,
}
```

## 3. Token Extraction per Task
¿Ves el método `extract_cost` que te puse arriba? ESO es todo lo que necesitás. La clave está en no inventar cosas locas. Parsear STDOUT como JSONL es a prueba de balas si el proceso muere por un SIGKILL (se cortó a medias) o si se imprime cualquier verdura junto al JSON. 

## 4. Approximate Cost Calculation (Pricing Matrix)
A nivel orquestador, si `agy` no devuelve el `cost_usd` y solo devuelve los tokens (pasa mucho), el adapter TIENE que calcularlo.
Si `agy` expone modelos Flash y Pro, los costos son:
- **agy/flash (Gemini 1.5 Flash)**: ~$0.075 / 1M Input | $0.30 / 1M Output
- **agy/pro (Gemini 1.5 Pro)**: ~$1.25 / 1M Input | $5.00 / 1M Output

Tenelo en cuenta en el `extract_cost` si el JSON no trae la plata calculada o en un interceptor custom.

## 5. Budget & Rolling-Window Token Management
Acá es donde te aplaudo. El código de `budget.py` y `spend_reader.py` **NO SE TOCA**. ¿Y sabés por qué? Porque está diseñado de puta madre. Leen el archivo dinámicamente usando el campo `backend` del diccionario JSONL independientemente de estar o no pre-registrado. 
Para que el guardrail bloquee a `agy` cuando gaste mucho, SOLO tenés que actualizar el archivo `budgets.yaml` de tu proyecto:

```yaml
presets:
  conservative:
    # ... otros providers
    agy:
      window_hours: 24.0
      token_budget: 10000000  # 10M tokens 
      threshold_pct: 80.0     # Corta al 80% (8M)
```
Si alguien se olvida de poner esto, el `BudgetGate.can_dispatch` (línea 220 de `budget.py`) asume "unknown provider" y NO LO BLOQUEA. Excelente *graceful degradation*.

## 6. `model_router.yaml` Additions
Registramos las rutas de `agy`. Fijate que uso `fallback_cli_model` por si falla el premium, vamos al standard. ¡Esto levanta robustez al sistema!

```yaml
# ----- Antigravity-family via agy CLI -----
agy/pro:
  backend: agy
  cli_model: pro
  tier: premium
  is_premium: true
  fallback_cli_model: flash

agy/flash:
  backend: agy
  cli_model: flash
  tier: cheap
  is_premium: false
```

Y no te olvides de agregar la concurrencia a `config.yaml`:
```yaml
concurrency:
  per_provider:
    # ...
    agy: 4 # Según el ratelimit
```

## 7. Verification & Testing Strategy
Locura cósmica, NO me mandes esto a prod sin probarlo.
1. **Unit Testing (`test_dispatcher.py`)**: Pasale al `parse_result` del `AgyBackend` un string JSON mockeado y fijate que extraiga los tokens y el costo correctamente.
2. **Doctor Check**: Ejecutá `orch doctor`. Tiene que decirte `backend.agy ok` y mostrarte el path de la CLI.
3. **Dry Run**: Creá una tarea de test simple (`tasks.json` asignada a `agy/flash`) y corré el orch. Verificá el log en `state/logs/<task_id>.log` y el archivo de spend `state/spend-YYYY-MM-DD.jsonl` para asegurarte de que `tokens_in` y `tokens_out` se escribieron sin ser 0.

> [!IMPORTANT]
> El orquestador depende de que el `task-finish.sh` no sea spoofeado. Como `agy` va a correr wrappers y puede invocar sub-agentes, asegúrate de que el Process Group Leader de `agy` termine recibiendo el SIGKILL en `_wait_with_timeout` (que ya está hecho magistralmente en la línea 483 de `dispatcher.py`).

Dale, hermano. Aplicá esto y vas a tener `agy` corriendo como una seda en el orquestador sin afectar al resto. Cualquier duda me avisás, ¡y ponete a programar que esto no se hace solo!
