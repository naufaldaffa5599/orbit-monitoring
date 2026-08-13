import { useCallback, useEffect, useState } from "react"
import { AppShell, Crumbs } from "@/components/shell/app-shell"
import {
  NodeTabs,
  VIEW_META,
  viewsFor,
  type NodeView,
  type Selection,
} from "@/components/shell/node-views"
import { DatacenterView, GroupView } from "@/components/nodes/datacenter"
import { NodeActions } from "@/components/nodes/node-actions"
import { NodeSummaryView } from "@/components/nodes/node-summary"
import { NodeServicesView } from "@/components/nodes/node-services"
import { NodeTasksView } from "@/components/nodes/node-tasks"
import { NodeFilesView } from "@/components/nodes/node-files"
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
import { ROOT_LABEL } from "@/lib/labels"

export function Dashboard({ onLogout }: { onLogout?: () => void }) {
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

  // The tab stays put when you move between machines — comparing the same page
  // across two of them is most of why you would — but the view is re-derived
  // rather than trusted, since not every node offers every page.
  const views = selected ? viewsFor(selected) : []
  const view: NodeView = views.includes(selection.view)
    ? selection.view
    : (views[0] ?? "summary")

  const openNode = (nodeId: string) => {
    setSelection((prev) => ({
      nodeId,
      // Shell is a live SSH connection, not a page: carrying it over would
      // open a session on every machine you happened to click through.
      view: prev.view === "shell" ? "summary" : prev.view,
    }))
    setDatacenterOpen(false)
  }

  return (
    <>
      <AppShell
        nodes={tree}
        selectedId={selection.nodeId}
        onSelect={openNode}
        datacenterOpen={datacenterOpen}
        onDatacenter={() => setDatacenterOpen(true)}
        onAddDevice={() => setAddOpen(true)}
        onLogout={onLogout}
        clock={updatedAt ? `Last update: ${formatClock(updatedAt)}` : "Last update: —"}
      >
        {!datacenterOpen && selected && (
          <>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <Crumbs
                trail={
                  // A group has one page, so naming it in the trail would only
                  // repeat the heading back at you.
                  selected.kind === "group"
                    ? [ROOT_LABEL, selected.label]
                    : [ROOT_LABEL, selected.label, VIEW_META[view].label]
                }
              />
              <NodeActions
                node={selected}
                onChanged={refresh}
                onDeleted={() => {
                  setDatacenterOpen(true)
                  refresh()
                }}
              />
            </div>
            <NodeTabs
              views={views}
              active={view}
              onSelect={(v) => setSelection((prev) => ({ ...prev, view: v }))}
            />
          </>
        )}

        {/* Keyed on the selection so navigating away from a crashed view
            clears the error rather than pinning it to every node after. */}
        <ErrorBoundary
          key={datacenterOpen ? "datacenter" : `${selection.nodeId}:${view}`}
          label={datacenterOpen ? ROOT_LABEL : selected?.label}
        >
        {datacenterOpen ? (
          <DatacenterView onOpenNode={openNode} />
        ) : !selected ? (
          <Panel>
            <PanelMessage>Pilih node di sebelah kiri.</PanelMessage>
          </Panel>
        ) : selected.kind === "group" ? (
          <GroupView key={selected.id} node={selected} onOpenNode={openNode} />
        ) : view === "services" ? (
          <NodeServicesView key={selected.id} node={selected} />
        ) : view === "tasks" ? (
          <NodeTasksView key={selected.id} node={selected} />
        ) : view === "apps" ? (
          <NodeAppsView key={selected.id} node={selected} />
        ) : view === "files" ? (
          <NodeFilesView key={selected.id} node={selected} />
        ) : view === "shell" ? (
          <NodeShellView key={selected.id} node={selected} />
        ) : selected.kind === "router9" ? (
          <Router9View key={selected.id} />
        ) : (
          <NodeSummaryView key={selected.id} node={selected} />
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
