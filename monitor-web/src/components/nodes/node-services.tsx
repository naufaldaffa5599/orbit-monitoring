import { useCallback, useMemo, useState } from "react"
import { FileText, Play, RotateCw, Search, Square } from "lucide-react"
import { toast } from "sonner"
import { Panel, PanelHead, PanelMessage, RowSkeleton } from "@/components/panel"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { LogsDialog } from "@/components/services/logs-dialog"
import { usePoll } from "@/hooks/use-poll"
import { api } from "@/lib/api"
import { cn } from "@/lib/utils"
import type { ServiceAction, ServiceUnit, TreeNode } from "@/types"

const STATE_TONE: Record<string, string> = {
  running: "border-ok/35 bg-ok/12 text-ok",
  failed: "border-destructive/35 bg-destructive/12 text-destructive",
  activating: "border-warn/35 bg-warn/12 text-warn",
  deactivating: "border-warn/35 bg-warn/12 text-warn",
}

function ServiceRow({
  unit,
  node,
  busy,
  onAction,
  onLogs,
}: {
  unit: ServiceUnit
  node: TreeNode
  busy: boolean
  onAction: (unit: ServiceUnit, action: ServiceAction) => void
  onLogs: (unit: ServiceUnit) => void
}) {
  const running = unit.state === "running"
  return (
    <div className="relative flex flex-wrap items-center justify-between gap-3 border-b py-2.5 pr-3.5 pl-4.5 transition-colors last:border-b-0 hover:bg-white/4 max-[820px]:flex-col max-[820px]:items-start">
      <span
        aria-hidden
        className={cn(
          "absolute inset-y-0 left-0 w-[3px]",
          running ? "bg-ok" : unit.state === "failed" ? "bg-destructive" : "bg-muted-foreground/60",
        )}
      />

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-semibold">{unit.name}</span>
          <Badge
            variant="outline"
            className={cn(
              "shrink-0 text-[0.6rem] tracking-wide uppercase",
              STATE_TONE[unit.state] ?? "bg-white/5 text-muted-foreground",
            )}
          >
            {unit.state}
          </Badge>
          {unit.startup && (
            <span className="shrink-0 text-[0.63rem] text-muted-foreground">
              {unit.startup}
            </span>
          )}
        </div>
        {unit.display && (
          <span className="truncate text-xs text-muted-foreground">{unit.display}</span>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-1.5 max-[820px]:w-full">
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() => onAction(unit, running ? "stop" : "start")}
          className={cn(
            "max-[820px]:flex-1",
            running
              ? "hover:border-destructive/40 hover:bg-destructive/12 hover:text-destructive"
              : "hover:border-ok/40 hover:bg-ok/12 hover:text-ok",
          )}
        >
          {running ? <Square className="size-3.5" /> : <Play className="size-3.5" />}
          {running ? "Stop" : "Start"}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() => onAction(unit, "restart")}
          className="hover:border-warn/40 hover:bg-warn/12 hover:text-warn max-[820px]:flex-1"
        >
          <RotateCw className="size-3.5" />
          Restart
        </Button>
        {/* Log tailing is journald-only, and only for the host running the API:
            it streams from a local journalctl, not from the target. */}
        {node.is_local && node.os !== "windows" && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => onLogs(unit)}
            className="hover:border-info/40 hover:bg-info/12 hover:text-info max-[820px]:flex-1"
          >
            <FileText className="size-3.5" />
            Logs
          </Button>
        )}
      </div>
    </div>
  )
}

export function NodeServicesView({ node }: { node: TreeNode }) {
  const [busy, setBusy] = useState<string | null>(null)
  const [showSystem, setShowSystem] = useState(false)
  const [query, setQuery] = useState("")
  const [logsFor, setLogsFor] = useState<{ id: string; label: string } | null>(null)

  const fetchServices = useCallback(() => api.nodeServices(node.id), [node.id])
  const { data, error, loading, refresh } = usePoll(fetchServices)

  const all = useMemo(() => data?.services ?? [], [data])
  const visible = useMemo(() => {
    const q = query.trim().toLowerCase()
    return all.filter((u) => {
      if (!showSystem && u.system) return false
      if (!q) return true
      return (
        u.name.toLowerCase().includes(q) || u.display.toLowerCase().includes(q)
      )
    })
  }, [all, showSystem, query])

  const hiddenCount = all.length - all.filter((u) => !u.system).length

  const runAction = async (unit: ServiceUnit, action: ServiceAction) => {
    if (!window.confirm(`Yakin mau ${action} "${unit.name}" di ${node.label}?`)) return
    setBusy(unit.name)
    try {
      await api.nodeServiceAction(node.id, unit.name, action)
      toast.success(`${unit.name} — ${action} terkirim`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `Gagal ${action}`)
    } finally {
      setBusy(null)
      setTimeout(refresh, 1200)
    }
  }

  return (
    <>
      <Panel>
        <PanelHead title={node.os === "windows" ? "Windows services" : "systemd units"}>
          <div className="flex items-center gap-2">
            <Badge variant="outline">{visible.length} tampil</Badge>
            {hiddenCount > 0 && (
              <Button
                size="sm"
                variant="ghost"
                className="h-7 text-xs"
                onClick={() => setShowSystem((v) => !v)}
              >
                {showSystem ? "Sembunyiin bawaan OS" : `+${hiddenCount} bawaan OS`}
              </Button>
            )}
          </div>
        </PanelHead>

        <div className="flex items-center gap-2 border-b px-3.5 py-2">
          <Search className="size-3.5 shrink-0 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Cari service…"
            className="h-8 border-0 bg-transparent px-0 focus-visible:ring-0"
          />
        </div>

        <div className="flex flex-col">
          {loading && !data ? (
            <RowSkeleton />
          ) : error ? (
            <PanelMessage>Gagal ambil daftar service — {error}</PanelMessage>
          ) : visible.length === 0 ? (
            <PanelMessage>
              {query ? "Nggak ada yang cocok." : "Nggak ada service non-bawaan di node ini."}
            </PanelMessage>
          ) : (
            visible.map((unit) => (
              <ServiceRow
                key={unit.name}
                unit={unit}
                node={node}
                busy={busy === unit.name}
                onAction={runAction}
                onLogs={(u) => setLogsFor({ id: u.name.replace(/\.service$/, ""), label: u.name })}
              />
            ))
          )}
        </div>
      </Panel>

      {data?.error && (
        <Panel>
          <PanelMessage>Refresh terakhir gagal: {data.error}</PanelMessage>
        </Panel>
      )}

      <LogsDialog service={logsFor} onClose={() => setLogsFor(null)} />
    </>
  )
}
