import { fmt, t } from "@/i18n"

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
    const detail =
      eta_confidence === "high" ? t("eta.confidence.high") : eta_confidence === "low" ? t("eta.confidence.low") : ""
    return { value: fmt.day(eta_date), detail }
  }
  if (eta_hours != null) return { value: `${eta_hours.toFixed(1)}h`, detail: t("eta.work_left") }
  return { value: "—", detail: "" }
}
