import { useState, type FormEvent } from "react"
import { Pencil, Power, RotateCw, Trash2, Zap } from "lucide-react"
import { toast } from "sonner"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { api } from "@/lib/api"
import type { TreeNode } from "@/types"

/**
 * Per-node actions: rename, delete, and the power controls.
 *
 * Wake/Restart/Shutdown used to live on the Devices tab. That tab is gone —
 * machines are nodes now — so the buttons moved here, to the node they act on.
 *
 * They sit above the view tabs and so appear on every page of a node, which on
 * a phone is where five labelled buttons would cost two rows on every screen.
 * Below sm they keep their icons and drop their labels; the title and
 * aria-label carry the name, and both destructive ones confirm first anyway.
 */
export function NodeActions({
  node,
  onChanged,
  onDeleted,
}: {
  node: TreeNode
  onChanged: () => void
  onDeleted: () => void
}) {
  const [renameOpen, setRenameOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const run = async (fn: () => Promise<unknown>, ok: string, fail: string) => {
    setBusy(true)
    try {
      await fn()
      toast.success(ok)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : fail)
    } finally {
      setBusy(false)
    }
  }

  const power = async (action: "restart" | "shutdown") => {
    const deviceId = node.device_id
    if (!deviceId) return
    const verb = action === "restart" ? "restart" : "matiin"
    if (!window.confirm(`Yakin mau ${verb} "${node.label}"?`)) return
    await run(
      () => api.power(deviceId, action),
      `Perintah ${action} dikirim ke "${node.label}"`,
      `Gagal kirim perintah ${action}`,
    )
  }

  const wake = async () => {
    const deviceId = node.wake_device_id
    if (!deviceId) return
    await run(
      () => api.wake(deviceId),
      `Magic packet dikirim ke "${node.label}"`,
      "Gagal kirim Wake on LAN",
    )
  }

  const remove = async () => {
    const count = node.device_ids?.length ?? 1
    const extra =
      node.kind === "vm"
        ? "\n\nVM-nya sendiri tetap ada di Proxmox — yang dihapus cuma kredensial akses, jadi node ini balik cuma nampilin angka dari hypervisor."
        : node.kind === "hypervisor"
          ? "\n\n⚠️  Ini node hypervisor. Menghapusnya bikin semua VM ilang dari tree."
          : ""
    if (
      !window.confirm(
        `Hapus "${node.label}"?` +
          (count > 1 ? `\n\n${count} entry device bakal dihapus.` : "") +
          extra,
      )
    ) {
      return
    }
    setBusy(true)
    try {
      await api.deleteNode(node.id)
      toast.success(`"${node.label}" dihapus`)
      onDeleted()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal hapus")
    } finally {
      setBusy(false)
    }
  }

  const hasDevices = (node.device_ids?.length ?? 0) > 0 || !!node.device_id

  return (
    <>
      <div className="flex flex-wrap items-center gap-1.5">
        {node.can_wake && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={wake}
            title="Wake Up"
            aria-label="Wake Up"
            className="hover:border-ok/40 hover:bg-ok/12 hover:text-ok"
          >
            <Zap className="size-3.5" />
            <span className="max-sm:hidden">Wake Up</span>
          </Button>
        )}
        {node.can_power && (
          <>
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => power("restart")}
              title="Restart"
              aria-label="Restart"
              className="hover:border-warn/40 hover:bg-warn/12 hover:text-warn"
            >
              <RotateCw className="size-3.5" />
              <span className="max-sm:hidden">Restart</span>
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => power("shutdown")}
              title="Shutdown"
              aria-label="Shutdown"
              className="hover:border-destructive/40 hover:bg-destructive/12 hover:text-destructive"
            >
              <Power className="size-3.5" />
              <span className="max-sm:hidden">Shutdown</span>
            </Button>
          </>
        )}
        <Button
          size="sm"
          variant="outline"
          onClick={() => setRenameOpen(true)}
          title="Rename"
          aria-label="Rename"
        >
          <Pencil className="size-3.5" />
          <span className="max-sm:hidden">Rename</span>
        </Button>
        {hasDevices && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={remove}
            title="Hapus"
            aria-label="Hapus"
            className="hover:border-destructive/40 hover:bg-destructive/12 hover:text-destructive"
          >
            <Trash2 className="size-3.5" />
            <span className="max-sm:hidden">Hapus</span>
          </Button>
        )}
      </div>

      <RenameDialog
        node={node}
        open={renameOpen}
        onOpenChange={setRenameOpen}
        onSaved={onChanged}
      />
    </>
  )
}

function RenameDialog({
  node,
  open,
  onOpenChange,
  onSaved,
}: {
  node: TreeNode
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  const [label, setLabel] = useState(node.label)
  const [icon, setIcon] = useState(node.icon ?? "")
  const [saving, setSaving] = useState(false)

  // Reopening on a different node must not show the previous node's name.
  const [seen, setSeen] = useState(node.id)
  if (seen !== node.id) {
    setSeen(node.id)
    setLabel(node.label)
    setIcon(node.icon ?? "")
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    try {
      await api.renameNode(node.id, label.trim(), icon.trim())
      toast.success("Nama disimpan")
      onOpenChange(false)
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal simpan nama")
    } finally {
      setSaving(false)
    }
  }

  const reset = async () => {
    setSaving(true)
    try {
      await api.renameNode(node.id, "", "")
      toast.success("Nama dikembalikan ke aslinya")
      onOpenChange(false)
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Gagal reset nama")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Rename node</DialogTitle>
        </DialogHeader>

        <form onSubmit={submit} className="flex flex-col gap-3">
          <div className="flex gap-2.5">
            <div className="flex w-20 flex-col gap-1.5">
              <Label htmlFor="rn-icon">Icon</Label>
              <Input
                id="rn-icon"
                maxLength={4}
                value={icon}
                onChange={(e) => setIcon(e.target.value)}
                placeholder="🖥️"
              />
            </div>
            <div className="flex flex-1 flex-col gap-1.5">
              <Label htmlFor="rn-label">Nama</Label>
              <Input
                id="rn-label"
                required
                maxLength={48}
                value={label}
                onChange={(e) => setLabel(e.target.value)}
                placeholder={node.label}
              />
            </div>
          </div>

          <p className="text-xs text-muted-foreground">
            Cuma mengubah tampilan di dashboard. Nama asli device dan nama VM di
            Proxmox nggak ikut berubah.
          </p>

          <DialogFooter className="sm:justify-between">
            <Button
              type="button"
              variant="ghost"
              disabled={saving}
              onClick={reset}
              className="text-muted-foreground"
            >
              Balikin ke asli
            </Button>
            <div className="flex gap-2">
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
              >
                Batal
              </Button>
              <Button type="submit" disabled={saving}>
                {saving ? "Menyimpan…" : "Simpan"}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
