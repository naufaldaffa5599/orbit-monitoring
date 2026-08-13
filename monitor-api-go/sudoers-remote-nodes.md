# Kontrol service di node remote

Dashboard bisa **melihat** service di node mana pun cuma dengan kredensial SSH
biasa — nggak perlu setup apa-apa. Yang butuh izin tambahan cuma tombol
**Start / Stop / Restart**, karena itu manggil `systemctl` sebagai root di mesin
tujuan.

Tanpa aturan di bawah, tombolnya tetap ada tapi bakal balik pesan:

```
perlu aturan sudoers di NAS — lihat sudoers-services.md
```

Itu bukan bug. `sudo -n` (non-interactive) sengaja dipakai supaya nggak ada
prompt password yang menggantung — koneksi SSH-nya nggak punya siapa-siapa buat
menjawab prompt itu.

## Linux (NAS, PVE, dan node Linux lain)

Di **mesin tujuan**, bukan di host dashboard:

```bash
sudo visudo -f /etc/sudoers.d/monitor-api-remote
```

Isi dengan salah satu dari dua pilihan berikut.

### Pilihan A — whitelist unit tertentu (paling aman)

Dipakai kalau lo cuma butuh ngontrol beberapa service yang lo tahu namanya.

```sudoers
# Ganti "daffa" dengan user SSH yang dipakai dashboard buat masuk ke mesin ini.
Cmnd_Alias MH_UNITS = \
    /usr/bin/systemctl start docker.service, \
    /usr/bin/systemctl stop docker.service, \
    /usr/bin/systemctl restart docker.service, \
    /usr/bin/systemctl start smbd.service, \
    /usr/bin/systemctl stop smbd.service, \
    /usr/bin/systemctl restart smbd.service

daffa ALL=(root) NOPASSWD: MH_UNITS
```

Kekurangannya: service yang baru lo pasang nanti muncul di dashboard (discovery
memang otomatis) tapi tombolnya bakal gagal sampai lo tambahin ke daftar ini.

### Pilihan B — semua unit (sesuai yang lo pilih: kontrol penuh)

```sudoers
daffa ALL=(root) NOPASSWD: /usr/bin/systemctl start *, \
                           /usr/bin/systemctl stop *, \
                           /usr/bin/systemctl restart *
```

**Sadari risikonya.** Wildcard di sudoers cuma cocokin argumen sebagai teks, dan
sudo nggak jalanin shell — jadi `; rm -rf /` nggak mungkin nyelip. Tapi siapa pun
yang bisa jalanin perintah sebagai user `daffa` di mesin itu jadi bisa
start/stop/restart service apa pun, termasuk mematikan firewall atau sshd.
Praktisnya: user itu setara root buat urusan service.

Lapisan pengaman dari sisi dashboard: nama unit yang dikirim lewat HTTP
divalidasi dulu di `validUnitName()` (`nodesapi.go`) — cuma huruf, angka, dan
`. - _ @ \ :` yang lolos, jadi spasi dan metakarakter shell ditolak sebelum
nyentuh SSH.

### Cek hasilnya

Dari **host dashboard**, sebagai user yang sama:

```bash
ssh daffa@192.168.100.100 'sudo -n systemctl status ssh >/dev/null; echo rc=$?'
```

`rc=0` berarti sudah jalan. Kalau muncul `sudo: a password is required`,
aturannya belum kebaca — cek nama user dan path `/usr/bin/systemctl`
(di beberapa distro ada di `/bin/systemctl`).

## Windows (Laptop Rumah, PC Kamar)

Nggak ada sudoers. Yang menentukan adalah hak akun Windows yang dipakai SSH:

- **Lihat** service dan proses: akun biasa sudah cukup.
- **Start/Stop/Restart** service: akunnya harus anggota **Administrators**.
- **End task** proses milik user lain atau proses sistem: juga butuh admin.

Kalau kurang hak, dashboard bakal nampilin:

```
akses ditolak — user SSH di PC Kamar perlu hak admin
```

Cek dari host dashboard:

```bash
ssh daffa@192.168.100.121 'powershell -NoProfile -Command "([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)"'
```

Balik `True` berarti aman.

## Kenapa nggak dibikin otomatis

Dashboard sengaja **nggak** bisa masang aturan ini sendiri. Kalau bisa, artinya
dia sudah punya akses root di mesin tujuan — persis hak yang aturannya mau
berikan. Memasangnya manual bikin lo yang memutuskan mesin mana yang boleh
dikontrol dari jauh, satu per satu.
