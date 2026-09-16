import { t } from "@/i18n"

interface MaybeHttpError {
  response?: { status?: number }
  message?: string
}

// describeLoadError turns a failed data load into a sentence a person can act
// on. Pages used to print axios' own text, "Request failed with status code
// 403", which names neither the problem nor the way out.
export function describeLoadError(error: unknown): string {
  const e = (error ?? {}) as MaybeHttpError
  const status = e.response?.status
  if (status === 401) return t("errors.expired")
  if (status === 403) return t("errors.forbidden")
  if (status !== undefined && status >= 500) return t("errors.server")
  if (status === undefined && (!e.message || e.message === "Network Error")) {
    return t("errors.unreachable")
  }
  return e.message ?? t("common.unknown_error")
}
