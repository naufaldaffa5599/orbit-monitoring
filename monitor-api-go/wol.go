package main

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"time"
)

// wolPacket builds the standard magic packet: 6 × 0xFF followed by the target
// MAC repeated 16 times.
func wolPacket(macAddress string) ([]byte, error) {
	macBytes, err := hex.DecodeString(macAddress)
	if err != nil {
		return nil, err
	}
	if len(macBytes) != 6 {
		return nil, fmt.Errorf("MAC harus 6 byte, dapat %d", len(macBytes))
	}
	packet := make([]byte, 0, 6+16*6)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, macBytes...)
	}
	return packet, nil
}

// sendWOL broadcasts the magic packet on UDP/9.
func sendWOL(macAddress string) error {
	packet, err := wolPacket(macAddress)
	if err != nil {
		return err
	}

	conn, err := net.DialTimeout("udp", "255.255.255.255:9", 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(packet)
	return err
}

func handleWake(w http.ResponseWriter, r *http.Request) error {
	d := devices.get(r.PathValue("device_id"))
	if d == nil {
		return errf(404, "device not found")
	}
	if d.Protocol != "wol" {
		return errf(400, "device ini bukan device Wake on LAN")
	}
	if d.MACAddress == "" {
		return errf(400, "device tidak punya MAC address")
	}
	if err := sendWOL(d.MACAddress); err != nil {
		return errf(500, "Gagal mengirim magic packet: "+err.Error())
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// WOL can only power on an already-off/sleeping machine — the NIC only listens
// for magic packets while the OS is down. Restarting/shutting down a machine
// that is already on needs a real command over a live connection, so this
// piggybacks on the same SSH creds used by the "ssh" protocol (attached to a
// "wol" device via enable_ssh_power).
var powerCommands = map[[2]string]string{
	{"windows", "restart"}:  "shutdown /r /t 0",
	{"windows", "shutdown"}: "shutdown /s /t 0",
	{"linux", "restart"}:    "sudo reboot",
	{"linux", "shutdown"}:   "sudo poweroff",
}

func handlePowerAction(w http.ResponseWriter, r *http.Request) error {
	action := r.PathValue("action")
	if action != "restart" && action != "shutdown" {
		return errf(400, "action must be 'restart' or 'shutdown'")
	}
	d := devices.get(r.PathValue("device_id"))
	if d == nil {
		return errf(404, "device not found")
	}
	if !d.hasSSH() {
		return errf(400, "device ini belum diset kredensial SSH buat kontrol power")
	}

	osName := d.OS
	if osName == "" {
		osName = "linux"
	}
	command, ok := powerCommands[[2]string{osName, action}]
	if !ok {
		return errf(400, "OS '"+osName+"' tidak didukung buat power action")
	}

	client, err := sshConnect(d)
	if err != nil {
		return errf(500, "Gagal kirim perintah: "+err.Error())
	}
	defer client.Close()

	// Fire and forget: the box goes down mid-command, so waiting for an exit
	// status would always surface the disconnect as a bogus failure.
	session, err := client.NewSession()
	if err != nil {
		return errf(500, "Gagal kirim perintah: "+err.Error())
	}
	defer session.Close()
	if err := session.Start(command); err != nil {
		return errf(500, "Gagal kirim perintah: "+err.Error())
	}

	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
