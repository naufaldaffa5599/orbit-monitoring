import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react"
import { Keyboard, Plus, X } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import {
  newSessionId,
  STATUS_LABEL,
  TerminalSession,
  type Modifier,
} from "@/lib/terminal-session"
import { cn } from "@/lib/utils"

const KEYBAR_COLLAPSE_KEY = "terminal-keybar-collapsed"

const KEYS: { label: string; key?: string; quick?: string; toggle?: Modifier }[] = [
  { label: "Esc", key: "Escape" },
  { label: "Tab", key: "Tab" },
  { label: "Ctrl", toggle: "ctrl" },
  { label: "Shift", toggle: "shift" },
  { label: "Alt", toggle: "alt" },
  { label: "↑", key: "ArrowUp" },
  { label: "↓", key: "ArrowDown" },
  { label: "←", key: "ArrowLeft" },
  { label: "→", key: "ArrowRight" },
  { label: "Home", key: "Home" },
  { label: "End", key: "End" },
  { label: "^C", quick: "c" },
  { label: "^D", quick: "d" },
  { label: "^L", quick: "l" },
  { label: "^Z", quick: "z" },
]

/** Mounts one session's xterm into a stable DOM node, exactly once. */
function Pane({ session, active }: { session: TerminalSession; active: boolean }) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (ref.current) session.mount(ref.current)
  }, [session])

  return (
    <div
      ref={ref}
      // touch-action: none — the drag handlers drive scrolling and selection
      // themselves, so the browser must not claim the gesture as a pan/zoom.
      style={{ touchAction: "none" }}
      className={cn("absolute inset-0 p-2", active ? "block" : "hidden")}
    />
  )
}

/**
 * The terminal, without any assumption about what surrounds it.
 *
 * Split out of the standalone page so the dashboard can host the same thing in
 * a panel. Everything stateful lives here — sessions, keybar, paste overlay —
 * while the caller supplies whatever chrome belongs to its context: the page
 * adds a title bar with a back link, the dashboard adds a panel header.
 */
export function TerminalConsole({
  deviceId,
  headerExtra,
}: {
  deviceId: string | null
  /** Rendered at the right end of the toolbar, beside the status pill. */
  headerExtra?: React.ReactNode
}) {

  const [sessions, setSessions] = useState<TerminalSession[]>([])
  const [activeId, setActiveId] = useState<string | null>(null)
  const [keysCollapsed, setKeysCollapsed] = useState(
    () => localStorage.getItem(KEYBAR_COLLAPSE_KEY) === "1",
  )
  const [pasteOpen, setPasteOpen] = useState(false)
  const [pasteText, setPasteText] = useState("")

  // Sessions mutate in place (status, modifier flags). This is how those
  // mutations reach the chrome without cloning an xterm instance.
  const [, bump] = useReducer((n: number) => n + 1, 0)
  const counter = useRef(0)
  const initialised = useRef(false)

  const hooks = useMemo(
    () => ({
      onStatus: bump,
      onCopy: (chars: number) => toast.success(`Disalin (${chars} karakter)`),
      onCopyFailed: () =>
        toast.info("Select teks dulu (tap 👆 Select lalu geser)"),
    }),
    [],
  )

  const active = sessions.find((s) => s.id === activeId) ?? null

  const createSession = useCallback(
    (sessionId?: string) => {
      if (!deviceId) return
      counter.current += 1
      const session = new TerminalSession(
        `t${counter.current}`,
        deviceId,
        sessionId ?? newSessionId(),
        hooks,
      )
      setSessions((prev) => [...prev, session])
      setActiveId(session.id)
    },
    [deviceId, hooks],
  )


  // Which terminals exist is the server's business: it reads them off tmux on
  // the target, so the list is the same from any browser or phone.
  useEffect(() => {
    if (!deviceId || initialised.current) return
    initialised.current = true

    void (async () => {
      const existing = await api
        .sessions(deviceId)
        .then((r) => r.sessions)
        // offline / API down — start a fresh tab rather than hang
        .catch(() => [])

      if (existing.length > 0) existing.forEach((s) => createSession(s.id))
      else createSession()
    })()
  }, [deviceId, createSession])

  // Show "Tab 1" first rather than whichever session was adopted last.
  useEffect(() => {
    if (sessions.length > 0 && !sessions.some((s) => s.id === activeId)) {
      setActiveId(sessions[0].id)
    }
  }, [sessions, activeId])

  // Fit on activation: a pane that mounted while hidden has no measurable size,
  // so this is the first chance to size it correctly.
  useEffect(() => {
    if (!active) return
    active.refit()
    active.term.refresh(0, active.term.rows - 1)
    active.focus()
  }, [active, keysCollapsed])

  const closeTab = (session: TerminalSession) => {
    session.dispose()
    // Closing the socket only detaches — the shell lives on in tmux, so an
    // explicit close has to say so or the session leaks on the target.
    void api.killSession(session.deviceId, session.sessionId)
    setSessions((prev) => {
      const rest = prev.filter((s) => s.id !== session.id)
      if (rest.length === 0) {
        // never end up with zero tabs
        queueMicrotask(() => createSession())
      }
      return rest
    })
  }

  useEffect(() => {
    let timer: number | undefined
    const onResize = () => {
      window.clearTimeout(timer)
      timer = window.setTimeout(() => active?.refit(), 200)
    }
    window.addEventListener("resize", onResize)
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener("resize", onResize)
    }
  }, [active])

  // Coming back to the foreground: revive sockets the OS killed, and adopt any
  // session started elsewhere. Tabs are only ever ADDED here — a list that came
  // back short because the API blipped must not close a terminal in use.
  useEffect(() => {
    if (!deviceId) return
    const onVisible = () => {
      if (document.visibilityState !== "visible") return
      sessions.forEach((s) => s.reconnectIfDead())
      void api
        .sessions(deviceId)
        .then(({ sessions: remote }) => {
          const known = new Set(sessions.map((s) => s.sessionId))
          remote
            .filter((s) => !known.has(s.id))
            .forEach((s) => createSession(s.id))
        })
        .catch(() => {})
    }
    document.addEventListener("visibilitychange", onVisible)
    return () => document.removeEventListener("visibilitychange", onVisible)
  }, [deviceId, sessions, createSession])

  const toggleKeys = () => {
    setKeysCollapsed((prev) => {
      const next = !prev
      try {
        localStorage.setItem(KEYBAR_COLLAPSE_KEY, next ? "1" : "0")
      } catch {
        /* private mode — the toggle just won't be remembered */
      }
      return next
    })
  }

  const openPaste = async () => {
    setPasteText("")
    setPasteOpen(true)
    try {
      const text = await navigator.clipboard?.readText()
      if (text) setPasteText(text)
    } catch {
      /* no permission / insecure context — the user pastes manually */
    }
  }

  const sendPaste = () => {
    if (pasteText) active?.send(pasteText)
    setPasteOpen(false)
    active?.focus()
  }

  if (!deviceId) {
    return (
      <div className="flex h-full items-center justify-center bg-screen text-destructive">
        Node ini nggak punya device buat dibuka shell-nya.
      </div>
    )
  }

  const status = active?.status ?? "connecting"

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      {/* One toolbar: tabs on the left, state and controls on the right. The
          standalone page used two rows because it also carried a title bar;
          embedded in a panel that would waste a third of a small screen. */}
      <div className="flex shrink-0 items-center gap-1.5 overflow-x-auto border-b bg-chrome p-2">
        <button
          type="button"
          onClick={() => createSession()}
          title="Terminal baru"
          className="flex size-8.5 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground"
        >
          <Plus className="size-4" />
        </button>
        {sessions.map((session, index) => (
          <div
            key={session.id}
            className={cn(
              "flex h-8.5 shrink-0 items-center gap-1 rounded-md border pr-1 pl-2.5 text-xs font-semibold",
              session.id === activeId
                ? "border-primary bg-primary text-primary-foreground"
                : "bg-white/5",
            )}
          >
            <button type="button" onClick={() => setActiveId(session.id)}>
              {/* Position, not an ever-incrementing counter: closing down to one
                  tab and opening another should read "Tab 2" again, not "Tab 6" */}
              Tab {index + 1}
            </button>
            <button
              type="button"
              onClick={() => closeTab(session)}
              aria-label={`Tutup Tab ${index + 1}`}
              className="flex size-5 items-center justify-center rounded-[4px] bg-black/20"
            >
              <X className="size-3" />
            </button>
          </div>
        ))}

        <div className="ml-auto flex shrink-0 items-center gap-2 pl-2">
          {headerExtra}
          <button
            type="button"
            onClick={toggleKeys}
            aria-label="Sembunyikan atau tampilkan bar tombol"
            className={cn(
              "flex size-8.5 items-center justify-center rounded-md border",
              keysCollapsed ? "bg-primary text-primary-foreground" : "bg-white/5",
            )}
          >
            <Keyboard className="size-4" />
          </button>
          <span
            className={cn(
              "rounded-full border px-2.5 py-0.5 text-[0.7rem] font-semibold whitespace-nowrap",
              status === "connected" && "border-ok/35 bg-ok/12 text-ok",
              status === "connecting" && "bg-white/5 text-muted-foreground",
              (status === "disconnected" ||
                status === "reconnecting" ||
                status === "error") &&
                "border-destructive/35 bg-destructive/12 text-destructive",
            )}
          >
            {STATUS_LABEL[status]}
          </span>
        </div>
      </div>

      {!keysCollapsed && (
        <div className="flex shrink-0 flex-wrap gap-2 border-b bg-chrome p-2">
          {KEYS.map((entry) => {
            const isOn =
              entry.toggle === "ctrl"
                ? active?.ctrlActive
                : entry.toggle === "shift"
                  ? active?.shiftActive
                  : entry.toggle === "alt"
                    ? active?.altActive
                    : false
            return (
              <KeyButton
                key={entry.label}
                label={entry.label}
                on={!!isOn}
                onPress={() => {
                  if (!active) return
                  if (entry.toggle) active.toggle(entry.toggle)
                  else if (entry.key) active.sendNamedKey(entry.key)
                  else if (entry.quick) active.sendCtrl(entry.quick)
                }}
              />
            )
          })}
          <KeyButton
            label="👆 Select"
            on={!!active?.selectModeActive}
            onPress={() => active?.toggle("select")}
          />
          <KeyButton label="📋 Copy" onPress={() => active?.copySelection()} />
          <KeyButton label="📥 Paste" onPress={openPaste} />
        </div>
      )}

      <div className="relative min-h-0 flex-1 bg-screen">
        {sessions.map((session) => (
          <Pane
            key={session.id}
            session={session}
            active={session.id === activeId}
          />
        ))}
      </div>

      {pasteOpen && (
        <div className="fixed inset-0 z-50 flex items-end bg-black/60">
          <div className="flex w-full flex-col gap-2.5 border-t bg-popover p-3.5 pb-[calc(0.875rem+env(safe-area-inset-bottom))]">
            <div className="text-xs text-muted-foreground">
              Tempel teks (tekan lama di kotak buat munculin menu Tempel), lalu
              Kirim
            </div>
            <textarea
              autoFocus
              value={pasteText}
              onChange={(e) => setPasteText(e.target.value)}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              placeholder="Tempel di sini…"
              className="min-h-24 w-full resize-y rounded-md border bg-sunken p-2.5 font-mono text-sm"
            />
            <div className="flex justify-end gap-2.5">
              <Button variant="outline" onClick={() => setPasteOpen(false)}>
                Batal
              </Button>
              <Button onClick={sendPaste}>Kirim</Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * A keybar key. On touch, preventDefault on touchend both keeps focus on the
 * terminal (so the mobile keyboard stays up) AND suppresses the emulated click
 * that would otherwise follow — which is why the action runs from touchend
 * directly, with onClick only for mice.
 */
function KeyButton({
  label,
  on,
  onPress,
}: {
  label: string
  on?: boolean
  onPress: () => void
}) {
  const fired = useRef(false)
  return (
    <button
      type="button"
      onMouseDown={(e) => e.preventDefault()}
      onTouchEnd={(e) => {
        e.preventDefault()
        fired.current = true
        onPress()
      }}
      onClick={() => {
        if (fired.current) {
          fired.current = false
          return
        }
        onPress()
      }}
      className={cn(
        "h-11 min-w-11 shrink-0 rounded-md border px-3 font-mono text-sm font-semibold",
        on ? "bg-primary text-primary-foreground" : "bg-white/5",
      )}
    >
      {label}
    </button>
  )
}
