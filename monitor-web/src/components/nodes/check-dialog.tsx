import { useEffect, useState, type FormEvent } from "react"
import { toast } from "sonner"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { api } from "@/lib/api"
import { cn } from "@/lib/utils"
import type { CheckState, ListeningPort, TreeNode } from "@/types"

/**
 * Add or edit one check.
 *
 * The port suggestions are the point of this dialog. Typing eight URLs by hand
 * for eight apps is the kind of chore that stops anyone setting checks up at
 * all, so the node is asked what it is actually listening on and each port
 * becomes one click.
 */
export function CheckDialog({
  node,
  check,
  open,
  onOpenChange,
  onSaved,
}: {
  node: TreeNode
  check: CheckState | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  const [label, setLabel] = useState("")
  const [url, setUrl] = useState("")
  const [insecure, setInsecure] = useState(false)
  const [enabled, setEnabled] = useState(true)
  const [saving, setSaving] = useState(false)
  const [ports, setPorts] = useState<ListeningPort[] | null>(null)
  const [portsHost, setPortsHost] = useState("127.0.0.1")
  const [portsError, setPortsError] = useState("")

  useEffect(() => {
    if (!open) return
    setLabel(check?.label ?? "")
    setUrl(check?.url ?? "")
    setInsecure(check?.insecure_tls ?? false)
    setEnabled(check?.enabled ?? true)
  }, [open, check])

  useEffect(() => {
    if (!open) return
    let alive = true
    setPorts(null)
    setPortsError("")
    api
      .nodePorts(node.id)
      .then((res) => {
        if (!alive) return
        setPorts(res.ports)
        setPortsHost(res.host)
        if (res.error) setPortsError(res.error)
      })
      .catch((err: Error) => alive && setPortsError(err.message))
    return () => {
      alive = false
    }
  }, [open, node.id])

  const applyPort = (port: ListeningPort) => {
    setUrl(`http://${portsHost}:${port.port}`)
    if (!label && port.process) setLabel(port.process)
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    const payload = {
      node_id: node.id,
      label: label.trim(),
      url: url.trim(),
      insecure_tls: insecure,
      enabled,
    }
    try {
      if (check) await api.updateCheck(check.id, payload)
      else await api.createCheck(payload)
      toast.success(check ? "Check disimpan" : `Check "${payload.label}" ditambahkan`)
      onOpenChange(false)
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal simpan check")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{check ? "Edit check" : "Tambah check"}</DialogTitle>
        </DialogHeader>

        <form onSubmit={submit} className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ck-label">Nama</Label>
            <Input
              id="ck-label"
              required
              maxLength={48}
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="mis. Poka Server"
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="ck-url">URL</Label>
            <Input
              id="ck-url"
              required
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="http://127.0.0.1:3000"
              className="font-mono text-sm"
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label>Port yang lagi kebuka di {node.label}</Label>
            {portsError ? (
              <p className="text-xs text-muted-foreground">
                Nggak bisa baca port: {portsError}
              </p>
            ) : ports === null ? (
              <p className="text-xs text-muted-foreground">Ngecek…</p>
            ) : ports.length === 0 ? (
              <p className="text-xs text-muted-foreground">Nggak ada yang dengerin.</p>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {ports.map((port) => (
                  <button
                    key={port.port}
                    type="button"
                    onClick={() => applyPort(port)}
                    title={
                      port.local && !node.is_local
                        ? "Cuma dengerin di loopback — nggak kejangkau dari host dashboard"
                        : port.process || "klik buat pakai"
                    }
                    className={cn(
                      "rounded-md border px-2 py-1 font-mono text-[0.7rem] transition-colors hover:bg-white/8",
                      port.local && !node.is_local
                        ? "border-dashed text-muted-foreground"
                        : "bg-white/5",
                    )}
                  >
                    :{port.port}
                    {port.process && (
                      <span className="ml-1.5 font-sans text-muted-foreground">
                        {port.process}
                      </span>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>

          <Label className="flex items-start gap-2 text-xs leading-relaxed font-normal text-muted-foreground">
            <Checkbox
              checked={insecure}
              onCheckedChange={(v) => setInsecure(v === true)}
            />
            Abaikan error sertifikat TLS — perlu buat HTTPS bersertifikat
            self-signed, misalnya UI Proxmox
          </Label>

          <Label className="flex items-start gap-2 text-xs leading-relaxed font-normal text-muted-foreground">
            <Checkbox
              checked={enabled}
              onCheckedChange={(v) => setEnabled(v === true)}
            />
            Aktif — kalau dimatiin, check-nya disimpan tapi nggak dijalanin
          </Label>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Batal
            </Button>
            <Button type="submit" disabled={saving}>
              {saving ? "Menyimpan…" : "Simpan"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
