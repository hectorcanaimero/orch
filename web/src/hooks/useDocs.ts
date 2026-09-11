import { useQuery } from "@tanstack/react-query"
import { apiClient, getToken, API_BASE_URL } from "@/lib/api"

export interface DocEntry {
  path: string
  title: string
  category: string
  sub_category: string
  size_bytes: number
  modified_iso: string
}

export interface DocsListResponse {
  docs: DocEntry[]
}

export function useDocs() {
  return useQuery<DocsListResponse>({
    queryKey: ["docs"],
    queryFn: async () => {
      const { data } = await apiClient.get<DocsListResponse>("/api/docs")
      return data
    },
    staleTime: 60_000,
  })
}

export function useDocContent(path: string | null) {
  return useQuery<string>({
    queryKey: ["doc-content", path],
    queryFn: async () => {
      if (!path) return ""
      const token = getToken()
      const url = new URL(
        `/api/docs/content?path=${encodeURIComponent(path)}`,
        API_BASE_URL,
      )
      if (token) url.searchParams.set("token", token)
      const res = await fetch(url.toString())
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
      return res.text()
    },
    enabled: !!path,
    staleTime: 120_000,
  })
}
