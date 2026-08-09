import { Component, type ErrorInfo, type ReactNode } from "react"
import { Panel, PanelBody, PanelHead } from "@/components/panel"
import { Button } from "@/components/ui/button"

/**
 * Catches a render crash in one view instead of losing the whole app.
 *
 * React unmounts the entire root when a render throws with nothing to catch
 * it, which turns a null field in one API response into a blank white page —
 * no nav, no way back, no clue what happened. That is exactly how a nil JSON
 * slice from the 9router endpoint used to take the dashboard down.
 *
 * Still a class: hooks have no equivalent of componentDidCatch, and React 19
 * has not changed that.
 */
export class ErrorBoundary extends Component<
  { children: ReactNode; label?: string },
  { error: Error | null }
> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The console is the only log this app has; keep the component stack, it
    // is the part that says which view died.
    console.error("View crashed:", error, info.componentStack)
  }

  render() {
    const { error } = this.state
    if (!error) return this.props.children

    return (
      <Panel>
        <PanelHead title={this.props.label ?? "View error"}>
          <Button
            variant="outline"
            size="sm"
            onClick={() => this.setState({ error: null })}
          >
            Coba lagi
          </Button>
        </PanelHead>
        <PanelBody>
          <p className="text-sm text-muted-foreground">
            View ini gagal dirender. Bagian lain dashboard masih jalan — pilih
            node lain di sidebar, atau reload.
          </p>
          <pre className="mt-3 overflow-x-auto rounded-md border bg-sunken p-3 text-xs text-destructive">
            {error.message}
          </pre>
        </PanelBody>
      </Panel>
    )
  }
}
