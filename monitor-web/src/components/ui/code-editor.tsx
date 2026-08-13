import { useEffect, useRef } from "react"
import { Compartment, EditorState, type Extension } from "@codemirror/state"
import { EditorView, keymap, lineNumbers, highlightActiveLine } from "@codemirror/view"
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands"
import {
  StreamLanguage,
  bracketMatching,
  foldGutter,
  indentOnInput,
  syntaxHighlighting,
  defaultHighlightStyle,
} from "@codemirror/language"
import { oneDark } from "@codemirror/theme-one-dark"

/**
 * The text editor behind the Files tab.
 *
 * Kept out of the main bundle: every language mode CodeMirror can load is
 * dead weight for the ninety-nine percent of visits that never open a file, so
 * the whole editor is imported lazily by whoever renders it, and each language
 * arrives on its own when a file that needs it is opened. What ships up front
 * is the plain-text editor; a .ts file pulls the JavaScript mode down after it
 * is already on screen.
 *
 * CodeMirror rather than a highlighter like Shiki because this field is
 * editable. A highlighter renders code and stops there, which would have meant
 * running one behind a textarea and keeping the two in sync — two components
 * to disagree about where the cursor is.
 */

/** Modes bundled at load: none. Each is fetched the first time it is needed. */
async function languageFor(filename: string): Promise<Extension | null> {
  const ext = filename.toLowerCase().split(".").pop() ?? ""
  const base = filename.toLowerCase()

  // Dockerfile carries its type in the whole name rather than an extension,
  // and it is common enough here to be worth the one special case.
  if (base === "dockerfile" || base.endsWith(".dockerfile")) {
    const { dockerFile } = await import("@codemirror/legacy-modes/mode/dockerfile")
    return StreamLanguage.define(dockerFile)
  }

  switch (ext) {
    case "ts":
    case "tsx":
    case "js":
    case "jsx":
    case "mjs":
    case "cjs": {
      const { javascript } = await import("@codemirror/lang-javascript")
      return javascript({ typescript: ext.startsWith("ts"), jsx: ext.endsWith("x") })
    }
    case "json":
    case "jsonc": {
      const { json } = await import("@codemirror/lang-json")
      return json()
    }
    case "yaml":
    case "yml": {
      const { yaml } = await import("@codemirror/lang-yaml")
      return yaml()
    }
    case "py": {
      const { python } = await import("@codemirror/lang-python")
      return python()
    }
    case "html":
    case "htm": {
      const { html } = await import("@codemirror/lang-html")
      return html()
    }
    case "css":
    case "scss": {
      const { css } = await import("@codemirror/lang-css")
      return css()
    }
    case "md":
    case "markdown": {
      const { markdown } = await import("@codemirror/lang-markdown")
      return markdown()
    }
    case "xml":
    case "svg": {
      const { xml } = await import("@codemirror/lang-xml")
      return xml()
    }
    case "sh":
    case "bash":
    case "zsh":
    case "profile":
    case "bashrc": {
      const { shell } = await import("@codemirror/legacy-modes/mode/shell")
      return StreamLanguage.define(shell)
    }
    // .ini covers desktop.ini and the .env files this app itself reads; the
    // properties mode handles both shapes.
    case "ini":
    case "conf":
    case "cfg":
    case "env":
    case "toml": {
      const { properties } = await import("@codemirror/legacy-modes/mode/properties")
      return StreamLanguage.define(properties)
    }
    case "go": {
      const { go } = await import("@codemirror/legacy-modes/mode/go")
      return StreamLanguage.define(go)
    }
    case "sql": {
      const { standardSQL } = await import("@codemirror/legacy-modes/mode/sql")
      return StreamLanguage.define(standardSQL)
    }
    case "ps1": {
      const { powerShell } = await import("@codemirror/legacy-modes/mode/powershell")
      return StreamLanguage.define(powerShell)
    }
    case "nginx": {
      const { nginx } = await import("@codemirror/legacy-modes/mode/nginx")
      return StreamLanguage.define(nginx)
    }
    default:
      return null // plain text, which is a perfectly good answer
  }
}

export function CodeEditor({
  value,
  filename,
  onChange,
  className,
}: {
  value: string
  /** Only the name matters — the mode is chosen from its extension. */
  filename: string
  onChange: (next: string) => void
  className?: string
}) {
  const host = useRef<HTMLDivElement>(null)
  // The language slot, empty until the mode for this file has been fetched.
  const langSlot = useRef(new Compartment())
  const view = useRef<EditorView | null>(null)
  // Held in a ref so the change listener never goes stale, which would
  // otherwise mean rebuilding the editor on every keystroke.
  const emit = useRef(onChange)
  emit.current = onChange

  useEffect(() => {
    if (!host.current) return
    let dead = false

    const state = EditorState.create({
      doc: value,
      extensions: [
        lineNumbers(),
        foldGutter(),
        history(),
        indentOnInput(),
        bracketMatching(),
        highlightActiveLine(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        // indentWithTab last so Tab indents rather than leaving the field —
        // the whole point here is editing config and scripts. It does take
        // away the keyboard's usual way out of a field, which is survivable
        // only because this lives in a dialog that Escape still closes.
        keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
        EditorView.lineWrapping,
        oneDark,
        langSlot.current.of([]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) emit.current(u.state.doc.toString())
        }),
        EditorView.theme({
          "&": { height: "100%", fontSize: "0.78rem" },
          ".cm-scroller": { fontFamily: "inherit", lineHeight: "1.6" },
          "&.cm-focused": { outline: "none" },
        }),
      ],
    })

    const v = new EditorView({ state, parent: host.current })
    view.current = v

    // The mode lands after the first paint, so a big file is readable and
    // editable immediately and simply gains colour a moment later.
    void languageFor(filename).then((lang) => {
      if (dead || !lang) return
      v.dispatch({ effects: langSlot.current.reconfigure(lang) })
    })

    return () => {
      dead = true
      v.destroy()
      view.current = null
    }
    // Rebuilt only when a different file is opened. `value` is deliberately
    // not a dependency: including it would tear the editor down on every
    // keystroke, taking the cursor and the undo history with it.
  }, [filename]) // eslint-disable-line react-hooks/exhaustive-deps

  // An outside change to `value` — a save that rewrote it, say — is pushed in
  // without disturbing the cursor when the text already matches.
  useEffect(() => {
    const v = view.current
    if (!v) return
    const current = v.state.doc.toString()
    if (current === value) return
    v.dispatch({ changes: { from: 0, to: current.length, insert: value } })
  }, [value])

  return <div ref={host} className={className} />
}
