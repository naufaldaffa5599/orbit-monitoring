import { useCallback } from "react"
import { Crumbs } from "@/components/shell/app-shell"
import { Panel, PanelBody, PanelHead, PanelMessage, RowSkeleton } from "@/components/panel"
import { Badge } from "@/components/ui/badge"
import {
  ChartContainer,
  ChartTooltip,
  type ChartConfig,
} from "@/components/ui/area-charts-2"
import { usePoll } from "@/hooks/use-poll"
import { api } from "@/lib/api"
import { DASH } from "@/lib/format"
import { cn } from "@/lib/utils"
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts"
import type { Router9Usage, TreeNode } from "@/types"

function Tile({ label, value, tone }: { label: string; value: string; tone?: "ok" | "warn" }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border bg-sunken p-3">
      <span className="text-[0.68rem] tracking-wide text-muted-foreground uppercase">
        {label}
      </span>
      <span
        className={cn(
          "tabular text-xl font-bold",
          tone === "ok" && "text-ok",
          tone === "warn" && "text-warn",
        )}
      >
        {value}
      </span>
    </div>
  )
}

function fmt(n: number | undefined): string {
  return n === undefined ? DASH : n.toLocaleString("id-ID")
}

// ── Charts ─────────────────────────────────────────────────────────────────

// Colours come from the theme tokens via the chart-config contract, so the
// areas and their tooltip swatches can never drift from the palette the
// gauges and badges use.
const requestChartConfig = {
  success: { label: "Sukses", color: "var(--ok)" },
  errors: { label: "Gagal", color: "var(--warn)" },
} satisfies ChartConfig

const tokenChartConfig = {
  prompt: { label: "Prompt", color: "var(--chart-1)" },
  completion: { label: "Completion", color: "var(--chart-2)" },
} satisfies ChartConfig

const requestSeries = [
  { key: "success", label: "Sukses", color: "var(--ok)" },
  { key: "errors", label: "Gagal", color: "var(--warn)" },
]

const tokenSeries = [
  { key: "prompt", label: "Prompt", color: "var(--chart-1)" },
  { key: "completion", label: "Completion", color: "var(--chart-2)" },
]

function UsageTooltip({
  active,
  payload,
  label,
  series,
}: {
  active?: boolean
  payload?: Array<{ dataKey: string; value: number }>
  label?: string
  series: { key: string; label: string; color: string }[]
}) {
  if (!active || !payload?.length) return null
  return (
    <div className="min-w-[180px] rounded-lg border bg-popover/95 p-4 shadow-lg backdrop-blur-sm">
      <div className="border-b border-border/50 pb-2 text-sm font-semibold text-popover-foreground">
        {label}
      </div>
      <div className="mt-3 space-y-1.5">
        {series.map((s) => {
          const p = payload.find((x) => x.dataKey === s.key)
          return (
            <div key={s.key} className="flex items-center justify-between gap-3">
              <span className="flex items-center gap-2">
                <span className="size-2.5 rounded-sm" style={{ backgroundColor: s.color }} />
                <span className="text-xs font-medium text-muted-foreground">{s.label}</span>
              </span>
              <span className="text-sm font-semibold text-popover-foreground">
                {(p?.value ?? 0).toLocaleString("id-ID")}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

/**
 * The 9router usage page. Not a machine — the node just points at the API
 * router's request log, so this view replaces the CPU/RAM summary entirely.
 */
export function Router9View({ node }: { node: TreeNode }) {
  const fetchUsage = useCallback(() => api.router9Usage(), [])
  const { data, error, loading } = usePoll(fetchUsage, 30000)

  const raw: Router9Usage | undefined = data?.usage
  const stale = data?.error || error

  // The API answers 200 with a zeroed usage object when the 9router login
  // fails, so a truthy `usage` is not proof of data — and its slices come back
  // empty. Both lists are pulled out once, defaulting to [], because a null
  // here used to throw and blank the entire dashboard: nothing renders an
  // error boundary above this view except the app-wide one.
  const days = raw?.days ?? []
  const models = raw?.models ?? []
  // Treat "no requests at all" as no data rather than as a wall of zeroes,
  // which otherwise reads as a working router that nobody used.
  const usage = raw && (raw.total_requests > 0 || days.length > 0) ? raw : undefined

  const requestData = days.map((d) => ({
    label: d.date.slice(5),
    success: d.success,
    errors: d.errors,
  }))
  const tokenData = days.map((d) => ({
    label: d.date.slice(5),
    prompt: d.prompt,
    completion: d.completion,
  }))

  return (
    <>
      <Crumbs trail={["Datacenter", node.label, "Summary"]} />

      <Panel>
        <PanelHead title="Usage 9router">
          <div className="flex items-center gap-2">
            {stale && (
              <Badge variant="outline" className="text-warn">
                data terakhir
              </Badge>
            )}
            {usage && (
              <span className="text-xs text-muted-foreground">
                {usage.generated_at.replace("T", " ").slice(0, 16)} WIB
              </span>
            )}
          </div>
        </PanelHead>
        <PanelBody>
          {loading && !usage ? (
            <RowSkeleton />
          ) : !usage ? (
            <PanelMessage>
              Gagal ambil usage 9router{stale ? ` — ${stale}` : ""}
            </PanelMessage>
          ) : (
            <>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
                <Tile label="Total request" value={fmt(usage.total_requests)} />
                <Tile label="Sukses" value={fmt(usage.success)} tone="ok" />
                <Tile label="Gagal" value={fmt(usage.errors)} tone="warn" />
                <Tile
                  label="Prompt token"
                  value={fmt(usage.prompt_tokens)}
                />
                <Tile label="Completion token" value={fmt(usage.completion_tokens)} />
              </div>
              <p className="mt-3 text-xs text-muted-foreground">
                Data dari log request dashboard 9router (maks. 1000 entri
                terbaru). Token cuma dicatat untuk sebagian provider, jadi angka
                token bisa lebih kecil dari request.
              </p>
            </>
          )}
        </PanelBody>
      </Panel>

      {usage && days.length > 0 && (
        <>
          <Panel>
            <PanelHead title="Request per hari">
              <span className="text-xs text-muted-foreground">zona WIB · sukses vs gagal</span>
            </PanelHead>
            <PanelBody>
              <ChartContainer config={requestChartConfig} className="h-64 w-full">
                <AreaChart
                  accessibilityLayer
                  data={requestData}
                  margin={{ top: 10, bottom: 10, left: 20, right: 20 }}
                >
                  <defs>
                    <linearGradient id="fillReqSuccess" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-success)" stopOpacity={0.7} />
                      <stop offset="95%" stopColor="var(--color-success)" stopOpacity={0.08} />
                    </linearGradient>
                    <linearGradient id="fillReqErrors" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-errors)" stopOpacity={0.7} />
                      <stop offset="95%" stopColor="var(--color-errors)" stopOpacity={0.08} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey="label"
                    tickLine={false}
                    axisLine={false}
                    tickMargin={10}
                    tick={{ fontSize: 12 }}
                    interval={0}
                  />
                  <YAxis hide />
                  <ChartTooltip
                    content={<UsageTooltip series={requestSeries} />}
                    cursor={{ stroke: "rgba(255,255,255,0.18)", strokeWidth: 1, strokeDasharray: "4 4" }}
                  />
                  <Area
                    dataKey="success"
                    type="natural"
                    stackId="a"
                    fill="url(#fillReqSuccess)"
                    fillOpacity={1}
                    stroke="var(--color-success)"
                    dot={false}
                    activeDot={{ r: 4, strokeWidth: 0 }}
                  />
                  <Area
                    dataKey="errors"
                    type="natural"
                    stackId="a"
                    fill="url(#fillReqErrors)"
                    fillOpacity={1}
                    stroke="var(--color-errors)"
                    dot={false}
                    activeDot={{ r: 4, strokeWidth: 0 }}
                  />
                </AreaChart>
              </ChartContainer>
            </PanelBody>
          </Panel>

          <Panel>
            <PanelHead title="Token per hari">
              <span className="text-xs text-muted-foreground">prompt + completion</span>
            </PanelHead>
            <PanelBody>
              <ChartContainer config={tokenChartConfig} className="h-64 w-full">
                <AreaChart
                  accessibilityLayer
                  data={tokenData}
                  margin={{ top: 10, bottom: 10, left: 20, right: 20 }}
                >
                  <defs>
                    <linearGradient id="fillTokPrompt" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-prompt)" stopOpacity={0.7} />
                      <stop offset="95%" stopColor="var(--color-prompt)" stopOpacity={0.08} />
                    </linearGradient>
                    <linearGradient id="fillTokCompletion" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-completion)" stopOpacity={0.7} />
                      <stop offset="95%" stopColor="var(--color-completion)" stopOpacity={0.08} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey="label"
                    tickLine={false}
                    axisLine={false}
                    tickMargin={10}
                    tick={{ fontSize: 12 }}
                    interval={0}
                  />
                  <YAxis hide />
                  <ChartTooltip
                    content={<UsageTooltip series={tokenSeries} />}
                    cursor={{ stroke: "rgba(255,255,255,0.18)", strokeWidth: 1, strokeDasharray: "4 4" }}
                  />
                  <Area
                    dataKey="prompt"
                    type="natural"
                    stackId="a"
                    fill="url(#fillTokPrompt)"
                    fillOpacity={1}
                    stroke="var(--color-prompt)"
                    dot={false}
                    activeDot={{ r: 4, strokeWidth: 0 }}
                  />
                  <Area
                    dataKey="completion"
                    type="natural"
                    stackId="a"
                    fill="url(#fillTokCompletion)"
                    fillOpacity={1}
                    stroke="var(--color-completion)"
                    dot={false}
                    activeDot={{ r: 4, strokeWidth: 0 }}
                  />
                </AreaChart>
              </ChartContainer>
            </PanelBody>
          </Panel>
        </>
      )}

      <Panel>
        <PanelHead title="Pemakaian per model">
          {models.length > 0 && (
            <span className="tabular text-xs text-muted-foreground">
              {models.length} model
            </span>
          )}
        </PanelHead>
        {loading && !usage ? (
          <RowSkeleton />
        ) : models.length === 0 ? (
          <PanelMessage>Belum ada pemakaian per model.</PanelMessage>
        ) : (
          // Six columns of numbers do not fit a phone, so the table scrolls
          // inside its own box rather than making the page scroll sideways.
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b bg-panel-head text-xs text-muted-foreground">
                  {/* w-full makes the name column absorb every spare pixel, so
                      the number columns sit against the right edge instead of
                      drifting apart on a wide screen. */}
                  <th className="w-full px-3.5 py-2 text-left font-medium">Model</th>
                  <th className="px-3.5 py-2 text-right font-medium">Request</th>
                  <th className="px-3.5 py-2 text-right font-medium">Prompt</th>
                  <th className="px-3.5 py-2 text-right font-medium">Completion</th>
                  <th className="px-3.5 py-2 text-right font-medium">Sukses</th>
                  <th className="px-3.5 py-2 text-right font-medium">Gagal</th>
                </tr>
              </thead>
              <tbody>
                {models.map((m) => (
                  <tr
                    key={m.model}
                    className="border-b last:border-0 hover:bg-white/[0.03]"
                  >
                    {/* Never truncated. The name is what identifies the row,
                        and clipping it turned four different deepseek builds
                        into four identical "deepseek-v4-…" lines. If it does
                        not fit, the table scrolls. */}
                    <td className="px-3.5 py-2 font-medium whitespace-nowrap">
                      {m.model}
                    </td>
                    <td className="tabular px-3.5 py-2 text-right whitespace-nowrap">
                      {fmt(m.requests)}
                    </td>
                    <td className="tabular px-3.5 py-2 text-right whitespace-nowrap">
                      {fmt(m.prompt)}
                    </td>
                    <td className="tabular px-3.5 py-2 text-right whitespace-nowrap">
                      {fmt(m.completion)}
                    </td>
                    {/* Coloured only when non-zero: a column of green and red
                        zeroes reads as a status report on nothing. */}
                    <td
                      className={cn(
                        "tabular px-3.5 py-2 text-right whitespace-nowrap",
                        m.success > 0 ? "text-ok" : "text-muted-foreground",
                      )}
                    >
                      {fmt(m.success)}
                    </td>
                    <td
                      className={cn(
                        "tabular px-3.5 py-2 text-right whitespace-nowrap",
                        m.errors > 0 ? "text-destructive" : "text-muted-foreground",
                      )}
                    >
                      {fmt(m.errors)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      {stale && (
        <Panel>
          <PanelMessage>Terakhir gagal ambil data: {stale}</PanelMessage>
        </Panel>
      )}
    </>
  )
}
