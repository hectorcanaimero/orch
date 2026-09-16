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
import { t } from "@/i18n"

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
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="space-y-2 text-center">
          <div className="mx-auto flex h-12 w-12 items-center justify-center">
            <img src="/favicon.svg" alt="Orch" className="h-12 w-12" />
          </div>
          <CardTitle className="text-lg">{t("login.title")}</CardTitle>
          <CardDescription>
            {t("login.paste")}{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">orch dashboard --token …</code>
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="token">{t("login.token")}</Label>
              <Input
                id="token"
                type="password"
                autoComplete="off"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder={t("login.placeholder")}
                autoFocus
              />
            </div>
            <Button type="submit" className="w-full" disabled={!value.trim()}>
              {t("login.enter")}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
