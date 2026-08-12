import { Activity, Globe, ListTree, Settings, SquareTerminal } from "lucide-react"
import { cn } from "@/lib/utils"
import type { TreeNode } from "@/types"

/** Which page of a node is open. */
export type NodeView = "summary" | "services" | "tasks" | "apps" | "shell"

export interface Selection {
  nodeId: string
  view: NodeView
}

export const VIEW_META: Record<NodeView, { label: string; icon: typeof Activity }> = {
  summary: { label: "Summary", icon: Activity },
  services: { label: "Services", icon: Settings },
  tasks: { label: "Task Manager", icon: ListTree },
  apps: { label: "Apps", icon: Globe },
  shell: { label: "Shell", icon: SquareTerminal },
}

/**
 * Which views a node offers, from the backend's capability flags: a node with
 * no credentials has no services or shell, and Task Manager is Windows-only.
 * Checks need no credentials, so Apps is always there.
 */
export function viewsFor(node: TreeNode): NodeView[] {
  if (node.kind === "group") return [] // a heading has no pages
  const views: NodeView[] = []
  if (
    node.can_services ||
    node.kind === "vm" ||
    node.kind === "hypervisor" ||
    node.kind === "router9"
  ) {
    views.push("summary")
  }
  if (node.can_services) views.push("services")
  if (node.can_tasks) views.push("tasks")
  views.push("apps")
  if (node.can_shell) views.push("shell")
  return views
}

/**
 * The view switcher, above the open node's content.
 *
 * These used to hang off the node in the tree, which gave one chevron two
 * jobs: collapsing a node's menu also collapsed the machines under it. Proxmox
 * puts them here for the same reason, and it costs the sidebar nothing —
 * on a phone, switching view no longer means opening the drawer at all.
 *
 * Labels give way to icons alone when there is no room for them, except on the
 * open tab, which keeps its label so the strip always says where you are.
 */
export function NodeTabs({
  views,
  active,
  onSelect,
}: {
  views: NodeView[]
  active: NodeView
  onSelect: (view: NodeView) => void
}) {
  if (views.length < 2) return null

  return (
    <div role="tablist" className="-mb-px flex gap-0.5 overflow-x-auto border-b">
      {views.map((view) => {
        const { label, icon: Icon } = VIEW_META[view]
        const open = view === active
        return (
          <button
            key={view}
            type="button"
            role="tab"
            aria-selected={open}
            title={label}
            onClick={() => onSelect(view)}
            className={cn(
              "flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2 text-[0.8rem] whitespace-nowrap transition-colors",
              open
                ? "border-primary text-[#ffb469]"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="size-3.5 shrink-0" />
            <span className={cn("hidden min-[560px]:inline", open && "inline")}>
              {label}
            </span>
          </button>
        )
      })}
    </div>
  )
}
