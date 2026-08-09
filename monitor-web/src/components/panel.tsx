import type { ReactNode } from "react"
import { cn } from "@/lib/utils"

/**
 * A Proxmox-style panel: translucent card with a header strip.
 *
 * Deliberately not shadcn's <Card>. Card centres on padded content blocks with
 * a spaced-out header; every panel here is a titled strip over a flush list or
 * a padded body, and fighting Card's spacing at each call site costs more than
 * these twenty lines.
 */
export function Panel({
  className,
  children,
}: {
  className?: string
  children: ReactNode
}) {
  return (
    <section
      className={cn(
        "glass overflow-hidden rounded-lg border bg-card shadow-[0_12px_30px_rgba(0,0,0,0.28)]",
        className,
      )}
    >
      {children}
    </section>
  )
}

export function PanelHead({
  title,
  children,
}: {
  title: string
  /** Right-hand slot: a badge, a toggle, a note. */
  children?: ReactNode
}) {
  return (
    <header className="flex min-h-10 items-center justify-between gap-3 border-b bg-panel-head px-3.5 py-2">
      <h2 className="text-[0.8rem] font-semibold tracking-[0.02em]">{title}</h2>
      {children}
    </header>
  )
}

export function PanelBody({
  className,
  children,
}: {
  className?: string
  children: ReactNode
}) {
  return <div className={cn("p-3.5", className)}>{children}</div>
}

/** Loading placeholder matching a list row's height. */
export function RowSkeleton() {
  return (
    <div className="m-2.5 h-16 animate-pulse rounded-md bg-white/5" />
  )
}

/** Empty/error state inside a flush panel body. */
export function PanelMessage({ children }: { children: ReactNode }) {
  return <p className="p-3.5 text-sm text-muted-foreground">{children}</p>
}
