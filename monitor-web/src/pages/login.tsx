import { useEffect, useRef, useState, type FormEvent } from "react"
import { Eye, EyeOff, Lock, LoaderCircle } from "lucide-react"
import { OrbitMark } from "@/components/shell/orbit-mark"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { api } from "@/lib/api"

/**
 * The password gate.
 *
 * Deliberately not the dashboard's shader backdrop: that canvas runs five fbm
 * evaluations per fragment and this screen is shown to someone who is waiting.
 * The drifting sparks below cost a few hundred fillRect calls a frame instead,
 * and stop entirely once the dashboard takes over.
 */

/** Sparks per square pixel. Tuned so a laptop gets a field, not a blizzard. */
const SPARK_DENSITY = 1 / 9000
/** Phones get a fraction of that — same lesson the shader learned. */
const SPARK_CAP_HANDHELD = 90
const SPARK_CAP_DESKTOP = 320

export function LoginPage({ onSuccess }: { onSuccess: () => void }) {
  const [password, setPassword] = useState("")
  const [remember, setRemember] = useState(true)
  const [reveal, setReveal] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const canvasRef = useRef<HTMLCanvasElement | null>(null)

  useEffect(() => {
    // The OS-level setting wins outright: no canvas, no rAF loop, nothing to
    // pause. The page still reads fine as a flat dark field.
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return

    const canvas = canvasRef.current
    const ctx = canvas?.getContext("2d")
    if (!canvas || !ctx) return

    type Spark = { x: number; y: number; v: number; o: number }
    let sparks: Spark[] = []
    let raf = 0

    // Backing store deliberately left at CSS pixels rather than scaled by
    // devicePixelRatio: at this size the sparks are 1px smudges either way,
    // and on a 3x phone the DPR version costs nine times as much to fill.
    const seed = (): Spark => ({
      x: Math.random() * canvas.width,
      y: Math.random() * canvas.height,
      v: Math.random() * 0.25 + 0.05,
      o: Math.random() * 0.35 + 0.15,
    })

    const build = () => {
      canvas.width = window.innerWidth
      canvas.height = window.innerHeight
      const cap =
        window.innerWidth <= 820 ? SPARK_CAP_HANDHELD : SPARK_CAP_DESKTOP
      const count = Math.min(
        Math.floor(canvas.width * canvas.height * SPARK_DENSITY),
        cap,
      )
      sparks = Array.from({ length: count }, seed)
    }

    const draw = () => {
      ctx.clearRect(0, 0, canvas.width, canvas.height)
      for (const s of sparks) {
        s.y -= s.v
        if (s.y < 0) {
          // Recycled rather than reallocated: this runs 60 times a second and
          // the garbage would be the most expensive thing on the page.
          s.x = Math.random() * canvas.width
          s.y = canvas.height + Math.random() * 40
          s.v = Math.random() * 0.25 + 0.05
          s.o = Math.random() * 0.35 + 0.15
        }
        ctx.fillStyle = `rgba(229, 112, 0, ${s.o})`
        ctx.fillRect(s.x, s.y, 0.7, 2.2)
      }
      raf = requestAnimationFrame(draw)
    }

    build()
    raf = requestAnimationFrame(draw)
    window.addEventListener("resize", build)
    return () => {
      window.removeEventListener("resize", build)
      cancelAnimationFrame(raf)
    }
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      await api.login(password, remember)
      onSuccess()
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal login")
      setPassword("")
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="fixed inset-0 overflow-hidden bg-background text-foreground">
      {/* Vignette, so the card sits in the brightest part of the field */}
      <div className="pointer-events-none absolute inset-0 [background:radial-gradient(80%_60%_at_50%_30%,rgba(255,255,255,0.05),transparent_60%)]" />

      <div className="login-lines" aria-hidden="true">
        <div className="hline" />
        <div className="hline" />
        <div className="hline" />
        <div className="vline" />
        <div className="vline" />
        <div className="vline" />
      </div>

      <canvas
        ref={canvasRef}
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 h-full w-full opacity-60 mix-blend-screen"
      />

      <header className="absolute inset-x-0 top-0 flex items-center justify-between border-b bg-chrome px-4 py-3">
        <div className="flex items-center gap-2">
          <OrbitMark className="size-6 text-primary" />
          <span className="text-[0.95rem] font-semibold tracking-tight">
            Orbit
          </span>
        </div>
        <span className="text-[0.62rem] font-semibold tracking-[0.14em] text-muted-foreground uppercase">
          Server Environment
        </span>
      </header>

      <div className="grid h-full w-full place-items-center px-4">
        <form
          onSubmit={submit}
          className="login-card w-full max-w-sm rounded-lg border bg-card p-6 shadow-[0_20px_50px_rgba(0,0,0,0.45)] backdrop-blur-xl"
        >
          <h1 className="text-2xl font-semibold tracking-tight">Masuk</h1>
          <p className="mt-1.5 text-sm text-muted-foreground">
            Dashboard ini bisa buka terminal dan matiin mesin. Masukin password
            dulu.
          </p>

          <div className="mt-6 grid gap-2">
            <Label htmlFor="password">Password</Label>
            <div className="relative">
              <Lock className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                id="password"
                type={reveal ? "text" : "password"}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="••••••••"
                autoFocus
                autoComplete="current-password"
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? "login-error" : undefined}
                className="bg-sunken pr-10 pl-10"
              />
              <button
                type="button"
                onClick={() => setReveal((v) => !v)}
                aria-label={reveal ? "Sembunyikan password" : "Tampilkan password"}
                className="absolute top-1/2 right-2 -translate-y-1/2 rounded-md p-2 text-muted-foreground hover:text-foreground"
              >
                {reveal ? (
                  <EyeOff className="size-4" />
                ) : (
                  <Eye className="size-4" />
                )}
              </button>
            </div>
          </div>

          {/* role=alert so a screen reader announces the rejection, which is
              otherwise a silent colour change */}
          {error && (
            <p
              id="login-error"
              role="alert"
              className="mt-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {error}
            </p>
          )}

          <div className="mt-4 flex items-center gap-2">
            <Checkbox
              id="remember"
              checked={remember}
              onCheckedChange={(v) => setRemember(v === true)}
            />
            <Label htmlFor="remember" className="text-muted-foreground">
              Ingat perangkat ini
            </Label>
          </div>

          <Button
            type="submit"
            disabled={busy || password === ""}
            className="mt-6 h-10 w-full"
          >
            {busy && <LoaderCircle className="size-4 animate-spin" />}
            {busy ? "Mengecek…" : "Masuk"}
          </Button>

          <p className="mt-4 text-center text-xs text-muted-foreground">
            Sesi bertahan {remember ? "14 hari" : "12 jam"}.
          </p>
        </form>
      </div>
    </section>
  )
}
