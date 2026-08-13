import { useCallback, useEffect, useState } from "react"
import { Bell, BellOff, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Panel, PanelHead, PanelMessage, RowSkeleton } from "@/components/panel"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { CheckDialog } from "@/components/nodes/check-dialog"
import { usePoll } from "@/hooks/use-poll"
import { api } from "@/lib/api"
import { cn } from "@/lib/utils"
import type { CheckState, NotifyStatus, TreeNode } from "@/types"

/** Bars for the recent results, oldest on the left — a compact uptime strip. */
function HistoryStrip({ history }: { history: CheckState["history"] }) {
  const recent = history.slice(-40)
  if (recent.length === 0) {
    return <span className="text-[0.63rem] text-muted-foreground">belum ada sampel</span>
  }
  return (
    <div className="flex items-end gap-px" title={`${recent.length} pengecekan terakhir`}>
      {recent.map((r, i) => (
        <span
          key={`${r.at}-${i}`}
          className={cn("h-4 w-1 rounded-[1px]", r.ok ? "bg-ok/70" : "bg-destructive")}
          title={`${new Date(r.at).toLocaleTimeString("id-ID")} — ${
            r.ok ? `${r.status} · ${r.latency_ms}ms` : r.error || "gagal"
          }`}
        />
      ))}
    </div>
  )
}

function sinceLabel(iso: string | undefined): string {
  if (!iso) return ""
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (secs < 90) return `${Math.round(secs)} detik`
  if (secs < 5400) return `${Math.round(secs / 60)} menit`
  if (secs < 172800) return `${Math.round(secs / 3600)} jam`
  return `${Math.round(secs / 86400)} hari`
}

function CheckRow({
  check,
  onEdit,
  onChanged,
}: {
  check: CheckState
  onEdit: (check: CheckState) => void
  onChanged: () => void
}) {
  const [busy, setBusy] = useState(false)
  const last = check.last
  const down = !!last && !last.ok

  const runNow = async () => {
    setBusy(true)
    try {
      await api.runCheck(check.id)
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal cek")
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!window.confirm(`Hapus check "${check.label}"?`)) return
    setBusy(true)
    try {
      await api.deleteCheck(check.id)
      toast.success(`Check "${check.label}" dihapus`)
      onChanged()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal hapus check")
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="relative flex flex-wrap items-center gap-3 border-b py-2.5 pr-3.5 pl-4.5 transition-colors last:border-b-0 hover:bg-white/4">
      <span
        aria-hidden
        className={cn(
          "absolute inset-y-0 left-0 w-[3px]",
          !check.enabled
            ? "bg-muted-foreground/40"
            : down
              ? "bg-destructive"
              : last
                ? "bg-ok"
                : "bg-muted-foreground/60",
        )}
      />

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-semibold">{check.label}</span>
          {!check.enabled ? (
            <Badge variant="outline" className="text-[0.6rem] uppercase">
              nonaktif
            </Badge>
          ) : (
            <Badge
              variant="outline"
              className={cn(
                "text-[0.6rem] uppercase",
                down
                  ? "border-destructive/35 bg-destructive/12 text-destructive"
                  : last
                    ? "border-ok/35 bg-ok/12 text-ok"
                    : "bg-white/5 text-muted-foreground",
              )}
            >
              {down ? "down" : last ? "up" : "belum dicek"}
            </Badge>
          )}
          {check.changed_at && last && (
            <span className="text-[0.63rem] text-muted-foreground">
              {down ? "down" : "up"} selama {sinceLabel(check.changed_at)}
            </span>
          )}
          {check.fail_streak > 1 && (
            <span className="text-[0.63rem] text-destructive">
              gagal {check.fail_streak}× berturut-turut
            </span>
          )}
        </div>
        <span className="truncate font-mono text-[0.68rem] text-muted-foreground">
          {check.url}
        </span>
        {down && last?.error && (
          <span className="truncate text-xs text-destructive">{last.error}</span>
        )}
      </div>

      <HistoryStrip history={check.history} />

      <span className="tabular w-20 shrink-0 text-right text-sm">
        {last ? (
          <>
            <span className="font-bold">{last.latency_ms}</span>
            <span className="text-muted-foreground">ms</span>
          </>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </span>

      <div className="flex shrink-0 items-center gap-1.5">
        <Button size="sm" variant="outline" disabled={busy} onClick={runNow} title="Cek sekarang">
          <RefreshCw className={cn("size-3.5", busy && "animate-spin")} />
        </Button>
        <Button size="sm" variant="outline" onClick={() => onEdit(check)} title="Edit">
          <Pencil className="size-3.5" />
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={remove}
          title="Hapus"
          className="hover:border-destructive/40 hover:bg-destructive/12 hover:text-destructive"
        >
          <Trash2 className="size-3.5" />
        </Button>
      </div>
    </div>
  )
}

/**
 * Health checks for the apps on a node.
 *
 * Distinct from Services on purpose: systemd answers "is the process alive",
 * this answers "does the app respond". A unit sits in `active` perfectly
 * happily while its port refuses connections or it returns 502.
 */
/**
 * Whether alerts are wired up, and a way to prove it without waiting for
 * something to break. A notification channel nobody has ever tested is a
 * notification channel you find out about on the day it matters.
 */
function NotifyBanner() {
  const [status, setStatus] = useState<NotifyStatus | null>(null)
  const [sending, setSending] = useState(false)

  useEffect(() => {
    let alive = true
    api
      .notifyStatus()
      .then((s) => alive && setStatus(s))
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [])

  if (!status) return null

  const test = async () => {
    setSending(true)
    try {
      await api.notifyTest()
      toast.success("Terkirim — cek channel-nya")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal kirim tes")
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t px-3.5 py-2.5 text-xs text-muted-foreground">
      <span className="flex items-center gap-2">
        {status.configured ? (
          <>
            <Bell className="size-3.5 text-ok" />
            Notifikasi aktif lewat <b className="text-foreground">{status.kind}</b> —
            dikirim setelah {status.threshold}× gagal berturut-turut, dan sekali lagi
            pas pulih.
          </>
        ) : (
          <>
            <BellOff className="size-3.5" />
            Notifikasi mati. Isi <code className="font-mono">NOTIFY_WEBHOOK</code> di{" "}
            <code className="font-mono">monitor-api-go/.env</code>, lalu restart API.
          </>
        )}
      </span>
      {status.configured && (
        <Button size="sm" variant="outline" disabled={sending} onClick={test}>
          {sending ? "Mengirim…" : "Tes notifikasi"}
        </Button>
      )}
    </div>
  )
}

export function NodeAppsView({ node }: { node: TreeNode }) {
  const [editing, setEditing] = useState<CheckState | null>(null)
  const [adding, setAdding] = useState(false)

  const fetchChecks = useCallback(() => api.nodeChecks(node.id), [node.id])
  const { data, error, loading, refresh } = usePoll(fetchChecks, 15000)

  const list = data?.checks ?? []
  const down = list.filter((c) => c.enabled && c.last && !c.last.ok).length

  return (
    <>
      <Panel>
        <PanelHead title="HTTP health checks">
          <div className="flex items-center gap-2">
            {down > 0 && (
              <Badge variant="outline" className="border-destructive/35 bg-destructive/12 text-destructive">
                {down} down
              </Badge>
            )}
            <Badge variant="outline">{list.length} check</Badge>
            <Button size="sm" onClick={() => setAdding(true)}>
              <Plus className="size-3.5" />
              Tambah check
            </Button>
          </div>
        </PanelHead>

        <div className="flex flex-col">
          {loading && !data ? (
            <RowSkeleton />
          ) : error ? (
            <PanelMessage>Gagal ambil daftar check — {error}</PanelMessage>
          ) : list.length === 0 ? (
            <PanelMessage>
              Belum ada check di node ini. Klik <b>Tambah check</b> — port yang lagi
              kebuka bakal disaranin otomatis, jadi nggak perlu ngetik URL manual.
            </PanelMessage>
          ) : (
            list.map((check) => (
              <CheckRow
                key={check.id}
                check={check}
                onEdit={setEditing}
                onChanged={refresh}
              />
            ))
          )}
        </div>

        <NotifyBanner />
      </Panel>

      <PanelMessage>
        Check dijalanin dari mesin yang ngehost dashboard ini, bukan dari dalam{" "}
        {node.label} — jadi hasilnya sama dengan yang dilihat klien. URL{" "}
        <code className="font-mono">127.0.0.1</code> cuma nyambung kalau app-nya
        ada di host dashboard.
      </PanelMessage>

      <CheckDialog
        node={node}
        check={editing}
        open={adding || editing !== null}
        onOpenChange={(open) => {
          if (!open) {
            setAdding(false)
            setEditing(null)
          }
        }}
        onSaved={refresh}
      />
    </>
  )
}
