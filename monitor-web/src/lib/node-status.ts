import type { TreeNode } from "@/types"

/**
 * Whether a node is up, across the three vocabularies the backend uses.
 *
 * Guests carry the hypervisor's word ("running"/"stopped"), machines carry a
 * reachability probe ("online"/"offline"/"unknown"), and the hypervisor and the
 * 9router view are up by definition — the first because the API reached it to
 * build the tree at all, the second because it is a view rather than a machine.
 *
 * Returns null when the node's state is genuinely unknown: a Wake-on-LAN entry
 * with no port to probe. That is a third answer, not a quiet "off", and the
 * dashboard shows it as its own shade rather than claiming the machine is down.
 */
export function nodeUp(node: TreeNode): boolean | null {
  if (node.kind === "hypervisor" || node.kind === "router9") return true
  if (node.status === "unknown" || !node.status) return null
  return node.status === "running" || node.status === "online"
}

/** The word to put on a node's badge. */
export function statusLabel(node: TreeNode): string {
  if (node.kind === "hypervisor") return "hypervisor"
  if (node.kind === "router9") return "router"
  return node.status || "unknown"
}
