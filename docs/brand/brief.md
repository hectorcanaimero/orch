# Orch — Brand Brief

**Fecha**: 2026-08-27
**Versión**: 0.1 — draft inicial, pendiente aprobación del founder

---

## 1. Qué es en una frase

> **Orch corre tus agentes AI en paralelo, controla el gasto por proveedor, y le da a tu cliente una URL para ver el progreso en tiempo real — sin abrir Slack.**

La frase tiene tres partes deliberadas: **qué hace técnicamente** (corre agentes), **qué protege** (el budget), **qué entrega al mundo exterior** (el dashboard del cliente). El diferenciador está en el tercio final.

---

## 2. Para quién

**ICP primario**: freelancers y small agencies (1–5 personas) que construyen software con AI agents para clientes que esperan ver avance.

**El dolor exacto**: "trabajo con AI pero cuando el cliente pregunta '¿cómo va?' tengo que armar un status report a mano."

**Tres personas concretas**:

| Persona | Situación | Lo que orch resuelve |
|---------|-----------|----------------------|
| **Consultor freelance** | Construye chatbot para cliente Y. El cliente pregunta por WhatsApp. | Manda una URL con token. El cliente ve el sprint en vivo. |
| **Agencia de desarrollo** | 5 proyectos activos, cada cliente quiere updates. | Un dashboard por cliente, autenticado, desde el mismo server. |
| **Founder de startup** | Experimentos PMF con AI. El board pide métricas. | Weekly digest auto-generado: spend, features shipped, ETA. |

---

## 3. Tres rasgos de personalidad

### Honesto
No promete lo que no puede entregar. El README tiene una sección "Advertencias honestas" con tres razones para **no** usar orch. El findings loop reporta hallazgos a GitHub — dogfooding real, no marketing. La doc distingue "lo que hay hoy" de "lo que viene." Cuando algo está roto, lo dice.

### Preciso
Zero-dep discipline en la SPA: shadcn hand-written, SVG charts a mano, bundle en 114 kB gzipped. Auth con 4 capas independientes, cada una falla loud. 1000+ tests. Conventional commits. No corta esquinas "porque total funciona." Cada decisión técnica tiene una razón documentada.

### Pragmático
"Path más pragmático" es una frase que aparece en los docs. Corta scope conscientemente. No construye IDE propio cuando una VS Code extension resuelve el 80%. No aspira a plataforma cuando "OSS enfocado con docs sólidos" ya es ganador. Resuelve el dolor real, no el dolor aspiracional.

---

## 4. Tres marcas de referencia (estilo, no copiar)

### Linear
Dark, preciso, veloz. Herramienta de dev con UI que un PM puede entender sin que se la expliquen. Sin iconos innecesarios, sin gradients de marketing. La confianza sale de la ejecución, no del color. Orch aspira a exactamente eso: que el dashboard se vea como tool de ingenieros, no como producto de marketing.

### Railway
Deploy platform que no te trata de idiota. Dark aesthetic, dashboard limpio, honesto sobre la complejidad interna. Tiene logs, métricas técnicas, usage — y las muestra sin disculparse. Un stakeholder puede entender el estado de un deploy sin saber qué es un pod de Kubernetes. Esa dualidad (técnico + legible para el cliente) es la misma que necesita orch.

### Warp
Terminal que tomó la decisión de verse bien sin traicionar su naturaleza de CLI. Dark, tipografía impecable, alta densidad de información. Demuestra que "CLI + UI cuidada" no es contradicción. Orch tiene ese mismo ADN: vive en la terminal pero su dashboard puede impresionar a un cliente.

---

## 5. Tres anti-referencias (lo que NO queremos parecer)

### Jira
El archienémigo conceptual. Burocracia acumulada durante 20 años. Configuración que requiere certificación. Si alguien mira el dashboard de orch y piensa "esto me recuerda a Jira," algo salió muy mal. Orch es lo que Jira fingiría ser si empezara hoy.

### Monday.com
Colorido, "amigable," bubble charts que no dicen nada. Parece un juguete con suscripción de $24/mes por usuario. No transmite precisión ni seriedad técnica. Orch opera en la intersección de dev tool y reporte para cliente — no puede verse como project management estilo millennial.

### Hashnode / DEV.to *(como referencia visual, no de contenido)*
Beige, muy redondeado, sans-serif "friendly," muchos emojis, mucho blanco. Evoca blog de dev junior. Orch resuelve problemas de ingeniería serios; tiene que verse proporcional a eso.

---

## 6. Paleta propuesta

### Base — Zinc (ya establecida)
El SPA actual usa zinc como base. No hay razón para cambiarla — funciona y tiene coherencia interna.

| Rol | Token Tailwind | Hex |
|-----|---------------|-----|
| App background | `zinc-950` | `#09090b` |
| Sidebar | `zinc-950` | `#09090b` |
| Card / surface | `white` / `zinc-900` | `#18181b` |
| Texto principal | `zinc-900` / `zinc-50` | — |
| Borde | `zinc-200` / `zinc-800` | — |

### Acento de marca — Violet 600 (propuesta)
El SPA ya usa violet para elementos de progreso y sprint (ETA card, velocity, badge de confianza). Es el color que más aparece en contextos positivos/de avance. La propuesta es **elevarlo a color de marca** en lugar de tratarlo como color semántico entre iguales.

| Rol | Token | Hex |
|-----|-------|-----|
| Brand accent | `violet-600` | `#7c3aed` |
| Brand accent hover | `violet-700` | `#6d28d9` |
| Brand light (badges, pills) | `violet-100` | `#ede9fe` |

**Alternativa a considerar — Indigo 600** (`#4f46e5`): más "tech blue," menos "startup." Queda entre el violet y el azul puro. Transmite precisión sin calidez. Decisión pendiente del founder (ver Preguntas abiertas).

### Semánticos — mantener
Estos colores ya tienen coherencia en la UI y no deben cambiar:

| Estado | Color | Tailwind |
|--------|-------|---------|
| Done / éxito | Emerald 500 | `#10b981` |
| Blocked / error | Rose 500 | `#f43f5e` |
| Warning / baja confianza | Amber 500 | `#f59e0b` |
| Info / restante | Sky 500 | `#0ea5e9` |

---

## 7. Tipografía — Google Fonts

### Opción A — Recomendada: DM Sans + JetBrains Mono

| Rol | Fuente | Pesos |
|-----|--------|-------|
| UI / headings / body | **DM Sans** | 300, 400, 500, 600 |
| Código / CLI / IDs | **JetBrains Mono** | 400, 500 |

**Por qué DM Sans**: geométrica sin serifa, alta legibilidad en fondos oscuros. Tiene carácter propio sin ser extravagante — distingue orch de las miles de tools con Inter. Transmite "diseñada por ingenieros, no por un brand studio." Usada por tools técnicos antes de que Inter la desplazara.

**Por qué JetBrains Mono**: el monospace nativo de JetBrains tiene la mejor legibilidad de glifos técnicos (`0`, `O`, `l`, `1`) y un ritmo visual limpio en snippets de CLI. Coherente con el ecosistema donde vive orch (IDEs, terminal).

---

### Opción B — Familiar y segura: Inter + JetBrains Mono

| Rol | Fuente | Pesos |
|-----|--------|-------|
| UI / headings / body | **Inter** | 400, 500, 600, 700 |
| Código / CLI / IDs | **JetBrains Mono** | 400, 500 |

El estándar de facto en dev tools. Funciona perfectamente. Riesgo: en un mercado donde Linear, Vercel, Railway y Supabase ya usan Inter (o sus clones), orch no se distingue tipográficamente. Solo recomendado si la paleta y el logo son lo suficientemente fuertes como para cargar toda la identidad.

---

### Opción C — Con carácter propio: Plus Jakarta Sans + IBM Plex Mono

| Rol | Fuente | Pesos |
|-----|--------|-------|
| UI + wordmark | **Plus Jakarta Sans** | 400, 500, 600, 700 |
| Código / CLI / IDs | **IBM Plex Mono** | 400, 500 |

**Plus Jakarta Sans** tiene más personalidad que Inter sin perder legibilidad. El tracking en mayúsculas funciona bien para wordmarks cortos como "ORCH." **IBM Plex Mono** (de IBM Research) tiene más carácter que Fira Code — los terminales `->`, `=>` y operadores se ven más intencionales. Opción para cuando se quiera una identidad más marcada.

---

## Decisiones de naming y color

**Nombre: orch — final.**
Cuatro letras minúsculas, una sola pronunciación, funciona como verbo ("let me orch this"). Mismo ADN estético de tools serias del ecosistema: `curl`, `grep`, `tmux`, `brew`, `make`. La "marketabilidad" no la da el nombre — la da la ejecución. Linear, Railway y Warp son nombres sin magia; la marca la construyó el producto.

**Color de marca: Violet 600 — final.**
Violet ya está en el SPA para todo lo que importa (ETA, velocidad, sprint health). Elevarlo a acento de marca no requiere cambiar nada existente — simplifica: un color que era semántico pasa a ser *el* color. Indigo es corporate blue disfrazado. Emerald se confunde con el estado "done." Violet es la elección correcta.

---

*Próximo paso: diseño del logo con `/logo-designer`.*
