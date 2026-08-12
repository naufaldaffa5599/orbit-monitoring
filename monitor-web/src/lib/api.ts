import type {
  ApiErrorBody,
  AuthStatus,
  CheckCreate,
  ChecksResponse,
  CheckState,
  NotifyStatus,
  PortsResponse,
  DatacenterResponse,
  DeviceCreate,
  DeviceInfo,
  DeviceStatusMap,
  ProcessDetail,
  ProcessesResponse,
  ProcessSort,
  ServiceInfo,
  SessionsResponse,
  NodeProcessesResponse,
  NodeServicesResponse,
  NodeSummaryResponse,
  Router9UsageResponse,
  ServiceAction,
  SystemStats,
  TreeNode,
} from "@/types"

/**
 * The dashboard is served by the Go API itself, so every call is same-origin.
 * In `npm run dev` Vite proxies /api and /ws to :8585 (see vite.config.ts),
 * which keeps this a bare path in both environments.
 */

/** Thrown for non-2xx responses, carrying the API's own `detail` message. */
export class ApiError extends Error {
  // Declared and assigned rather than a constructor parameter property:
  // `erasableSyntaxOnly` (on in this tsconfig) forbids the latter, since it is
  // syntax that has to be compiled away rather than merely erased.
  readonly status: number

  constructor(status: number, detail: string) {
    super(detail)
    this.name = "ApiError"
    this.status = status
  }
}

/**
 * Called when the API says a session is gone, so the app can drop back to the
 * login screen instead of every poll failing silently behind a dashboard that
 * still looks alive.
 */
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  })
  if (res.status === 401 && !path.startsWith("/api/auth/")) {
    // The auth routes are excluded on purpose: a wrong password is also a 401,
    // and treating it as an expired session would bounce the user off the very
    // form they are trying to use.
    onUnauthorized?.()
  }
  if (!res.ok) {
    // A failure body is not guaranteed to be JSON (a proxy 502, say), so the
    // status line is the fallback message.
    const body = (await res.json().catch(() => null)) as ApiErrorBody | null
    throw new ApiError(res.status, body?.detail || `HTTP ${res.status}`)
  }
  return (await res.json()) as T
}

export const api = {
  // ── Auth ──────────────────────────────────────────────────────────────
  authMe: () => request<AuthStatus>("/api/auth/me"),
  login: (password: string, remember: boolean) =>
    request<{ ok: boolean }>("/api/auth/login", {
      method: "POST",
      body: JSON.stringify({ password, remember }),
    }),
  logout: () => request<{ ok: boolean }>("/api/auth/logout", { method: "POST" }),

  system: () => request<SystemStats>("/api/system"),

  processes: (sort: ProcessSort) =>
    request<ProcessesResponse>(`/api/processes?sort=${sort}`),
  processDetail: (pid: number) => request<ProcessDetail>(`/api/processes/${pid}`),

  services: () => request<ServiceInfo[]>("/api/services"),
  serviceAction: (id: string, action: ServiceAction) =>
    request<{ ok: boolean }>(
      `/api/services/${encodeURIComponent(id)}/action/${action}`,
      { method: "POST" },
    ),

  devices: () => request<DeviceInfo[]>("/api/devices"),
  devicesStatus: () => request<DeviceStatusMap>("/api/devices/status"),
  addDevice: (payload: DeviceCreate) =>
    request<{ id: string }>("/api/devices", {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  deleteDevice: (id: string) =>
    request<{ ok: boolean }>(`/api/devices/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),
  wake: (id: string) =>
    request<unknown>(`/api/devices/${encodeURIComponent(id)}/wol`, {
      method: "POST",
    }),
  power: (id: string, action: "restart" | "shutdown") =>
    request<unknown>(
      `/api/devices/${encodeURIComponent(id)}/power/${action}`,
      { method: "POST" },
    ),

  // ── Resource tree ─────────────────────────────────────────────────────
  nodes: () => request<TreeNode[]>("/api/nodes"),
  datacenter: () => request<DatacenterResponse>("/api/datacenter"),
  notifyStatus: () => request<NotifyStatus>("/api/notify"),
  notifyTest: () =>
    request<{ ok: boolean }>("/api/notify/test", { method: "POST" }),

  // ── App health checks ─────────────────────────────────────────────────
  checks: () => request<ChecksResponse>("/api/checks"),
  nodeChecks: (nodeId: string) =>
    request<ChecksResponse>(`/api/nodes/${encodeURIComponent(nodeId)}/checks`),
  nodePorts: (nodeId: string) =>
    request<PortsResponse>(`/api/nodes/${encodeURIComponent(nodeId)}/ports`),
  createCheck: (payload: CheckCreate) =>
    request<CheckState>("/api/checks", {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  updateCheck: (id: string, payload: CheckCreate) =>
    request<CheckState>(`/api/checks/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(payload),
    }),
  deleteCheck: (id: string) =>
    request<{ ok: boolean }>(`/api/checks/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),
  runCheck: (id: string) =>
    request<CheckState>(`/api/checks/${encodeURIComponent(id)}/run`, {
      method: "POST",
    }),

  renameNode: (nodeId: string, label: string, icon: string) =>
    request<{ ok: boolean }>(`/api/nodes/${encodeURIComponent(nodeId)}`, {
      method: "PATCH",
      body: JSON.stringify({ label, icon }),
    }),
  deleteNode: (nodeId: string) =>
    request<{ ok: boolean; removed: number }>(
      `/api/nodes/${encodeURIComponent(nodeId)}`,
      { method: "DELETE" },
    ),
  nodeSummary: (nodeId: string) =>
    request<NodeSummaryResponse>(`/api/nodes/${encodeURIComponent(nodeId)}/summary`),
  nodeServices: (nodeId: string) =>
    request<NodeServicesResponse>(`/api/nodes/${encodeURIComponent(nodeId)}/services`),
  nodeServiceAction: (nodeId: string, unit: string, action: ServiceAction) =>
    request<{ ok: boolean }>(
      `/api/nodes/${encodeURIComponent(nodeId)}/services/${encodeURIComponent(unit)}/${action}`,
      { method: "POST" },
    ),
  nodeProcesses: (nodeId: string, sort: "cpu" | "ram") =>
    request<NodeProcessesResponse>(
      `/api/nodes/${encodeURIComponent(nodeId)}/processes?sort=${sort}`,
    ),
  nodeKill: (nodeId: string, pid: number) =>
    request<{ ok: boolean }>(
      `/api/nodes/${encodeURIComponent(nodeId)}/processes/${pid}/kill`,
      { method: "POST" },
    ),

  router9Usage: () => request<Router9UsageResponse>("/api/router9/usage"),

  sessions: (deviceId: string) =>
    request<SessionsResponse>(
      `/api/devices/${encodeURIComponent(deviceId)}/sessions`,
    ),
  killSession: (deviceId: string, sessionId: string) =>
    fetch(
      `/api/devices/${encodeURIComponent(deviceId)}/sessions/${encodeURIComponent(sessionId)}`,
      // keepalive: the request has to outlive the page when a tab is closed
      // during unload, otherwise the tmux session leaks on the target.
      { method: "DELETE", keepalive: true },
    ),
}

/** ws:// or wss:// to match however the page itself was loaded. */
export function wsUrl(path: string): string {
  const proto = location.protocol === "https:" ? "wss" : "ws"
  return `${proto}://${location.host}${path}`
}
