import { useEffect, useState, type ReactNode } from "react"
import { LogOut, Menu, Plus } from "lucide-react"
import { ShaderBackground } from "@/components/ui/ethereal-whispers"
import { OrbitMark } from "@/components/shell/orbit-mark"
import { ResourceTree } from "@/components/shell/resource-tree"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import type { TreeNode } from "@/types"

const BREAKPOINT = 820

export function AppShell({
  nodes,
  selectedId,
  onSelect,
  datacenterOpen,
  onDatacenter,
  onAddDevice,
  onLogout,
  clock,
  children,
}: {
  nodes: TreeNode[]
  selectedId: string
  onSelect: (nodeId: string) => void
  datacenterOpen: boolean
  onDatacenter: () => void
  onAddDevice: () => void
  /** Absent when no password is configured — nothing to sign out of. */
  onLogout?: () => void
  clock: string
  children: ReactNode
}) {
  const [drawerOpen, setDrawerOpen] = useState(false)

  // The sidebar is only a drawer below the breakpoint; left "open" across a
  // resize it would pin itself over the content on desktop.
  useEffect(() => {
    const onResize = () => {
      if (window.innerWidth > BREAKPOINT) setDrawerOpen(false)
    }
    window.addEventListener("resize", onResize)
    return () => window.removeEventListener("resize", onResize)
  }, [])

  const close = () => setDrawerOpen(false)

  return (
    <>
      <ShaderBackground className="shader-canvas fixed inset-0 -z-20" />
      <div className="shader-scrim pointer-events-none fixed inset-0 -z-10" />

      <div className="flex min-h-dvh flex-col">
        <header className="glass sticky top-0 z-30 flex h-13 shrink-0 items-center justify-between gap-4 border-b bg-chrome px-4">
          <div className="flex min-w-0 items-center gap-3">
            <button
              type="button"
              onClick={() => setDrawerOpen((open) => !open)}
              aria-label="Toggle navigation"
              aria-expanded={drawerOpen}
              className="flex size-8.5 items-center justify-center rounded-md border bg-white/5 min-[821px]:hidden"
            >
              <Menu className="size-4" />
            </button>

            <div className="flex min-w-0 items-center gap-2">
              <OrbitMark className="size-7 shrink-0 text-primary" />
              <span className="truncate text-[0.95rem] font-semibold tracking-tight">
                Orbit
              </span>
            </div>

            <span className="border-l pl-3 text-xs text-muted-foreground max-[820px]:hidden">
              Server Environment
            </span>
          </div>

          <div className="flex items-center gap-3">
            <span className="tabular text-xs text-muted-foreground max-[820px]:hidden">
              {clock}
            </span>
            <div className="flex items-center gap-1.5 rounded-full border border-ok/30 bg-ok/10 px-2 py-0.5 text-[0.62rem] font-semibold tracking-[0.1em] text-ok">
              <span className="size-1.5 animate-pulse rounded-full bg-ok shadow-[0_0_8px_var(--ok)]" />
              LIVE
            </div>

            {onLogout && (
              <button
                type="button"
                onClick={onLogout}
                aria-label="Keluar"
                title="Keluar"
                className="flex size-8.5 items-center justify-center rounded-md border bg-white/5 text-muted-foreground hover:text-foreground"
              >
                <LogOut className="size-4" />
              </button>
            )}
          </div>
        </header>

        <div className="flex flex-1 items-start">
          <aside
            className={cn(
              "glass z-20 flex h-[calc(100dvh-var(--spacing)*13)] w-64 shrink-0 flex-col border-r bg-sidebar",
              "sticky top-13",
              "max-[820px]:fixed max-[820px]:top-13 max-[820px]:left-0 max-[820px]:shadow-2xl max-[820px]:transition-transform",
              drawerOpen
                ? "max-[820px]:translate-x-0"
                : "max-[820px]:-translate-x-full",
            )}
          >
            <ResourceTree
              nodes={nodes}
              selectedId={selectedId}
              onSelect={(nodeId) => {
                onSelect(nodeId)
                close()
              }}
              datacenterOpen={datacenterOpen}
              onDatacenter={() => {
                onDatacenter()
                close()
              }}
            />

            {/* Machines are added at the datacenter level, beside the guests
                the hypervisor discovered on its own. */}
            <div className="border-t p-2">
              <Button
                variant="outline"
                size="sm"
                className="w-full"
                onClick={() => {
                  onAddDevice()
                  close()
                }}
              >
                <Plus className="size-3.5" />
                Add device
              </Button>
            </div>
          </aside>

          {drawerOpen && (
            <div
              className="fixed inset-x-0 top-13 bottom-0 z-10 bg-black/60 min-[821px]:hidden"
              onClick={close}
            />
          )}

          <main className="min-w-0 flex-1 px-5 pt-4 pb-12 max-[820px]:px-3.5">
            <div className="mx-auto flex max-w-[1180px] flex-col gap-4">
              {children}
            </div>
          </main>
        </div>
      </div>
    </>
  )
}

/** Breadcrumb row above each view's panels. */
export function Crumbs({ trail }: { trail: string[] }) {
  return (
    <div className="flex min-h-7.5 items-center gap-1.5 text-sm text-muted-foreground">
      {trail.map((part, i) => (
        <span key={`${part}-${i}`} className="flex items-center gap-1.5">
          {i > 0 && <span className="opacity-60">›</span>}
          <span
            className={cn(
              "truncate",
              i === trail.length - 1 && "font-semibold text-foreground",
            )}
          >
            {part}
          </span>
        </span>
      ))}
    </div>
  )
}
