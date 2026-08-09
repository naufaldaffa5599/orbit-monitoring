import { useCallback, useMemo, useState } from "react"
import { Search, X } from "lucide-react"
import { toast } from "sonner"
import { Crumbs } from "@/components/shell/app-shell"
import { Panel, PanelHead, PanelMessage, RowSkeleton } from "@/components/panel"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { usePoll } from "@/hooks/use-poll"
import { api } from "@/lib/api"
import { formatBytes } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { TreeNode } from "@/types"

type Sort = "cpu" | "ram"

/**
 * Task Manager for a remote machine.
 *
 * Windows' own split between "Apps" and "Background processes" cannot be
 * reproduced here: it keys off each process's main window, and an SSH session
 * runs in a different session to the desktop, so MainWindowTitle comes back
 * empty for everything. What is left — and what this shows — is the Processes
 * tab: one row per process, sorted by what it is consuming.
 */
export function NodeTasksView({ node }: { node: TreeNode }) {
  const [sort, setSort] = useState<Sort>("cpu")
  const [query, setQuery] = useState("")
  const [killing, setKilling] = useState<number | null>(null)

  const fetchProcesses = useCallback(
    () => api.nodeProcesses(node.id, sort),
    [node.id, sort],
  )
  const { data, error, loading, refresh } = usePoll(fetchProcesses)

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase()
    const list = data?.processes ?? []
    return q ? list.filter((p) => p.name.toLowerCase().includes(q)) : list
  }, [data, query])

  const endTask = async (pid: number, name: string) => {
    if (
      !window.confirm(
        `End task "${name}" (PID ${pid}) di ${node.label}?\n\n` +
          `Proses ini dimatiin paksa — kerjaan yang belum disimpan bakal hilang.`,
      )
    ) {
      return
    }
    setKilling(pid)
    try {
      await api.nodeKill(node.id, pid)
      toast.success(`${name} (PID ${pid}) dimatiin`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal end task")
    } finally {
      setKilling(null)
      setTimeout(refresh, 1200)
    }
  }

  return (
    <>
      <Crumbs trail={["Datacenter", node.label, "Task Manager"]} />

      <Panel>
        <PanelHead title="Processes">
          <div className="flex items-center gap-2">
            <Badge variant="outline">{rows.length}</Badge>
            <div className="flex gap-0.5 rounded-md border bg-sunken p-0.5">
              {(["cpu", "ram"] as const).map((key) => (
                <button
                  key={key}
                  type="button"
                  onClick={() => setSort(key)}
                  className={cn(
                    "rounded-[3px] px-2.5 py-1 text-xs font-semibold transition-colors",
                    sort === key
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {key.toUpperCase()}
                </button>
              ))}
            </div>
          </div>
        </PanelHead>

        <div className="flex items-center gap-2 border-b px-3.5 py-2">
          <Search className="size-3.5 shrink-0 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Cari proses…"
            className="h-8 border-0 bg-transparent px-0 focus-visible:ring-0"
          />
        </div>

        <div className="flex flex-col">
          {loading && !data ? (
            <RowSkeleton />
          ) : error ? (
            <PanelMessage>Gagal ambil daftar proses — {error}</PanelMessage>
          ) : rows.length === 0 ? (
            <PanelMessage>{query ? "Nggak ada yang cocok." : "Kosong."}</PanelMessage>
          ) : (
            rows.map((proc) => (
              <div
                key={proc.pid}
                className="relative flex items-center gap-3 overflow-hidden border-b px-3.5 py-2 last:border-b-0 hover:bg-white/4"
              >
                <span
                  aria-hidden
                  className="absolute inset-y-0 left-0 bg-gradient-to-r from-info/20 to-info/5 transition-[width] duration-500"
                  style={{ width: `${Math.min(proc.cpu, 100)}%` }}
                />
                <span className="relative flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-sm font-semibold">{proc.name}</span>
                  <span className="tabular text-xs text-muted-foreground">
                    PID {proc.pid}
                    {proc.user ? ` · ${proc.user}` : ""}
                  </span>
                </span>
                <span className="tabular relative w-16 shrink-0 text-right text-sm font-bold">
                  {proc.cpu.toFixed(1)}%
                </span>
                <span className="tabular relative w-20 shrink-0 text-right text-sm text-secondary-foreground">
                  {formatBytes(proc.mem_bytes)}
                </span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={killing === proc.pid}
                  onClick={() => endTask(proc.pid, proc.name)}
                  title="End task"
                  className="relative shrink-0 hover:border-destructive/40 hover:bg-destructive/12 hover:text-destructive"
                >
                  <X className="size-3.5" />
                </Button>
              </div>
            ))
          )}
        </div>
      </Panel>

      {data?.error && (
        <Panel>
          <PanelMessage>Refresh terakhir gagal: {data.error}</PanelMessage>
        </Panel>
      )}
    </>
  )
}
