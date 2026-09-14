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
  if (status === 401) return "Your session or link is no longer valid. Open the link you were sent again."
  if (status === 403) return "This page is not available with your access. The summary is."
  if (status !== undefined && status >= 500) return "The dashboard ran into an error reading the project. Try again in a moment."
  if (status === undefined && (!e.message || e.message === "Network Error")) {
    return "Couldn't reach the dashboard. Check that orch dashboard is still running."
  }
  return e.message ?? "Unknown error"
}
