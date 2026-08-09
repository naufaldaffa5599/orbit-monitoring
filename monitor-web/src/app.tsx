import { useCallback, useEffect, useState } from "react"
import { Dashboard } from "@/pages/dashboard"
import { LoginPage } from "@/pages/login"
import { OrbitMark } from "@/components/shell/orbit-mark"
import { api, setUnauthorizedHandler } from "@/lib/api"

/**
 * Decides whether to show the dashboard or the password gate.
 *
 * The answer comes from the server, never from localStorage: the session is an
 * HttpOnly cookie this code cannot read, which is the point — a flag in
 * storage would only ever be a suggestion, and one the browser's devtools
 * could flip.
 */
type Gate = "checking" | "locked" | "open"

export function App() {
  const [gate, setGate] = useState<Gate>("checking")
  // Whether a password is configured at all. With none set there is nothing to
  // sign out of, so the header should not offer it.
  const [gated, setGated] = useState(false)

  useEffect(() => {
    let alive = true
    api
      .authMe()
      .then((s) => {
        if (!alive) return
        setGated(s.required)
        setGate(s.authenticated ? "open" : "locked")
      })
      // A failure here is the API being unreachable, not a rejection. Showing
      // the dashboard lets its own panels report what is actually wrong,
      // rather than a login form that would fail the same way.
      .catch(() => alive && setGate("open"))
    return () => {
      alive = false
    }
  }, [])

  // Any 401 from anywhere — an expired session, a restarted API — drops
  // straight back here rather than leaving a dashboard full of dead panels.
  useEffect(() => {
    setUnauthorizedHandler(() => setGate("locked"))
    return () => setUnauthorizedHandler(null)
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } finally {
      // Locked either way: if the call failed the cookie may still be live,
      // but leaving the dashboard up would be the worse of the two mistakes.
      setGate("locked")
    }
  }, [])

  if (gate === "checking") {
    // One frame of nothing is better than a login form that flashes away for
    // someone who was already signed in.
    return (
      <div className="grid min-h-dvh place-items-center bg-background">
        <OrbitMark className="size-8 animate-pulse text-primary" />
      </div>
    )
  }

  if (gate === "locked") {
    return (
      <LoginPage
        onSuccess={() => {
          setGated(true)
          setGate("open")
        }}
      />
    )
  }

  return <Dashboard onLogout={gated ? logout : undefined} />
}
