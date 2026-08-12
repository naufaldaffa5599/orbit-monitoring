import { useCallback } from "react"
import { Cpu, HardDrive, MemoryStick } from "lucide-react"
import { Panel, PanelBody, PanelHead, PanelMessage } from "@/components/panel"
import { Badge } from "@/components/ui/badge"
import { UsageChart } from "@/components/system/usage-chart"
import { usePoll } from "@/hooks/use-poll"
import { api } from "@/lib/api"
import {
  DASH,
  formatBytes,
  formatPercent,
  formatUptime,
  levelFor,
  type Level,
} from "@/lib/format"
import { nodeUp } from "@/lib/node-status"
import { cn } from "@/lib/utils"
import type { NodeSummary, TreeNode } from "@/types"

const FILL: Record<Level, string> = {
  ok: "bg-gradient-to-r from-[#b85a00] to-primary",
  warn: "bg-gradient-to-r from-[#b58a12] to-warn",
  crit: "bg-gradient-to-r from-[#ad3f3b] to-destructive",
}

export function Gauge({
  icon: Icon,
  label,
  percent,
  sub,
}: {
  icon: typeof Cpu
  label: string
  percent: number | undefined
  sub: string
}) {
  const level = levelFor(percent)
  return (
    <div>
      <div className="mb-1.5 flex items-baseline justify-between gap-2">
        <span className="flex items-center gap-1.5 text-sm text-secondary-foreground">
          <Icon className="size-3.5" />
          {label}
        </span>
        <span className="tabular text-[0.95rem] font-bold">
          {formatPercent(percent)}
        </span>
      </div>
      <div className="h-2.5 overflow-hidden rounded-[3px] border bg-sunken">
        <div
          className={cn(
            "h-full rounded-[2px] transition-[width] duration-500",
            FILL[level],
          )}
          style={{ width: `${Math.max(0, Math.min(percent ?? 0, 100))}%` }}
        />
      </div>
      <div className="tabular mt-1.5 text-xs text-muted-foreground">{sub}</div>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b px-2.5 py-2 text-sm last:border-b-0 odd:bg-white/2">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className="tabular truncate font-semibold">{value}</span>
    </div>
  )
}

const SOURCE_LABEL: Record<string, string> = {
  local: "gopsutil (host ini)",
  ssh: "SSH",
  hypervisor: "hypervisor",
}

function usage(used: number | undefined, total: number | undefined): string {
  if (!total) return DASH
  return `${formatBytes(used)} / ${formatBytes(total)}`
}

export function NodeSummaryView({ node }: { node: TreeNode }) {
  const fetchSummary = useCallback(
    () => api.nodeSummary(node.id),
    [node.id],
  )
  const { data, error, loading } = usePoll(fetchSummary)

  // History exists only for the host running the API — nothing records a
  // remote node's past, so the chart is shown where it means something.
  const fetchHistory = useCallback(() => api.history(), [])
  const history = usePoll(node.is_local ? fetchHistory : noHistory)

  const summary: NodeSummary | undefined = data?.summary
  const stale = data?.error || error
  const worst = summary
    ? Math.max(summary.cpu_percent, summary.mem_percent, summary.disk_percent)
    : undefined
  const level = levelFor(worst)
  const health = { ok: "Healthy", warn: "Under load", crit: "Critical" }[level]

  // A machine that did not answer its probe reports zeros for everything, and
  // zero CPU on zero cores reads as "Healthy" — the one thing it is not. Say
  // it is off, the same way a stopped guest does.
  if (nodeUp(node) === false) {
    return (
      <Panel>
        <PanelHead title="Status">
          <Badge variant="outline">offline</Badge>
        </PanelHead>
        <PanelMessage>
          {node.label} nggak nyaut di {node.host || "alamatnya"}.{" "}
          {node.can_wake
            ? "Coba Wake Up di atas buat nyalain."
            : "Nyalain dulu buat lihat metriknya."}
        </PanelMessage>
      </Panel>
    )
  }

  if (node.kind === "vm" && node.status !== "running") {
    return (
      <Panel>
        <PanelHead title="Status">
          <Badge variant="outline">{node.status}</Badge>
        </PanelHead>
        <PanelMessage>
          VM ini sedang <b>{node.status}</b>. Jalanin dari Proxmox dulu buat
          lihat metriknya.
        </PanelMessage>
      </Panel>
    )
  }

  return (
    <>
      {summary?.source === "hypervisor" && (
        <Panel>
          <PanelMessage>
            Angka di bawah dilaporin hypervisor. Tambahin kredensial SSH buat
            node ini kalau mau lihat disk, service, dan proses di dalamnya.
          </PanelMessage>
        </Panel>
      )}

      <Panel>
        <PanelHead title="Status">
          <div className="flex items-center gap-2">
            {stale && (
              <Badge variant="outline" className="text-warn">
                data terakhir
              </Badge>
            )}
            <Badge
              variant="outline"
              className={cn(
                "text-xs",
                level === "crit" && "text-destructive",
                level === "warn" && "text-warn",
                level === "ok" && "text-ok",
              )}
            >
              {summary ? health : DASH}
            </Badge>
          </div>
        </PanelHead>

        <PanelBody className="grid gap-4.5 min-[901px]:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
          <div className="flex flex-col gap-4">
            <Gauge
              icon={Cpu}
              label="CPU usage"
              percent={summary?.cpu_percent}
              sub={
                summary
                  ? `${summary.cpu_count} CPU(s)` +
                    (summary.load_avg?.length
                      ? ` · Load ${summary.load_avg.map((l) => l.toFixed(2)).join(" / ")}`
                      : "")
                  : DASH
              }
            />
            <Gauge
              icon={MemoryStick}
              label="RAM usage"
              percent={summary?.mem_percent}
              sub={usage(summary?.mem_used, summary?.mem_total)}
            />
            {summary?.source === "hypervisor" ? null : (
              <Gauge
                icon={HardDrive}
                label="HD space"
                percent={summary?.disk_percent}
                sub={usage(summary?.disk_used, summary?.disk_total)}
              />
            )}
          </div>

          <div className="flex flex-col self-start overflow-hidden rounded-md border">
            <Row label="Hostname" value={summary?.hostname || node.label} />
            <Row label="Uptime" value={formatUptime(summary?.uptime_seconds)} />
            <Row label="Host" value={node.host || DASH} />
            <Row label="OS" value={node.os} />
            {node.vmid ? <Row label="VMID" value={String(node.vmid)} /> : null}
            <Row
              label="Sumber data"
              value={summary ? (SOURCE_LABEL[summary.source] ?? summary.source) : DASH}
            />
          </div>
        </PanelBody>
      </Panel>

      {node.is_local && (
        <Panel>
          <PanelHead title="CPU & Memory usage">
            <span className="text-xs text-muted-foreground">Last hour</span>
          </PanelHead>
          <PanelBody>
            <UsageChart history={history.data} />
          </PanelBody>
        </Panel>
      )}

      {loading && !summary && (
        <Panel>
          <PanelMessage>Mengumpulkan data dari {node.label}…</PanelMessage>
        </Panel>
      )}
      {stale && (
        <Panel>
          <PanelMessage>Terakhir gagal ambil data: {stale}</PanelMessage>
        </Panel>
      )}
    </>
  )
}

/** Stand-in for nodes with no history endpoint; never resolves to data. */
const noHistory = async () => null
