# orch — Presentation Scope (Trilingual)

**Fecha**: 2026-08-26  
**Audiencia**: Freelancers y agencias pequeñas (1–5 personas) que construyen con AI agents para clientes  
**Idiomas**: es-AR · pt-BR · en-US  
**Formato destino**: Single-page scrollable (web) con secciones animadas  
**Stack build**: `/frontend-design` + motion skill + da-vinci (imágenes) + tripo3d (3D)

---

## Marco psicológico aplicado

| Modelo | Dónde se aplica |
|--------|----------------|
| **Jobs to Be Done** | Hook + Problema — el trabajo real que hace orch |
| **Loss Aversion** | Hook + Gap — coste de NO usarlo |
| **Contrast Effect** | Problema + Solución — Before/After visual |
| **AIDA** | Arco completo de la presentación |
| **Reciprocity** | Demo del dashboard — dar antes de pedir |
| **Peak-End Rule** | Demo = pico emocional; CTA = cierre memorable |
| **BJ Fogg (Ability)** | Sección "Cómo funciona" — reducir fricción percibida |
| **Foot-in-the-Door** | CTA — `pip install orch` como primer paso mínimo |
| **Social Proof** | Personas + comparativa vs. alternativas |
| **Anchoring** | Comparativa — anclar en "3 semanas de trabajo custom" |

---

## Estructura de secciones (8 bloques)

---

### BLOQUE 0 — HOOK (Attention)
**Trigger**: Pattern interrupt + Loss Aversion

El problema antes de que lo identifiquen como problema.  
No empezamos con el producto. Empezamos con el dolor.

**Concepto visual**: Terminal oscura, un mensaje de Slack que aparece — "any update?"  
**Animación**: El mensaje entra desde el borde, pulsa, se queda. Incomodidad visual intencional.

#### Copies por idioma

| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "Tu cliente acaba de escribir '¿alguna novedad?'. De nuevo." | "Seu cliente acabou de mandar outra mensagem: 'tem novidade?'" | "Your client just sent another 'any update?' message." |
| Subtítulo: "Cada hora sin respuesta es un cliente que empieza a dudar." | Subtítulo: "Cada hora sem resposta é um cliente que começa a duvidar." | Subtítulo: "Every hour without an answer is a client reconsidering." |

**Psychological note**: Loss Aversion dice que la pérdida pesa el doble que la ganancia equivalente. No decimos "ganá más clientes" — decimos "no pierdas el que ya tenés".

---

### BLOQUE 1 — PROBLEMA (Interest)
**Trigger**: Jobs to Be Done + Contrast Effect (Before)

El freelancer / agencia no tiene un problema de código. Tiene un problema de visibilidad.

**Concepto visual**: Split-screen animado
- Izquierda: Developer en la terminal, agentes corriendo, logs fluyendo — INVISIBLE para el cliente
- Derecha: Cliente en Slack, WhatsApp, email — esperando, dudando

**Texto ancla**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "Tus agentes trabajan. Tu cliente no lo sabe." | "Seus agentes trabalham. Seu cliente não sabe disso." | "Your agents are running. Your client has no idea." |

**3 puntos de dolor** (bullets animados, uno por uno):
- Screenshots manuales de "progreso"
- Updates por Slack que interrumpen tu flujo
- Reuniones de status que no agregan valor

**Psychological note**: JTBD — el trabajo que el freelancer contrata a orch: "mostrale al cliente que todo está bajo control, sin que yo tenga que explicar nada".

---

### BLOQUE 2 — EL GAP (Interest → Desire)
**Trigger**: Anchoring + Loss Aversion + Contrast vs. alternativas

Hay otras herramientas de orquestación. Ninguna cierra este gap.

**Concepto visual**: Tabla minimalista (estilo Linear) con animación de checkmarks/crosses
```
                    Orquesta agentes   Dashboard para cliente   Setup en minutos
LangGraph               ✓                      ✗                     ✗
CrewAI                  ✓                      ✗                     ✓
Custom solution         ✓                      ✓                    (3 semanas)
orch                    ✓                      ✓                     ✓
```

**Texto ancla**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "Todas las alternativas te dan logs. Ninguna le da algo a tu cliente." | "Todas as alternativas te dão logs. Nenhuma dá algo pro seu cliente." | "Every alternative gives you logs. None gives your client anything." |

**Anchoring note**: "3 semanas de trabajo custom" es el ancla. Hace que orch (10 minutos) parezca trivial. El ancla se planta AQUÍ, antes de revelar el precio/esfuerzo de orch.

---

### BLOQUE 3 — SOLUCIÓN (Desire)
**Trigger**: Simplicity + Contrast Effect (After) + Reciprocity (anticipation)

La presentación de orch. Una frase, una visual, un momento.

**Concepto visual**: Pantalla en negro → transición suave → aparece el combination mark (logo con barras paralelas + wordmark). Las barras se animan como si despacharan tareas. da-vinci genera una imagen ambiente: código + cliente sonriendo al mismo tiempo.

**Frase principal**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "orch: orquestá agentes de IA y mostrales el progreso a tus clientes — en tiempo real." | "orch: orquestre agentes de IA e mostre o progresso para seus clientes — em tempo real." | "orch: run AI agents and show clients live progress — no Slack required." |

**Tagline secundario**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "Un link. Eso es todo lo que tu cliente necesita." | "Um link. É tudo que seu cliente precisa." | "One link. That's all your client needs." |

---

### BLOQUE 4 — CÓMO FUNCIONA (Interest + Desire)
**Trigger**: BJ Fogg (Ability) — reducir fricción percibida

3 pasos. No más. La complejidad es el enemigo de la adopción.

**Concepto visual**: 3 tarjetas animadas que entran en secuencia — tripo3d para render 3D de los íconos si aplica, o ilustraciones minimalistas

**Paso 1 — Definís las tareas**
```json
{ "id": "landing", "description": "Build landing page", "depends_on": [] }
```
"Describís lo que tu agente tiene que hacer. Un JSON simple."

**Paso 2 — orch despacha**
Terminal animation: `orch dispatch`
"orch camina el grafo de dependencias y despacha cada tarea al agente correcto."

**Paso 3 — Tu cliente ve el dashboard**
Browser mockup: URL real, status en tiempo real, ETA, blockers.
"Compartís una URL. Tu cliente sabe exactamente en qué está el agente."

**Copies**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "No hay magia. Hay orden." | "Não tem mágica. Tem organização." | "No magic. Just clarity." |

**BJ Fogg note**: Ability está satisfecha (3 pasos simples). Motivation ya la construimos (dolor del bloque 0-1). El Prompt viene en el CTA. Los tres elementos deben estar presentes para que el comportamiento ocurra.

---

### BLOQUE 5 — DEMO DEL DASHBOARD (Desire → emotional peak)
**Trigger**: Reciprocity + Endowment Effect + Peak-End Rule

Este es el PICO emocional. Se da antes de pedir nada. Reciprocity: dar primero.

**Concepto visual**: Screen recording animado (o recreación interactiva) del dashboard stakeholder view
- Sprint health: barra de progreso real
- ETA calculado
- Lista de tareas con status
- Budget spend

**El momento "aha"**: El usuario ve exactamente lo que VE el cliente. No es un log de developer. Es lenguaje de negocio.

**Copies**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "Esto es lo que ve tu cliente. Sin que vos expliques nada." | "Isso é o que seu cliente vê. Sem você explicar nada." | "This is what your client sees. Without you explaining anything." |

**Endowment Effect note**: Si el viewer puede imaginarse dándole ese link a su próximo cliente, ya "posee" mentalmente orch. Aumenta el valor percibido antes de que instale nada.

---

### BLOQUE 6 — PERSONAS (Social Proof)
**Trigger**: Liking + Unity + Bandwagon

3 perfiles. El viewer se identifica con al menos uno.

**Concepto visual**: 3 cards — da-vinci genera un avatar minimalista por persona. Estilo ilustración de líneas, no foto.

#### Persona 1 — El Freela Solo
> "Tengo 2 clientes activos. Ambos me piden updates constantemente. Con orch les mandé el dashboard link el primer día — no me preguntaron más nada en 3 semanas."

Context: 1 dev, Python, builds AI chatbots for SMBs. Violet accent.

#### Persona 2 — La Agencia Pequeña
> "Somos 3. Teníamos 5 proyectos con agentes corriendo y ninguna forma de mostrarle a los clientes qué pasaba. orch nos ahorró las reuniones de status de toda una semana."

Context: 3 devs, mix of stacks, serving 5+ clients simultaneously.

#### Persona 3 — El Startup Founder
> "Estábamos usando orch para nuestro propio producto. Cuando los inversores pidieron ver el progreso del sprint, les mandamos el link del dashboard. Quedaron impactados."

Context: side project → product, using orch as internal tool.

**Copy por idioma**: adaptar cada quote al registro local (voseo para es-AR, você para pt-BR, directo para en-US).

---

### BLOQUE 7 — ADOPCIÓN / CTA (Action)
**Trigger**: Foot-in-the-Door + Activation Energy + Commitment & Consistency

El CTA no es "comprar". Es dar el primer paso mínimo. Después el pie en la puerta hace el resto.

**3 variantes de CTA** según el estado del viewer:

**CTA principal — Install**
```bash
pip install orch
orch init
orch dispatch
```
"10 minutos. Un proyecto real. Tu primer dashboard funcionando."

**CTA secundario — Ver demo**
"Mirá el dashboard en vivo →" (link a instancia demo pública)

**CTA terciario — Leer docs**
"Manual completo →" (Reciprocity: gratis, sin fricción)

**Copies por idioma**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "3 comandos. Tu próximo cliente tiene su dashboard." | "3 comandos. Seu próximo cliente tem o dashboard dele." | "3 commands. Your next client gets their dashboard." |

**Activation Energy note**: El primer paso (`pip install orch`) es trivialmente fácil. No pedimos que "adopten orch" — pedimos que instalen un paquete. La segunda acción (`orch init`) tiene un wizard que los guía. El compromiso crece de forma orgánica.

---

### BLOQUE 8 — CIERRE / REFUERZO (Action → Retention)
**Trigger**: Peak-End Rule (end) + Commitment & Consistency

El último momento que el viewer recuerda. Debe ser memorable.

**Concepto visual**: Full dark screen, combination mark centrado, tagline final animado. Las barras paralelas se sincronizan como agentes completando tareas — todas verdes.

**Frase de cierre**:
| es-AR | pt-BR | en-US |
|-------|-------|-------|
| "Los mejores freelas no son los que trabajan más — son los que muestran mejor lo que hacen." | "Os melhores freelas não são os que trabalham mais — são os que mostram melhor o que fazem." | "The best freelancers don't work more — they show their work better." |

**Por qué esta frase**: No es sobre orch. Es sobre el viewer. Los hace sentir que la adopción de orch es consistente con su identidad de "profesional que sabe lo que hace". Commitment & Consistency: el viewer que se identifica con esta frase tiene más probabilidad de instalar la herramienta.

---

## Guía de adaptación por idioma

### es-AR (Rioplatense)
- Voseo en toda la copy: "usás", "tenés", "mandás"
- "freela" como sustantivo natural (no "freelancer")
- Tono: directo, cálido, sin corporativo
- Registro: el que usarías en una charla técnica con colegas
- Evitar: "usted", anglicismos innecesarios, tono formal

### pt-BR
- "Você" form en toda la copy
- "freela" también funciona en pt-BR
- Tono: más suave que es-AR, igualmente directo
- Registro: startup brasileiro — informal pero profesional
- Evitar: "senhor/senhora", construcciones pasivas innecesarias

### en-US
- Minimalista — closest to the brand voice (Linear, Railway, Warp)
- Short sentences. No padding.
- Evitar: "leverage", "utilize", "synergy", cualquier jargon corporativo
- Registro: el README de una CLI tool que funciona

---

## Notas para el build

### Assets de marca disponibles (usar estos, no generar)
```
logos/export/logo.svg       — combination mark (barras + wordmark "orch"), viewBox 1024×512
logos/export/icon.svg       — icon mark solo (barras en zinc-950 rounded square), viewBox 512×512
logos/export/logo-512.png   — PNG grande para og:image / social
logos/export/icon-192.png   — PNG para PWA / favicons
logos/export/icon-512.png   — PNG grande del mark solo
```
- Bloque 3 (hero de solución): usar `logo.svg` centrado, animar las barras con motion
- Bloque 8 (cierre): usar `icon.svg` centrado, all bars turning green
- Navbar / header: `logo.svg` a ~160px de ancho

### Assets a generar con da-vinci
1. Imagen ambiente bloque 1: developer en terminal (dark) / cliente en Slack (luz) — split composition, no personas reales
2. Iconos de los 3 pasos (bloque 4): minimalistas, flat, violet accent — deben resonar con la estética de barras paralelas del logo
3. Avatares de los 3 personas (bloque 6): line-art consistente, no fotografía, zinc-950 + violet accent
4. Background texture bloques de transición: network graph / nodes animados — misma paleta

### Assets a generar con tripo3d
1. Logo mark en 3D (bloque 3 → hero moment): las 4 barras paralelas extruídas/flotando en 3D, violet-600 + violet-400, zinc-950 bg
2. Browser 3D mockup (bloque 5): frame de browser con profundidad mostrando el dashboard de orch — el "aha" visual

### Animaciones con motion skill
- Bloque 0: Slack message fade-in, pulsación, hold
- Bloque 1: Split-screen slide-in, bullets aparecen uno por uno con delay
- Bloque 2: Tabla con filas que entran + checkmarks/crosses que se revelan
- Bloque 4: Cards que entran desde abajo con stagger (misma estética que las barras del logo)
- Bloque 5: Dashboard screen-recording o lottie loop
- Bloque 8: Barras del logo sincronizándose, todas turning green

### Paleta y tipografía
- **Base**: zinc-950 (`#09090b`) + zinc-800 para superficies
- **Accent**: violet-600 (`#7c3aed`) + violet-400 (`#a78bfa`) para highlights
- **Texto**: `DM Sans` (Google Fonts) — body, UI
- **Código**: `JetBrains Mono` — snippets de terminal
- **On dark**: texto zinc-50 / zinc-400 para secundario

---

## Orden de producción recomendado

1. **HTML shell** con 8 secciones, layout y paleta — `/frontend-design`
2. **Copy** en los 3 idiomas (toggle de idioma en la presentación) 
3. **Animaciones** por sección — motion skill
4. **Imágenes ambiente** — da-vinci
5. **Elementos 3D** — tripo3d (logo 3D + browser mockup)
6. **Polish** + revisión de flujo emocional completo

---

*Scope creado: 2026-08-26. Basado en marca definida en `docs/brand/brief.md` y assets en `logos/export/`.*
