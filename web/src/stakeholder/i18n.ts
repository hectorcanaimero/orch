// The words the client portal uses, one dictionary per language. English is
// the source of truth: es and pt are typed against it, so a key added to en
// and forgotten in another language fails the type check instead of printing
// `undefined` to a client. Dates, relative times and plurals come from Intl.

export type Lang = "en" | "es" | "pt"

export const LOCALES: Record<Lang, string> = { en: "en-US", es: "es", pt: "pt-BR" }

// plural picks a word form by the language's own plural rules.
function plural(lang: Lang, n: number, one: string, other: string): string {
  return new Intl.PluralRules(LOCALES[lang]).select(n) === "one" ? one : other
}

const en = {
  summary: "Overview",
  roadmap: "Roadmap",
  progress: (done: number, total: number) => `${done} of ${total} deliverables`,
  since: "Since your last visit",
  sinceFirst: "Delivered this week",
  nothingNew: "Nothing new since your last visit.",
  onHold: "On hold",
  now: "Now",
  next: "Next",
  done: "Delivered",
  inProgress: "In progress",
  blocked: "On hold",
  pending: "Not started",
  updated: (rel: string) => `Updated ${rel}`,
  other: "Other deliverables",
  budget: "Budget",
  spent: "Spent",
  loadError: "We couldn't load the project status. Try again in a moment.",
  linkInvalid: "This link is no longer valid. Ask the team for a new one.",
  fileBlocked:
    "Your browser blocks a local file from reading another local file. Serve this folder over HTTP (for example `python3 -m http.server`) and open it from there.",
  loading: "Loading…",
  showAll: (n: number) => `Show all ${n} deliveries`,
  showLess: "Show less",
  documents: "Documents",
  allDocuments: "All documents",
  updatedOn: (day: string) => `Updated ${day}`,
  noDocument: "That document is no longer published.",
  quality: "How every delivery is checked",
  gates: {
    tests: "Automated tests",
    typecheck: "Type checks",
    lint: "Code style checks",
    build: "A full build",
    review: "Automated code review",
  },
  verified: (verified: number, delivered: number, firstPass: number) =>
    `${verified} of ${delivered} ${plural("en", delivered, "delivery", "deliveries")} passed every check, ${firstPass} on the first try.`,
  allDelivered: "Everything planned has been delivered.",
  finish: (day: string, early: boolean) => `We expect to finish around ${day}${early ? " (early estimate)" : ""}.`,
  working: "Work is in progress.",
  nowPhase: (name: string) => `Now: ${name}.`,
  itemsOnHold: (n: number) => `${n} ${plural("en", n, "item", "items")} on hold.`,
}

export type Copy = typeof en

const es: Copy = {
  summary: "Resumen",
  roadmap: "Hoja de ruta",
  progress: (done, total) => `${done} de ${total} entregables`,
  since: "Desde tu última visita",
  sinceFirst: "Entregado esta semana",
  nothingNew: "Nada nuevo desde tu última visita.",
  onHold: "En espera",
  now: "Ahora",
  next: "Después",
  done: "Entregado",
  inProgress: "En curso",
  blocked: "En espera",
  pending: "Pendiente",
  updated: (rel) => `Actualizado ${rel}`,
  other: "Otros entregables",
  budget: "Presupuesto",
  spent: "Consumido",
  loadError: "No pudimos cargar el estado del proyecto. Vuelve a intentarlo en un momento.",
  linkInvalid: "Este enlace ya no es válido. Pide al equipo un enlace nuevo.",
  fileBlocked:
    "Tu navegador no deja que un archivo local lea otro archivo local. Sirve esta carpeta por HTTP (por ejemplo `python3 -m http.server`) y ábrela desde ahí.",
  loading: "Cargando…",
  showAll: (n) => `Ver las ${n} entregas`,
  showLess: "Ver menos",
  documents: "Documentos",
  allDocuments: "Todos los documentos",
  updatedOn: (day) => `Actualizado el ${day}`,
  noDocument: "Ese documento ya no está publicado.",
  quality: "Cómo verificamos cada entrega",
  gates: {
    tests: "Pruebas automáticas",
    typecheck: "Revisión de tipos",
    lint: "Revisión de estilo del código",
    build: "Compilación completa",
    review: "Revisión automática del código",
  },
  verified: (verified, delivered, firstPass) =>
    `${verified} de ${delivered} ${plural("es", delivered, "entrega pasó", "entregas pasaron")} todas las verificaciones, ${firstPass} a la primera.`,
  allDelivered: "Todo lo planificado está entregado.",
  finish: (day, early) => `Estimamos terminar el ${day}${early ? " (estimación preliminar)" : ""}.`,
  working: "El trabajo está en curso.",
  nowPhase: (name) => `Ahora: ${name}.`,
  itemsOnHold: (n) => `${n} ${plural("es", n, "tema", "temas")} en espera.`,
}

const pt: Copy = {
  summary: "Resumo",
  roadmap: "Roteiro",
  progress: (done, total) => `${done} de ${total} entregas`,
  since: "Desde a sua última visita",
  sinceFirst: "Entregue esta semana",
  nothingNew: "Nada de novo desde a sua última visita.",
  onHold: "Em espera",
  now: "Agora",
  next: "Depois",
  done: "Entregue",
  inProgress: "Em andamento",
  blocked: "Em espera",
  pending: "Não iniciado",
  updated: (rel) => `Atualizado ${rel}`,
  other: "Outras entregas",
  budget: "Orçamento",
  spent: "Consumido",
  loadError: "Não conseguimos carregar o status do projeto. Tente novamente em instantes.",
  linkInvalid: "Este link não é mais válido. Peça um novo link à equipe.",
  fileBlocked:
    "Seu navegador impede que um arquivo local leia outro arquivo local. Sirva esta pasta por HTTP (por exemplo `python3 -m http.server`) e abra-a por lá.",
  loading: "Carregando…",
  showAll: (n) => `Ver todas as ${n} entregas`,
  showLess: "Ver menos",
  documents: "Documentos",
  allDocuments: "Todos os documentos",
  updatedOn: (day) => `Atualizado em ${day}`,
  noDocument: "Esse documento não está mais publicado.",
  quality: "Como verificamos cada entrega",
  gates: {
    tests: "Testes automatizados",
    typecheck: "Verificação de tipos",
    lint: "Verificação de estilo do código",
    build: "Build completo",
    review: "Revisão automática do código",
  },
  verified: (verified, delivered, firstPass) =>
    `${verified} de ${delivered} ${plural("pt", delivered, "entrega passou", "entregas passaram")} em todas as verificações, ${firstPass} na primeira tentativa.`,
  allDelivered: "Tudo o que foi planejado está entregue.",
  finish: (day, early) => `Prevemos terminar em ${day}${early ? " (estimativa preliminar)" : ""}.`,
  working: "O trabalho está em andamento.",
  nowPhase: (name) => `Agora: ${name}.`,
  itemsOnHold: (n) => `${n} ${plural("pt", n, "item", "itens")} em espera.`,
}

export const copy: Record<Lang, Copy> = { en, es, pt }

// readerLang is the language of the browser, for the moments before the
// snapshot (and so the project's language) has arrived.
export function readerLang(): Lang {
  const nav = typeof navigator !== "undefined" ? navigator.language.toLowerCase() : ""
  if (nav.startsWith("es")) return "es"
  if (nav.startsWith("pt")) return "pt"
  return "en"
}
