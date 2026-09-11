import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import "./index.css"
import App from "./App"
import { adoptTokenFromQuery } from "@/hooks/useAuth"

// Runs BEFORE the first render so a shared `?token=` URL boots straight into
// the dashboard instead of bouncing through /login. Also scrubs the token out
// of the address bar once it is stored.
adoptTokenFromQuery()

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
