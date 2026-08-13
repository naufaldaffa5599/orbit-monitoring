import { createRoot } from "react-dom/client"
import { TerminalPage } from "@/pages/terminal"
import "@xterm/xterm/css/xterm.css"
import "@/index.css"

// No <StrictMode> here, unlike the dashboard. Its double-invoked effects would
// open a second websocket per tab in dev, and every one of those creates a real
// tmux session on the target that then has to be cleaned up by hand.
createRoot(document.getElementById("root")!).render(<TerminalPage />)
