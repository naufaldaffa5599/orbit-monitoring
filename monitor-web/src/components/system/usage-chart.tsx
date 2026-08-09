import { useEffect, useRef } from "react"
import type { HistoryPoint, SystemHistory } from "@/types"

const PAD = { top: 10, right: 10, bottom: 25, left: 35 }

/**
 * The RRD-style history chart, drawn straight to a canvas.
 *
 * Hand-rolled rather than pulled from a chart library: it plots two fixed
 * 0–100% series and nothing else — no axes to configure, no tooltips, no
 * legend engine — so a charting dependency would be several hundred KB for
 * about forty lines of drawing code.
 */
export function UsageChart({ history }: { history: SystemHistory | null }) {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const draw = () => {
      const ctx = canvas.getContext("2d")
      if (!ctx) return
      const rect = canvas.getBoundingClientRect()
      if (rect.width === 0) return

      const dpr = window.devicePixelRatio || 1
      canvas.width = rect.width * dpr
      canvas.height = rect.height * dpr
      ctx.scale(dpr, dpr)
      const W = rect.width
      const H = rect.height
      ctx.clearRect(0, 0, W, H)

      // Colours come from the theme tokens so the chart can never drift from
      // the palette the gauges and badges use.
      const style = getComputedStyle(canvas)
      const token = (name: string) => style.getPropertyValue(name).trim()
      const cpuColor = token("--chart-1") || "#e57000"
      const ramColor = token("--chart-2") || "#5b9bd5"
      const mutedColor = token("--muted-foreground") || "#7d8698"

      const cpuData = history?.cpu ?? []
      const ramData = history?.ram ?? []

      if (cpuData.length < 2) {
        ctx.fillStyle = mutedColor
        ctx.font = "13px system-ui, sans-serif"
        ctx.textAlign = "center"
        ctx.fillText("Collecting data…", W / 2, H / 2)
        return
      }

      const cW = W - PAD.left - PAD.right
      const cH = H - PAD.top - PAD.bottom

      ctx.strokeStyle = "rgba(255,255,255,0.07)"
      ctx.lineWidth = 1
      for (let i = 0; i <= 4; i++) {
        const y = PAD.top + (cH / 4) * i
        ctx.beginPath()
        ctx.moveTo(PAD.left, y)
        ctx.lineTo(W - PAD.right, y)
        ctx.stroke()
        ctx.fillStyle = mutedColor
        ctx.font = "10px system-ui, sans-serif"
        ctx.textAlign = "right"
        ctx.fillText(`${100 - i * 25}%`, PAD.left - 5, y + 3)
      }

      const drawSeries = (data: HistoryPoint[], color: string, alpha: number) => {
        if (data.length < 2) return
        ctx.setLineDash([])
        ctx.strokeStyle = color
        ctx.lineWidth = 2
        ctx.globalAlpha = alpha
        ctx.beginPath()
        data.forEach((p, i) => {
          const x = PAD.left + (i / (data.length - 1)) * cW
          const y = PAD.top + cH * (1 - p.v / 100)
          if (i === 0) ctx.moveTo(x, y)
          else ctx.lineTo(x, y)
        })
        ctx.stroke()

        ctx.globalAlpha = alpha * 0.16
        ctx.lineTo(PAD.left + cW, PAD.top + cH)
        ctx.lineTo(PAD.left, PAD.top + cH)
        ctx.closePath()
        ctx.fillStyle = color
        ctx.fill()
        ctx.globalAlpha = 1
      }

      drawSeries(cpuData, cpuColor, 1)
      drawSeries(ramData, ramColor, 0.9)

      ctx.font = "10px system-ui, sans-serif"
      ctx.textAlign = "left"
      ctx.fillStyle = cpuColor
      ctx.fillText("● CPU", PAD.left, H - 5)
      ctx.fillStyle = ramColor
      ctx.fillText("● RAM", PAD.left + 50, H - 5)
    }

    draw()
    // The old chart only redrew on the 5s poll, so dragging the window edge
    // left it stretched until the next tick.
    const observer = new ResizeObserver(draw)
    observer.observe(canvas)
    return () => observer.disconnect()
  }, [history])

  return <canvas ref={canvasRef} className="block h-50 w-full" />
}
