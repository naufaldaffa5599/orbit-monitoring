import { useState, type FormEvent } from "react"
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { api } from "@/lib/api"
import type { DeviceCreate, DeviceProtocol } from "@/types"

const EMPTY = {
  label: "",
  icon: "",
  os: "linux",
  protocol: "ssh" as DeviceProtocol,
  host: "",
  port: "22",
  username: "",
  mac: "",
  sshPower: false,
  authType: "password" as "password" | "key",
  password: "",
  keyPath: "",
}

export function AddDeviceDialog({
  open,
  onOpenChange,
  onAdded,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdded: () => void
}) {
  const [form, setForm] = useState(EMPTY)
  const [error, setError] = useState("")
  const [saving, setSaving] = useState(false)

  const set = <K extends keyof typeof EMPTY>(key: K, value: (typeof EMPTY)[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }))

  const isWol = form.protocol === "wol"
  // WOL itself needs nothing but a MAC; the SSH block only earns its place if
  // the device is also meant to accept restart/shutdown over SSH.
  const showSsh = form.protocol === "ssh" || (isWol && form.sshPower)

  const changeProtocol = (protocol: DeviceProtocol) => {
    setForm((prev) => ({
      ...prev,
      protocol,
      sshPower: protocol === "wol" ? prev.sshPower : false,
      os: protocol === "wol" ? "windows" : prev.os === "android" ? "linux" : prev.os,
      port: "22",
    }))
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setError("")

    const payload: DeviceCreate = {
      label: form.label.trim(),
      icon: form.icon.trim() || undefined,
      os: form.os,
      protocol: form.protocol,
      host: form.host.trim(),
    }
    if (isWol) {
      payload.mac_address = form.mac.trim()
      payload.enable_ssh_power = form.sshPower
    }
    if (showSsh) {
      payload.port = Number.parseInt(form.port, 10) || 22
      payload.username = form.username.trim()
      payload.auth_type = form.authType
      if (form.authType === "key") payload.key_path = form.keyPath.trim()
      else payload.password = form.password
    }

    setSaving(true)
    try {
      await api.addDevice(payload)
      toast.success(`Device "${payload.label}" ditambahkan`)
      setForm(EMPTY)
      onOpenChange(false)
      onAdded()
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal nambah device")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Tambah Device</DialogTitle>
        </DialogHeader>

        <form onSubmit={submit} className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="d-label">Nama</Label>
            <Input
              id="d-label"
              required
              placeholder="mis. Laptop Windows"
              value={form.label}
              onChange={(e) => set("label", e.target.value)}
            />
          </div>

          <div className="flex gap-2.5">
            <div className="flex flex-1 flex-col gap-1.5">
              <Label htmlFor="d-icon">Icon (emoji)</Label>
              <Input
                id="d-icon"
                maxLength={4}
                placeholder="🖥️"
                value={form.icon}
                onChange={(e) => set("icon", e.target.value)}
              />
            </div>
            <div className="flex flex-1 flex-col gap-1.5">
              <Label htmlFor="d-os">OS</Label>
              <Select value={form.os} onValueChange={(v) => set("os", v)}>
                <SelectTrigger id="d-os" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="linux">Linux</SelectItem>
                  <SelectItem value="windows">Windows</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="d-protocol">Cara Akses</Label>
            <Select
              value={form.protocol}
              onValueChange={(v) => changeProtocol(v as DeviceProtocol)}
            >
              <SelectTrigger id="d-protocol" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="ssh">SSH Terminal</SelectItem>
                <SelectItem value="wol">Wake on LAN</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex gap-2.5">
            <div className="flex flex-1 flex-col gap-1.5">
              <Label htmlFor="d-host">Host / IP</Label>
              <Input
                id="d-host"
                required={showSsh}
                placeholder="192.168.1.50"
                value={form.host}
                onChange={(e) => set("host", e.target.value)}
              />
            </div>
            {showSsh && (
              <div className="flex w-24 flex-col gap-1.5">
                <Label htmlFor="d-port">Port</Label>
                <Input
                  id="d-port"
                  type="number"
                  value={form.port}
                  onChange={(e) => set("port", e.target.value)}
                />
              </div>
            )}
          </div>

          {isWol && (
            <>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="d-mac">MAC Address</Label>
                <Input
                  id="d-mac"
                  placeholder="AA:BB:CC:DD:EE:FF"
                  value={form.mac}
                  onChange={(e) => set("mac", e.target.value)}
                />
              </div>
              <Label className="flex items-start gap-2 text-xs leading-relaxed font-normal text-muted-foreground">
                <Checkbox
                  checked={form.sshPower}
                  onCheckedChange={(checked) => set("sshPower", checked === true)}
                />
                Aktifkan kontrol SSH (Restart/Shutdown) — perlu OpenSSH Server aktif
                di device ini
              </Label>
            </>
          )}

          {showSsh && (
            <>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="d-username">Username</Label>
                <Input
                  id="d-username"
                  required
                  placeholder="mis. daffa"
                  value={form.username}
                  onChange={(e) => set("username", e.target.value)}
                />
              </div>

              <div className="flex flex-col gap-1.5">
                <Label htmlFor="d-auth">Metode Auth</Label>
                <Select
                  value={form.authType}
                  onValueChange={(v) => set("authType", v as "password" | "key")}
                >
                  <SelectTrigger id="d-auth" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="password">Password</SelectItem>
                    <SelectItem value="key">SSH Key (path file di server)</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              {form.authType === "key" ? (
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="d-key">Path Private Key (di server ini)</Label>
                  <Input
                    id="d-key"
                    placeholder="/home/daffa/.ssh/id_ed25519"
                    value={form.keyPath}
                    onChange={(e) => set("keyPath", e.target.value)}
                  />
                </div>
              ) : (
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="d-password">Password</Label>
                  <Input
                    id="d-password"
                    type="password"
                    autoComplete="new-password"
                    value={form.password}
                    onChange={(e) => set("password", e.target.value)}
                  />
                </div>
              )}
            </>
          )}

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
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
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
