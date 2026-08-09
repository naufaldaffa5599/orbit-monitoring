import { useEffect } from "react"
import { ArrowLeft } from "lucide-react"
import { Toaster } from "@/components/ui/sonner"
import { TerminalConsole } from "@/components/terminal/terminal-console"

/**
 * The standalone terminal page, still served at /terminal.html.
 *
 * The dashboard now hosts the same console inline, so this page exists for the
 * cases the panel cannot cover: a bookmark, a home-screen shortcut, and the
 * "buka di tab baru" button for when one terminal wants the whole screen. All
 * it adds is a title bar — the console itself is shared.
 */
export function TerminalPage() {
  const params = new URLSearchParams(location.search)
  const deviceId = params.get("id")
  const label = params.get("label") || deviceId || "Terminal"

  useEffect(() => {
    document.title = `${label} — Orbit`
  }, [label])

  const goBack = (event: React.MouseEvent) => {
    // Opened from the dashboard with window.open? Just close, returning to the
    // tab that is already there. Otherwise follow the href.
    if (window.opener && !window.opener.closed) {
      event.preventDefault()
      window.close()
    }
  }

  return (
    <div className="flex h-dvh flex-col overflow-hidden">
      <div className="flex h-13 shrink-0 items-center gap-2.5 border-b bg-chrome px-4">
        <a
          href="index.html"
          onClick={goBack}
          className="flex h-8.5 shrink-0 items-center gap-1.5 rounded-md border bg-white/5 px-2.5 text-xs font-semibold"
        >
          <ArrowLeft className="size-3.5" />
          Dashboard
        </a>
        <span className="truncate text-sm font-semibold">{label}</span>
      </div>

      <div className="min-h-0 flex-1">
        <TerminalConsole deviceId={deviceId} />
      </div>

      <Toaster position="bottom-center" />
    </div>
  )
}
