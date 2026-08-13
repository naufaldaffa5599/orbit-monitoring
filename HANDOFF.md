# Handoff — Orbit

Paste this whole file as the opening message ke agent baru.

---

Gua lagi ngelanjutin project **Orbit** di `/home/daffa/projects/orbit-monitoring`.
Dashboard buat ngontrol dan mantau homelab gua: satu host Proxmox, VM-VM di
atasnya, plus beberapa mesin fisik (Windows). Disajikan di subdomain `moon`.
Baca dulu semuanya di bawah sebelum ngoding — banyak jebakan di lingkungan ini
yang udah kemakan waktu.

> **Namanya "Orbit", tapi folder dan service-nya masih pakai nama lama**
> (`monitor-api-go`, `monitor-web`, `monitor-api.service`). Itu disengaja:
> aturan sudoers mencocokkan `monitor-api.service` sebagai string persis, jadi
> ganti nama unit langsung mematikan tombol Services di dashboard sekaligus
> perintah restart. Jangan dirapiin tanpa ngurus sudoers duluan.

## Layout repo

```
monitoring/
├── monitor-api-go/     Backend Go — API, SSH, WebSocket terminal, koleksi metrik
├── monitor-web/        Frontend React (INI yang live)
└── monitor-app/        Frontend lama vanilla JS — sudah TIDAK dipakai
```

Backend build jadi binary `monitor-api-go/monitor-api`, jalan sebagai systemd
service `monitor-api.service` di port **8585**. Frontend di-build jadi file
statis dan di-serve langsung sama binary Go itu — nggak ada Node runtime di
produksi. `monitor-api-go/.env` punya `FRONTEND_DIR` yang nunjuk ke
`monitor-web/dist`.

## Status sekarang — semua ini SUDAH jalan dan live

Tree ala Proxmox:

```
Datacenter                    ← halaman pembuka, ringkasan seluruh armada
└── pve (proxmox root)        ← Summary · Services · Apps · Shell
    ├── NAS       #100        ← auto-discovered dari `qm list`
    ├── PROJECT   #200        ← VM tempat API ini jalan (metrik gopsutil lokal)
    ├── Laptop Server         ← Windows: + Task Manager
    └── PC Kamar              ← Windows: + Task Manager
9router                       ← pemakaian API router, bukan mesin
```

- **Auto-discovery VM** dari `qm list` + `qm status` lewat SSH ke host Proxmox.
  VM baru muncul sendiri, VM yang dihapus ilang sendiri.
- **Summary per node**, tiga jalur: gopsutil (host lokal), SSH+shell (Linux),
  SSH+PowerShell (Windows). Semua agentless — nol instalasi di target.
- **Services per node**, dinamis, unit bawaan OS ditandai dan disembunyiin di
  balik toggle (bukan dibuang), plus kotak cari. Start/stop/restart ada.
- **Apps** — health check HTTP. Beda pertanyaan dari Services: yang satu
  "prosesnya idup", yang satu "aplikasinya jawab". Ada strip riwayat, latency,
  fail streak, dan form-nya nyaranin port hasil scan node.
- **Task Manager** buat node Windows: proses + CPU% + RAM + End Task.
- **Shell** = halaman terminal xterm.js, sesi persisten lewat tmux di target.
- **Rename & hapus node**, tombol Wake on LAN / Restart / Shutdown.

## Build & deploy — HAFALIN INI

```bash
cd monitor-web    && npm run build     # → dist/
cd monitor-api-go && ./build.sh        # → monitor-api

sudo -n /usr/bin/systemctl restart monitor-api.service
```

Aturan sudoers-nya cocokin **string persis**. `sudo systemctl restart monitor-api`
(tanpa path absolut, tanpa suffix `.service`) bakal minta password dan gagal.

## Jebakan lingkungan — ini yang bikin gua rugi waktu

1. **Rebuild `dist/` tanpa rebuild binary Go bisa nge-brick produksi.**
   Kalau frontend manggil endpoint baru yang binary lama belum punya, produksi
   langsung rusak begitu `npm run build` selesai. Nambah endpoint = deploy
   dua-duanya bareng. Ini udah kejadian sekali.

2. **JANGAN kasih nama file Go `*_linux.go` atau `*_windows.go`.**
   Go anggap itu build constraint implisit, jadi filenya cuma ikut dikompilasi
   pas `GOOS` cocok. Collector Windows sempat nggak pernah ke-build gara-gara
   ini. Makanya namanya `collectlinux.go` / `collectwindows.go`.

3. **Go toolchain ada di `~/.local/go`** (dipasang manual, nggak ada di package
   manager), di-symlink ke `~/.local/bin/go`. Kalau `go: command not found`,
   itu PATH.

4. **`pkill -f <pola>` bisa bunuh diri sendiri** kalau polanya juga ada di
   command line yang lagi jalan. Matiin proses lewat PID dari `ss -lptn`.

5. **Screenshot headless.** `chrome --headless --screenshot` **nggak pernah
   selesai** di app ini — polling-nya bikin virtual-time nggak pernah idle. Dan
   `--user-data-dir` bikin Chromium hang total. Yang jalan: playwright-core di
   `/tmp/claude-1000/shots/shot.mjs` (browser sudah ada di
   `~/.cache/ms-playwright/chromium-1228/`). **Overlay scrollbar dan favicon
   nggak ikut ke-render di screenshot** — dua hal itu harus dicek manual di
   browser beneran.

6. **`buildTree()` di `nodes.go` ada beberapa titik append** (mesin manual, lalu
   node 9router di ujung). Kalau nambah sesuatu yang harus kena semua node,
   taruh setelah append terakhir — pernah ada patch yang meleset karena nempel
   di tengah dan hasilnya diem-diem nol.

7. **File yang isinya data/kredensial, semua sudah di `.gitignore`:**
   `.env`, `devices.json`, `node-overrides.json`, `checks.json`. Jangan pernah
   di-cat isinya ke output, jangan di-commit.

## Keputusan arsitektur + alasannya

- **Frontend**: Vite + React 19 + TypeScript strict + Tailwind v4 + shadcn/ui
  (preset `radix-nova`). Dua entry point (`index.html`, `terminal.html`), bukan
  router — terminal dibuka pakai `window.open` dan URL-nya harus tetap sama
  kayak versi lama biar bookmark/shortcut HP nggak mati. Bonus: xterm.js cuma
  ke-load di halaman yang butuh.

- **`monitor-web/src/lib/terminal-session.ts` sengaja class biasa, bukan hook.**
  Instance xterm punya canvas + scrollback yang nggak boleh dibangun ulang, dan
  handler touch-nya harus di fase **capture** pada node DOM yang stabil biar
  ngalahin listener bawaan xterm. React cuma pegang chrome-nya. **Jangan
  "React-kan" isi file ini** — logika scroll/select di HP itu hasil beberapa
  iterasi dan gampang banget regresi.

- **Koleksi metrik agentless + cache per node** (`remote.go`). Koneksi SSH
  di-pool, hasil di-cache di balik TTL ~12 detik, dan refresh yang gagal
  **nggak** ngebuang snapshot lama — node yang ngedip sekali tetap nampilin data
  terakhir plus pesan errornya, bukan ngosongin dashboard.

- **VM dicocokin ke device pakai nama** (`linkVM` di `nodes.go`), karena VM-VM
  ini **nggak ada QEMU guest agent** — hypervisor nggak bisa ngasih tau IP guest.
  "NAS.100" → device `nas`; "project" → hostname `project-daffa`. Kalau tebakan
  namanya meleset, isi `"vmid": 100` di entry `devices.json`, itu selalu menang.

- **Nama custom node di `node-overrides.json`,** bukan ditulis balik ke
  `devices.json`. VM hasil discovery bisa nggak punya entry device sama sekali,
  dan `devices.json` isinya kredensial — makin jarang ditulis makin baik.

- **Health check dijalanin dari host dashboard**, bukan dari dalam node yang
  ditandain. Itu ngukur apa yang klien lihat dan nggak butuh curl/agent di
  target. Tag node = "mesin mana yang harus diliat kalau rusak".

- **Unit bawaan OS di-flag, bukan dibuang** (`isSystemUnit`). List service yang
  diem-diem ngilangin sesuatu lebih jahat daripada list yang kepanjangan.

- **Indentasi tree murni dari nesting DOM**, nggak ada aritmetika `depth * n`.
  Versi lama ngaliin depth *sekaligus* nesting kontainer, jadi indent kehitung
  dua kali buat node anak dan sekali buat view — nggak ada yang lurus.

- **Logo** dideskripsikan sekali di
  `monitor-web/src/components/shell/orbit-geometry.json`. Komponen topbar baca
  file itu, dan `npm run icons` generate favicon (`public/orbit.svg`) plus icon
  PWA dari angka yang sama — jangan edit SVG/PNG-nya langsung. Geometrinya
  disetel buat ukuran terkecil: tepi dalam cincin (`ry` − separuh stroke) harus
  bebas dari radius titik, kalau nggak nyatu jadi gumpalan di 16px. Aturan itu
  di-assert di script-nya, jadi angka yang jelek bakal ditolak sebelum nulis
  file.

## Yang belum selesai

1. **Kontrol service di node remote belum aktif.** Tombolnya ada dan backend-nya
   jalan, tapi `sudo -n` di NAS/PVE masih minta password, jadi balik pesan jelas
   *"perlu aturan sudoers di …"*. Langkahnya ada di
   `monitor-api-go/sudoers-remote-nodes.md`. **Jangan dipasang otomatis** — buat
   masang aturan itu, dashboard harus udah punya root di mesin tujuan, persis
   hak yang mau dikasih. Ini keputusan sadar.

2. **`monitor-app/` masih ada**, tinggal dihapus. Sekalian buang route mati
   `GET /android.html` di `main.go`.

3. **Backend ADB/Android masih nyangkut** — `android.go`, `/ws/android/{id}`,
   `adbPort()`, `adbStartServer()`. ~350 baris tanpa pemanggil. Aman dibuang
   tapi nyentuh validasi protokol di `devices.go` dan satu test.

4. **Nggak ada penyimpanan time-series.** Histori CPU/RAM cuma host lokal dan
   in-memory (ilang tiap restart); riwayat check juga cuma ~30 menit terakhir di
   memori. Ini yang ngeblok: grafik per node remote, uptime %, tren bandwidth.
   Nambahin SQLite kecil bakal ngebuka semuanya sekaligus.

5. **Pemisahan "Apps" vs "Background process" ala Task Manager Windows nggak
   mungkin lewat SSH** — ngandelin `MainWindowTitle`, dan sesi SSH jalan di
   session terpisah dari desktop jadi selalu balik kosong. Sudah dites di dua
   mesin.

6. **`laptop-rumah` + `laptop-rumah-ssh`** masih dua entry di `devices.json`
   buat satu mesin (satu WOL, satu SSH). Di tree sudah digabung jadi satu node,
   datanya belum dirapiin.

7. **Rollup status check di halaman Datacenter belum ada** — badge-nya baru di
   tree. Notifikasi kalau ada yang down juga belum.

## Konvensi

- **Komentar kode dalam bahasa Inggris**, jelasin *kenapa* bukan *apa*.
  **Teks UI dalam bahasa Indonesia informal.**
- Go: `gofmt -w .`, `go build ./...`, `go vet ./...`, `go test ./...` sebelum
  bilang selesai. Parser punya test pakai output asli mesin (`nodes_test.go`) —
  kalau ngubah parser, update fixture-nya.
- Frontend: `npx tsc -b` (strict), `npm run lint` (oxlint). Dua warning di
  `badge.tsx`/`button.tsx` itu bawaan shadcn, abaikan.
- **Verifikasi lawan mesin asli, jangan mock.** PVE `192.168.100.99`, NAS
  `.100`, Laptop `.120`, PC Kamar `.121`. Semua kejangkau dari host ini.
- Jangan ngetes pakai data asli gua. Kalau perlu bikin device/check dummy lewat
  API, hapus lagi setelah selesai.
- Gua mau laporan jujur: kalau ada yang gagal atau ke-skip, bilang. Kalau
  ngerusak sesuatu, bilang duluan sebelum gua nemu sendiri.
- Sebelum aksi yang susah dibalik atau nyentuh service produksi, konfirmasi
  dulu — kecuali gua udah bilang gas.

## Yang mau gua kerjain berikutnya

<!-- ISI SENDIRI sebelum di-paste ke agent baru -->
