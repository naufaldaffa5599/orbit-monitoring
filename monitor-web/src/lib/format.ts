/** Display helpers. Every one of them has to survive a missing value, because
 *  the API's usage blocks come back empty when a probe fails. */

export const DASH = "—"

export function formatUptime(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null) return DASH
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export function formatBytes(bytes: number | undefined | null): string {
  if (bytes === undefined || bytes === null) return DASH
  const units = ["B", "KB", "MB", "GB", "TB"]
  let i = 0
  let val = bytes
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024
    i++
  }
  return `${val.toFixed(1)} ${units[i]}`
}

export function formatPercent(value: number | undefined | null): string {
  return value === undefined || value === null ? DASH : `${value}%`
}

/** "2.61 / 5.79 GB", or "—" when either half is missing. */
export function formatUsage(
  used: number | undefined,
  total: number | undefined,
): string {
  if (used === undefined || total === undefined) return DASH
  return `${used} / ${total} GB`
}

export function formatClock(date = new Date()): string {
  return date.toLocaleTimeString("id-ID", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })
}

/** Severity band shared by the gauges, the health chip and the process rows. */
export type Level = "ok" | "warn" | "crit"

export function levelFor(percent: number | undefined): Level {
  if (percent === undefined) return "ok"
  if (percent > 90) return "crit"
  if (percent > 70) return "warn"
  return "ok"
}

/** Friendlier labels for common process names; also strips a version suffix
 *  like "next-server (v16.2.1)" → "next-server". */
const PROC_ALIASES: Record<string, string> = {
  "next-server": "Next.js",
  node: "Node.js",
  chrome: "Chrome",
  python: "Python",
  python3: "Python",
  cloudflared: "Cloudflared",
  "livekit-server": "LiveKit",
  claude: "Claude Code",
  codex: "Codex",
}

export function prettyProcName(name: string): string {
  const base = name.replace(/\s*\(v[\d.]+\)\s*$/i, "").trim()
  return PROC_ALIASES[base] ?? base
}
