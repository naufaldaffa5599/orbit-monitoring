# monitor-api (Go)

Serves the `monitor-api` systemd unit on port 8585. Started life as a port of
the previous `server.py`, keeping the same HTTP + websocket surface, the same
JSON field names and the same error shape, so `../monitor-app` runs against it
unchanged.

The Python implementation it replaced was deleted at cutover. A source-only
archive of it is kept at `../monitor-api-python-backup.tar.gz` (20 KB, no venv)
purely as a reference for the original behaviour — delete it once you are sure
you will not want to diff against it.

## Build

```bash
./build.sh              # this machine
./build.sh arm64        # cross-compile, no toolchain needed
```

Output is a single static binary. No venv, no interpreter, no `pip install` on
the target — `scp monitor-api box:` is the whole deploy.

## Run

```bash
API_PORT=8686 ./monitor-api
```

Paths are resolved from the binary's directory (or `WorkingDirectory` under
systemd):

| what | where | override |
| --- | --- | --- |
| device registry | `./devices.json` | `MONITOR_DIR` |
| env file | `./.env` | — |
| frontend | `../monitor-app` | `FRONTEND_DIR` |
| temperature source | this machine's sensors | `TEMP_DEVICE` |

`MONITOR_DIR` matters during development: `go run` builds into a temp dir, so
without it the binary would look for `devices.json` next to the temp binary.

### Temperature from another machine

This host is a VM and reports no thermal sensors, so the temperature card was
permanently "N/A". `TEMP_DEVICE` names a device from the registry to read
instead — currently the Proxmox host:

```
TEMP_DEVICE=pve-proxmox-root
```

A background goroutine SSHes there every 30s, runs `sensors -j`, and caches the
result; `/api/system` only ever reads that cache, so the endpoint's latency is
unchanged and a dashboard poll never triggers an SSH login. A fresh connection
per poll is deliberate: one handshake every 30s is cheap, and it avoids both a
permanently open root session to the hypervisor and the stale-connection
failure modes that come with keeping one.

Readings older than 90s are dropped rather than served, so an unreachable
source shows "N/A" instead of a number that silently stopped updating. The
response carries `temperature_source` and the card leads with it — otherwise
the temperatures would read as belonging to the machine named in the header,
which they do not.

Labels are rewritten from lm-sensors' chip names (`k10temp-pci-00c3` / `Tctl`
→ `CPU Tctl`). That is load-bearing: the frontend picks its headline reading by
matching `/package|core 0|cpu/i` and otherwise takes whichever key sorts first,
which on this box would be the GPU.

If the machine ever moves to bare metal, clearing `TEMP_DEVICE` restores the
local sensor path with no code change.

## Test

```bash
go test ./...
```

Covers the logic that HTTP tests can't reach cheaply: session-id validation
(which is what makes the value safe to interpolate into a tmux command), the
WOL magic-packet layout, MAC/slug normalization, the history ring buffer, and
the UTF-8-safe scrollback trim.

## Deploying a rebuild

```bash
./build.sh
sudo systemctl restart monitor-api
```

The restart is not optional — systemd keeps running the old binary until the
process is replaced.

`monitor-api` is in this service's own managed-services whitelist, so it can
also be restarted from the dashboard's Services tab. That works, but the
request restarting the service is being served *by* that service, so the
response never arrives and the UI shows a failure even on success. From a
shell it is unambiguous.

The sudoers rule (`sudoers-services.md`) is unchanged from the Python era: it
scopes passwordless sudo to specific `systemctl` verbs and units, not to a
particular caller, so this binary inherited it as-is.

### The cutover itself

Done once, by `cutover.sh`. It installed the unit, verified the API answered on
8585, and only then deleted the Python tree — rolling back automatically if
verification failed. Kept in the repo as the record of what happened; there is
no reason to run it again.

Terminal sessions survived the swap: they live on the target machine as tmux
sessions named `mh-<id>`, not in this process, so this build simply reattached
to the sessions the Python build had created.

## Deliberate differences from the Python version

Everything below is a change I made on purpose. Everything else is intended to
behave identically.

- **`memory.percent` / `used_gb` are computed from `MemAvailable`.** gopsutil's
  built-in `UsedPercent` uses an older `total - free - buffers - cached`
  heuristic and read ~5 points lower than psutil on this host. Deriving from
  `MemAvailable` matches psutil exactly, so historical readings stay
  comparable.

- **Process rows have a deterministic tiebreak.** Go randomizes map iteration,
  and on an idle box most rows sit at exactly 0% CPU, so sorting on CPU alone
  reshuffled the bottom of the list on every poll. Ties now fall back to the
  other metric, then the label. The Python version got stable-looking output
  for free from `process_iter` order.

- **Process status is translated to psutil's vocabulary.** gopsutil says
  `sleep`/`stop`; the frontend's `statusMap` is keyed on `sleeping`/`stopped`
  and silently degrades to raw text otherwise.

- **`devices.json` is written `0600`, atomically.** It stores SSH passwords in
  plaintext, and it is rewritten on every add/delete. The Python version wrote
  it `0644` and non-atomically.

- **Password auth also offers keyboard-interactive.** Some sshd/PAM configs
  advertise only that method, where a plain password auth fails with a
  confusing error.

- **Host keys are still accepted unconditionally**, matching paramiko's
  `AutoAddPolicy`. These are hand-registered LAN boxes with no known-hosts
  store to check against — noted because it is a real limitation carried over,
  not an oversight.

## Footprint

Measured on this host, both serving the same frontend:

| | Python | Go |
| --- | --- | --- |
| RSS at idle | 71 MB | 14 MB |
| deploy artifact | 79 MB venv | 7.6 MB binary |
| runtime deps on target | Python 3, paramiko (builds crypto), psutil | none |

## Layout

| file | what |
| --- | --- |
| `main.go` | routing, CORS, startup |
| `config.go` | paths, env, managed-service whitelist |
| `system.go` | `/api/system`, history ring buffer |
| `process.go` | process grouping, per-PID detail |
| `devices.go` | device registry, CRUD, reachability probes |
| `sshterm.go` | SSH sessions, tmux persistence, `/ws/ssh` |
| `android.go` | ADB screen streaming and input, `/ws/android` |
| `services.go` | systemd status/logs/actions, `/ws/logs` |
| `wol.go` | Wake on LAN, power actions |
| `static.go` | frontend serving |
| `ws.go` | websocket upgrade, per-connection write locking |
