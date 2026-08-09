/**
 * Response shapes of the Go API (monitor-api-go).
 *
 * These mirror the handlers exactly, including the awkward parts: several
 * blocks are built as bare `map[string]any` that stay EMPTY when the
 * underlying gopsutil call fails, so `memory.percent` and friends are
 * genuinely optional rather than merely nullable. The old JS read them
 * unguarded and rendered "undefined%" on a sensor hiccup; typing them
 * optional is what forces the "—" fallback at every use site.
 */

/** system.go — an empty object when the gopsutil probe failed. */
export interface UsageStats {
  total_gb?: number
  used_gb?: number
  percent?: number
}

export interface TempReading {
  current: number
  high: number | null
  critical: number | null
}

export interface SystemStats {
  hostname: string
  cpu: {
    percent: number
    count: number
    load_avg: number[]
  }
  memory: UsageStats
  disk: UsageStats
  /** Keyed by sensor label, e.g. "Package id 0" / "Tctl". */
  temperature: Record<string, TempReading>
  /** Name of the machine the readings describe when TEMP_DEVICE is set. */
  temperature_source: string
  uptime_seconds: number
  /** Cumulative counters since boot, not deltas. */
  network: { bytes_sent: number; bytes_recv: number }
  timestamp: string
}

export interface HistoryPoint {
  ts: string
  v: number
}

export interface SystemHistory {
  cpu: HistoryPoint[]
  ram: HistoryPoint[]
}

/** services.go — systemd states, passed through verbatim from systemctl. */
export type ServiceState =
  | "active"
  | "inactive"
  | "failed"
  | "activating"
  | "deactivating"
  | (string & {})

export interface ServiceInfo {
  id: string
  label: string
  icon: string
  unit: string
  active_state: ServiceState
  sub_state: string
  enabled: string
  description: string
}

export type ServiceAction = "start" | "stop" | "restart"

/** devices.go — credentials are never sent to the client, only `ssh_power`. */
export type DeviceProtocol = "ssh" | "wol" | "android"

export interface DeviceInfo {
  id: string
  label: string
  icon: string
  os: string
  host: string
  protocol: DeviceProtocol
  /** Whether SSH credentials exist, i.e. whether power actions are possible. */
  ssh_power: boolean
}

/** null = unknown (device not probeable), not offline. */
export type DeviceStatusMap = Record<string, boolean | null>

export interface DeviceWithStatus extends DeviceInfo {
  online?: boolean | null
}

export interface DeviceCreate {
  label: string
  icon?: string
  os: string
  protocol: DeviceProtocol
  host: string
  port?: number
  username?: string
  auth_type?: "password" | "key"
  password?: string
  key_path?: string
  mac_address?: string
  enable_ssh_power?: boolean
}

/** process.go — rows are grouped by name, `count` is how many were folded in. */
export interface ProcessRow {
  label: string
  name: string
  project: string | null
  user: string
  count: number
  top_pid: number
  cpu: number
  mem_bytes: number
  mem_percent: number
}

export interface ProcessesResponse {
  sort: ProcessSort
  ncpu: number
  processes: ProcessRow[]
}

export type ProcessSort = "cpu" | "ram"

export interface ProcessDetail {
  pid: number
  name: string
  project: string | null
  user: string
  status: string
  cmdline: string
  cwd: string
  ports: number[]
  uptime_seconds: number
  num_threads: number
  cpu: number
  mem_bytes: number
  mem_percent: number
  ppid: number
  parent_name: string
}

/** sshterm.go — a terminal that exists on the target, usually a tmux session. */
export interface SessionInfo {
  id: string
  /** Unix seconds; null when tmux did not report a creation time. */
  created_at: number | null
  attached: boolean
  persistent: boolean
}

export interface SessionsResponse {
  /** Whether the target supports tmux, i.e. whether sessions survive a reload. */
  persistent: boolean
  sessions: SessionInfo[]
}

/** Error body shape, inherited from the FastAPI original: {"detail": "..."}. */
export interface ApiErrorBody {
  detail?: string
}

/* ── Resource tree (nodes.go) ─────────────────────────────────────────────
 * A node is one machine in the tree: the hypervisor, a guest discovered from
 * it, or a machine added by hand. Guests exist in the tree whether or not
 * anyone has supplied credentials — the capability flags say what can
 * actually be opened.
 */

export type NodeKind = "hypervisor" | "vm" | "machine" | "router9"

export interface TreeNode {
  id: string
  label: string
  kind: NodeKind
  parent?: string
  os: string
  icon?: string
  host?: string
  device_id?: string
  vmid?: number
  /** "running" | "stopped" for guests; "unknown" for machines until probed. */
  status: string
  can_shell: boolean
  can_services: boolean
  can_tasks: boolean
  can_wake: boolean
  can_power: boolean
  device_ids?: string[]
  wake_device_id?: string
  /** Hypervisor figures, present for guests even with no credentials. */
  guest_mem_max?: number
  guest_mem_used?: number
  guest_cpus?: number
  guest_uptime?: number
  guest_disk_max?: number
  unreachable?: string
  is_local: boolean
  check_count: number
  checks_failing: number
}

export interface NodeSummary {
  hostname: string
  cpu_percent: number
  cpu_count: number
  load_avg?: number[]
  mem_total: number
  mem_used: number
  mem_percent: number
  disk_total: number
  disk_used: number
  disk_percent: number
  uptime_seconds: number
  /** "local" | "ssh" | "hypervisor" — hypervisor means no disk figure exists. */
  source: string
}

export interface ServiceUnit {
  name: string
  display: string
  state: "running" | "stopped" | "failed" | "activating" | "deactivating" | (string & {})
  sub?: string
  startup?: string
  /** Ships with the OS; hidden behind a toggle rather than dropped. */
  system: boolean
}

export interface NodeProcess {
  pid: number
  name: string
  cpu: number
  mem_bytes: number
  user?: string
}

/** Endpoints wrap their payload with a non-fatal error from the last refresh. */
export interface NodeSummaryResponse {
  summary: NodeSummary
  error?: string
}
export interface NodeServicesResponse {
  services: ServiceUnit[]
  error?: string
}
export interface NodeProcessesResponse {
  processes: NodeProcess[]
  error?: string
}

export interface DatacenterRow {
  node: TreeNode
  summary: NodeSummary
  error?: string
}
export interface DatacenterResponse {
  nodes: DatacenterRow[]
  generated_at: string
}

/** router9.go — request log dari dashboard API router, diagregasi per hari
 *  (WIB) dan per model. Token sering 0: router cuma mencatatnya untuk
 *  sebagian provider. */
export interface Router9Day {
  date: string
  requests: number
  prompt: number
  completion: number
  success: number
  errors: number
}

export interface Router9Model {
  model: string
  requests: number
  prompt: number
  completion: number
  success: number
  errors: number
}

export interface Router9Usage {
  generated_at: string
  total_requests: number
  success: number
  errors: number
  prompt_tokens: number
  completion_tokens: number
  days: Router9Day[]
  models: Router9Model[]
}

export interface Router9UsageResponse {
  usage: Router9Usage
  error?: string
}

/* ── App health checks (checks.go) ────────────────────────────────────────
 * Checks run from the API host, not from the node they are tagged with: the
 * tag says which machine to go look at, the result says what a client sees.
 */

export interface Check {
  id: string
  node_id?: string
  label: string
  url: string
  /** 0 accepts any 2xx/3xx. */
  expect_status?: number
  timeout_ms?: number
  insecure_tls?: boolean
  enabled: boolean
}

export interface CheckResult {
  at: string
  ok: boolean
  status?: number
  latency_ms: number
  error?: string
}

export interface CheckState extends Check {
  last?: CheckResult
  /** Oldest-first, in-memory only — a backend restart clears it. */
  history: CheckResult[]
  changed_at?: string
  fail_streak: number
}

export interface ChecksResponse {
  checks: CheckState[]
}

export interface ListeningPort {
  port: number
  process?: string
  /** Bound to loopback only, so unreachable from the API host unless local. */
  local: boolean
}

export interface PortsResponse {
  ports: ListeningPort[]
  host: string
  is_local: boolean
  error?: string
}

export interface CheckCreate {
  node_id?: string
  label: string
  url: string
  expect_status?: number
  timeout_ms?: number
  insecure_tls?: boolean
  enabled?: boolean
}

/** notify.go — status webhook notifikasi. URL-nya sendiri nggak pernah
 *  dikirim ke klien; itu kredensial. */
export interface NotifyStatus {
  configured: boolean
  /** "discord" | "ntfy" | "generic" | "" kalau belum diisi. */
  kind: string
  /** Berapa kali gagal berturut-turut sebelum dianggap beneran down. */
  threshold: number
}
