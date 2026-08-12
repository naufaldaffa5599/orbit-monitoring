import { useCallback } from "react"
import { Cpu, HardDrive, MemoryStick, Server } from "lucide-react"
import { Crumbs } from "@/components/shell/app-shell"
import { Panel, PanelBody, PanelHead, PanelMessage, RowSkeleton } from "@/components/panel"
import { Badge } from "@/components/ui/badge"
import { Gauge } from "@/components/nodes/node-summary"
import { usePoll } from "@/hooks/use-poll"
import { api } from "@/lib/api"
import { DASH, formatBytes, formatUptime, levelFor } from "@/lib/format"
import { ROOT_LABEL } from "@/lib/labels"
import { nodeUp, statusLabel } from "@/lib/node-status"
import { cn } from "@/lib/utils"
import type { DatacenterRow, TreeNode } from "@/types"

function Tile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border bg-sunken p-3">
      <span className="text-[0.68rem] tracking-wide text-muted-foreground uppercase">
        {label}
      </span>
      <span className="tabular text-xl font-bold">{value}</span>
      {sub && <span className="text-xs text-muted-foreground">{sub}</span>}
    </div>
  )
}

function statusTone(node: TreeNode) {
  return nodeUp(node)
    ? "border-ok/35 bg-ok/12 text-ok"
    : "bg-white/5 text-muted-foreground"
}

function GuestRow({
  row,
  onOpen,
}: {
  row: DatacenterRow
  onOpen: (nodeId: string) => void
}) {
  const { node, summary } = row
  const live = node.kind !== "vm" || node.status === "running"

  return (
    <button
      type="button"
      onClick={() => onOpen(node.id)}
      className="flex w-full items-center gap-3 border-b px-3.5 py-2.5 text-left transition-colors last:border-b-0 hover:bg-white/4"
    >
      <span className="shrink-0 text-base">{node.icon || "🖥️"}</span>

      <span className="flex min-w-0 flex-1 flex-col">
        <span className="flex items-center gap-2">
          <span className="truncate text-sm font-semibold">{node.label}</span>
          {node.vmid ? (
            <span className="tabular shrink-0 text-[0.63rem] text-muted-foreground">
              #{node.vmid}
            </span>
          ) : null}
        </span>
        <span className="truncate text-xs text-muted-foreground">
          {node.host || DASH} · {node.os}
          {row.error ? ` · ${row.error}` : ""}
        </span>
      </span>

      {live && summary.cpu_count > 0 ? (
        <>
          <span className="tabular hidden w-24 shrink-0 text-right text-xs sm:block">
            <span
              className={cn(
                "font-semibold",
                levelFor(summary.cpu_percent) === "crit" && "text-destructive",
                levelFor(summary.cpu_percent) === "warn" && "text-warn",
              )}
            >
              {summary.cpu_percent}%
            </span>
            <span className="text-muted-foreground"> CPU</span>
          </span>
          <span className="tabular hidden w-32 shrink-0 text-right text-xs md:block">
            <span
              className={cn(
                "font-semibold",
                levelFor(summary.mem_percent) === "crit" && "text-destructive",
                levelFor(summary.mem_percent) === "warn" && "text-warn",
              )}
            >
              {summary.mem_percent}%
            </span>
            <span className="text-muted-foreground">
              {" "}
              of {formatBytes(summary.mem_total)}
            </span>
          </span>
          <span className="tabular hidden w-20 shrink-0 text-right text-xs text-muted-foreground lg:block">
            {formatUptime(summary.uptime_seconds)}
          </span>
        </>
      ) : (
        <span className="w-24 shrink-0 text-right text-xs text-muted-foreground sm:w-56 md:w-76">
          {node.kind === "vm"
            ? node.status
            : node.kind === "router9"
              ? "API usage"
              : nodeUp(node) === false
                ? "offline"
                : "belum ada kredensial"}
        </span>
      )}

      <Badge
        variant="outline"
        className={cn("shrink-0 text-[0.6rem] uppercase", statusTone(node))}
      >
        {statusLabel(node)}
      </Badge>
    </button>
  )
}

/**
 * The datacenter overview: the hypervisor's own load, plus one row per machine.
 * Mirrors what Proxmox puts on its Datacenter screen — the point is to see the
 * whole fleet at once rather than to drill into any one node.
 */
export function DatacenterView({
  onOpenNode,
}: {
  onOpenNode: (nodeId: string) => void
}) {
  const fetchDatacenter = useCallback(() => api.datacenter(), [])
  const { data, error, loading } = usePoll(fetchDatacenter, 10000)

  const rows = data?.nodes ?? []
  const host = rows.find((r) => r.node.kind === "hypervisor")
  const guests = rows.filter((r) => r.node.kind !== "hypervisor")

  // Only a node known to be down is excluded. A machine with nothing to probe
  // reads as unknown, and unknown is not evidence that it is off.
  const running = guests.filter((r) => nodeUp(r.node) !== false).length
  // Only machines that actually answered contribute to the totals; a node that
  // is off would otherwise drag the fleet's memory figure toward zero.
  const answered = rows.filter((r) => r.summary.mem_total > 0)
  const memTotal = answered.reduce((sum, r) => sum + r.summary.mem_total, 0)
  const memUsed = answered.reduce((sum, r) => sum + r.summary.mem_used, 0)
  const cpuCount = answered.reduce((sum, r) => sum + r.summary.cpu_count, 0)

  return (
    <>
      <Crumbs trail={[ROOT_LABEL]} />

      <Panel>
        <PanelHead title={ROOT_LABEL}>
          <Badge variant="outline">
            {running}/{guests.length} node aktif
          </Badge>
        </PanelHead>
        <PanelBody className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Tile
            label="Nodes"
            value={String(rows.length)}
            sub={`${guests.filter((r) => r.node.kind === "vm").length} VM · ${
              guests.filter((r) => r.node.kind === "machine").length
            } mesin`}
          />
          <Tile label="vCPU total" value={String(cpuCount)} sub="dari node yang menjawab" />
          <Tile
            label="RAM terpakai"
            value={memTotal ? `${Math.round((memUsed / memTotal) * 100)}%` : DASH}
            sub={`${formatBytes(memUsed)} / ${formatBytes(memTotal)}`}
          />
          <Tile
            label="Hypervisor"
            value={host ? `${host.summary.cpu_percent}%` : DASH}
            sub={host?.node.label ?? "tidak terdeteksi"}
          />
        </PanelBody>
      </Panel>

      {host && (
        <Panel>
          <PanelHead title={`${host.node.label} — beban host`}>
            <Badge variant="outline">
              <Server className="mr-1 size-3" />
              hypervisor
            </Badge>
          </PanelHead>
          <PanelBody className="grid gap-4 sm:grid-cols-3">
            <Gauge
              icon={Cpu}
              label="CPU"
              percent={host.summary.cpu_percent}
              sub={`${host.summary.cpu_count} CPU(s)`}
            />
            <Gauge
              icon={MemoryStick}
              label="RAM"
              percent={host.summary.mem_percent}
              sub={`${formatBytes(host.summary.mem_used)} / ${formatBytes(host.summary.mem_total)}`}
            />
            <Gauge
              icon={HardDrive}
              label="Disk"
              percent={host.summary.disk_percent}
              sub={`${formatBytes(host.summary.disk_used)} / ${formatBytes(host.summary.disk_total)}`}
            />
          </PanelBody>
        </Panel>
      )}

      <Panel>
        <PanelHead title="Guests & machines">
          <span className="text-xs text-muted-foreground">
            {data ? `diperbarui ${data.generated_at}` : ""}
          </span>
        </PanelHead>
        <div className="flex flex-col">
          {loading && !data ? (
            <RowSkeleton />
          ) : error ? (
            <PanelMessage>Gagal ambil ringkasan {ROOT_LABEL} — {error}</PanelMessage>
          ) : (
            guests.map((row) => (
              <GuestRow key={row.node.id} row={row} onOpen={onOpenNode} />
            ))
          )}
        </div>
      </Panel>
    </>
  )
}

/**
 * The rollup for a heading in the tree — Outposts and any group after it.
 *
 * Deliberately not the same thing as the hypervisor's Summary, which reports
 * one physical machine whose figures already contain its guests. Nothing runs
 * "inside" a group: these are separate boxes that share nothing, so every
 * number here is a sum across them and is labelled as one.
 *
 * It reads the datacenter endpoint rather than adding one of its own — that
 * response already carries a row per node, parent included, and the two views
 * poll on the same cache.
 */
export function GroupView({
  node,
  onOpenNode,
}: {
  node: TreeNode
  onOpenNode: (nodeId: string) => void
}) {
  const fetchDatacenter = useCallback(() => api.datacenter(), [])
  const { data, error, loading } = usePoll(fetchDatacenter, 10000)

  const rows = (data?.nodes ?? []).filter((r) => r.node.parent === node.id)
  const online = rows.filter((r) => nodeUp(r.node) === true).length
  const offline = rows.filter((r) => nodeUp(r.node) === false).length

  // A machine that is off reports zeros, and counting those would quietly
  // shrink the group's capacity instead of showing that a member is missing.
  const answered = rows.filter((r) => r.summary.mem_total > 0)
  const memTotal = answered.reduce((sum, r) => sum + r.summary.mem_total, 0)
  const memUsed = answered.reduce((sum, r) => sum + r.summary.mem_used, 0)
  const cpuCount = answered.reduce((sum, r) => sum + r.summary.cpu_count, 0)
  const failing = rows.reduce((sum, r) => sum + r.node.checks_failing, 0)

  // Not every group is a group of machines. Relays are views over something
  // out on the network — no CPU, no RAM — and tiles reading "0 vCPU · RAM —"
  // would be reporting the absence of figures as if it were a measurement.
  const measurable = answered.length > 0

  return (
    <>
      {measurable && (
      <Panel>
        <PanelHead title={`${node.label} — total`}>
          <Badge variant="outline">
            {online}/{rows.length} online
          </Badge>
        </PanelHead>
        <PanelBody className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Tile
            label="Mesin"
            value={String(rows.length)}
            sub={offline > 0 ? `${offline} lagi mati` : "semua nyaut"}
          />
          <Tile
            label="vCPU total"
            value={String(cpuCount)}
            sub={`dijumlah dari ${answered.length} mesin`}
          />
          <Tile
            label="RAM terpakai"
            value={memTotal ? `${Math.round((memUsed / memTotal) * 100)}%` : DASH}
            sub={`${formatBytes(memUsed)} / ${formatBytes(memTotal)}`}
          />
          <Tile
            label="Check gagal"
            value={String(failing)}
            sub={failing > 0 ? "ada app yang down" : "semua check lolos"}
          />
        </PanelBody>
      </Panel>
      )}

      <Panel>
        <PanelHead title={measurable ? "Mesin di grup ini" : `Isi ${node.label}`}>
          <span className="text-xs text-muted-foreground">
            {data ? `diperbarui ${data.generated_at}` : ""}
          </span>
        </PanelHead>
        <div className="flex flex-col">
          {loading && !data ? (
            <RowSkeleton />
          ) : error ? (
            <PanelMessage>Gagal ambil ringkasan — {error}</PanelMessage>
          ) : rows.length === 0 ? (
            <PanelMessage>Belum ada isinya.</PanelMessage>
          ) : (
            rows.map((row) => (
              <GuestRow key={row.node.id} row={row} onOpen={onOpenNode} />
            ))
          )}
        </div>
      </Panel>

      {measurable && (
        <PanelMessage>
          Angka di atas dijumlah dari {rows.length} mesin yang berdiri sendiri —
          beda dari hypervisor, yang angkanya milik satu mesin dan udah termasuk
          beban VM di dalamnya.
        </PanelMessage>
      )}
    </>
  )
}
