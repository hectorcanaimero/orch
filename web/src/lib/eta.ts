interface EtaFields {
  eta_date?: string | null
  eta_confidence?: string
  eta_hours: number | null
}

// formatEta is the one way a finish estimate is shown. The date comes from
// the same velocity projection as the Pace page; hours of work are only a
// fallback for a project with no measured pace yet.
export function formatEta({ eta_date, eta_confidence, eta_hours }: EtaFields): {
  value: string
  detail: string
} {
  if (eta_date) {
    const day = new Date(`${eta_date}T00:00:00Z`)
    const value = day.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" })
    return { value, detail: eta_confidence ? `${eta_confidence} confidence` : "" }
  }
  if (eta_hours != null) return { value: `${eta_hours.toFixed(1)}h`, detail: "of work left" }
  return { value: "—", detail: "" }
}
