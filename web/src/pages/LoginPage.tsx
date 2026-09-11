import { useState, type FormEvent } from "react"
import { useNavigate } from "react-router-dom"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/hooks/useAuth"

export function LoginPage() {
  const [value, setValue] = useState("")
  const { setToken } = useAuth()
  const navigate = useNavigate()

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    const trimmed = value.trim()
    if (!trimmed) return
    setToken(trimmed)
    navigate("/", { replace: true })
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-50 px-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="space-y-2 text-center">
          <div className="mx-auto flex h-12 w-12 items-center justify-center">
            <img src="/favicon.svg" alt="Orch" className="h-12 w-12" />
          </div>
          <CardTitle>Orch Dashboard</CardTitle>
          <CardDescription>
            Paste the token you used to start{" "}
            <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">
              orch dashboard --token ...
            </code>
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="token">Bearer token</Label>
              <Input
                id="token"
                type="password"
                autoComplete="off"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="e.g. s3cr3t-token"
                autoFocus
              />
            </div>
            <Button type="submit" className="w-full" disabled={!value.trim()}>
              Enter
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
