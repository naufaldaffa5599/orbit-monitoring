# Setup: izin start/stop/restart service dari dashboard

`monitor-api.service` jalan sebagai user `daffa` (bukan root), tapi
`poka-server`, `poka-watch`, `reminder-bot-*`, dan `monitor-api` sendiri
adalah system service yang butuh root buat di-start/stop/restart.

Supaya tombol Start/Stop/Restart di dashboard bisa jalan tanpa diminta
password tiap kali, tambahkan rule sudo yang **dibatasi ketat** — cuma boleh
`systemctl start|stop|restart` buat 5 unit ini, gak ada akses root lain.

## Install

```bash
sudo install -m 0440 /home/daffa/projects/orbit-monitoring/monitor-api/monitor-api-services.sudoers /etc/sudoers.d/monitor-api-services
sudo visudo -c
```

`visudo -c` di akhir buat mastiin syntax-nya valid — kalau ada error,
`/etc/sudoers.d/monitor-api-services` boleh dihapus lagi tanpa efek samping
(gak ada yang lain nyantol ke file ini).

## Kalau mau cabut izin ini lagi

```bash
sudo rm /etc/sudoers.d/monitor-api-services
```

Tombol Start/Stop/Restart di dashboard bakal balik minta password (gagal
dengan pesan "Sudo belum di-setup...") — status & logs tetap jalan normal
karena baca journal gak butuh sudo (user `daffa` udah di grup `adm`).
