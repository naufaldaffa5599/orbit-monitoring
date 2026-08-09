import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import { wsUrl } from "@/lib/api"

/**
 * One terminal tab: an xterm instance, its websocket to the SSH backend, and
 * the touch handling that makes it usable on a phone.
 *
 * Deliberately a plain class rather than a hook or a component. Everything in
 * here is imperative and must survive every React render: the xterm instance
 * owns a canvas and a scrollback buffer that cannot be rebuilt without losing
 * the session, and the touch listeners have to sit in the CAPTURE phase on a
 * stable DOM node to beat xterm's own listeners (see attachTouch). React owns
 * the chrome around this — tab bar, keybar, status — and nothing inside it.
 */

export type TermStatus =
  | "connecting"
  | "connected"
  | "disconnected"
  | "reconnecting"
  | "error"

export const STATUS_LABEL: Record<TermStatus, string> = {
  connecting: "connecting…",
  connected: "connected",
  disconnected: "disconnected",
  reconnecting: "reconnecting…",
  error: "error",
}

export type Modifier = "ctrl" | "shift" | "alt" | "select"

const NAMED_KEY_SEQ: Record<string, string> = {
  Escape: "\x1b",
  Tab: "\t",
  Home: "\x1b[H",
  End: "\x1b[F",
  ArrowUp: "\x1b[A",
  ArrowDown: "\x1b[B",
  ArrowRight: "\x1b[C",
  ArrowLeft: "\x1b[D",
}

const CSI_LETTER: Record<string, string> = {
  ArrowUp: "A",
  ArrowDown: "B",
  ArrowRight: "C",
  ArrowLeft: "D",
  Home: "H",
  End: "F",
}

function ctrlCode(letter: string): string {
  return String.fromCharCode(letter.toUpperCase().charCodeAt(0) & 0x1f)
}

/** Becomes a tmux session name on the target, so it has to stay inside the
 *  charset the server accepts ([A-Za-z0-9_-], max 32). */
export function newSessionId(): string {
  const bytes = new Uint8Array(6)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")
}

const DRAG_THRESHOLD = 6

export interface SessionHooks {
  onStatus: () => void
  onCopy: (chars: number) => void
  onCopyFailed: () => void
}

export class TerminalSession {
  readonly id: string
  readonly sessionId: string
  readonly term: Terminal
  private readonly fitAddon = new FitAddon()
  readonly deviceId: string
  private readonly hooks: SessionHooks

  private ws: WebSocket | null = null
  private reconnectAttempts = 0
  private reconnectTimer: number | null = null
  private container: HTMLElement | null = null
  private detachTouch: (() => void) | null = null
  private disposed = false

  status: TermStatus = "connecting"
  ctrlActive = false
  shiftActive = false
  altActive = false
  selectModeActive = false

  constructor(
    id: string,
    deviceId: string,
    sessionId: string,
    hooks: SessionHooks,
  ) {
    this.id = id
    this.deviceId = deviceId
    this.sessionId = sessionId
    this.hooks = hooks
    this.term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      theme: { background: "#05070b", foreground: "#d4dae5", cursor: "#e57000" },
    })
    this.term.loadAddon(this.fitAddon)
    this.term.onData((data) => this.handleInput(data))
  }

  /** Idempotent: React may run the mounting effect more than once. */
  mount(container: HTMLElement) {
    if (this.container || this.disposed) return
    this.container = container
    this.term.open(container)
    this.attachTouch(container)
    this.connect()
  }

  private setStatus(status: TermStatus) {
    this.status = status
    this.hooks.onStatus()
  }

  connect() {
    if (this.disposed) return
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    this.term.reset()
    this.setStatus("connecting")

    const ws = new WebSocket(
      wsUrl(
        `/ws/ssh/${encodeURIComponent(this.deviceId)}/${encodeURIComponent(this.sessionId)}`,
      ),
    )
    this.ws = ws

    ws.onopen = () => {
      this.reconnectAttempts = 0
      this.setStatus("connected")
      this.refit()
    }
    ws.onmessage = (event: MessageEvent<string>) => this.term.write(event.data)
    ws.onclose = () => {
      if (this.disposed) return
      this.setStatus("disconnected")
      this.scheduleReconnect()
    }
    ws.onerror = () => this.setStatus("error")
  }

  private scheduleReconnect() {
    if (this.reconnectTimer !== null || this.disposed) return
    this.reconnectAttempts++
    const delay = Math.min(1000 * this.reconnectAttempts, 5000)
    this.setStatus("reconnecting")
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
  }

  /** Reconnect if the socket died while the page was backgrounded — mobile
   *  browsers suspend websockets and won't tell anyone. */
  reconnectIfDead() {
    if (!this.ws || this.ws.readyState === WebSocket.CLOSED) this.connect()
  }

  private get open(): boolean {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN
  }

  send(data: string) {
    if (this.open) this.ws!.send(data)
  }

  private handleInput(data: string) {
    if (!this.open) return
    if (this.ctrlActive && data.length === 1) {
      this.send(ctrlCode(data))
      return
    }
    if (this.altActive) {
      this.altActive = false
      this.hooks.onStatus()
      this.send("\x1b" + data)
      return
    }
    this.send(data)
  }

  refit() {
    this.fitAddon.fit()
    if (this.open) {
      this.send(
        JSON.stringify({
          type: "resize",
          cols: this.term.cols,
          rows: this.term.rows,
        }),
      )
    }
  }

  focus() {
    this.term.focus()
  }

  toggle(modifier: Modifier) {
    if (modifier === "ctrl") this.ctrlActive = !this.ctrlActive
    if (modifier === "shift") this.shiftActive = !this.shiftActive
    if (modifier === "alt") this.altActive = !this.altActive
    if (modifier === "select") this.selectModeActive = !this.selectModeActive
    this.hooks.onStatus()
    if (modifier !== "select" || !this.selectModeActive) this.focus()
  }

  private modifierCode(): number {
    let n = 1
    if (this.shiftActive) n += 1
    if (this.altActive) n += 2
    if (this.ctrlActive) n += 4
    return n
  }

  sendNamedKey(key: string) {
    let seq: string
    if (key === "Escape") {
      seq = NAMED_KEY_SEQ.Escape
    } else if (key === "Tab") {
      seq = this.shiftActive ? "\x1b[Z" : "\t"
    } else {
      const code = this.modifierCode()
      seq = code === 1 ? NAMED_KEY_SEQ[key] : `\x1b[1;${code}${CSI_LETTER[key]}`
    }
    this.send(seq)
    this.focus()
    if (this.altActive) {
      this.altActive = false
      this.hooks.onStatus()
    }
  }

  sendCtrl(letter: string) {
    this.send(ctrlCode(letter))
    this.focus()
  }

  copySelection() {
    const selection = this.term.getSelection()
    if (!selection) {
      this.hooks.onCopyFailed()
      return
    }
    void copyText(selection)
    this.hooks.onCopy(selection.length)
    this.focus()
  }

  /**
   * One-finger touch behaviour, ported verbatim in intent from the static
   * page — this is the part that took the most iterations to get right.
   *
   * xterm binds its own touchstart/touchmove on term.element (a DESCENDANT of
   * this container) and scrolls the viewport itself on drag, which fights a
   * selection drag. Registering with capture:true on the ANCESTOR lets these
   * handlers intercept and stopPropagation() before xterm's bubble-phase ones
   * ever see the touch.
   *
   *   • not in select mode: drag → scroll scrollback, tap → focus (keyboard)
   *   • in select mode: drag → selection, release → copy
   */
  private attachTouch(container: HTMLElement) {
    const target =
      this.term.element?.querySelector<HTMLElement>(".xterm-screen") ??
      this.term.element
    if (!target) return

    let dragScrolling = false
    let dragMoved = false
    let dragStartY = 0
    let dragLastY = 0

    // Mirror a real wheel event onto the node xterm binds its wheel handler
    // to, so a full-screen TUI with no scrollback (claude, htop) gets the
    // arrow-key sequences it scrolls with instead of nothing. Finger down
    // (px > 0) reveals OLDER output = wheel up = negative deltaY.
    const scrollByWheel = (px: number, touch: Touch) => {
      target.dispatchEvent(
        new WheelEvent("wheel", {
          deltaY: -px,
          deltaMode: 0,
          bubbles: true,
          cancelable: true,
          view: window,
          clientX: touch.clientX,
          clientY: touch.clientY,
        }),
      )
    }

    const fireMouse = (type: string, touch: Touch) => {
      target.dispatchEvent(
        new MouseEvent(type, {
          bubbles: true,
          cancelable: true,
          view: window,
          // xterm's SelectionService only starts a selection when detail === 1
          detail: 1,
          clientX: touch.clientX,
          clientY: touch.clientY,
          button: 0,
          buttons: type === "mouseup" ? 0 : 1,
        }),
      )
    }

    const onTouchStart = (event: TouchEvent) => {
      if (event.touches.length !== 1) return
      if (this.selectModeActive) {
        event.stopPropagation()
        fireMouse("mousedown", event.touches[0])
        return
      }
      // preventDefault here is what stops the browser synthesising the
      // compatibility mouse events xterm uses to focus its input — those, not
      // a touch listener, are what popped the keyboard on every scroll drag.
      // A genuine tap re-focuses explicitly in touchend.
      event.preventDefault()
      event.stopPropagation()
      dragScrolling = true
      dragMoved = false
      dragStartY = event.touches[0].clientY
      dragLastY = event.touches[0].clientY
    }

    const onTouchMove = (event: TouchEvent) => {
      if (event.touches.length !== 1) return
      if (this.selectModeActive) {
        event.preventDefault()
        event.stopPropagation()
        fireMouse("mousemove", event.touches[0])
        return
      }
      if (!dragScrolling) return
      const y = event.touches[0].clientY
      // ignore tiny jitter so a still finger stays a tap
      if (!dragMoved && Math.abs(y - dragStartY) < DRAG_THRESHOLD) return
      dragMoved = true
      event.preventDefault()
      event.stopPropagation()
      scrollByWheel(y - dragLastY, event.touches[0])
      dragLastY = y
    }

    const onTouchEnd = (event: TouchEvent) => {
      if (this.selectModeActive) {
        event.stopPropagation()
        fireMouse("mouseup", event.changedTouches[0])
        window.setTimeout(() => {
          const selection = this.term.getSelection()
          if (selection && selection.trim()) {
            void copyText(selection)
            this.hooks.onCopy(selection.length)
          }
        }, 30)
        return
      }
      if (dragMoved) {
        // a scroll drag must not fire a tap that opens the keyboard
        event.preventDefault()
        event.stopPropagation()
      } else {
        this.focus()
      }
      dragScrolling = false
      dragMoved = false
    }

    const opts: AddEventListenerOptions = { capture: true, passive: false }
    container.addEventListener("touchstart", onTouchStart, opts)
    container.addEventListener("touchmove", onTouchMove, opts)
    container.addEventListener("touchend", onTouchEnd, opts)

    this.detachTouch = () => {
      container.removeEventListener("touchstart", onTouchStart, opts)
      container.removeEventListener("touchmove", onTouchMove, opts)
      container.removeEventListener("touchend", onTouchEnd, opts)
    }
  }

  /** Closes the websocket and frees xterm. Does NOT end the remote session. */
  dispose() {
    this.disposed = true
    if (this.reconnectTimer !== null) window.clearTimeout(this.reconnectTimer)
    if (this.ws) {
      this.ws.onclose = null // a manual close must not trigger reconnect
      this.ws.close()
      this.ws = null
    }
    this.detachTouch?.()
    this.term.dispose()
  }
}

async function copyText(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      /* no permission / insecure context — fall through */
    }
  }
  // execCommand is deprecated but remains the only path that works over plain
  // http on a LAN address, which is exactly how this dashboard is reached.
  const ta = document.createElement("textarea")
  ta.value = text
  ta.style.position = "fixed"
  ta.style.opacity = "0"
  ta.style.top = "0"
  document.body.appendChild(ta)
  ta.focus()
  ta.select()
  try {
    document.execCommand("copy")
  } catch {
    /* ignore */
  }
  document.body.removeChild(ta)
}

export { copyText }
