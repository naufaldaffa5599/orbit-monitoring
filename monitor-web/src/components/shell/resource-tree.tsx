import { useState } from "react"
import { ChevronDown, ChevronRight, HardDrive, Server } from "lucide-react"
import { cn } from "@/lib/utils"
import { ROOT_LABEL } from "@/lib/labels"
import { nodeUp } from "@/lib/node-status"
import type { TreeNode } from "@/types"

/**
 * One indent step, applied by nesting rather than by multiplying a depth.
 *
 * ml-2 (0.5rem) + pl-2 (0.5rem) = 1rem, which is exactly the width of the
 * chevron gutter every row carries — so a child's icon lands one gutter to the
 * right of its parent's, and the border sits between the two columns.
 */
const INDENT = "ml-2"

function statusDot(node: TreeNode) {
  const up = nodeUp(node)
  if (up === null) return "bg-muted-foreground/40" // no way to tell
  return up ? "bg-ok" : "bg-muted-foreground"
}

/**
 * The Server View tree.
 *
 *   Station
 *   ├── PVE                  ← the hypervisor, with its guests inside it
 *   │   ├── NAS
 *   │   └── PROJECT
 *   ├── OUTPOSTS             ← a heading, not a machine
 *   │   ├── Laptop Server
 *   │   └── …
 *   └── 9router
 *
 * Machines only. Each node's own pages (Summary, Services, Shell…) are tabs
 * over the content instead — see NodeTabs. They used to hang here as child
 * rows, which meant a node's chevron collapsed its menu and its machines
 * together, and there was no way to hide one without the other.
 *
 * Guests nest under the hypervisor because they genuinely live inside it; a
 * machine added by hand is a peer of the hypervisor, not its child.
 */
export function ResourceTree({
  nodes,
  selectedId,
  onSelect,
  datacenterOpen,
  onDatacenter,
}: {
  nodes: TreeNode[]
  selectedId: string
  onSelect: (nodeId: string) => void
  datacenterOpen: boolean
  onDatacenter: () => void
}) {
  // Explicit user choices only; everything else falls back to openByDefault.
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const roots = nodes.filter((n) => !n.parent)
  const childrenOf = (id: string) => nodes.filter((n) => n.parent === id)

  const renderNode = (node: TreeNode) => {
    const kids = childrenOf(node.id)
    const isSelected = selectedId === node.id && !datacenterOpen
    // A heading is styled as one, but it does open a page — a rollup of the
    // machines beneath it — so it selects like any other row.
    const isHeading = node.kind === "group"
    // The hypervisor and the headings are the spine of the tree, and the node
    // you are looking at should show where you are; an explicit collapse still
    // wins over all three.
    const isOpen =
      expanded[node.id] ??
      (node.kind === "hypervisor" || isHeading || isSelected)
    return (
      <div key={node.id}>
        <div className="flex items-center gap-1">
          {kids.length > 0 ? (
            <button
              type="button"
              onClick={() => setExpanded((prev) => ({ ...prev, [node.id]: !isOpen }))}
              aria-label={isOpen ? "Collapse" : "Expand"}
              aria-expanded={isOpen}
              className="flex size-4 shrink-0 items-center justify-center text-muted-foreground hover:text-foreground"
            >
              {isOpen ? (
                <ChevronDown className="size-3" />
              ) : (
                <ChevronRight className="size-3" />
              )}
            </button>
          ) : (
            <span className="size-4 shrink-0" />
          )}

          <button
            type="button"
            onClick={() => onSelect(node.id)}
            className={cn(
              "flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1.5 text-left transition-colors",
              isHeading
                ? "text-[0.72rem] font-semibold tracking-[0.04em]"
                : "text-sm",
              isSelected
                ? "bg-primary/15 text-[#ffb469] shadow-[inset_2px_0_0_var(--primary)]"
                : isHeading
                  ? "text-secondary-foreground hover:bg-white/5 hover:text-foreground"
                  : "text-sidebar-foreground hover:bg-white/5 hover:text-foreground",
            )}
          >
            {node.kind === "hypervisor" ? (
              <Server className="size-3.5 shrink-0" />
            ) : (
              <span className="shrink-0 text-xs">{node.icon || "🖥️"}</span>
            )}
            <span className="truncate">{node.label}</span>
            {node.vmid ? (
              <span className="shrink-0 text-[0.6rem] text-muted-foreground">
                {node.vmid}
              </span>
            ) : null}
            {node.checks_failing > 0 && (
              <span
                className="ml-auto shrink-0 rounded-full bg-destructive/15 px-1.5 text-[0.6rem] font-semibold text-destructive"
                title={`${node.checks_failing} check gagal`}
              >
                {node.checks_failing}
              </span>
            )}
            {/* A heading has no status of its own; the machines under it do. */}
            {!isHeading && (
              <span
                className={cn(
                  "shrink-0 rounded-full",
                  node.checks_failing > 0 ? "size-1.5" : "ml-auto size-1.5",
                  statusDot(node),
                )}
                title={node.status}
              />
            )}
          </button>
        </div>

        {/* One indent step per level, produced by nesting alone — no depth
            arithmetic. Each child carries the same size-4 gutter the chevron
            occupies on its parent's row. */}
        {isOpen && kids.length > 0 && (
          <div className={cn("flex flex-col gap-0.5 border-l pl-2", INDENT)}>
            {kids.map((kid) => renderNode(kid))}
          </div>
        )}
      </div>
    )
  }

  return (
    <>
      <div className="flex items-center justify-between border-b px-3.5 py-2.5 text-[0.72rem] font-semibold tracking-[0.04em] text-secondary-foreground uppercase">
        <span>Server View</span>
      </div>

      <nav className="flex flex-1 flex-col gap-0.5 overflow-y-auto p-1.5">
        <div className="flex items-center gap-1">
          <span className="size-4 shrink-0" />
          <button
            type="button"
            onClick={onDatacenter}
            className={cn(
              "flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1.5 text-left text-sm font-semibold transition-colors",
              datacenterOpen
                ? "bg-primary/15 text-[#ffb469] shadow-[inset_2px_0_0_var(--primary)]"
                : "hover:bg-white/5",
            )}
          >
            <HardDrive className="size-3.5 shrink-0" />
            <span>{ROOT_LABEL}</span>
          </button>
        </div>

        {/* Everything hangs off the root with the same step every other level
            uses, so the whole tree reads as one column of icons. */}
        <div className={cn("flex flex-col gap-0.5 border-l pl-2", INDENT)}>
          {roots.map((node) => renderNode(node))}

          {nodes.length === 0 && (
            <span className="px-1.5 py-2 text-xs text-muted-foreground">
              Belum ada node.
            </span>
          )}
        </div>
      </nav>
    </>
  )
}
