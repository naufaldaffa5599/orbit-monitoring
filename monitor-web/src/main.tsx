import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { App } from "@/app"
import "@/index.css"
import "@/lib/register-sw"

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
