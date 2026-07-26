"""
Monitor API — FastAPI backend for system metrics + web SSH terminal.
"""

import asyncio
import os
import json
import re
import shutil
import socket
import threading
import time
from collections import deque
from datetime import datetime, timezone, timedelta
from pathlib import Path

import paramiko
import psutil
from dotenv import load_dotenv
from fastapi import FastAPI, HTTPException, WebSocket, WebSocketDisconnect
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, Response
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

# ── Config ──────────────────────────────────────────────────────────────────
load_dotenv(Path(__file__).parent / ".env")

API_PORT = int(os.getenv("API_PORT", "8585"))

WIB = timezone(timedelta(hours=7))

# ── Device registry (SSH-reachable devices) ─────────────────────────────────
DEVICES_FILE = Path(__file__).parent / "devices.json"


def _load_devices() -> dict:
    if not DEVICES_FILE.exists():
        return {}
    try:
        with open(DEVICES_FILE) as f:
            device_list = json.load(f)
        return {d["id"]: d for d in device_list}
    except Exception as e:
        print(f"⚠️  Failed to load devices.json: {e}")
        return {}


def _save_devices():
    with open(DEVICES_FILE, "w") as f:
        json.dump(list(DEVICES.values()), f, indent=2, ensure_ascii=False)


def _slugify(label: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", label.lower()).strip("-")
    return slug or "device"


def _normalize_mac(mac: str) -> str | None:
    """Accepts "AA:BB:CC:DD:EE:FF", "AA-BB-CC-DD-EE-FF" or bare hex, returns
    12 lowercase hex chars, or None if it's not a valid MAC address."""
    hex_only = re.sub(r"[^0-9a-fA-F]", "", mac)
    if len(hex_only) != 12:
        return None
    return hex_only.lower()


DEVICES = _load_devices()

# ── Managed systemd services (this host only) ───────────────────────────────
# Whitelist only — the unit name is never taken from the request, so this is
# the sole gate on which systemd units are visible/controllable from the API.
MANAGED_SERVICES = {
    "monitor-api": {"unit": "monitor-api.service", "label": "Monitor Hub API", "icon": "🛡️"},
    "poka-server": {"unit": "poka-server.service", "label": "Poka Server", "icon": "📦"},
    "poka-watch": {"unit": "poka-watch.service", "label": "Poka Watch (build)", "icon": "👀"},
    "reminder-bot-backend": {"unit": "reminder-bot-backend.service", "label": "Reminder Bot Backend", "icon": "⏰"},
    "reminder-bot-frontend": {"unit": "reminder-bot-frontend.service", "label": "Reminder Bot Frontend", "icon": "🗓️"},
    "prd-project": {"unit": "prd-project.service", "label": "PRD Project", "icon": "⚡"},
}

SYSTEMCTL_BIN = shutil.which("systemctl") or "/usr/bin/systemctl"
JOURNALCTL_BIN = shutil.which("journalctl") or "/usr/bin/journalctl"
SUDO_BIN = shutil.which("sudo") or "/usr/bin/sudo"
ADB_BIN = shutil.which("adb") or os.getenv("ADB_BIN", "adb")


async def _run_cmd(*args: str, timeout: float = 10.0) -> tuple[int, str, str]:
    proc = await asyncio.create_subprocess_exec(
        *args, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
    )
    try:
        out, err = await asyncio.wait_for(proc.communicate(), timeout=timeout)
    except asyncio.TimeoutError:
        # The process may already be gone (e.g. an adb client that exited while
        # its forked daemon kept the pipe open), so kill() can race.
        try:
            proc.kill()
        except ProcessLookupError:
            pass
        await proc.wait()
        return -1, "", "timeout"
    return proc.returncode, out.decode(errors="replace"), err.decode(errors="replace")


async def _service_status(unit: str) -> dict:
    rc, out, _ = await _run_cmd(
        SYSTEMCTL_BIN, "show", unit,
        "--property=ActiveState,SubState,UnitFileState,Description",
    )
    props: dict = {}
    for line in out.splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            props[k] = v
    return {
        "active_state": props.get("ActiveState", "unknown"),
        "sub_state": props.get("SubState", "unknown"),
        "enabled": props.get("UnitFileState", "unknown"),
        "description": props.get("Description", ""),
    }


# ── In-memory metrics history (kept for 1 hour, sampled ~30s) ──────────────
MAX_HISTORY = 120  # 120 samples × 30s = 1 hour
cpu_history: deque = deque(maxlen=MAX_HISTORY)
ram_history: deque = deque(maxlen=MAX_HISTORY)

# ── FastAPI app ─────────────────────────────────────────────────────────────
app = FastAPI(title="Monitor API", version="1.0.0")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

FRONTEND_DIR = Path(__file__).parent.parent / "monitor-app"


# ── System metrics ──────────────────────────────────────────────────────────
@app.get("/api/system")
async def get_system():
    """CPU, RAM, disk, temperature, uptime for this machine (public - no auth required)."""
    cpu_percent = psutil.cpu_percent(interval=0.5)
    cpu_count = psutil.cpu_count()
    load_avg = os.getloadavg()

    mem = psutil.virtual_memory()
    disk = psutil.disk_usage("/")

    # Temperature
    temps = {}
    try:
        sensor_temps = psutil.sensors_temperatures()
        for chip, entries in sensor_temps.items():
            for entry in entries:
                label = entry.label or chip
                temps[label] = {
                    "current": entry.current,
                    "high": entry.high,
                    "critical": entry.critical,
                }
    except Exception:
        pass

    # Uptime
    boot_time = psutil.boot_time()
    uptime_seconds = int(time.time() - boot_time)

    # Network I/O
    net = psutil.net_io_counters()

    return {
        "hostname": os.uname().nodename,
        "cpu": {
            "percent": cpu_percent,
            "count": cpu_count,
            "load_avg": list(load_avg),
        },
        "memory": {
            "total_gb": round(mem.total / 1024**3, 2),
            "used_gb": round(mem.used / 1024**3, 2),
            "percent": mem.percent,
        },
        "disk": {
            "total_gb": round(disk.total / 1024**3, 2),
            "used_gb": round(disk.used / 1024**3, 2),
            "percent": disk.percent,
        },
        "temperature": temps,
        "uptime_seconds": uptime_seconds,
        "network": {
            "bytes_sent": net.bytes_sent,
            "bytes_recv": net.bytes_recv,
        },
        "timestamp": datetime.now(WIB).isoformat(),
    }


@app.get("/api/system/history")
async def get_system_history():
    """Return CPU/RAM history (last ~1 hour) (public - no auth required)."""
    return {
        "cpu": list(cpu_history),
        "ram": list(ram_history),
    }


HOME_DIR = str(Path.home())


def _project_label(cwd: str | None) -> str | None:
    """Derive a human-friendly project name from a process's working dir, so
    three different Next.js apps show as 'project-cctv' / '9router' /
    'Pocket-Asik' instead of all collapsing into one 'Next.js' row."""
    if not cwd:
        return None
    if cwd == f"{HOME_DIR}/.9router" or cwd.startswith(f"{HOME_DIR}/.9router/"):
        return "9router"
    if "/projects/" in cwd:
        rest = cwd.split("/projects/", 1)[1]
        first = rest.split("/", 1)[0]
        if first:
            return first
    return None


def _proc_cwd(p: psutil.Process) -> str | None:
    try:
        return p.cwd()
    except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, FileNotFoundError):
        return None


@app.get("/api/processes")
async def get_processes(sort: str = "cpu", limit: int = 8):
    """Top processes by CPU or RAM (task-manager style) for this host.

    Grouped by project (folder they run from) when we can tell, else by
    process name — like Windows Task Manager's per-app grouping. CPU% is
    normalized to the whole machine (divided by core count) so values sum to
    ~100% across all cores; psutil's raw cpu_percent is per-core."""
    if sort not in ("cpu", "ram"):
        sort = "cpu"
    limit = max(1, min(limit, 30))
    ncpu = psutil.cpu_count() or 1

    # cpu_percent() measures usage since the previous call per process, so we
    # prime every process, wait a moment, then read the delta.
    procs = list(psutil.process_iter(["pid", "name", "username"]))
    for p in procs:
        try:
            p.cpu_percent(None)
        except (psutil.NoSuchProcess, psutil.AccessDenied):
            pass
    await asyncio.sleep(0.4)

    groups: dict[str, dict] = {}
    for p in procs:
        try:
            with p.oneshot():
                name = p.info.get("name") or "?"
                cpu = p.cpu_percent(None) / ncpu
                mem = p.memory_info().rss
                mem_pct = p.memory_percent()
                user = p.info.get("username") or ""
        except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess):
            continue

        # Only bother resolving cwd for processes worth showing (cheap-ish
        # readlink, but skip the long idle tail).
        project = None
        if cpu >= 0.1 or mem >= 5 * 1024 * 1024:
            project = _project_label(_proc_cwd(p))

        # Group key: project if known (keeps distinct apps separate), else name.
        label = project or name
        gkey = f"proj:{project}" if project else f"name:{name}"

        g = groups.get(gkey)
        metric = cpu if sort == "cpu" else mem
        if g is None:
            groups[gkey] = {
                "label": label, "name": name, "project": project, "user": user,
                "count": 1, "cpu": cpu, "mem_bytes": mem, "mem_percent": mem_pct,
                "top_pid": p.pid, "_top_metric": metric,
            }
        else:
            g["count"] += 1
            g["cpu"] += cpu
            g["mem_bytes"] += mem
            g["mem_percent"] += mem_pct
            if metric > g["_top_metric"]:
                g["_top_metric"] = metric
                g["top_pid"] = p.pid
                g["user"] = user

    rows = []
    for g in groups.values():
        # Drop kernel threads / truly idle noise (0% CPU and near-0 RAM)
        if g["cpu"] < 0.1 and g["mem_bytes"] < 5 * 1024 * 1024:
            continue
        rows.append({
            "label": g["label"],
            "name": g["name"],
            "project": g["project"],
            "user": g["user"],
            "count": g["count"],
            "top_pid": g["top_pid"],
            "cpu": round(g["cpu"], 1),
            "mem_bytes": g["mem_bytes"],
            "mem_percent": round(g["mem_percent"], 1),
        })

    key = "cpu" if sort == "cpu" else "mem_bytes"
    rows.sort(key=lambda r: r[key], reverse=True)
    return {"sort": sort, "ncpu": ncpu, "processes": rows[:limit]}


def _listening_ports(pid: int) -> list[int]:
    """TCP ports this PID is LISTENing on (best-effort; needs no root for own
    processes)."""
    ports = set()
    try:
        for c in psutil.Process(pid).net_connections(kind="tcp"):
            if c.status == psutil.CONN_LISTEN and c.laddr:
                ports.add(c.laddr.port)
    except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess):
        pass
    return sorted(ports)


@app.get("/api/processes/{pid}")
async def process_detail(pid: int):
    """Full detail for one process — command, working dir, ports, uptime — so
    you can tell what an app actually is (which project a Next.js is serving)."""
    try:
        p = psutil.Process(pid)
        with p.oneshot():
            name = p.name()
            cmdline = p.cmdline()
            status = p.status()
            create_time = p.create_time()
            num_threads = p.num_threads()
            mem = p.memory_info().rss
            mem_pct = p.memory_percent()
            username = p.username()
            try:
                ppid = p.ppid()
                parent_name = psutil.Process(ppid).name() if ppid else ""
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                ppid, parent_name = 0, ""
    except psutil.NoSuchProcess:
        raise HTTPException(404, "proses sudah tidak ada (mungkin sudah mati)")
    except psutil.AccessDenied:
        raise HTTPException(403, "tidak punya akses ke detail proses ini")

    cwd = _proc_cwd(p)
    ncpu = psutil.cpu_count() or 1
    p.cpu_percent(None)
    await asyncio.sleep(0.3)
    try:
        cpu = round(p.cpu_percent(None) / ncpu, 1)
    except psutil.NoSuchProcess:
        cpu = 0.0

    return {
        "pid": pid,
        "name": name,
        "project": _project_label(cwd),
        "user": username,
        "status": status,
        "cmdline": " ".join(cmdline) if cmdline else name,
        "cwd": cwd or "",
        "ports": _listening_ports(pid),
        "uptime_seconds": int(time.time() - create_time),
        "num_threads": num_threads,
        "cpu": cpu,
        "mem_bytes": mem,
        "mem_percent": round(mem_pct, 1),
        "ppid": ppid,
        "parent_name": parent_name,
    }


# ── Devices + web SSH terminal ──────────────────────────────────────────────
@app.get("/api/devices")
async def get_devices():
    """List configured devices (public - credentials are never included)."""
    return [
        {"id": d["id"], "label": d["label"], "icon": d.get("icon", "🖥️"),
         "os": d.get("os", "linux"), "host": d.get("host", ""),
         "protocol": d.get("protocol", "ssh"),
         # Whether this device also has SSH creds attached for power actions
         # (restart/shutdown) — relevant for "wol" devices, which are
         # otherwise fire-and-forget with no way to reach an already-on box.
         "ssh_power": "auth" in d and "username" in d}
        for d in DEVICES.values()
    ]


# ping isn't installed by default on every machine (no iputils-ping package);
# probing is best-effort, so fall back gracefully instead of erroring.
PING_BIN = shutil.which("ping")


async def _tcp_probe(host: str, port: int, timeout: float = 1.5) -> bool:
    try:
        _, writer = await asyncio.wait_for(asyncio.open_connection(host, port), timeout=timeout)
    except Exception:
        return False
    writer.close()
    try:
        await writer.wait_closed()
    except Exception:
        pass
    return True


async def _ping_probe(host: str, timeout: float = 2.0) -> bool:
    if not PING_BIN:
        return False
    try:
        proc = await asyncio.create_subprocess_exec(
            PING_BIN, "-c", "1", "-W", "1", host,
            stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL,
        )
        rc = await asyncio.wait_for(proc.wait(), timeout=timeout)
    except Exception:
        return False
    return rc == 0


async def _check_device_up(device: dict):
    """Returns True/False if reachability could be determined, None if we
    have no way to check (WOL-only device, no SSH creds, no ping binary)."""
    host = (device.get("host") or "").strip()
    if not host:
        return None
    if device.get("protocol") == "android":
        return await _tcp_probe(host, device.get("port", 5555))
    has_ssh = "auth" in device and "username" in device
    if has_ssh:
        return await _tcp_probe(host, device.get("port", 22))
    if PING_BIN:
        return await _ping_probe(host)
    return None


@app.get("/api/devices/status")
async def get_devices_status():
    """Online/offline check for each device (public - no creds involved)."""
    ids = list(DEVICES.keys())
    results = await asyncio.gather(*(_check_device_up(DEVICES[i]) for i in ids))
    return dict(zip(ids, results))


class DeviceCreate(BaseModel):
    label: str
    icon: str = ""  # per-protocol fallback applied below (📱 for android)
    host: str = ""
    port: int = 22
    username: str = ""
    os: str = "linux"
    protocol: str = "ssh"  # "ssh" | "wol" | "android"
    auth_type: str = "password"  # "password" | "key"
    password: str | None = None
    key_path: str | None = None
    passphrase: str | None = None
    mac_address: str | None = None  # Wake on LAN only
    enable_ssh_power: bool = False  # Wake on LAN only: also attach SSH creds for restart/shutdown


def _build_ssh_auth(auth_type: str, password: str | None, key_path: str | None,
                     passphrase: str | None) -> dict:
    if auth_type == "key":
        if not key_path:
            raise HTTPException(400, "key_path is required for key auth")
        auth = {"type": "key", "path": key_path}
        if passphrase:
            auth["passphrase"] = passphrase
        return auth
    elif auth_type == "password":
        return {"type": "password", "value": password or ""}
    else:
        raise HTTPException(400, "auth_type must be 'password' or 'key'")


@app.post("/api/devices")
async def add_device(payload: DeviceCreate):
    if payload.protocol not in ("ssh", "wol", "android"):
        raise HTTPException(400, "protocol must be 'ssh', 'wol', or 'android'")

    base_id = _slugify(payload.label)
    device_id = base_id
    n = 2
    while device_id in DEVICES:
        device_id = f"{base_id}-{n}"
        n += 1

    if payload.protocol == "wol":
        mac = _normalize_mac(payload.mac_address or "")
        if not mac:
            raise HTTPException(400, "mac_address wajib diisi dan harus format MAC yang valid")
        device = {
            "id": device_id,
            "label": payload.label,
            "icon": payload.icon or "🖥️",
            "host": payload.host,
            "os": payload.os,
            "protocol": "wol",
            "mac_address": mac,
        }
        if payload.enable_ssh_power:
            device["port"] = payload.port
            device["username"] = payload.username
            device["auth"] = _build_ssh_auth(
                payload.auth_type, payload.password, payload.key_path, payload.passphrase
            )

    elif payload.protocol == "android":
        device = {
            "id": device_id,
            "label": payload.label,
            "icon": payload.icon or "📱",
            "host": payload.host,
            "port": payload.port,  # ADB port, typically 5555
            "os": "android",
            "protocol": "android",
        }
    else:
        auth = _build_ssh_auth(payload.auth_type, payload.password, payload.key_path, payload.passphrase)
        device = {
            "id": device_id,
            "label": payload.label,
            "icon": payload.icon or "🖥️",
            "host": payload.host,
            "port": payload.port,
            "username": payload.username,
            "os": payload.os,
            "protocol": "ssh",
            "auth": auth,
        }

    DEVICES[device_id] = device
    _save_devices()
    return {"id": device_id, "label": device["label"], "icon": device["icon"],
            "os": device["os"], "host": device.get("host", ""), "protocol": device["protocol"]}


@app.delete("/api/devices/{device_id}")
async def delete_device(device_id: str):
    if device_id not in DEVICES:
        raise HTTPException(404, "device not found")
    del DEVICES[device_id]
    _save_devices()

    with SSH_SESSIONS_LOCK:
        stale_keys = [k for k in SSH_SESSIONS if k[0] == device_id]
        sessions = [SSH_SESSIONS.pop(k) for k in stale_keys]
    for sess in sessions:
        sess.close()

    return {"ok": True}


def _ssh_run(client: paramiko.SSHClient, command: str,
             timeout: float = 8.0) -> tuple[int, str]:
    """Run a short command, returning (exit_code, stdout). -1 on timeout.

    recv_exit_status() waits on the channel with no deadline of its own and is
    documented to hang if nothing drains stdout, so this polls and reads
    instead — probing a device must never wedge the connect path.
    """
    _, stdout, _ = client.exec_command(command, timeout=timeout)
    chan = stdout.channel
    chunks: list[bytes] = []
    deadline = time.monotonic() + timeout
    while True:
        while chan.recv_ready():
            chunks.append(chan.recv(32768))
        if chan.exit_status_ready() and not chan.recv_ready():
            break
        if time.monotonic() > deadline:
            chan.close()
            return -1, b"".join(chunks).decode(errors="replace")
        time.sleep(0.05)
    while chan.recv_ready():
        chunks.append(chan.recv(32768))
    return chan.recv_exit_status(), b"".join(chunks).decode(errors="replace")


def _ssh_exit_status(client: paramiko.SSHClient, command: str,
                     timeout: float = 8.0) -> int:
    return _ssh_run(client, command, timeout)[0]


def _ssh_connect(device: dict) -> paramiko.SSHClient:
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    auth = device.get("auth", {})
    kwargs = dict(
        hostname=device["host"],
        port=device.get("port", 22),
        username=device["username"],
        timeout=10,
        allow_agent=False,
        look_for_keys=False,
    )
    if auth.get("type") == "key":
        pkey = paramiko.Ed25519Key.from_private_key_file(
            auth["path"], password=auth.get("passphrase") or None
        )
        client.connect(pkey=pkey, **kwargs)
    else:
        client.connect(password=auth.get("value", ""), **kwargs)
    return client


# Sessions survive the browser tab closing/backgrounding: the SSH channel
# lives here, decoupled from any one websocket, and only gets torn down
# after SSH_IDLE_TIMEOUT with nobody attached. Reconnecting a websocket to
# the same device re-attaches to the same shell and replays the scrollback.
SSH_IDLE_TIMEOUT = 6 * 3600  # 6 hours with no client attached
SSH_BUFFER_MAX = 200_000  # chars of scrollback kept for replay on reattach

# ...but that only covers the *client* going away. Anything running in the
# shell still dies with this process, so restarting monitor-api (deploying a
# fix, the Services tab, a reboot) kills every long-running job on every
# device. Wrapping the shell in tmux moves its lifetime onto the target
# machine instead: we attach on connect and detach on disconnect, so the API
# becomes disposable. Targets without tmux (Windows) just get a plain shell.
TMUX_SUPPORT: dict[str, bool] = {}  # device_id → tmux available, probed once

# The tmux session list on the target is the authoritative record of which
# terminals exist for a device — it outlives this process and every browser,
# which is what lets you pick a session up from a different phone/laptop. That
# only works if the name round-trips back to a session id, so ids are
# restricted to a charset tmux is happy with (no '.' or ':') and short enough
# that nothing gets truncated. Anything else is rejected rather than mangled.
TMUX_PREFIX = "mh-"
_SESSION_ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,32}$")


def _tmux_session_name(session_id: str) -> str:
    # This gets interpolated into a shell command, so the charset check is
    # load-bearing, not cosmetic — enforce it here so no caller can skip it.
    if not _SESSION_ID_RE.match(session_id):
        raise ValueError(f"invalid session id: {session_id!r}")
    return TMUX_PREFIX + session_id


def _session_id_from_tmux(name: str) -> str | None:
    if not name.startswith(TMUX_PREFIX):
        return None
    sid = name[len(TMUX_PREFIX):]
    return sid if _SESSION_ID_RE.match(sid) else None


def _open_shell(device: dict, device_id: str, session_id: str):
    """Connect and open an interactive shell. Returns (client, channel)."""
    client = _ssh_connect(device)
    if device_id not in TMUX_SUPPORT:
        try:
            TMUX_SUPPORT[device_id] = _ssh_exit_status(client, "command -v tmux") == 0
        except Exception:
            TMUX_SUPPORT[device_id] = False

    channel = client.get_transport().open_session()
    channel.get_pty(term="xterm-256color")
    if TMUX_SUPPORT[device_id]:
        # -A attaches to the session if it already exists and creates it
        # otherwise, which is exactly the reconnect semantics we want: same
        # session_id (persisted in the browser's localStorage) → same shell,
        # whether we're reconnecting after a network blip or after the API
        # process was restarted out from under it.
        channel.exec_command(f"tmux new-session -A -s {_tmux_session_name(session_id)}")
    else:
        channel.invoke_shell()
    return client, channel


def _tmux_kill(device: dict, session_id: str, client: paramiko.SSHClient | None = None):
    own_client = client is None
    if own_client:
        client = _ssh_connect(device)
    try:
        _ssh_exit_status(client, f"tmux kill-session -t {_tmux_session_name(session_id)}")
    finally:
        if own_client:
            client.close()


class SSHSession:
    def __init__(self, key: tuple[str, str], client: paramiko.SSHClient, channel):
        self.key = key  # (device_id, session_id) — session_id is what lets a
        # device run multiple independent terminal tabs instead of every
        # connection sharing (mirroring) one shell.
        self.client = client
        self.channel = channel
        self.buffer = ""
        self.buffer_lock = threading.Lock()
        self.websockets: set[WebSocket] = set()
        self.idle_handle: asyncio.TimerHandle | None = None
        self.loop = asyncio.get_event_loop()

    def append_buffer(self, text: str):
        with self.buffer_lock:
            self.buffer += text
            if len(self.buffer) > SSH_BUFFER_MAX:
                self.buffer = self.buffer[-SSH_BUFFER_MAX:]

    def snapshot_buffer(self) -> str:
        with self.buffer_lock:
            return self.buffer

    def close(self):
        try:
            self.channel.close()
        except Exception:
            pass
        try:
            self.client.close()
        except Exception:
            pass


SSH_SESSIONS: dict[tuple[str, str], SSHSession] = {}
SSH_SESSIONS_LOCK = threading.Lock()


def _ssh_reader(session: SSHSession):
    try:
        while not session.channel.closed:
            data = session.channel.recv(4096)
            if not data:
                break
            text = data.decode(errors="replace")
            session.append_buffer(text)
            for ws in list(session.websockets):
                asyncio.run_coroutine_threadsafe(ws.send_text(text), session.loop)
    except Exception:
        pass
    finally:
        with SSH_SESSIONS_LOCK:
            if SSH_SESSIONS.get(session.key) is session:
                SSH_SESSIONS.pop(session.key, None)
        session.close()


@app.websocket("/ws/ssh/{device_id}/{session_id}")
async def ssh_terminal(websocket: WebSocket, device_id: str, session_id: str):
    await websocket.accept()

    device = DEVICES.get(device_id)
    if not device:
        await websocket.close(code=4404)
        return
    if not _SESSION_ID_RE.match(session_id):
        await websocket.send_text(
            "\r\n\x1b[31mInvalid session id.\x1b[0m\r\n")
        await websocket.close(code=4400)
        return

    key = (device_id, session_id)
    with SSH_SESSIONS_LOCK:
        session = SSH_SESSIONS.get(key)

    if session is None or session.channel.closed:
        try:
            client, channel = await asyncio.to_thread(
                _open_shell, device, device_id, session_id)
        except Exception as e:
            await websocket.send_text(f"\r\n\x1b[31mConnection failed: {e}\x1b[0m\r\n")
            await websocket.close(code=4500)
            return
        session = SSHSession(key, client, channel)
        with SSH_SESSIONS_LOCK:
            SSH_SESSIONS[key] = session
        threading.Thread(target=_ssh_reader, args=(session,), daemon=True).start()
    else:
        if session.idle_handle:
            session.idle_handle.cancel()
            session.idle_handle = None
        buffered = session.snapshot_buffer()
        if buffered:
            await websocket.send_text(buffered)

    session.websockets.add(websocket)

    try:
        while True:
            msg = await websocket.receive_text()
            try:
                parsed = json.loads(msg)
                if isinstance(parsed, dict) and parsed.get("type") == "resize":
                    session.channel.resize_pty(width=parsed["cols"], height=parsed["rows"])
                    continue
            except (json.JSONDecodeError, KeyError):
                pass
            session.channel.send(msg)
    except WebSocketDisconnect:
        pass
    finally:
        session.websockets.discard(websocket)
        if not session.websockets:
            loop = asyncio.get_event_loop()

            def _teardown(key=key, sess=session):
                with SSH_SESSIONS_LOCK:
                    if SSH_SESSIONS.get(key) is sess and not sess.websockets:
                        SSH_SESSIONS.pop(key, None)
                        sess.close()

            session.idle_handle = loop.call_later(SSH_IDLE_TIMEOUT, _teardown)


def _live_client(device_id: str) -> paramiko.SSHClient | None:
    """An already-open SSH connection to this device, if any — listing
    sessions shouldn't pay for a fresh handshake when a terminal is open."""
    with SSH_SESSIONS_LOCK:
        for (did, _), sess in SSH_SESSIONS.items():
            if did == device_id and not sess.channel.closed:
                return sess.client
    return None


_TMUX_LIST_CMD = (
    "tmux list-sessions -F "
    "'#{session_name}\t#{session_created}\t#{session_attached}'"
)


def _collect_sessions(device: dict, device_id: str) -> list[dict]:
    """Terminals that exist for this device, newest last.

    tmux sessions on the target are the durable ones; the in-memory registry
    is folded in too so devices without tmux (Windows) still list whatever is
    currently open instead of nothing.
    """
    found: dict[str, dict] = {}

    if TMUX_SUPPORT.get(device_id, True):
        client = _live_client(device_id)
        own_client = client is None
        try:
            if own_client:
                client = _ssh_connect(device)
            rc, out = _ssh_run(client, _TMUX_LIST_CMD)
            # rc != 0 is the normal "no server running" case, not an error.
            if rc == 0:
                TMUX_SUPPORT[device_id] = True
                for line in out.splitlines():
                    parts = line.strip().split("\t")
                    if len(parts) != 3:
                        continue
                    sid = _session_id_from_tmux(parts[0])
                    if not sid:
                        continue  # someone else's tmux session, not ours
                    found[sid] = {
                        "id": sid,
                        "created_at": int(parts[1]) if parts[1].isdigit() else None,
                        "attached": parts[2] == "1",
                        "persistent": True,
                    }
        except Exception as e:
            print(f"⚠️  tmux list-sessions failed for {device_id}: {e}")
        finally:
            if own_client and client is not None:
                client.close()

    with SSH_SESSIONS_LOCK:
        open_ids = [sid for (did, sid) in SSH_SESSIONS if did == device_id]
    for sid in open_ids:
        found.setdefault(sid, {"id": sid, "created_at": None,
                               "attached": True, "persistent": False})

    return sorted(found.values(), key=lambda s: s["created_at"] or 0)


@app.get("/api/devices/{device_id}/sessions")
async def list_ssh_sessions(device_id: str):
    device = DEVICES.get(device_id)
    if not device:
        raise HTTPException(404, "device not found")
    sessions = await asyncio.to_thread(_collect_sessions, device, device_id)
    return {"persistent": bool(TMUX_SUPPORT.get(device_id)), "sessions": sessions}


@app.delete("/api/devices/{device_id}/sessions/{session_id}")
async def kill_ssh_session(device_id: str, session_id: str):
    """Explicitly end a terminal session (the tab's ✕ button).

    Detaching is the default now, so without this every closed tab would leave
    a detached tmux session running on the target forever."""
    device = DEVICES.get(device_id)
    if not device:
        raise HTTPException(404, "device not found")
    if not _SESSION_ID_RE.match(session_id):
        raise HTTPException(400, "invalid session id")

    key = (device_id, session_id)
    with SSH_SESSIONS_LOCK:
        session = SSH_SESSIONS.pop(key, None)
    if session and session.idle_handle:
        session.idle_handle.cancel()

    # Unknown support (nothing connected since the last restart) still gets an
    # attempt — the probe result is cached, so a non-tmux device only pays for
    # the extra connection once.
    if TMUX_SUPPORT.get(device_id, True):
        try:
            await asyncio.to_thread(
                _tmux_kill, device, session_id, session.client if session else None)
        except Exception as e:
            print(f"⚠️  tmux kill-session failed for {device_id}/{session_id}: {e}")

    if session:
        # Boot any other device still attached to this session, so it sees a
        # clean close instead of a channel that silently stopped working.
        for ws in list(session.websockets):
            try:
                await ws.close(code=4410)
            except Exception:
                pass
        session.close()
    return {"ok": True}


# ── Wake on LAN ──────────────────────────────────────────────────────────────
def _send_wol(mac_address: str, broadcast_ip: str = "255.255.255.255", port: int = 9):
    mac_bytes = bytes.fromhex(mac_address)
    magic_packet = b"\xff" * 6 + mac_bytes * 16
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
        sock.sendto(magic_packet, (broadcast_ip, port))
    finally:
        sock.close()


@app.post("/api/devices/{device_id}/wol")
async def wake_device(device_id: str):
    device = DEVICES.get(device_id)
    if not device:
        raise HTTPException(404, "device not found")
    if device.get("protocol") != "wol":
        raise HTTPException(400, "device ini bukan device Wake on LAN")
    mac = device.get("mac_address")
    if not mac:
        raise HTTPException(400, "device tidak punya MAC address")
    try:
        await asyncio.to_thread(_send_wol, mac)
    except Exception as e:
        raise HTTPException(500, f"Gagal mengirim magic packet: {e}")
    return {"ok": True}


# WOL can only power on an already-off/sleeping machine — the NIC only
# listens for magic packets while the OS is down. Restarting/shutting down a
# machine that's already on needs an actual command sent over a live
# connection, so this piggybacks on the same SSH creds used by the "ssh"
# protocol (attached to a "wol" device via enable_ssh_power).
POWER_COMMANDS = {
    ("windows", "restart"): "shutdown /r /t 0",
    ("windows", "shutdown"): "shutdown /s /t 0",
    ("linux", "restart"): "sudo reboot",
    ("linux", "shutdown"): "sudo poweroff",
}


def _run_power_command(device: dict, command: str):
    client = _ssh_connect(device)
    try:
        client.exec_command(command)
    finally:
        client.close()


@app.post("/api/devices/{device_id}/power/{action}")
async def power_action(device_id: str, action: str):
    if action not in ("restart", "shutdown"):
        raise HTTPException(400, "action must be 'restart' or 'shutdown'")
    device = DEVICES.get(device_id)
    if not device:
        raise HTTPException(404, "device not found")
    if "auth" not in device or "username" not in device:
        raise HTTPException(400, "device ini belum diset kredensial SSH buat kontrol power")

    command = POWER_COMMANDS.get((device.get("os", "linux"), action))
    if not command:
        raise HTTPException(400, f"OS '{device.get('os')}' tidak didukung buat power action")

    try:
        await asyncio.to_thread(_run_power_command, device, command)
    except Exception as e:
        raise HTTPException(500, f"Gagal kirim perintah: {e}")
    return {"ok": True}



# ── Android remote (scrcpy-like via ADB) ────────────────────────────────────
import base64 as _b64
import struct as _struct

ANDROID_SESSIONS: dict[str, asyncio.subprocess.Process] = {}


async def _adb_cmd(host: str, port: int, *args: str, timeout: float = 10.0):
    serial = f"{host}:{port}"
    return await _run_cmd(ADB_BIN, "-s", serial, *args, timeout=timeout)


async def _adb_start_server():
    """Boot the adb daemon once, detached from our pipes.

    The daemon inherits stdout/stderr from whichever adb client spawns it, so
    if the first command we run is a plain `adb connect` its pipes never close
    and _run_cmd times out even though the client already finished.
    """
    try:
        proc = await asyncio.create_subprocess_exec(
            ADB_BIN, "start-server",
            stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL,
        )
        await asyncio.wait_for(proc.wait(), timeout=20)
    except Exception:
        pass


async def _ensure_adb_connected(host: str, port: int) -> bool:
    serial = f"{host}:{port}"
    await _adb_start_server()
    # `adb connect` exits 0 even when the target refuses, so the only reliable
    # signal is the message itself ("connected to" / "already connected to").
    rc, out, err = await _run_cmd(ADB_BIN, "connect", serial, timeout=15)
    if rc != 0:
        return False
    return "connected to" in (out + err).lower()


async def _adb_wake(host: str, port: int, timeout: float = 6.0) -> bool:
    """Wake the display and wait until it actually reports ON.

    screenrecord aborts with INVALID_LAYER_STACK if the display is off, and the
    panel needs a moment after the wake keyevent before it is really on, so
    polling beats a fixed sleep here.
    """
    await _adb_cmd(host, port, "shell", "input", "keyevent", "224", timeout=5)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        rc, out, _ = await _adb_cmd(host, port, "shell", "dumpsys", "display", timeout=5)
        if rc == 0 and "mScreenState=ON" in out:
            return True
        await asyncio.sleep(0.4)
    return False


async def _adb_screenrecord(host: str, port: int, size: str, bitrate: str):
    """Start screenrecord piping a raw H.264 Annex-B stream to stdout."""
    serial = f"{host}:{port}"
    return await asyncio.create_subprocess_exec(
        ADB_BIN, "-s", serial, "exec-out",
        "screenrecord", "--output-format=h264", "--time-limit", "0",
        "--bit-rate", bitrate, "--size", size, "-",
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.DEVNULL,
    )


async def _adb_screencap_jpeg(host: str, port: int, quality: int = 70) -> bytes | None:
    serial = f"{host}:{port}"
    proc = await asyncio.create_subprocess_exec(
        ADB_BIN, "-s", serial, "exec-out",
        "screencap", "-p",
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    try:
        out, _ = await asyncio.wait_for(proc.communicate(), timeout=8)
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        return None
    if not out or len(out) < 100:
        return None
    return out


async def _adb_get_screen_size(host: str, port: int) -> tuple[int, int]:
    rc, out, _ = await _adb_cmd(host, port, "shell", "wm", "size")
    if rc == 0:
        m = re.search(r"(\d+)x(\d+)", out)
        if m:
            return int(m.group(1)), int(m.group(2))
    return 1080, 1920


@app.websocket("/ws/android/{device_id}")
async def android_remote(websocket: WebSocket, device_id: str):
    await websocket.accept()

    device = DEVICES.get(device_id)
    if not device or device.get("protocol") != "android":
        await websocket.close(code=4404)
        return

    host = device["host"]
    port = device.get("port", 5555)

    connected = await _ensure_adb_connected(host, port)
    if not connected:
        await websocket.send_json({"type": "error", "msg": f"Gagal connect ADB ke {host}:{port}"})
        await websocket.close(code=4500)
        return

    params = websocket.query_params
    mode = params.get("mode", "h264")
    size = params.get("size", "720x1560")
    bitrate = params.get("bitrate", "8M")
    if not re.fullmatch(r"\d{3,5}x\d{3,5}", size):
        size = "720x1560"
    if not re.fullmatch(r"\d{1,3}M|\d{4,9}", bitrate):
        bitrate = "8M"

    width, height = await _adb_get_screen_size(host, port)
    await websocket.send_json({
        "type": "info", "width": width, "height": height, "mode": mode,
    })

    streaming = True

    async def stream_png():
        """Fallback: poll screencap. ~2 fps, but needs no WebCodecs support."""
        nonlocal streaming
        while streaming:
            png_data = await _adb_screencap_jpeg(host, port)
            if png_data and streaming:
                try:
                    await websocket.send_bytes(png_data)
                except Exception:
                    break
            await asyncio.sleep(0.12)

    async def stream_h264():
        """Stream the device's hardware H.264 encoder straight to the browser.

        screenrecord dies whenever the display turns off, so each pass wakes the
        screen first and the loop simply starts a new one — the client resets its
        decoder on the stream_restart notice because a fresh process emits new
        SPS/PPS and an IDR.
        """
        nonlocal streaming
        first_pass = True
        while streaming:
            if not await _adb_wake(host, port):
                await websocket.send_json(
                    {"type": "error", "msg": "Layar HP tidak bisa dinyalakan"})
                return
            if not first_pass:
                await websocket.send_json({"type": "stream_restart"})
            first_pass = False

            proc = await _adb_screenrecord(host, port, size, bitrate)
            try:
                # screenrecord reports failures as plain text on *stdout*, so the
                # first chunk has to be inspected rather than trusting exit codes.
                head = await proc.stdout.read(65536)
                if not head:
                    continue
                if head.startswith(b"ERROR"):
                    await websocket.send_json({
                        "type": "error",
                        "msg": head.decode(errors="replace").strip()[:200],
                    })
                    return
                await websocket.send_bytes(head)
                while streaming:
                    chunk = await proc.stdout.read(65536)
                    if not chunk:
                        break
                    await websocket.send_bytes(chunk)
            except Exception:
                return
            finally:
                if proc.returncode is None:
                    try:
                        proc.kill()
                    except ProcessLookupError:
                        pass
                await proc.wait()

    stream_task = asyncio.create_task(
        stream_h264() if mode == "h264" else stream_png())

    try:
        while True:
            raw = await websocket.receive_text()
            msg = json.loads(raw)
            t = msg.get("type")
            if t == "tap":
                x, y = int(msg["x"]), int(msg["y"])
                await _adb_cmd(host, port, "shell", "input", "tap", str(x), str(y), timeout=5)
            elif t == "swipe":
                x1, y1, x2, y2 = int(msg["x1"]), int(msg["y1"]), int(msg["x2"]), int(msg["y2"])
                dur = int(msg.get("duration", 300))
                await _adb_cmd(host, port, "shell", "input", "swipe",
                               str(x1), str(y1), str(x2), str(y2), str(dur), timeout=5)
            elif t == "keyevent":
                key = int(msg["code"])
                await _adb_cmd(host, port, "shell", "input", "keyevent", str(key), timeout=5)
            elif t == "text":
                text = msg.get("text", "")
                if text:
                    await _adb_cmd(host, port, "shell", "input", "text",
                                   text.replace(" ", "%s"), timeout=5)
            elif t == "longpress":
                x, y = int(msg["x"]), int(msg["y"])
                await _adb_cmd(host, port, "shell", "input", "swipe",
                               str(x), str(y), str(x), str(y), "600", timeout=5)
    except WebSocketDisconnect:
        pass
    except Exception:
        pass
    finally:
        streaming = False
        stream_task.cancel()


@app.get("/android.html")
async def serve_android():
    page = FRONTEND_DIR / "android.html"
    if page.exists():
        return FileResponse(page, headers={"Cache-Control": "no-store"})
    return Response(status_code=404)


# ── Managed services: status / logs / start-stop-restart ───────────────────
@app.get("/api/services")
async def list_services():
    """Status of all whitelisted systemd services on this host."""
    results = await asyncio.gather(*(
        _service_status(svc["unit"]) for svc in MANAGED_SERVICES.values()
    ))
    return [
        {"id": sid, "label": svc["label"], "icon": svc["icon"], "unit": svc["unit"], **status}
        for (sid, svc), status in zip(MANAGED_SERVICES.items(), results)
    ]


@app.get("/api/services/{service_id}/logs")
async def service_logs(service_id: str, lines: int = 200):
    svc = MANAGED_SERVICES.get(service_id)
    if not svc:
        raise HTTPException(404, "service not found")
    lines = max(1, min(lines, 1000))
    rc, out, err = await _run_cmd(
        JOURNALCTL_BIN, "-u", svc["unit"], "-n", str(lines), "--no-pager", "-o", "short-iso",
    )
    if rc != 0:
        raise HTTPException(500, err.strip() or "gagal ambil log")
    return {"lines": out.splitlines()}


@app.post("/api/services/{service_id}/action/{action}")
async def service_action(service_id: str, action: str):
    if action not in ("start", "stop", "restart"):
        raise HTTPException(400, "action must be 'start', 'stop', or 'restart'")
    svc = MANAGED_SERVICES.get(service_id)
    if not svc:
        raise HTTPException(404, "service not found")

    # Requires a passwordless-sudo rule scoped to exactly these systemctl
    # start/stop/restart <unit> commands (see monitor-api/sudoers-services.md)
    # — the API process itself runs unprivileged (User=daffa in the unit file).
    rc, out, err = await _run_cmd(
        SUDO_BIN, "-n", SYSTEMCTL_BIN, action, svc["unit"], timeout=20,
    )
    if rc != 0:
        detail = err.strip() or out.strip()
        if "password is required" in detail or "a password is required" in detail:
            detail = "Sudo belum di-setup buat kontrol service ini. Lihat monitor-api/sudoers-services.md."
        raise HTTPException(500, detail or f"gagal {action} service")
    return {"ok": True}


@app.websocket("/ws/logs/{service_id}")
async def logs_ws(websocket: WebSocket, service_id: str):
    """Live-tail a service's journal (journalctl -f) over a websocket."""
    await websocket.accept()
    svc = MANAGED_SERVICES.get(service_id)
    if not svc:
        await websocket.close(code=4404)
        return

    try:
        proc = await asyncio.create_subprocess_exec(
            JOURNALCTL_BIN, "-u", svc["unit"], "-n", "200", "-f", "-o", "short-iso",
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL,
        )
    except Exception as e:
        await websocket.send_text(f"[error starting journalctl: {e}]")
        await websocket.close(code=4500)
        return

    async def pump():
        try:
            while True:
                line = await proc.stdout.readline()
                if not line:
                    break
                await websocket.send_text(line.decode(errors="replace").rstrip("\n"))
        except Exception:
            pass

    pump_task = asyncio.create_task(pump())
    try:
        while True:
            await websocket.receive_text()
    except WebSocketDisconnect:
        pass
    finally:
        pump_task.cancel()
        try:
            proc.kill()
            await proc.wait()
        except Exception:
            pass


# ── Serve frontend ──────────────────────────────────────────────────────────
@app.get("/")
async def serve_index():
    index = FRONTEND_DIR / "index.html"
    if index.exists():
        return FileResponse(index, headers={"Cache-Control": "no-store"})
    return {"message": "Monitor API is running. Frontend not found."}


@app.get("/sw.js")
async def serve_sw():
    sw = FRONTEND_DIR / "sw.js"
    if sw.exists():
        return FileResponse(sw, media_type="application/javascript",
                          headers={"Cache-Control": "no-store"})
    return Response(status_code=404)


# This gets iterated on a lot and is opened straight from bookmarks/home-
# screen shortcuts on phones, which cache far more aggressively than desktop
# browsers — without no-store, a stale copy can silently keep being served
# after every fix, making it look like nothing changed.
@app.get("/terminal.html")
async def serve_terminal():
    page = FRONTEND_DIR / "terminal.html"
    if page.exists():
        return FileResponse(page, headers={"Cache-Control": "no-store"})
    return Response(status_code=404)


# Mount static files for frontend
if FRONTEND_DIR.exists():
    app.mount("/", StaticFiles(directory=str(FRONTEND_DIR), html=True), name="frontend")


# ── Background task: collect metrics history ────────────────────────────────
async def _collect_metrics():
    """Background loop: sample CPU/RAM every 30 seconds."""
    while True:
        ts = datetime.now(WIB).isoformat()
        cpu = psutil.cpu_percent(interval=1)
        ram = psutil.virtual_memory().percent
        cpu_history.append({"ts": ts, "v": cpu})
        ram_history.append({"ts": ts, "v": ram})
        await asyncio.sleep(30)


@app.on_event("startup")
async def startup():
    asyncio.create_task(_collect_metrics())
    asyncio.create_task(_adb_start_server())


# ── Main ────────────────────────────────────────────────────────────────────
if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=API_PORT)
