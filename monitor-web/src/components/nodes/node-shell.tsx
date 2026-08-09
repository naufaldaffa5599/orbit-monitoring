import { lazy, Suspense } from "react"
import { ExternalLink } from "lucide-react"
import { Crumbs } from "@/components/shell/app-shell"
import { Panel } from "@/components/panel"
import { Button } from "@/components/ui/button"
import type { TreeNode } from "@/types"

/**
 * xterm.js and its addon are ~340 kB, and most visits never open a shell.
 * Loading the console lazily keeps that weight out of the dashboard's entry
 * chunk — Vite splits it automatically and it arrives only when this view does.
 */
const TerminalConsole = lazy(() =>
  import("@/components/terminal/terminal-console").then((m) => ({
    default: m.TerminalConsole,
  })),
)

export function NodeShellView({ node }: { node: TreeNode }) {
  const deviceId = node.device_id ?? null

  const openInTab = () => {
    if (!deviceId) return
    const url = `terminal.html?id=${encodeURIComponent(deviceId)}&label=${encodeURIComponent(node.label)}`
    window.open(url, "_blank", "noopener")
  }

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Crumbs trail={["Datacenter", node.label, "Shell"]} />
        <Button size="sm" variant="outline" onClick={openInTab} disabled={!deviceId}>
          <ExternalLink className="size-3.5" />
          Buka di tab baru
        </Button>
      </div>

      {/* A definite height, because xterm's fit addon measures its container —
          inside the dashboard's scrolling column there is nothing to measure
          otherwise. Tall enough to be a real terminal, short enough that the
          panel header stays on screen. */}
      <Panel className="h-[calc(100dvh-13rem)] min-h-96">
        <Suspense
          fallback={
            <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
              Memuat terminal…
            </div>
          }
        >
          <TerminalConsole
            // Remounting per node is what swaps the session set; the shells
            // themselves live in tmux on the target and survive it.
            key={node.id}
            deviceId={deviceId}
          />
        </Suspense>
      </Panel>
    </>
  )
}
