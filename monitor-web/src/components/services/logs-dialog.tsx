import { useEffect, useRef, useState } from "react"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { wsUrl } from "@/lib/api"
import { cn } from "@/lib/utils"

/** Live `journalctl -f` tail over a WebSocket. */
export function LogsDialog({
  service,
  onClose,
}: {
  service: { id: string; label: string } | null
  onClose: () => void
}) {
  const [lines, setLines] = useState<string[]>([])
  const [live, setLive] = useState(false)
  const boxRef = useRef<HTMLPreElement>(null)
  // Whether to keep following the tail. Read inside the socket handler, so it
  // has to be a ref — a state read there would close over a stale value.
  const followRef = useRef(true)

  useEffect(() => {
    if (!service) return
    setLines([])
    setLive(false)
    followRef.current = true

    const socket = new WebSocket(
      wsUrl(`/ws/logs/${encodeURIComponent(service.id)}`),
    )
    socket.addEventListener("open", () => setLive(true))
    socket.addEventListener("close", () => setLive(false))
    socket.addEventListener("message", (event: MessageEvent<string>) => {
      const box = boxRef.current
      // Decide before the append, while the old scroll height still applies.
      followRef.current = box
        ? box.scrollTop + box.clientHeight >= box.scrollHeight - 20
        : true
      setLines((prev) => [...prev, event.data])
    })

    return () => socket.close()
  }, [service])

  useEffect(() => {
    if (followRef.current && boxRef.current) {
      boxRef.current.scrollTop = boxRef.current.scrollHeight
    }
  }, [lines])

  return (
    <Dialog open={service !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-190">
        <DialogHeader className="flex-row items-center justify-between gap-3 space-y-0">
          <DialogTitle className="truncate">
            Logs — {service?.label ?? ""}
          </DialogTitle>
          <span
            title={live ? "Live" : "Terputus"}
            className={cn(
              "mr-6 size-2 shrink-0 rounded-full",
              live
                ? "animate-pulse bg-ok shadow-[0_0_8px_var(--ok)]"
                : "bg-muted-foreground",
            )}
          />
        </DialogHeader>

        <pre
          ref={boxRef}
          className="h-[62dvh] overflow-auto rounded-md border bg-screen p-3 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap text-[#c7d2e0]"
        >
          {lines.length === 0 ? "Menyambungkan…" : lines.join("\n")}
        </pre>
      </DialogContent>
    </Dialog>
  )
}
