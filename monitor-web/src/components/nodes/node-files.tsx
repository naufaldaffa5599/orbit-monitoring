import { Suspense, lazy, useCallback, useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import {
  ArrowUp,
  ClipboardPaste,
  Copy,
  Download,
  File as FileIcon,
  FilePen,
  Folder,
  FolderPlus,
  Image as ImageIcon,
  Link2,
  ChevronLeft,
  ChevronRight,
  RefreshCw,
  Scissors,
  Trash2,
  Upload,
} from "lucide-react"
import { Panel, PanelBody, PanelHead, PanelMessage, RowSkeleton } from "@/components/panel"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { api, ApiError } from "@/lib/api"
import { formatBytes, formatStamp } from "@/lib/format"
import { cn } from "@/lib/utils"

// Split out of the main bundle: CodeMirror and its language modes are only
// worth downloading once someone actually opens a file to edit.
const CodeEditor = lazy(() =>
  import("@/components/ui/code-editor").then((m) => ({ default: m.CodeEditor })),
)
import type { FileEntry, FileListing, TreeNode } from "@/types"

/**
 * The file manager for one node.
 *
 * Not polled. Every other node page shows something that changes on its own —
 * load, services, processes — and refreshing those under the reader is the
 * point. A directory is different: the rows are things you are about to act
 * on, and having them reorder themselves between deciding and clicking is how
 * the wrong file gets deleted. It reloads when you navigate, when an operation
 * finishes, and when you ask.
 *
 * Copy and cut park a path in `clipboard` and do nothing else; the work
 * happens on Paste, in whichever directory is open then. That is the model
 * every desktop file manager uses, and it is the only one that lets a move
 * cross directories without a second pane to drag between.
 */
export function NodeFilesView({ node }: { node: TreeNode }) {
  // "" means "wherever this login lands" — the server answers with the real
  // path, which is what every later request uses.
  const [path, setPath] = useState("")
  const [listing, setListing] = useState<FileListing | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [clipboard, setClipboard] = useState<{
    entry: FileEntry
    cut: boolean
  } | null>(null)
  const [busy, setBusy] = useState(false)

  const [editing, setEditing] = useState<EditorState | null>(null)
  const [viewing, setViewing] = useState<FileEntry | null>(null)
  const [prompt, setPrompt] = useState<PromptState | null>(null)
  const uploadRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)
  const refocusAfterLoad = useRef(false)

  const load = useCallback(
    async (target: string) => {
      refocusAfterLoad.current = !!listRef.current?.contains(document.activeElement)
      setLoading(true)
      setError(null)
      try {
        const res = await api.files.list(node.id, target)
        setListing(res)
        setPath(res.path)
        setSelected(null)
      } catch (err) {
        setError(err instanceof Error ? err.message : "Gagal baca folder")
        setListing(null)
      } finally {
        setLoading(false)
      }
    },
    [node.id],
  )

  useEffect(() => {
    void load("")
  }, [load])

  const reload = useCallback(() => void load(path), [load, path])

  /** Runs an operation, then reloads — so the list always reflects what
   *  actually happened rather than what was requested. */
  const run = useCallback(
    async (label: string, fn: () => Promise<unknown>) => {
      setBusy(true)
      try {
        await fn()
        toast.success(label)
        await load(path)
        return true
      } catch (err) {
        toast.error(err instanceof Error ? err.message : `${label} gagal`)
        return false
      } finally {
        setBusy(false)
      }
    },
    [load, path],
  )

  const entry = listing?.entries.find((e) => e.path === selected) ?? null

  function open(e: FileEntry) {
    if (e.dir) {
      void load(e.path)
      return
    }
    if (isImage(e)) {
      setViewing(e)
      return
    }
    void openEditor(e)
  }

  /**
   * Puts keyboard focus back on the list.
   *
   * Needed because the rows are the only thing listening for arrow keys, and
   * two ordinary actions destroy whatever was focused: opening a dialog (each
   * one unmounts on close rather than closing, so Radix never runs its own
   * focus restore) and walking into a folder (the focused row itself is
   * replaced). Either way focus lands on <body>, where an arrow key scrolls
   * the page instead of moving the selection.
   *
   * The selected row is preferred; the list itself is the fallback for when
   * a reload cleared the selection, and it is focusable only from script.
   */
  const focusList = useCallback((path?: string | null) => {
    const ul = listRef.current
    if (!ul) return
    const row = path
      ? ul.querySelector<HTMLButtonElement>(`[data-file-row="${CSS.escape(path)}"]`)
      : null
    ;(row ?? ul).focus({ preventScroll: !row })
  }, [])

  // A dialog just closed. Tracked as a transition rather than "is closed", or
  // this would fire on first render and steal focus from the page.
  const dialogOpen = !!editing || !!viewing || !!prompt
  const dialogWasOpen = useRef(false)
  useEffect(() => {
    if (dialogOpen) {
      dialogWasOpen.current = true
      return
    }
    if (!dialogWasOpen.current) return
    dialogWasOpen.current = false
    // Deferred, and that timeout is the whole point. Radix's focus scope
    // restores focus from a setTimeout(0) of its own on unmount, aimed at
    // whatever was focused when the dialog opened. If the listing reloaded
    // meanwhile — saving does exactly that — the row it remembers has been
    // replaced, so it falls back to document.body and would undo this. Its
    // timeout is queued during cleanup, before this effect runs, so ours
    // lands second.
    const t = setTimeout(() => focusList(selected), 0)
    return () => clearTimeout(t)
  }, [dialogOpen, selected, focusList])

  // A folder was opened from the keyboard or a double click. Only restores
  // focus if it was in the list to begin with, so arriving on this tab does
  // not yank focus away from wherever the reader actually is.
  useEffect(() => {
    if (!refocusAfterLoad.current) return
    refocusAfterLoad.current = false
    focusList(selected)
  }, [listing, selected, focusList])

  /**
   * Arrow keys walk the list, Enter opens whatever is on.
   *
   * Handled on the <ul> rather than on each row so there is one place that
   * knows the order, and the event still arrives because the focused row is a
   * button inside it. Focus moves along with the selection — otherwise the
   * next arrow press would carry on from wherever focus was left behind, and
   * the browser would not scroll the row into view.
   */
  function onListKeys(ev: React.KeyboardEvent<HTMLUListElement>) {
    // Up a level, the way Explorer has always read Backspace. Checked before
    // the empty-list guard on purpose: an empty folder is exactly the one you
    // most want to back out of, and it has no rows to move between.
    if (ev.key === "Backspace") {
      if (!listing?.parent || busy) return
      ev.preventDefault() // browsers still map it to history-back in some setups
      void load(listing.parent)
      return
    }

    const items = listing?.entries ?? []
    if (items.length === 0) return
    const at = items.findIndex((x) => x.path === selected)

    if (ev.key === "Enter") {
      if (at < 0) return
      // Without this the button underneath turns Enter into a click, which
      // only re-selects the row that is already selected.
      ev.preventDefault()
      open(items[at])
      return
    }

    let next: number
    if (ev.key === "ArrowDown") next = at < 0 ? 0 : Math.min(at + 1, items.length - 1)
    else if (ev.key === "ArrowUp") next = at < 0 ? items.length - 1 : Math.max(at - 1, 0)
    else return

    ev.preventDefault() // or the panel scrolls under the selection
    setSelected(items[next].path)
    listRef.current
      ?.querySelectorAll<HTMLButtonElement>("[data-file-row]")
      [next]?.focus()
  }

  async function openEditor(e: FileEntry) {
    setBusy(true)
    try {
      const res = await api.files.read(node.id, e.path)
      setEditing({ path: res.path, text: res.content, encoding: res.encoding })
    } catch (err) {
      // 413 and 415 are the expected answers for a log or a binary, and the
      // server's message already says what to do instead.
      toast.error(err instanceof ApiError ? err.message : "Gagal buka file")
    } finally {
      setBusy(false)
    }
  }

  async function save(text: string) {
    if (!editing) return
    const ok = await run("Tersimpan", () =>
      api.files.write(node.id, editing.path, text, editing.encoding),
    )
    if (ok) setEditing(null)
  }

  function paste() {
    if (!clipboard) return
    const dest = joinPath(path, clipboard.entry.name)
    const verb = clipboard.cut ? "Dipindah" : "Disalin"
    void run(`${clipboard.entry.name} — ${verb.toLowerCase()}`, async () => {
      if (clipboard.cut) await api.files.move(node.id, clipboard.entry.path, dest)
      else await api.files.copy(node.id, clipboard.entry.path, dest)
      setClipboard(null)
    })
  }

  async function upload(files: FileList | null) {
    if (!files || files.length === 0) return
    await run(`${files.length} file terkirim`, () =>
      api.files.upload(node.id, path, Array.from(files)),
    )
  }

  return (
    <>
      <Panel>
        <PanelHead title="Files">
          <Breadcrumb path={path} onGo={(p) => void load(p)} />
        </PanelHead>

        <div className="flex flex-wrap items-center gap-1.5 border-b px-3 py-2">
          <Button
            size="sm"
            variant="ghost"
            disabled={!listing?.parent || busy}
            onClick={() => listing?.parent && void load(listing.parent)}
            title="Naik satu folder"
          >
            <ArrowUp className="size-3.5" />
          </Button>
          <Button size="sm" variant="ghost" onClick={reload} disabled={busy} title="Muat ulang">
            <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />
          </Button>

          <span className="mx-1 h-4 w-px bg-border" />

          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() =>
              setPrompt({
                kind: "mkdir",
                title: "Folder baru",
                label: "Nama folder",
                value: "",
              })
            }
          >
            <FolderPlus className="size-3.5" /> Folder
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => uploadRef.current?.click()}
          >
            <Upload className="size-3.5" /> Upload
          </Button>
          <input
            ref={uploadRef}
            type="file"
            multiple
            hidden
            onChange={(ev) => {
              void upload(ev.target.files)
              ev.target.value = "" // so the same file can be picked twice
            }}
          />

          <span className="mx-1 h-4 w-px bg-border" />

          <Button
            size="sm"
            variant="ghost"
            disabled={!entry || busy}
            onClick={() => entry && setClipboard({ entry, cut: false })}
          >
            <Copy className="size-3.5" /> Copy
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={!entry || busy}
            onClick={() => entry && setClipboard({ entry, cut: true })}
          >
            <Scissors className="size-3.5" /> Cut
          </Button>
          {clipboard ? (
            <Button size="sm" variant="outline" disabled={busy} onClick={paste}>
              <ClipboardPaste className="size-3.5" />
              Paste {clipboard.entry.name}
              {clipboard.cut ? " (pindah)" : ""}
            </Button>
          ) : null}

          <span className="ml-auto flex items-center gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              disabled={!entry || entry.dir || busy}
              onClick={() => entry && open(entry)}
            >
              {entry && isImage(entry) ? (
                <>
                  <ImageIcon className="size-3.5" /> Lihat
                </>
              ) : (
                <>
                  <FilePen className="size-3.5" /> Edit
                </>
              )}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={!entry || entry.dir || busy}
              asChild={!!entry && !entry.dir}
            >
              {entry && !entry.dir ? (
                <a href={api.files.downloadUrl(node.id, entry.path)} download={entry.name}>
                  <Download className="size-3.5" /> Download
                </a>
              ) : (
                <span>
                  <Download className="size-3.5" /> Download
                </span>
              )}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={!entry || busy}
              onClick={() =>
                entry &&
                setPrompt({
                  kind: "rename",
                  title: "Ganti nama",
                  label: "Nama baru",
                  value: entry.name,
                  entry,
                })
              }
            >
              Rename
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="text-destructive hover:text-destructive"
              disabled={!entry || busy}
              onClick={() =>
                entry &&
                setPrompt({
                  kind: "delete",
                  title: entry.dir ? "Hapus folder" : "Hapus file",
                  label: entry.dir ? `Ketik "${entry.name}" buat konfirmasi` : "",
                  value: "",
                  entry,
                })
              }
            >
              <Trash2 className="size-3.5" /> Hapus
            </Button>
          </span>
        </div>

        <PanelBody className="p-0">
          {loading && !listing ? (
            <RowSkeleton />
          ) : error ? (
            <PanelMessage>{error}</PanelMessage>
          ) : !listing || listing.entries.length === 0 ? (
            <PanelMessage>Folder kosong.</PanelMessage>
          ) : (
            <ul
              ref={listRef}
              tabIndex={-1}
              onKeyDown={onListKeys}
              className="divide-y outline-none"
            >
              {listing.entries.map((e) => (
                <li key={e.path}>
                  <button
                    type="button"
                    data-file-row={e.path}
                    onClick={() => setSelected(e.path)}
                    onDoubleClick={() => open(e)}
                    className={cn(
                      "flex w-full items-center gap-3 px-3.5 py-2 text-left transition-colors",
                      selected === e.path ? "bg-primary/15" : "hover:bg-white/4",
                      clipboard?.cut && clipboard.entry.path === e.path && "opacity-50",
                    )}
                  >
                    {e.dir ? (
                      <Folder className="size-4 shrink-0 text-primary" />
                    ) : isImage(e) ? (
                      <ImageIcon className="size-4 shrink-0 text-muted-foreground" />
                    ) : e.symlink ? (
                      <Link2 className="size-4 shrink-0 text-muted-foreground" />
                    ) : (
                      <FileIcon className="size-4 shrink-0 text-muted-foreground" />
                    )}
                    <span className="min-w-0 flex-1 truncate text-sm">
                      {e.name}
                      {e.symlink && e.target ? (
                        <span className="text-muted-foreground"> → {e.target}</span>
                      ) : null}
                    </span>
                    <span className="hidden shrink-0 font-mono text-[0.7rem] text-muted-foreground sm:inline">
                      {e.mode}
                    </span>
                    <span className="w-20 shrink-0 text-right text-xs text-muted-foreground">
                      {e.dir ? "—" : formatBytes(e.size)}
                    </span>
                    <span className="hidden w-32 shrink-0 text-right text-xs text-muted-foreground md:inline">
                      {formatStamp(new Date(e.mtime * 1000).toISOString())}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </PanelBody>
      </Panel>

      <ImageViewer
        nodeId={node.id}
        entry={viewing}
        siblings={(listing?.entries ?? []).filter(isImage)}
        onGo={setViewing}
        onClose={() => setViewing(null)}
      />
      <EditorDialog
        state={editing}
        busy={busy}
        onClose={() => setEditing(null)}
        onSave={save}
      />
      <PromptDialog
        state={prompt}
        busy={busy}
        onClose={() => setPrompt(null)}
        onSubmit={async (value) => {
          if (!prompt) return
          if (prompt.kind === "mkdir") {
            const ok = await run(`${value} dibuat`, () =>
              api.files.mkdir(node.id, joinPath(path, value)),
            )
            if (ok) setPrompt(null)
          } else if (prompt.kind === "rename" && prompt.entry) {
            const ok = await run(`Jadi ${value}`, () =>
              api.files.move(node.id, prompt.entry!.path, joinPath(path, value)),
            )
            if (ok) setPrompt(null)
          } else if (prompt.kind === "delete" && prompt.entry) {
            const ok = await run(`${prompt.entry.name} dihapus`, () =>
              api.files.remove(node.id, prompt.entry!.path, prompt.entry!.dir),
            )
            if (ok) setPrompt(null)
          }
        }}
      />
    </>
  )
}

/** Each segment is a jump target, so getting out of /etc/systemd/system/foo
 *  does not mean clicking Up four times. */
function Breadcrumb({ path, onGo }: { path: string; onGo: (p: string) => void }) {
  const parts = path.split("/").filter(Boolean)
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-0.5 font-mono text-xs">
      <button
        type="button"
        onClick={() => onGo("/")}
        className="rounded px-1 py-0.5 hover:bg-white/10"
      >
        /
      </button>
      {parts.map((part, i) => (
        <span key={i} className="flex items-center gap-0.5">
          <button
            type="button"
            onClick={() => onGo("/" + parts.slice(0, i + 1).join("/"))}
            className={cn(
              "max-w-[12rem] truncate rounded px-1 py-0.5 hover:bg-white/10",
              i === parts.length - 1 && "text-foreground",
            )}
          >
            {part}
          </button>
          {i < parts.length - 1 ? <span className="text-muted-foreground">/</span> : null}
        </span>
      ))}
    </div>
  )
}

/** Which names the viewer will draw. Kept in step with imageType() in
 *  files.go — the server decides what it will serve inline, and offering a
 *  preview it would refuse is worse than not offering one. */
const IMAGE_EXT = /\.(jpe?g|png|gif|webp|avif|bmp|ico|svg)$/i

function isImage(e: FileEntry) {
  return !e.dir && IMAGE_EXT.test(e.name)
}

function joinPath(dir: string, name: string) {
  return dir.endsWith("/") ? dir + name : dir + "/" + name
}

interface PromptState {
  kind: "mkdir" | "rename" | "delete"
  title: string
  label: string
  value: string
  entry?: FileEntry
}

function PromptDialog({
  state,
  busy,
  onClose,
  onSubmit,
}: {
  state: PromptState | null
  busy: boolean
  onClose: () => void
  onSubmit: (value: string) => void | Promise<void>
}) {
  const [value, setValue] = useState("")
  useEffect(() => setValue(state?.value ?? ""), [state])
  if (!state) return null

  // Deleting a directory takes the whole tree with it, so that one asks for
  // the name to be typed out. A single file is one undo-less click either
  // way, and a confirm button is enough for it.
  const needsTyping = state.kind === "delete" && !!state.entry?.dir
  const ready = needsTyping ? value === state.entry?.name : state.kind === "delete" || value.trim() !== ""

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{state.title}</DialogTitle>
        </DialogHeader>
        {state.kind === "delete" ? (
          <p className="text-sm text-muted-foreground">
            <span className="font-mono text-foreground">{state.entry?.name}</span>
            {state.entry?.dir
              ? " dan seluruh isinya akan dihapus permanen."
              : " akan dihapus permanen."}
          </p>
        ) : null}
        {state.label ? (
          <div className="space-y-1.5">
            <Label htmlFor="file-prompt">{state.label}</Label>
            <Input
              id="file-prompt"
              autoFocus
              value={value}
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && ready && !busy) void onSubmit(value.trim())
              }}
            />
          </div>
        ) : null}
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Batal
          </Button>
          <Button
            variant={state.kind === "delete" ? "destructive" : "default"}
            disabled={!ready || busy}
            onClick={() => void onSubmit(value.trim())}
          >
            {state.kind === "delete" ? "Hapus" : "Simpan"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

interface EditorState {
  path: string
  text: string
  /** Passed straight back on save; the editor never converts anything. */
  encoding: string
}

function EditorDialog({
  state,
  busy,
  onClose,
  onSave,
}: {
  state: EditorState | null
  busy: boolean
  onClose: () => void
  onSave: (text: string) => void | Promise<void>
}) {
  const [text, setText] = useState("")
  useEffect(() => setText(state?.text ?? ""), [state])
  if (!state) return null
  const dirty = text !== state.text

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      {/* Full screen rather than a centred box. Editing a config file is the
          one thing here that wants every pixel, and the dialog's own layout —
          centred, capped at sm:max-w-sm, rounded — has to be undone class by
          class, since tailwind-merge only overrides a variant with the same
          variant. h-dvh rather than h-screen so the footer is not left under
          a phone's browser chrome. */}
      <DialogContent className="top-0 left-0 flex h-dvh w-screen max-w-none translate-x-0 translate-y-0 flex-col rounded-none sm:max-w-none">
        <DialogHeader>
          <DialogTitle className="truncate font-mono text-sm">{state.path}</DialogTitle>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-hidden rounded-md border bg-black/30 font-mono">
          <Suspense
            fallback={
              <p className="p-3 text-xs text-muted-foreground">Menyiapkan editor…</p>
            }
          >
            <CodeEditor
              value={text}
              filename={state.path.split("/").pop() ?? state.path}
              onChange={setText}
              className="h-full"
            />
          </Suspense>
        </div>
        <DialogFooter>
          <span className="mr-auto text-xs text-muted-foreground">
            {dirty ? "Belum disimpan" : "Tersimpan"}
            {state.encoding && state.encoding !== "utf-8" ? (
              <span className="ml-2 font-mono uppercase">{state.encoding}</span>
            ) : null}
          </span>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Tutup
          </Button>
          <Button disabled={!dirty || busy} onClick={() => void onSave(text)}>
            Simpan
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The picture viewer.
 *
 * It steps through the images in the folder it was opened from rather than
 * showing one and closing, because that is what a folder of photos is for —
 * having to close and reopen the dialog for each one turns twenty pictures
 * into forty clicks.
 *
 * The bytes come straight from an <img src>, so the browser streams and
 * decodes them the way it does any other image; nothing passes through JS,
 * and a large photo costs this component nothing but the wait.
 */
function ImageViewer({
  nodeId,
  entry,
  siblings,
  onGo,
  onClose,
}: {
  nodeId: string
  entry: FileEntry | null
  siblings: FileEntry[]
  onGo: (e: FileEntry) => void
  onClose: () => void
}) {
  const [broken, setBroken] = useState(false)
  const [loaded, setLoaded] = useState(false)

  const index = entry ? siblings.findIndex((s) => s.path === entry.path) : -1
  const step = useCallback(
    (delta: number) => {
      if (index < 0 || siblings.length < 2) return
      // Wraps, so the last picture leads back to the first instead of
      // dead-ending on a disabled button.
      const next = (index + delta + siblings.length) % siblings.length
      onGo(siblings[next])
    },
    [index, siblings, onGo],
  )

  useEffect(() => {
    setBroken(false)
    setLoaded(false)
  }, [entry?.path])

  useEffect(() => {
    if (!entry) return
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === "ArrowLeft") step(-1)
      if (ev.key === "ArrowRight") step(1)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [entry, step])

  if (!entry) return null

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-h-[90vh] w-[min(94vw,80rem)] max-w-none flex-col">
        <DialogHeader>
          <DialogTitle className="truncate font-mono text-sm">
            {entry.name}
            <span className="ml-2 font-sans text-xs font-normal text-muted-foreground">
              {formatBytes(entry.size)}
              {siblings.length > 1 ? ` · ${index + 1}/${siblings.length}` : ""}
            </span>
          </DialogTitle>
        </DialogHeader>

        <div className="relative flex min-h-[40vh] flex-1 items-center justify-center overflow-hidden rounded-md bg-black/40">
          {broken ? (
            <p className="p-6 text-sm text-muted-foreground">
              Gagal menampilkan gambar ini. Coba Download buat ambil filenya.
            </p>
          ) : (
            <>
              {!loaded ? (
                <span className="absolute text-xs text-muted-foreground">Memuat…</span>
              ) : null}
              <img
                src={api.files.previewUrl(nodeId, entry.path)}
                alt={entry.name}
                onLoad={() => setLoaded(true)}
                onError={() => setBroken(true)}
                className={cn(
                  "max-h-[70vh] max-w-full object-contain transition-opacity",
                  loaded ? "opacity-100" : "opacity-0",
                )}
              />
            </>
          )}

          {siblings.length > 1 ? (
            <>
              <button
                type="button"
                onClick={() => step(-1)}
                aria-label="Gambar sebelumnya"
                className="absolute left-2 rounded-full bg-black/50 p-2 text-white/80 hover:bg-black/70 hover:text-white"
              >
                <ChevronLeft className="size-5" />
              </button>
              <button
                type="button"
                onClick={() => step(1)}
                aria-label="Gambar berikutnya"
                className="absolute right-2 rounded-full bg-black/50 p-2 text-white/80 hover:bg-black/70 hover:text-white"
              >
                <ChevronRight className="size-5" />
              </button>
            </>
          ) : null}
        </div>

        <DialogFooter>
          <span className="mr-auto truncate font-mono text-[0.7rem] text-muted-foreground">
            {entry.path}
          </span>
          <Button variant="ghost" asChild>
            <a href={api.files.downloadUrl(nodeId, entry.path)} download={entry.name}>
              <Download className="size-3.5" /> Download
            </a>
          </Button>
          <Button variant="ghost" onClick={onClose}>
            Tutup
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
