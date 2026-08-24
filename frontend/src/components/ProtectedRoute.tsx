import { useEffect, useState } from "react"
import { Navigate } from "react-router-dom"
import { useAuth } from "@/hooks/useAuth"
import type { ReactNode } from "react"

interface ProtectedRouteProps {
  children: ReactNode
}

export function ProtectedRoute({ children }: ProtectedRouteProps) {
  const { isAuthenticated } = useAuth()
  const [isSetup, setIsSetup] = useState<boolean | null>(null)
  const [isLoading, setIsLoading] = useState(true)

  useEffect(() => {
    const checkSetup = async () => {
      try {
        const response = await fetch("/api/config/status")
        if (response.ok) {
          const data = await response.json()
          setIsSetup(data.is_setup ?? true)
        } else {
          setIsSetup(true)
        }
      } catch {
        setIsSetup(true)
      } finally {
        setIsLoading(false)
      }
    }
    checkSetup()
  }, [])

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }

  if (isLoading) {
    return <div className="flex items-center justify-center h-screen">Loading...</div>
  }

  if (!isSetup) {
    return <Navigate to="/setup" replace />
  }

  return <>{children}</>
}
