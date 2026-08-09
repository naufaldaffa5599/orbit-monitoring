import { useCallback, useEffect, useRef, useState } from "react"

export interface PollResult<T> {
  data: T | null
  error: string | null
  /** True only until the first response settles, so tabs can show a skeleton. */
  loading: boolean
  updatedAt: Date | null
  refresh: () => void
}

/**
 * Polls `fetcher` on an interval, replacing the old global setInterval.
 *
 * Two things the original refresh loop got wrong and this does not:
 *  - a poll that is still in flight is never stacked on top of by the next
 *    tick, so a slow /api/devices/status probe can't queue up requests;
 *  - polling pauses while the tab is hidden and fires once on return, which
 *    matters on a phone where this runs as an installed PWA.
 *
 * `fetcher` is a dependency — wrap it in useCallback or the loop restarts on
 * every render.
 */
export function usePoll<T>(
  fetcher: () => Promise<T>,
  intervalMs = 5000,
): PollResult<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null)
  const inFlight = useRef(false)
  const alive = useRef(true)

  const run = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      const next = await fetcher()
      if (!alive.current) return
      setData(next)
      setError(null)
      setUpdatedAt(new Date())
    } catch (err) {
      if (!alive.current) return
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      inFlight.current = false
      if (alive.current) setLoading(false)
    }
  }, [fetcher])

  useEffect(() => {
    alive.current = true
    void run()

    const tick = () => {
      if (document.visibilityState === "visible") void run()
    }
    const id = window.setInterval(tick, intervalMs)
    document.addEventListener("visibilitychange", tick)

    return () => {
      alive.current = false
      window.clearInterval(id)
      document.removeEventListener("visibilitychange", tick)
    }
  }, [run, intervalMs])

  return { data, error, loading, updatedAt, refresh: run }
}
