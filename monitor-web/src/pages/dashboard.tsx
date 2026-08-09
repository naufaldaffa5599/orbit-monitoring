import { useCallback, useEffect, useState } from "react"
import { AppShell } from "@/components/shell/app-shell"
import type { Selection } from "@/components/shell/resource-tree"
import { DatacenterView } from "@/components/nodes/datacenter"
import { NodeSummaryView } from "@/components/nodes/node-summary"
import { NodeServicesView } from "@/components/nodes/node-services"
import { NodeTasksView } from "@/components/nodes/node-tasks"
import { NodeAppsView } from "@/components/nodes/node-apps"
import { NodeShellView } from "@/components/nodes/node-shell"
import { Router9View } from "@/components/nodes/node-router9"
import { AddDeviceDialog } from "@/components/devices/add-device-dialog"
import { ErrorBoundary } from "@/components/error-boundary"
import { Panel, PanelMessage } from "@/components/panel"
import { Toaster } from "@/components/ui/sonner"
import { usePoll } from "@/hooks/use-poll"
import { api } from "@/lib/api"
import { formatClock } from "@/lib/format"

export function Dashboard() {
  // The datacenter overview is the landing screen, the way Proxmox opens on
  // its own Datacenter node rather than on a particular guest.
  const [datacenterOpen, setDatacenterOpen] = useState(true)
  const [selection, setSelection] = useState<Selection>({
    nodeId: "",
    view: "summary",
  })
  const [addOpen, setAddOpen] = useState(false)

  // The tree changes rarely (a VM created in Proxmox, a device added here), so
  // it is polled far more slowly than the metrics inside a view.
  const fetchNodes = useCallback(() => api.nodes(), [])
  const { data: nodes, updatedAt, refresh } = usePoll(fetchNodes, 30000)

  const tree = nodes ?? []
  const selected = tree.find((n) => n.id === selection.nodeId) ?? null

  // A node can disappear (VM destroyed, device deleted) while its view is open.
  useEffect(() => {
    if (!datacenterOpen && selection.nodeId && tree.length > 0 && !selected) {
      setDatacenterOpen(true)
    }
  }, [datacenterOpen, selection.nodeId, tree.length, selected])

  const openNode = (nodeId: string) => {
    setSelection({ nodeId, view: "summary" })
    setDatacenterOpen(false)
  }

  return (
    <>
      <AppShell
        nodes={tree}
        selection={selection}
        onSelect={(sel) => {
          setSelection(sel)
          setDatacenterOpen(false)
        }}
        datacenterOpen={datacenterOpen}
        onDatacenter={() => setDatacenterOpen(true)}
        onAddDevice={() => setAddOpen(true)}
        clock={updatedAt ? `Last update: ${formatClock(updatedAt)}` : "Last update: —"}
      >
        {/* Keyed on the selection so navigating away from a crashed view
            clears the error rather than pinning it to every node after. */}
        <ErrorBoundary
          key={datacenterOpen ? "datacenter" : `${selection.nodeId}:${selection.view}`}
          label={datacenterOpen ? "Datacenter" : selected?.label}
        >
        {datacenterOpen ? (
          <DatacenterView onOpenNode={openNode} />
        ) : !selected ? (
          <Panel>
            <PanelMessage>Pilih node di sebelah kiri.</PanelMessage>
          </Panel>
        ) : selection.view === "services" ? (
          <NodeServicesView key={selected.id} node={selected} />
        ) : selection.view === "tasks" ? (
          <NodeTasksView key={selected.id} node={selected} />
        ) : selection.view === "apps" ? (
          <NodeAppsView key={selected.id} node={selected} />
        ) : selection.view === "shell" ? (
          <NodeShellView key={selected.id} node={selected} />
        ) : selected.kind === "router9" ? (
          <Router9View key={selected.id} node={selected} />
        ) : (
          <NodeSummaryView
            key={selected.id}
            node={selected}
            onNodesChanged={refresh}
            onDeleted={() => {
              setDatacenterOpen(true)
              refresh()
            }}
          />
        )}
        </ErrorBoundary>
      </AppShell>

      <AddDeviceDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        onAdded={refresh}
      />
      <Toaster />
    </>
  )
}
