# monitor-web

React frontend for **Orbit**. This is what the Go API serves; `monitor-app/`
(vanilla HTML + `app.js` + `style.css`) is the previous frontend and is no
longer reachable — see "Leftovers" below.

**Stack:** Vite 8 · React 19 · TypeScript (strict) · Tailwind v4 · shadcn/ui
(radix-nova) · xterm.js. Builds to plain static files — the Go API serves them,
there is no Node runtime in production.

> The directory is still called `monitor-web` and the service is still
> `monitor-api.service`. Only the product name changed; renaming the unit would
> break the sudoers rule that matches it, and with it the dashboard's own
> Services buttons. See the note at the top of `deploy/monitor-api.service`.

## Develop

```bash
npm run dev        # http://localhost:5173, proxies /api and /ws to :8585
```

The Go API must be running (`systemctl status monitor-api`). Every request is
same-origin in production, so the proxy exists only for dev and `preview`.

```bash
npm run build      # tsc -b && vite build  →  dist/
npm run preview    # serves dist/ with the same API proxy
npm run lint
```

**Adding an endpoint means deploying both halves.** The service serves whatever
sits in `dist/`, so a UI-only change needs no restart — but a frontend that
calls a route the running binary does not have will break production the moment
`npm run build` finishes. Build and deploy the Go binary in the same step.

## Two entry points, no router

`index.html` (dashboard) and `terminal.html` (SSH terminal) are separate Vite
inputs. The terminal is opened with `window.open("terminal.html?id=…")`, so
keeping it a real file means:

- the URL is unchanged from the old static page — bookmarks and home-screen
  shortcuts still work;
- the Go server needs no SPA-fallback route;
- xterm.js (~345 kB) only loads on the page that uses it.

## Layout

```
src/
├── components/
│   ├── ui/            shadcn components + ethereal-whispers.tsx (shader backdrop)
│   ├── shell/         topbar, Orbit mark, resource tree, mobile drawer
│   ├── nodes/         every per-node view: summary, services, tasks, apps,
│   │                  actions, the datacenter overview, the 9router usage page
│   ├── services/      live log dialog
│   ├── devices/       add-device dialog
│   └── panel.tsx      the Proxmox-style panel primitive
├── lib/
│   ├── api.ts         typed fetch wrappers for the Go API
│   ├── terminal-session.ts   xterm + websocket + touch handling (imperative)
│   └── format.ts      bytes/uptime/percent formatting
├── hooks/use-poll.ts  the polling loop (pauses while the tab is hidden)
└── types.ts           response shapes, mirrored from the Go handlers
```

`terminal-session.ts` is a plain class on purpose. An xterm instance owns a
canvas and a scrollback buffer that cannot be rebuilt without losing the
session, and its touch listeners must sit in the capture phase on a stable DOM
node to beat xterm's own handlers. React owns the chrome around it and nothing
inside it.

## Resource tree

The dashboard is organised the way Proxmox organises its own: a Datacenter
overview, the hypervisor under it, and every machine hanging off that.

```
Datacenter                 ← fleet overview, the landing screen
└── pve (proxmox root)     ← the Proxmox host
    ├── NAS      #100      ← discovered from `qm list`, linked to a device
    ├── PROJECT  #200      ← this VM; metrics come from gopsutil, not SSH
    ├── Laptop Server      ← added by hand
    └── PC Kamar           ← Windows: adds a Task Manager view
9router                    ← API usage, not a machine
```

Each node offers **Summary**, **Services**, **Apps**, **Shell**, and — on
Windows — **Task Manager**, depending on capability flags the backend works out
per node. Guests appear whether or not credentials exist for them; without
credentials a guest still shows the CPU and memory the hypervisor reports.

Indentation comes from DOM nesting alone — there is no `depth * n` arithmetic.
An earlier version multiplied depth *and* nested the containers, so the indent
was counted twice for child nodes and once for views, and nothing lined up.

Nodes can be renamed and deleted from the Summary view. Renames are display-only
and live in `node-overrides.json`, keyed by node id, because a guest discovered
from the hypervisor may have no device entry to store a name on.

Metrics are collected agentlessly over SSH (`collectlinux.go`,
`collectwindows.go`) and cached per node, so several open dashboards produce one
command per cycle rather than one per viewer. Nothing is installed on the
targets.

Controlling services on a remote node needs a sudoers rule there — see
`monitor-api-go/sudoers-remote-nodes.md`. Everything else works with plain SSH
credentials.

## Apps vs Services

Two views that answer different questions, and the difference is the point:

- **Services** reads systemd (or `Win32_Service`) — *is the process alive.*
- **Apps** performs an HTTP request — *does the app actually respond.*

A unit sits in `active` perfectly happily while its port refuses connections or
it returns 502. Checks run **from the machine hosting Orbit**, not from inside
the node they are tagged with: that measures what a client sees and needs no
curl or agent on the target. The node tag says which machine to go and look at
when it breaks, which is why a `127.0.0.1` URL only works for apps on the
dashboard host.

The add-check dialog asks the node what it is listening on and offers each port
as a chip, so a URL is one click rather than something to type out.

## Branding

The mark is two shapes — a filled centre and a tilted ring — described once in
`src/components/shell/orbit-geometry.json`. Everything else is derived from it:

| Consumer | How |
|---|---|
| Topbar | `orbit-mark.tsx` imports the JSON, renders with `currentColor` |
| Favicon | `public/orbit.svg` — generated |
| PWA icons | `public/icons/icon-{192,512}.png` — generated |

```bash
npm run icons      # regenerates the SVG and both PNGs
```

Generated files are committed, so a fresh clone builds without running it. Run
it after editing the geometry.

The geometry is tuned for the smallest size, not the largest: the ring's inner
edge (`ry` − half the stroke) has to clear the dot's radius or the two merge
into an orange blob at 16px. That is not left to memory — the script asserts a
minimum gap and refuses to write anything when the numbers fail it:

```
✗ Mark tidak akan kebaca di ukuran kecil.
  Tepi dalam cincin (ry 4.5 − stroke/2 1.2 = 3.30)
  cuma berjarak -0.10 unit dari titik (r 3.4); minimal 1.2.
```

Bump `CACHE_NAME` in `public/sw.js` and the `?v=` in `src/lib/register-sw.ts`
together whenever the shell changes — otherwise some clients keep serving the
old one from cache.

## Deployment

`FRONTEND_DIR` in `monitor-api-go/.env` points the Go server at `dist/`.

```bash
cd monitor-web    && npm run build
cd monitor-api-go && ./build.sh
sudo -n /usr/bin/systemctl restart monitor-api.service   # exact form required
```

The sudoers rule matches that command as a literal string — no absolute path or
no `.service` suffix means a password prompt and a failed deploy.

## Leftovers

- `monitor-app/` — the old vanilla frontend. Nothing serves it; safe to delete.
- `GET /android.html` in `main.go` — a dead route pointing at a page that no
  longer exists.
- `android.go`, `/ws/android/{id}`, `adbPort()`, `adbStartServer()` — roughly
  350 lines of ADB screen-mirroring backend with no caller since the Android
  page was dropped. Removing it touches protocol validation in `devices.go` and
  one test.
- History for the CPU/RAM chart is in-memory and host-only, so it resets on
  every restart and remote nodes have no chart at all. Check history is the
  same: the last ~30 minutes, in memory.
