import { useState } from "react"
import { AlertCircle, CheckCircle2, ChevronRight } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Alert, AlertDescription } from "@/components/ui/alert"

interface SetupStep {
  id: string
  title: string
  description: string
  completed: boolean
}

const BUDGET_PRESETS = ["conservative", "balanced", "aggressive"]
const STATE_BACKENDS = ["file", "sqlite"]
const MODEL_TIERS = ["premium", "standard", "cheap"]

export function SetupWizardPage() {
  const [step, setStep] = useState(0)
  const [projectId, setProjectId] = useState("")
  const [projectRoot, setProjectRoot] = useState("")
  const [stateBackend, setStateBackend] = useState("file")
  const [budgetPreset, setBudgetPreset] = useState("conservative")
  const [specRoot, setSpecRoot] = useState("specs")
  const [modelChoices, setModelChoices] = useState<Record<string, string>>({
    premium: "",
    standard: "",
    cheap: "",
  })
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState("")
  const [success, setSuccess] = useState(false)

  const steps: SetupStep[] = [
    { id: "project", title: "Project Setup", description: "Configure your project", completed: !!projectId && !!projectRoot },
    { id: "backend", title: "State Backend", description: "Choose your data backend", completed: !!stateBackend },
    { id: "budget", title: "Budget Preset", description: "Set spending limits", completed: !!budgetPreset },
    { id: "models", title: "Model Tiers", description: "Choose default models", completed: Object.values(modelChoices).some(Boolean) },
  ]

  const handleNext = async () => {
    if (step === steps.length - 1) {
      await handleSubmit()
    } else {
      setStep(step + 1)
    }
  }

  const handleBack = () => {
    if (step > 0) {
      setStep(step - 1)
    }
  }

  const handleSubmit = async () => {
    if (!projectId || !projectRoot) {
      setError("Project ID and Root are required")
      return
    }

    setIsLoading(true)
    setError("")

    try {
      const response = await fetch("/api/config/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          project_id: projectId,
          project_root: projectRoot,
          state_backend: stateBackend,
          budget_preset: budgetPreset,
          spec_root: specRoot,
          model_choices: Object.fromEntries(
            Object.entries(modelChoices).filter(([_, v]) => v)
          ),
        }),
      })

      if (!response.ok) {
        throw new Error("Failed to save configuration")
      }

      setSuccess(true)
      setTimeout(() => {
        window.location.href = "/"
      }, 2000)
    } catch (err) {
      setError(err instanceof Error ? err.message : "An error occurred")
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <div className="min-h-screen bg-gradient-to-br from-blue-50 to-indigo-100 flex items-center justify-center p-4">
      <div className="w-full max-w-2xl">
        {/* Header */}
        <div className="text-center mb-12">
          <h1 className="text-4xl font-bold text-gray-900 mb-2">orch Setup</h1>
          <p className="text-lg text-gray-600">Configure your orchestrator project</p>
        </div>

        {/* Progress Steps */}
        <div className="mb-8">
          <div className="flex justify-between mb-4">
            {steps.map((s, i) => (
              <div
                key={s.id}
                className={`flex flex-col items-center cursor-pointer transition-all ${
                  i === step ? "opacity-100" : "opacity-60"
                }`}
                onClick={() => i < step && setStep(i)}
              >
                <div
                  className={`w-10 h-10 rounded-full flex items-center justify-center mb-2 transition-all ${
                    i < step
                      ? "bg-green-500 text-white"
                      : i === step
                        ? "bg-blue-500 text-white"
                        : "bg-gray-300 text-gray-600"
                  }`}
                >
                  {i < step ? <CheckCircle2 size={20} /> : i + 1}
                </div>
                <p className="text-sm font-medium text-gray-700">{s.title}</p>
              </div>
            ))}
          </div>
          <div className="h-2 bg-gray-200 rounded-full overflow-hidden">
            <div
              className="h-full bg-blue-500 transition-all duration-300"
              style={{ width: `${((step + 1) / steps.length) * 100}%` }}
            />
          </div>
        </div>

        {/* Error Alert */}
        {error && (
          <Alert className="mb-6 border-red-200 bg-red-50">
            <AlertCircle className="h-4 w-4 text-red-600" />
            <AlertDescription className="text-red-800">{error}</AlertDescription>
          </Alert>
        )}

        {/* Success State */}
        {success && (
          <Alert className="mb-6 border-green-200 bg-green-50">
            <CheckCircle2 className="h-4 w-4 text-green-600" />
            <AlertDescription className="text-green-800">
              Configuration saved! Redirecting...
            </AlertDescription>
          </Alert>
        )}

        {/* Card with Step Content */}
        <Card className="mb-8 shadow-lg">
          <CardHeader>
            <CardTitle>{steps[step].title}</CardTitle>
            <CardDescription>{steps[step].description}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            {step === 0 && (
              <>
                <div className="space-y-2">
                  <Label htmlFor="project-id">Project ID</Label>
                  <Input
                    id="project-id"
                    placeholder="my-project"
                    value={projectId}
                    onChange={(e) => setProjectId(e.target.value)}
                    pattern="^[a-z0-9][a-z0-9_-]*$"
                  />
                  <p className="text-xs text-gray-500">
                    Lowercase letters, digits, hyphens, and underscores only
                  </p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="project-root">Project Root Path</Label>
                  <Input
                    id="project-root"
                    placeholder="/path/to/project"
                    value={projectRoot}
                    onChange={(e) => setProjectRoot(e.target.value)}
                  />
                  <p className="text-xs text-gray-500">
                    Absolute or relative path where your project will live
                  </p>
                </div>
              </>
            )}

            {step === 1 && (
              <div className="space-y-2">
                <Label htmlFor="backend">State Backend</Label>
                <Select value={stateBackend} onValueChange={setStateBackend}>
                  <SelectTrigger id="backend">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {STATE_BACKENDS.map((backend) => (
                      <SelectItem key={backend} value={backend}>
                        <span className="font-medium">{backend}</span>
                        {backend === "file" && " (JSON files, simple)"}
                        {backend === "sqlite" && " (Single DB, performant)"}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-xs text-gray-500">
                  {stateBackend === "file"
                    ? "JSONL/JSON files in state/ directory. Good for small projects."
                    : "Single SQLite database. Better for multi-project setups."}
                </p>
              </div>
            )}

            {step === 2 && (
              <>
                <div className="space-y-2">
                  <Label htmlFor="budget">Budget Preset</Label>
                  <Select value={budgetPreset} onValueChange={setBudgetPreset}>
                    <SelectTrigger id="budget">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {BUDGET_PRESETS.map((preset) => (
                        <SelectItem key={preset} value={preset}>
                          {preset.charAt(0).toUpperCase() + preset.slice(1)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="spec-root">Spec Root (relative to project)</Label>
                  <Input
                    id="spec-root"
                    placeholder="specs"
                    value={specRoot}
                    onChange={(e) => setSpecRoot(e.target.value)}
                  />
                  <p className="text-xs text-gray-500">
                    Directory where your specification files live
                  </p>
                </div>
              </>
            )}

            {step === 3 && (
              <div className="space-y-4">
                <p className="text-sm text-gray-600 mb-4">
                  Select default models for each tier (optional)
                </p>
                {MODEL_TIERS.map((tier) => (
                  <div key={tier} className="space-y-2">
                    <Label htmlFor={`tier-${tier}`} className="capitalize">
                      {tier} Tier
                    </Label>
                    <Select value={modelChoices[tier]} onValueChange={(v) => setModelChoices({ ...modelChoices, [tier]: v })}>
                      <SelectTrigger id={`tier-${tier}`}>
                        <SelectValue placeholder={`Select ${tier} model`} />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="">None</SelectItem>
                        {/* Mock models - in production, fetch from /api/models */}
                        <SelectItem value={`claude-${tier}-5`}>claude-{tier}-5</SelectItem>
                        <SelectItem value={`codex-${tier}`}>codex-{tier}</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Navigation Buttons */}
        <div className="flex gap-4">
          <Button
            variant="outline"
            onClick={handleBack}
            disabled={step === 0 || isLoading}
            className="flex-1"
          >
            Back
          </Button>
          <Button
            onClick={handleNext}
            disabled={isLoading || success}
            className="flex-1 bg-blue-600 hover:bg-blue-700"
          >
            {isLoading ? "Saving..." : step === steps.length - 1 ? "Complete Setup" : "Next"}
            <ChevronRight size={18} className="ml-2" />
          </Button>
        </div>
      </div>
    </div>
  )
}
