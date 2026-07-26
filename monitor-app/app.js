/* ================================================================
   Monitor Hub — Frontend Logic
   ================================================================ */

(() => {
    "use strict";

    // ── Config ──────────────────────────────────────────────────
    const API_BASE = window.location.origin;
    const REFRESH_INTERVAL = 5000; // 5 seconds
    let refreshTimer = null;
    let currentTab = "system";
    let procSort = "cpu"; // top-processes sort: "cpu" | "ram"

    // DOM caches for keyed diff (avoid full innerHTML rebuild every 5s)
    const deviceCardMap = new Map();   // id → element
    const serviceCardMap = new Map();  // id → element
    const procRowMap = new Map();      // top_pid → element


    // ── DOM refs ────────────────────────────────────────────────
    const $ = (s) => document.querySelector(s);
    const $$ = (s) => document.querySelectorAll(s);

    const lastUpdate = $("#last-update");
    const tabs = $$(".tab");
    const toastContainer = $("#toast-container");

    // ── Helpers ─────────────────────────────────────────────────
    async function api(path, opts = {}) {
        const url = `${API_BASE}${path}`;
        const headers = { "Content-Type": "application/json", ...(opts.headers || {}) };
        try {
            const res = await fetch(url, { ...opts, headers });
            return await res.json();
        } catch (err) {
            console.error(`API error: ${path}`, err);
            return null;
        }
    }

    function formatUptime(seconds) {
        if (!seconds && seconds !== 0) return "—";
        const d = Math.floor(seconds / 86400);
        const h = Math.floor((seconds % 86400) / 3600);
        const m = Math.floor((seconds % 3600) / 60);
        if (d > 0) return `${d}d ${h}h`;
        if (h > 0) return `${h}h ${m}m`;
        return `${m}m`;
    }

    function formatBytes(bytes) {
        if (!bytes && bytes !== 0) return "—";
        const units = ["B", "KB", "MB", "GB", "TB"];
        let i = 0;
        let val = bytes;
        while (val >= 1024 && i < units.length - 1) {
            val /= 1024;
            i++;
        }
        return `${val.toFixed(1)} ${units[i]}`;
    }

    function toast(msg, type = "success") {
        const el = document.createElement("div");
        el.className = `toast ${type}`;
        el.textContent = msg;
        toastContainer.appendChild(el);
        setTimeout(() => el.remove(), 3000);
    }

    function setRing(id, percent) {
        const el = document.getElementById(id);
        if (!el) return;
        const circumference = 326.73;
        const offset = circumference * (1 - Math.min(percent, 100) / 100);
        el.style.strokeDashoffset = offset;
        // Color change based on value — always reassigned, otherwise a ring
        // that spiked once stays red/yellow forever.
        if (percent > 90) el.style.stroke = "var(--red)";
        else if (percent > 70) el.style.stroke = "var(--yellow)";
        else el.style.stroke = "";
    }

    // The grids/lists ship a <div class="skeleton-card"> placeholder in the
    // HTML, and the error branches below replace the container's contents. The
    // keyed renderers only ever append their own nodes, so anything else in the
    // container has to be dropped explicitly — otherwise the placeholder sticks
    // around as an empty shimmering box next to the real cards. This also puts
    // the nodes back in list order (only moving the ones actually out of
    // place), which is what keeps Top Processes sorted after the first paint.
    function syncChildren(container, els) {
        const wanted = new Set(els);
        for (const child of Array.from(container.children)) {
            if (!wanted.has(child)) child.remove();
        }
        els.forEach((el, i) => {
            if (container.children[i] !== el) {
                container.insertBefore(el, container.children[i] || null);
            }
        });
    }

    startRefresh();

    // ── Tabs ────────────────────────────────────────────────────
    tabs.forEach((tab) => {
        tab.addEventListener("click", () => {
            tabs.forEach((t) => t.classList.remove("active"));
            tab.classList.add("active");
            const target = tab.dataset.tab;
            currentTab = target;
            ["system", "services", "devices"].forEach((t) => {
                const el = $(`#tab-${t}`);
                if (el) el.classList.toggle("hidden", t !== target);
            });
            refreshData();
        });
    });

    // ── Device Rendering ────────────────────────────────────────
    function escapeHtml(s) {
        return String(s ?? "").replace(/[&<>"']/g, (c) => ({
            "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
        }[c]));
    }

    async function renderDevices(list) {
        const grid = $("#device-grid");
        const count = $("#device-count");
        if (!list) {
            grid.innerHTML = '<div class="bot-card"><p style="color:var(--text-muted)">Gagal load device list</p></div>';
            deviceCardMap.clear();
            if (count) count.textContent = "—";
            return;
        }
        if (count) count.textContent = `${list.length} device`;
        if (list.length === 0) {
            grid.innerHTML = '<div class="bot-card"><p style="color:var(--text-muted)">Belum ada device. Klik + Add buat nambah.</p></div>';
            deviceCardMap.clear();
            return;
        }

        const currentIds = new Set(list.map((d) => d.id));
        // Remove stale cards
        for (const [id, cached] of deviceCardMap) {
            if (!currentIds.has(id)) {
                cached.el.remove();
                deviceCardMap.delete(id);
            }
        }

        list.forEach((d, i) => {
            const cached = deviceCardMap.get(d.id);
            if (cached) {
                const prev = cached.data;
                // Update only fields that changed
                const changed =
                    prev.label !== d.label ||
                    prev.icon !== d.icon ||
                    prev.online !== d.online ||
                    prev.protocol !== d.protocol ||
                    prev.host !== d.host ||
                    prev.os !== d.os ||
                    prev.ssh_power !== d.ssh_power;
                if (changed) {
                    cached.el.className = `bot-card ${deviceStatusClass(d)}`;
                    cached.el.querySelector(".bot-name").textContent = d.label;
                    cached.el.querySelector(".bot-type").textContent = deviceMeta(d);
                    cached.el.querySelector(".bot-icon").textContent = d.icon || "🖥️";
                    // Rebuild actions (they depend on protocol/online state)
                    cached.el.querySelector(".bot-actions").innerHTML = deviceActionsHtml(d);
                    bindDeviceActions(cached.el, d);
                    cached.data = d;
                }
            } else {
                const el = createDeviceCard(d, i);
                deviceCardMap.set(d.id, { data: d, el });
                grid.appendChild(el);
            }
        });

        syncChildren(grid, list.map((d) => deviceCardMap.get(d.id).el));
    }

    function deviceStatusClass(d) {
        return d.online === true ? "active" : d.online === false ? "inactive" : "";
    }

    // Built from the parts we actually have — a device saved without an OS
    // would otherwise render as "192.168.1.5 ·  · SSH".
    function deviceMeta(d) {
        const kind = d.protocol === "wol" ? "Wake on LAN"
            : d.protocol === "android" ? "Android ADB"
                : "SSH";
        const parts = [d.host, d.os, kind].filter(Boolean);
        if (d.online === true) parts.push("🟢 Online");
        else if (d.online === false) parts.push("🔴 Offline");
        return parts.join(" · ");
    }

    function deviceActionsHtml(d) {
        const isWol = d.protocol === "wol";
        const id = escapeHtml(d.id);
        const label = escapeHtml(d.label);
        const primary = isWol
            ? `<button class="btn-action start" data-wake="${id}" data-label="${label}">⚡ Wake Up</button>`
            : d.protocol === "android"
                ? `<button class="btn-action start" data-android="${id}" data-label="${label}">📱 Remote</button>`
                : `<button class="btn-action start" data-connect="${id}" data-label="${label}">▶ Connect</button>`;
        const power = isWol && d.ssh_power
            ? `<button class="btn-action" data-power="restart" data-device="${id}" data-label="${label}" title="Restart">🔁 Restart</button>
               <button class="btn-action" data-power="shutdown" data-device="${id}" data-label="${label}" title="Shutdown">⏻ Shutdown</button>`
            : "";
        return `${primary}${power}<button class="btn-action delete" data-delete="${id}" data-label="${label}" title="Hapus device">✕</button>`;
    }

    function createDeviceCard(d, i) {
        const card = document.createElement("div");
        card.className = `bot-card ${deviceStatusClass(d)}`;
        card.style.animationDelay = `${i * 0.08}s`;
        card.setAttribute("data-id", d.id);
        card.innerHTML = `
            <div class="bot-card-top">
                <div class="bot-info">
                    <div class="bot-icon">${escapeHtml(d.icon || "🖥️")}</div>
                    <div>
                        <div class="bot-name">${escapeHtml(d.label)}</div>
                        <div class="bot-type">${escapeHtml(deviceMeta(d))}</div>
                    </div>
                </div>
            </div>
            <div class="bot-actions">${deviceActionsHtml(d)}</div>
        `;
        bindDeviceActions(card, d);
        return card;
    }

    function bindDeviceActions(card, d) {
        const isWol = d.protocol === "wol";
        card.querySelectorAll("[data-android]").forEach((btn) => {
            btn.addEventListener("click", () => {
                const url = `android.html?id=${encodeURIComponent(btn.dataset.android)}&label=${encodeURIComponent(btn.dataset.label)}`;
                window.open(url, "_blank", "noopener");
            });
        });
        card.querySelectorAll("[data-connect]").forEach((btn) => {
            btn.addEventListener("click", () => {
                const url = `terminal.html?id=${encodeURIComponent(btn.dataset.connect)}&label=${encodeURIComponent(btn.dataset.label)}`;
                window.open(url, "_blank", "noopener");
            });
        });
        card.querySelectorAll("[data-wake]").forEach((btn) => {
            btn.addEventListener("click", async () => {
                btn.disabled = true;
                const original = btn.textContent;
                btn.textContent = "Mengirim…";
                try {
                    const res = await fetch(`${API_BASE}/api/devices/${encodeURIComponent(btn.dataset.wake)}/wol`, {
                        method: "POST",
                    });
                    if (res.ok) {
                        toast(`Magic packet dikirim ke "${btn.dataset.label}"`, "success");
                    } else {
                        const err = await res.json().catch(() => null);
                        toast(err?.detail || "Gagal kirim Wake on LAN", "error");
                    }
                } catch (err) {
                    toast("Gagal konek ke server", "error");
                } finally {
                    btn.disabled = false;
                    btn.textContent = original;
                }
            });
        });
        card.querySelectorAll("[data-power]").forEach((btn) => {
            btn.addEventListener("click", async () => {
                const action = btn.dataset.power;
                const verb = action === "restart" ? "restart" : "matiin";
                if (!confirm(`Yakin mau ${verb} "${btn.dataset.label}"?`)) return;
                btn.disabled = true;
                const original = btn.textContent;
                btn.textContent = "Mengirim…";
                try {
                    const res = await fetch(`${API_BASE}/api/devices/${encodeURIComponent(btn.dataset.device)}/power/${action}`, {
                        method: "POST",
                    });
                    if (res.ok) {
                        toast(`Perintah ${action} dikirim ke "${btn.dataset.label}"`, "success");
                    } else {
                        const err = await res.json().catch(() => null);
                        toast(err?.detail || `Gagal kirim perintah ${action}`, "error");
                    }
                } catch (err) {
                    toast("Gagal konek ke server", "error");
                } finally {
                    btn.disabled = false;
                    btn.textContent = original;
                }
            });
        });
        card.querySelectorAll("[data-delete]").forEach((btn) => {
            btn.addEventListener("click", async () => {
                if (!confirm(`Hapus device "${btn.dataset.label}"?`)) return;
                const res = await fetch(`${API_BASE}/api/devices/${encodeURIComponent(btn.dataset.delete)}`, {
                    method: "DELETE",
                });
                if (res.ok) {
                    toast(`Device "${btn.dataset.label}" dihapus`, "success");
                    refreshData();
                } else {
                    toast("Gagal hapus device", "error");
                }
            });
        });
    }

    // ── Service Rendering (systemd control) ────────────────────
    const SERVICE_STATE_LABEL = {
        active: "Running",
        inactive: "Stopped",
        failed: "Failed",
        activating: "Starting…",
        deactivating: "Stopping…",
    };

    function svcStatusClass(activeState) {
        if (activeState === "active") return "running";
        if (activeState === "failed") return "failed";
        return "stopped";
    }

    async function renderServices(list) {
        const grid = $("#service-grid");
        const count = $("#service-count");
        if (!Array.isArray(list)) {
            grid.innerHTML = '<div class="bot-card"><p style="color:var(--text-muted)">Gagal load service list — coba restart monitor-api.</p></div>';
            serviceCardMap.clear();
            if (count) count.textContent = "—";
            return;
        }
        if (count) count.textContent = `${list.length} service`;

        const currentIds = new Set(list.map((s) => s.id));
        for (const [id, cached] of serviceCardMap) {
            if (!currentIds.has(id)) {
                cached.el.remove();
                serviceCardMap.delete(id);
            }
        }

        list.forEach((s, i) => {
            const cached = serviceCardMap.get(s.id);
            if (cached) {
                const prev = cached.data;
                const changed =
                    prev.active_state !== s.active_state ||
                    prev.label !== s.label ||
                    prev.unit !== s.unit ||
                    prev.icon !== s.icon;
                if (changed) {
                    const cls = svcStatusClass(s.active_state);
                    cached.el.className = `bot-card ${cls === "running" ? "active" : cls === "failed" ? "failed" : "inactive"}`;
                    cached.el.querySelector(".bot-name").textContent = s.label;
                    cached.el.querySelector(".bot-type").textContent = s.unit;
                    cached.el.querySelector(".svc-status").className = `svc-status ${cls}`;
                    cached.el.querySelector(".svc-status").textContent =
                        SERVICE_STATE_LABEL[s.active_state] || s.active_state;
                    const isRunning = s.active_state === "active";
                    cached.el.querySelector(".bot-actions").innerHTML = `
                        ${isRunning
                            ? `<button class="btn-action stop" data-svc-action="stop" data-id="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">⏹ Stop</button>`
                            : `<button class="btn-action start" data-svc-action="start" data-id="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">▶ Start</button>`}
                        <button class="btn-action restart" data-svc-action="restart" data-id="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">🔁 Restart</button>
                        <button class="btn-action logs" data-svc-logs="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">📜 Logs</button>
                    `;
                    bindServiceActions(cached.el);
                    cached.data = s;
                }
            } else {
                const el = createServiceCard(s, i);
                serviceCardMap.set(s.id, { data: s, el });
                grid.appendChild(el);
            }
        });

        syncChildren(grid, list.map((s) => serviceCardMap.get(s.id).el));
    }

    function createServiceCard(s, i) {
        const cls = svcStatusClass(s.active_state);
        const card = document.createElement("div");
        card.className = `bot-card ${cls === "running" ? "active" : cls === "failed" ? "failed" : "inactive"}`;
        card.style.animationDelay = `${i * 0.08}s`;
        card.setAttribute("data-id", s.id);
        const isRunning = s.active_state === "active";
        card.innerHTML = `
            <div class="bot-card-top">
                <div class="bot-info">
                    <div class="bot-icon">${escapeHtml(s.icon || "⚙️")}</div>
                    <div>
                        <div class="bot-name">${escapeHtml(s.label)}</div>
                        <div class="bot-type">${escapeHtml(s.unit)}</div>
                    </div>
                </div>
                <span class="svc-status ${cls}">${escapeHtml(SERVICE_STATE_LABEL[s.active_state] || s.active_state)}</span>
            </div>
            <div class="bot-actions">
                ${isRunning
                    ? `<button class="btn-action stop" data-svc-action="stop" data-id="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">⏹ Stop</button>`
                    : `<button class="btn-action start" data-svc-action="start" data-id="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">▶ Start</button>`}
                <button class="btn-action restart" data-svc-action="restart" data-id="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">🔁 Restart</button>
                <button class="btn-action logs" data-svc-logs="${escapeHtml(s.id)}" data-label="${escapeHtml(s.label)}">📜 Logs</button>
            </div>
        `;
        bindServiceActions(card);
        return card;
    }

    function bindServiceActions(card) {
        card.querySelectorAll("[data-svc-action]").forEach((btn) => {
            btn.addEventListener("click", async () => {
                const action = btn.dataset.svcAction;
                const id = btn.dataset.id;
                const label = btn.dataset.label;
                const verbId = { start: "start", stop: "stop", restart: "restart" }[action];
                let warn = `Yakin mau ${verbId} "${label}"?`;
                if (id === "monitor-api") {
                    warn += " Dashboard ini bakal sempat keputus koneksi pas API-nya restart.";
                }
                if (!confirm(warn)) return;
                const original = btn.textContent;
                card.querySelectorAll("button").forEach((b) => (b.disabled = true));
                btn.textContent = "Memproses…";
                try {
                    const res = await fetch(`${API_BASE}/api/services/${encodeURIComponent(id)}/action/${action}`, {
                        method: "POST",
                    });
                    if (res.ok) {
                        toast(`Service "${label}" — ${action} berhasil dikirim`, "success");
                    } else {
                        const err = await res.json().catch(() => null);
                        toast(err?.detail || `Gagal ${action} service`, "error");
                    }
                } catch (err) {
                    toast("Gagal konek ke server", "error");
                } finally {
                    setTimeout(refreshData, 800);
                }
            });
        });
        card.querySelectorAll("[data-svc-logs]").forEach((btn) => {
            btn.addEventListener("click", () => {
                openLogsModal(btn.dataset.svcLogs, btn.dataset.label);
            });
        });
    }

    // ── Service logs modal (live tail via WebSocket) ────────────
    const logsModal = $("#logs-modal-overlay");
    const logsBox = $("#logs-box");
    const logsTitle = $("#logs-modal-title");
    const logsLiveDot = $("#logs-live-dot");
    let logsSocket = null;

    function closeLogsModal() {
        logsModal.classList.add("hidden");
        if (logsSocket) {
            logsSocket.close();
            logsSocket = null;
        }
        logsLiveDot.classList.remove("on");
    }

    function openLogsModal(serviceId, label) {
        logsTitle.textContent = `Logs — ${label}`;
        logsBox.textContent = "Menyambungkan…";
        logsModal.classList.remove("hidden");

        if (logsSocket) logsSocket.close();
        const wsProto = API_BASE.startsWith("https") ? "wss" : "ws";
        const wsUrl = `${wsProto}://${window.location.host}/ws/logs/${encodeURIComponent(serviceId)}`;
        logsSocket = new WebSocket(wsUrl);
        let firstLine = true;

        logsSocket.addEventListener("open", () => {
            logsLiveDot.classList.add("on");
        });
        logsSocket.addEventListener("message", (ev) => {
            if (firstLine) {
                logsBox.textContent = "";
                firstLine = false;
            }
            const atBottom = logsBox.scrollTop + logsBox.clientHeight >= logsBox.scrollHeight - 20;
            logsBox.textContent += ev.data + "\n";
            if (atBottom) logsBox.scrollTop = logsBox.scrollHeight;
        });
        logsSocket.addEventListener("close", () => {
            logsLiveDot.classList.remove("on");
        });
        logsSocket.addEventListener("error", () => {
            toast("Koneksi log terputus", "error");
        });
    }

    if (logsModal) {
        $("#logs-close").addEventListener("click", closeLogsModal);
        logsModal.addEventListener("click", (e) => {
            if (e.target === logsModal) closeLogsModal();
        });
    }

    // ── Add Device modal ───────────────────────────────────────
    const deviceModal = $("#device-modal-overlay");
    const deviceForm = $("#device-form");
    const deviceError = $("#d-error");
    const protocolSelect = $("#d-protocol");
    const authTypeSelect = $("#d-auth-type");
    const authTypeWrap = $("#d-authtype-wrap");
    const passwordWrap = $("#d-password-wrap");
    const keyWrap = $("#d-key-wrap");
    const macWrap = $("#d-mac-wrap");
    const hostInput = $("#d-host");
    const portWrap = $("#d-port-wrap");
    const usernameWrap = $("#d-username-wrap");
    const usernameInput = $("#d-username");
    const portInput = $("#d-port");
    const osSelect = $("#d-os");
    const submitBtn = $("#d-submit");
    const sshPowerWrap = $("#d-ssh-power-wrap");
    const sshPowerCheckbox = $("#d-ssh-power");

    function openDeviceModal() {
        deviceForm.reset();
        deviceError.textContent = "";
        updateProtocolFieldVisibility();
        deviceModal.classList.remove("hidden");
        $("#d-label").focus();
    }

    function closeDeviceModal() {
        deviceModal.classList.add("hidden");
    }

    function updateAuthFieldVisibility() {
        const isKey = authTypeSelect.value === "key";
        passwordWrap.classList.toggle("hidden", isKey);
        keyWrap.classList.toggle("hidden", !isKey);
    }

    // For "wol" devices, the SSH fields (port/username/auth) are only
    // relevant if "enable_ssh_power" is checked — WOL itself needs none of
    // that, it's just a MAC address.
    function updateSshFieldVisibility() {
        const protocol = protocolSelect.value;
        const isWol = protocol === "wol";
        const isAndroid = protocol === "android";
        
        // Android needs Host & Port, NO username/auth.
        // SSH needs all.
        // WOL needs all IF sshPowerCheckbox is checked.
        const showSsh = protocol === "ssh" || (isWol && sshPowerCheckbox.checked);
        const showHostPort = protocol === "android" || showSsh;
        
        portWrap.classList.toggle("hidden", !showHostPort);
        hostInput.required = showHostPort;
        
        usernameWrap.classList.toggle("hidden", !showSsh);
        authTypeWrap.classList.toggle("hidden", !showSsh);
        usernameInput.required = showSsh;
        
        if (showSsh) {
            updateAuthFieldVisibility();
        } else {
            passwordWrap.classList.add("hidden");
            keyWrap.classList.add("hidden");
        }
    }

    function updateProtocolFieldVisibility() {
        const protocol = protocolSelect.value;
        const isWol = protocol === "wol";
        const isAndroid = protocol === "android";
        macWrap.classList.toggle("hidden", !isWol);
        sshPowerWrap.classList.toggle("hidden", !isWol);
        if (!isWol) sshPowerCheckbox.checked = false;
        
        updateSshFieldVisibility();
        
        if (isWol) {
            osSelect.value = "windows";
        } else if (isAndroid) {
            portInput.value = 5555;
            osSelect.value = "android";
        } else {
            portInput.value = 22;
            // Switching away from Android: "android" is not a valid OS for an
            // SSH device, and leaving it set breaks the power commands.
            if (osSelect.value === "android") osSelect.value = "linux";
        }
    }

    if (deviceModal) {
        $("#btn-add-device").addEventListener("click", openDeviceModal);
        $("#d-cancel").addEventListener("click", closeDeviceModal);
        deviceModal.addEventListener("click", (e) => {
            if (e.target === deviceModal) closeDeviceModal();
        });
        authTypeSelect.addEventListener("change", updateAuthFieldVisibility);
        protocolSelect.addEventListener("change", updateProtocolFieldVisibility);
        sshPowerCheckbox.addEventListener("change", updateSshFieldVisibility);

        deviceForm.addEventListener("submit", async (e) => {
            e.preventDefault();
            deviceError.textContent = "";

            const protocol = protocolSelect.value;
            const showSsh = protocol === "ssh" || (protocol === "wol" && sshPowerCheckbox.checked);
            const showPortOnly = protocol === "android";
            const payload = {
                label: $("#d-label").value.trim(),
                icon: $("#d-icon").value.trim() || undefined,
                os: osSelect.value,
                protocol,
                host: $("#d-host").value.trim(),
            };
            if (protocol === "wol") {
                payload.mac_address = $("#d-mac").value.trim();
                payload.enable_ssh_power = sshPowerCheckbox.checked;
            }
            if (showSsh || showPortOnly) {
                payload.port = parseInt(portInput.value, 10) || (protocol === "android" ? 5555 : 22);
            }
            if (showSsh) {
                payload.username = $("#d-username").value.trim();
                payload.auth_type = authTypeSelect.value;
                if (payload.auth_type === "key") {
                    payload.key_path = $("#d-key-path").value.trim();
                } else {
                    payload.password = $("#d-password").value;
                }
            }

            submitBtn.disabled = true;
            submitBtn.textContent = "Menyimpan…";
            try {
                const res = await fetch(`${API_BASE}/api/devices`, {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify(payload),
                });
                if (res.ok) {
                    closeDeviceModal();
                    toast(`Device "${payload.label}" ditambahkan`, "success");
                    refreshData();
                } else {
                    const err = await res.json().catch(() => null);
                    deviceError.textContent = err?.detail
                        ? (Array.isArray(err.detail) ? err.detail.map((d) => d.msg).join(", ") : err.detail)
                        : "Gagal nambah device";
                }
            } catch (err) {
                deviceError.textContent = "Gagal konek ke server";
            } finally {
                submitBtn.disabled = false;
                submitBtn.textContent = "Simpan";
            }
        });
    }

    // ── System Rendering ────────────────────────────────────────
    function renderSystem(data) {
        if (!data) return;
        $("#sys-hostname").textContent = data.hostname || "—";

        // CPU
        $("#cpu-value").textContent = `${data.cpu.percent}%`;
        setRing("cpu-ring", data.cpu.percent);
        const load = data.cpu.load_avg.map((l) => l.toFixed(2)).join(" / ");
        $("#cpu-detail").textContent = `${data.cpu.count} cores · Load ${load}`;

        // RAM
        $("#ram-value").textContent = `${data.memory.percent}%`;
        setRing("ram-ring", data.memory.percent);
        $("#ram-detail").textContent = `${data.memory.used_gb} / ${data.memory.total_gb} GB`;

        // Disk
        $("#disk-value").textContent = `${data.disk.percent}%`;
        setRing("disk-ring", data.disk.percent);
        $("#disk-detail").textContent = `${data.disk.used_gb} / ${data.disk.total_gb} GB`;

        // Temperature
        const tempEntries = Object.entries(data.temperature || {});
        if (tempEntries.length > 0) {
            // Find CPU temp (prefer entries with "Core" or "Package")
            let mainTemp = null;
            for (const [label, t] of tempEntries) {
                if (/package|core 0|cpu/i.test(label)) {
                    mainTemp = t;
                    break;
                }
            }
            if (!mainTemp) mainTemp = tempEntries[0][1];
            $("#temp-value").textContent = `${mainTemp.current.toFixed(0)}°C`;
            // Show all temps
            const details = tempEntries
                .slice(0, 4)
                .map(([l, t]) => `${l}: ${t.current.toFixed(0)}°`)
                .join(" · ");
            $("#temp-detail").textContent = details;
        } else {
            $("#temp-value").textContent = "N/A";
            $("#temp-detail").textContent = "No sensor data";
        }

        // Uptime
        $("#sys-uptime").textContent = formatUptime(data.uptime_seconds);

        // Network
        $("#net-sent").textContent = formatBytes(data.bytes_sent_delta || data.network?.bytes_sent);
        $("#net-recv").textContent = formatBytes(data.bytes_recv_delta || data.network?.bytes_recv);
    }

    // ── Chart (simple canvas) ───────────────────────────────────
    function renderChart(historyData) {
        const canvas = document.getElementById("history-chart");
        if (!canvas || !historyData) return;
        const ctx = canvas.getContext("2d");
        const dpr = window.devicePixelRatio || 1;
        const rect = canvas.getBoundingClientRect();
        canvas.width = rect.width * dpr;
        canvas.height = rect.height * dpr;
        ctx.scale(dpr, dpr);
        const W = rect.width;
        const H = rect.height;

        ctx.clearRect(0, 0, W, H);

        const cpuData = historyData.cpu || [];
        const ramData = historyData.ram || [];

        if (cpuData.length < 2) {
            ctx.fillStyle = "rgba(148, 163, 184, 0.3)";
            ctx.font = "13px Inter, sans-serif";
            ctx.textAlign = "center";
            ctx.fillText("Collecting data...", W / 2, H / 2);
            return;
        }

        const pad = { top: 10, right: 10, bottom: 25, left: 35 };
        const cW = W - pad.left - pad.right;
        const cH = H - pad.top - pad.bottom;

        // Grid lines
        ctx.strokeStyle = "rgba(255,255,255,0.04)";
        ctx.lineWidth = 1;
        for (let i = 0; i <= 4; i++) {
            const y = pad.top + (cH / 4) * i;
            ctx.beginPath();
            ctx.moveTo(pad.left, y);
            ctx.lineTo(W - pad.right, y);
            ctx.stroke();
            // Labels
            ctx.fillStyle = "rgba(148, 163, 184, 0.5)";
            ctx.font = "10px Inter, sans-serif";
            ctx.textAlign = "right";
            ctx.fillText(`${100 - i * 25}%`, pad.left - 5, y + 3);
        }

        function drawLine(data, color, alpha = 1) {
            if (data.length < 2) return;
            const maxPoints = data.length;
            ctx.setLineDash([]);
            ctx.strokeStyle = color;
            ctx.lineWidth = 2;
            ctx.globalAlpha = alpha;
            ctx.beginPath();
            data.forEach((p, i) => {
                const x = pad.left + (i / (maxPoints - 1)) * cW;
                const y = pad.top + cH * (1 - p.v / 100);
                if (i === 0) ctx.moveTo(x, y);
                else ctx.lineTo(x, y);
            });
            ctx.stroke();

            // Fill gradient
            ctx.globalAlpha = alpha * 0.1;
            ctx.lineTo(pad.left + cW, pad.top + cH);
            ctx.lineTo(pad.left, pad.top + cH);
            ctx.closePath();
            ctx.fillStyle = color;
            ctx.fill();
            ctx.globalAlpha = 1;
        }

        drawLine(cpuData, "#3b82f6", 0.9);
        drawLine(ramData, "#a855f7", 0.7);

        // Legend
        ctx.font = "10px Inter, sans-serif";
        ctx.fillStyle = "#3b82f6";
        ctx.textAlign = "left";
        ctx.fillText("● CPU", pad.left, H - 5);
        ctx.fillStyle = "#a855f7";
        ctx.fillText("● RAM", pad.left + 50, H - 5);
    }

    // ── Top processes (task-manager style) ──────────────────────
    // Friendlier labels for common process names; also strips version suffix
    // like "next-server (v16.2.1)" → "next-server".
    const PROC_ALIASES = {
        "next-server": "Next.js",
        "node": "Node.js",
        "chrome": "Chrome",
        "python": "Python",
        "python3": "Python",
        "cloudflared": "Cloudflared",
        "livekit-server": "LiveKit",
        "claude": "Claude Code",
        "codex": "Codex",
    };

    function prettyProcName(name) {
        const base = name.replace(/\s*\(v[\d.]+\)\s*$/i, "").trim();
        return PROC_ALIASES[base] || base;
    }

    function renderProcesses(data) {
        const listEl = $("#proc-list");
        if (!listEl) return;
        if (!data || !Array.isArray(data.processes)) {
            listEl.innerHTML = '<p style="color:var(--text-muted);font-size:0.75rem">Gagal load proses</p>';
            procRowMap.clear();
            return;
        }
        const rows = data.processes;
        if (rows.length === 0) {
            listEl.innerHTML = '<p style="color:var(--text-muted);font-size:0.75rem">Tidak ada data proses</p>';
            procRowMap.clear();
            return;
        }

        const currentPids = new Set(rows.map((p) => p.top_pid));
        for (const [pid, cached] of procRowMap) {
            if (!currentPids.has(pid)) {
                cached.el.remove();
                procRowMap.delete(pid);
            }
        }

        rows.forEach((p) => {
            const usage = procSort === "cpu" ? p.cpu : p.mem_percent;
            const heat = usage > 60 ? "hot" : usage > 25 ? "warm" : "";
            const cached = procRowMap.get(p.top_pid);
            if (cached) {
                const prev = cached.data;
                const changed =
                    prev.cpu !== p.cpu ||
                    prev.mem_percent !== p.mem_percent ||
                    prev.mem_bytes !== p.mem_bytes ||
                    prev.count !== p.count ||
                    prev.label !== (p.label || prettyProcName(p.name)) ||
                    prev.project !== p.project ||
                    prev.user !== p.user ||
                    prev.top_pid !== p.top_pid;
                if (changed) {
                    cached.el.className = `proc-row ${heat}`;
                    cached.el.style.setProperty("--usage", `${Math.min(usage, 100)}%`);
                    let valMain, valSub;
                    if (procSort === "cpu") {
                        valMain = `${p.cpu.toFixed(1)}%`;
                        valSub = formatBytes(p.mem_bytes);
                    } else {
                        valMain = formatBytes(p.mem_bytes);
                        valSub = `${p.mem_percent.toFixed(1)}% RAM`;
                    }
                    const countBadge = p.count > 1
                        ? `<span class="proc-count">×${p.count}</span>`
                        : "";
                    const label = p.label || prettyProcName(p.name);
                    const sub = p.project ? prettyProcName(p.name) : (p.user || "system");
                    cached.el.querySelector(".proc-name").innerHTML = `${escapeHtml(label)}${countBadge}`;
                    cached.el.querySelector(".proc-meta").textContent = sub;
                    cached.el.querySelector(".proc-val").innerHTML = `${escapeHtml(valMain)}<span class="proc-val-sub">${escapeHtml(valSub)}</span>`;
                    cached.data = { ...p, label };
                }
            } else {
                const row = document.createElement("div");
                row.className = `proc-row ${heat}`;
                row.style.setProperty("--usage", `${Math.min(usage, 100)}%`);
                row.setAttribute("role", "button");
                row.tabIndex = 0;
                row.setAttribute("data-pid", p.top_pid);

                let valMain, valSub;
                if (procSort === "cpu") {
                    valMain = `${p.cpu.toFixed(1)}%`;
                    valSub = formatBytes(p.mem_bytes);
                } else {
                    valMain = formatBytes(p.mem_bytes);
                    valSub = `${p.mem_percent.toFixed(1)}% RAM`;
                }
                const countBadge = p.count > 1
                    ? `<span class="proc-count">×${p.count}</span>`
                    : "";
                const label = p.label || prettyProcName(p.name);
                const sub = p.project ? prettyProcName(p.name) : (p.user || "system");

                row.innerHTML = `
                    <div class="proc-name-wrap">
                        <span class="proc-name">${escapeHtml(label)}${countBadge}</span>
                        <span class="proc-meta">${escapeHtml(sub)}</span>
                    </div>
                    <div class="proc-val">${escapeHtml(valMain)}<span class="proc-val-sub">${escapeHtml(valSub)}</span></div>
                    <span class="proc-chevron">›</span>
                `;
                const open = () => openProcModal(p.top_pid, label);
                row.addEventListener("click", open);
                row.addEventListener("keydown", (e) => {
                    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); }
                });
                procRowMap.set(p.top_pid, { data: { ...p, label }, el: row });
                listEl.appendChild(row);
            }
        });

        syncChildren(listEl, rows.map((p) => procRowMap.get(p.top_pid).el));
    }

    // toggle CPU / RAM
    $$("[data-proc-sort]").forEach((btn) => {
        btn.addEventListener("click", async () => {
            if (procSort === btn.dataset.procSort) return;
            procSort = btn.dataset.procSort;
            procRowMap.clear();
            const listEl = $("#proc-list");
            if (listEl) listEl.innerHTML = "";
            $$("[data-proc-sort]").forEach((b) => b.classList.toggle("active", b === btn));
            const data = await api(`/api/processes?sort=${procSort}`);
            renderProcesses(data);
        });
    });

    // ── Process detail modal ────────────────────────────────────
    const procModal = $("#proc-modal-overlay");
    const procModalBody = $("#proc-modal-body");
    const procModalTitle = $("#proc-modal-title");

    function closeProcModal() {
        procModal.classList.add("hidden");
    }

    function detailRow(label, value, opts = {}) {
        if (value === undefined || value === null || value === "") return "";
        const cls = opts.mono ? "pd-value mono" : "pd-value";
        return `<div class="pd-row"><span class="pd-label">${escapeHtml(label)}</span><span class="${cls}">${escapeHtml(String(value))}</span></div>`;
    }

    async function openProcModal(pid, label) {
        procModalTitle.textContent = label || "Detail Proses";
        procModalBody.innerHTML = '<p class="pd-loading">Memuat detail…</p>';
        procModal.classList.remove("hidden");

        const d = await api(`/api/processes/${pid}`);
        if (!d || d.detail) {
            procModalBody.innerHTML = `<p class="pd-loading">${escapeHtml(d?.detail || "Gagal memuat detail")}</p>`;
            return;
        }

        const ports = Array.isArray(d.ports) && d.ports.length
            ? d.ports.join(", ")
            : "—";
        const statusMap = { running: "Berjalan", sleeping: "Idle/nunggu", stopped: "Berhenti", zombie: "Zombie", disk_sleep: "Nunggu disk" };
        const statusTxt = statusMap[d.status] || d.status;

        procModalBody.innerHTML = `
            <div class="pd-stats">
                <div class="pd-stat"><span class="pd-stat-val">${d.cpu.toFixed(1)}%</span><span class="pd-stat-lbl">CPU</span></div>
                <div class="pd-stat"><span class="pd-stat-val">${formatBytes(d.mem_bytes)}</span><span class="pd-stat-lbl">RAM (${d.mem_percent}%)</span></div>
                <div class="pd-stat"><span class="pd-stat-val">${formatUptime(d.uptime_seconds)}</span><span class="pd-stat-lbl">Uptime</span></div>
            </div>
            <div class="pd-section">
                ${detailRow("Project", d.project || "—")}
                ${detailRow("Port", ports)}
                ${detailRow("Status", statusTxt)}
                ${detailRow("User", d.user)}
                ${detailRow("PID", d.pid)}
                ${detailRow("Threads", d.num_threads)}
                ${detailRow("Parent", d.parent_name ? `${d.parent_name} (PID ${d.ppid})` : "—")}
            </div>
            <div class="pd-block">
                <span class="pd-label">Folder</span>
                <code class="pd-code">${escapeHtml(d.cwd || "—")}</code>
            </div>
            <div class="pd-block">
                <span class="pd-label">Command</span>
                <code class="pd-code">${escapeHtml(d.cmdline || "—")}</code>
            </div>
        `;
    }

    if (procModal) {
        $("#proc-modal-close").addEventListener("click", closeProcModal);
        procModal.addEventListener("click", (e) => {
            if (e.target === procModal) closeProcModal();
        });
    }

    // ── Refresh logic ───────────────────────────────────────────
    async function refreshData() {
        const now = new Date().toLocaleTimeString("id-ID", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
        lastUpdate.textContent = `Last update: ${now}`;

        if (currentTab === "system") {
            const [sys, hist, procs] = await Promise.all([
                api("/api/system"),
                api("/api/system/history"),
                api(`/api/processes?sort=${procSort}`),
            ]);
            renderSystem(sys);
            renderChart(hist);
            renderProcesses(procs);
        } else if (currentTab === "services") {
            const list = await api("/api/services");
            renderServices(list);
        } else if (currentTab === "devices") {
            const [data, status] = await Promise.all([
                api("/api/devices"),
                api("/api/devices/status"),
            ]);
            const merged = data ? data.map((d) => ({ ...d, online: status ? status[d.id] : undefined })) : data;
            renderDevices(merged);
        }
    }

    function startRefresh() {
        refreshData();
        refreshTimer = setInterval(refreshData, REFRESH_INTERVAL);
    }

    function stopRefresh() {
        if (refreshTimer) clearInterval(refreshTimer);
        refreshTimer = null;
    }

    // Force update service worker (bust old cache-first v1)
    if ("serviceWorker" in navigator) {
        navigator.serviceWorker.getRegistrations().then((regs) => {
            regs.forEach((r) => r.unregister());
        }).then(() => {
            navigator.serviceWorker.register("sw.js?v=6");
        }).catch(() => { });
    }
})();
