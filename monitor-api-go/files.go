package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Browsing and editing files on a node, over SFTP.
//
// Everything else in this file's neighbourhood is a shell command whose output
// gets parsed (see remote.go). File work is the exception, and deliberately so:
// two of the machines here run Windows, and a listing built from `ls` on one
// and `Get-ChildItem` on the other is two parsers, two quoting problems and two
// sets of edge cases for every operation. SFTP is one code path for both, it
// carries size, mode and mtime as structured data rather than as a line to
// scrape, and it moves bytes without a base64 detour.
//
// Windows serves it through the same subsystem, with paths that read
// "/C:/Users/daffa" and a root listing of drives. That happens to behave like
// an ordinary tree, so the UI needs no special case: it starts wherever the
// server says "." is and walks from there.
//
// Copy is the one operation SFTP has no word for, so it goes back through the
// shell — and pays the two-platform cost that only it has to pay.

const (
	// How much of a file the editor will load. Large enough for any config or
	// script worth editing in a browser, small enough that opening a log by
	// accident cannot wedge the tab.
	maxEditBytes = 2 << 20

	// How much of a file is sniffed to decide whether it is text.
	sniffBytes = 8 << 10

	fileOpTimeout = 60 * time.Second
)

// ── SFTP client pool ────────────────────────────────────────────────────────

// An SFTP session rides on a pooled SSH connection, so it is cached the same
// way and for the same reason: the handshake costs far more than the operation.
//
// The SSH client is remembered next to it because poolClient hands back a new
// connection after a redial, and an SFTP session still bound to the dead one
// fails every call until something notices. Comparing the two catches that on
// the next request instead.
type pooledSFTP struct {
	client *sftp.Client
	over   *ssh.Client
}

var sftpPool = struct {
	sync.Mutex
	m map[string]*pooledSFTP
}{m: map[string]*pooledSFTP{}}

func sftpClient(d *Device) (*sftp.Client, error) {
	ssh, err := poolClient(d)
	if err != nil {
		return nil, err
	}

	sftpPool.Lock()
	if ps := sftpPool.m[d.ID]; ps != nil {
		if ps.over == ssh {
			sftpPool.Unlock()
			return ps.client, nil
		}
		ps.client.Close() // bound to a connection that has since been replaced
		delete(sftpPool.m, d.ID)
	}
	sftpPool.Unlock()

	client, err := sftp.NewClient(ssh)
	if err != nil {
		dropClient(d.ID)
		return nil, fmt.Errorf("sftp tidak bisa dibuka: %w", err)
	}
	sftpPool.Lock()
	sftpPool.m[d.ID] = &pooledSFTP{client: client, over: ssh}
	sftpPool.Unlock()
	return client, nil
}

// dropSFTP forgets a device's SFTP session. Called from closeDeviceMonitoring
// so a deleted device leaves nothing behind.
func dropSFTP(deviceID string) {
	sftpPool.Lock()
	if ps := sftpPool.m[deviceID]; ps != nil {
		ps.client.Close()
		delete(sftpPool.m, deviceID)
	}
	sftpPool.Unlock()
}

// ── Shapes ──────────────────────────────────────────────────────────────────

type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mtime"`
	// A symlink reports what it points at, and whether that target is a
	// directory — the tree has to know which one to walk into.
	Symlink bool   `json:"symlink,omitempty"`
	Target  string `json:"target,omitempty"`
}

type fileListing struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []FileEntry `json:"entries"`
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// requestPath reads the path a request is asking about, defaulting to wherever
// the server says "." is — the login user's home on Linux, "/C:/Users/x" on
// Windows. Starting at "/" instead would open every session on a directory
// nobody keeps anything in.
func requestPath(c *sftp.Client, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "~" {
		home, err := c.RealPath(".")
		if err != nil {
			return "", errf(502, "gagal menentukan folder awal: "+err.Error())
		}
		return home, nil
	}
	if !strings.HasPrefix(raw, "/") {
		return "", errf(400, "path harus absolut")
	}
	return path.Clean(raw), nil
}

// parentOf returns the directory above p, stopping at the root rather than
// walking past it. On Windows "/C:" sits directly under "/", which lists the
// drives, so the same walk keeps working there.
func parentOf(p string) string {
	if p == "/" {
		return ""
	}
	parent := path.Dir(p)
	if parent == p {
		return ""
	}
	return parent
}

func entryFrom(c *sftp.Client, dir string, fi os.FileInfo) FileEntry {
	full := path.Join(dir, fi.Name())
	e := FileEntry{
		Name:    fi.Name(),
		Path:    full,
		Dir:     fi.IsDir(),
		Size:    fi.Size(),
		Mode:    fi.Mode().String(),
		ModTime: fi.ModTime().Unix(),
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		e.Symlink = true
		if target, err := c.ReadLink(full); err == nil {
			e.Target = target
			// A link is only useful in a file tree if you can tell whether
			// clicking it opens a folder, so the target is resolved too.
			if st, err := c.Stat(full); err == nil {
				e.Dir = st.IsDir()
				e.Size = st.Size()
			}
		}
	}
	return e
}

// sftpErr turns a failed operation into something a person can act on. The
// library's own errors are terse ("file does not exist") and lose the path.
func sftpErr(op, p string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return errf(404, fmt.Sprintf("%s: %s tidak ada", op, p))
	case errors.Is(err, os.ErrPermission):
		return errf(403, fmt.Sprintf("%s: tidak punya izin ke %s", op, p))
	default:
		return errf(502, fmt.Sprintf("%s %s gagal: %v", op, p, err))
	}
}

// ── Handlers ────────────────────────────────────────────────────────────────

func nodeSFTP(r *http.Request) (*Device, *sftp.Client, error) {
	_, dev, err := resolveNode(r, true)
	if err != nil {
		return nil, nil, err
	}
	if dev.Protocol == "android" {
		return nil, nil, errf(400, "node ini diakses lewat adb, bukan SSH")
	}
	c, err := sftpClient(dev)
	if err != nil {
		return nil, nil, errf(502, err.Error())
	}
	return dev, c, nil
}

func handleNodeFiles(w http.ResponseWriter, r *http.Request) error {
	_, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	dir, err := requestPath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	infos, err := c.ReadDir(dir)
	if err != nil {
		return sftpErr("baca folder", dir, err)
	}

	entries := make([]FileEntry, 0, len(infos))
	for _, fi := range infos {
		entries = append(entries, entryFrom(c, dir, fi))
	}
	// Folders first, then by name, case-insensitively: the order a file
	// manager is expected to have, done here so every client agrees.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	writeJSON(w, 200, fileListing{Path: dir, Parent: parentOf(dir), Entries: entries})
	return nil
}

func handleNodeFileRead(w http.ResponseWriter, r *http.Request) error {
	_, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	p, err := requestPath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	st, err := c.Stat(p)
	if err != nil {
		return sftpErr("baca", p, err)
	}
	if st.IsDir() {
		return errf(400, "itu folder, bukan file")
	}
	if st.Size() > maxEditBytes {
		return errf(413, fmt.Sprintf(
			"file %s terlalu besar buat editor (%s, batas %s) — pakai Download",
			path.Base(p), humanBytes(st.Size()), humanBytes(maxEditBytes)))
	}

	f, err := c.Open(p)
	if err != nil {
		return sftpErr("buka", p, err)
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, maxEditBytes+1))
	if err != nil {
		return sftpErr("baca", p, err)
	}
	text, enc, ok := decodeText(buf)
	if !ok {
		return errf(415, "file ini bukan teks — pakai Download buat ambil isinya")
	}

	writeJSON(w, 200, map[string]any{
		"path": p, "content": text, "size": st.Size(), "encoding": string(enc),
		"mtime": st.ModTime().Unix(), "mode": st.Mode().String(),
	})
	return nil
}

// ── Text encodings ──────────────────────────────────────────────────────────

// How a file's bytes spell its text. Carried out to the editor and back so a
// file is written in whatever it was already written in.
//
// This exists because of Windows. desktop.ini, exported .reg files and
// anything PowerShell redirects to disk are UTF-16LE, which is text that looks
// exactly like a binary to a NUL-byte check: every ASCII character is stored
// as one byte followed by a zero. Rejecting those left two of the machines
// here with files that could be listed and downloaded but never opened.
//
// Only a byte-order mark is trusted. UTF-16 without one is guessable, but the
// guess is wrong often enough — on short files especially — that a wrong
// answer would corrupt the file on save, and there is no BOM-less UTF-16 in
// practice on these machines.
type textEncoding string

const (
	encUTF8    textEncoding = "utf-8"
	encUTF8BOM textEncoding = "utf-8-bom"
	encUTF16LE textEncoding = "utf-16le"
	encUTF16BE textEncoding = "utf-16be"
)

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// decodeText turns a file's bytes into the UTF-8 the editor works in, saying
// which spelling it found. ok is false when the file is not text at all.
func decodeText(b []byte) (string, textEncoding, bool) {
	switch {
	case bytes.HasPrefix(b, bomUTF8):
		body := b[len(bomUTF8):]
		if !looksTextual(body) {
			return "", "", false
		}
		return string(body), encUTF8BOM, true

	case bytes.HasPrefix(b, bomUTF16LE):
		return decodeUTF16(b[2:], binary.LittleEndian, encUTF16LE)

	case bytes.HasPrefix(b, bomUTF16BE):
		return decodeUTF16(b[2:], binary.BigEndian, encUTF16BE)

	default:
		if !looksTextual(b) {
			return "", "", false
		}
		return string(b), encUTF8, true
	}
}

func decodeUTF16(b []byte, order binary.ByteOrder, enc textEncoding) (string, textEncoding, bool) {
	// An odd length cannot be UTF-16, so whatever this is, it is not text
	// that survives a round trip.
	if len(b)%2 != 0 {
		return "", "", false
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = order.Uint16(b[i*2:])
	}
	s := string(utf16.Decode(units))
	// A binary file that happens to open with a BOM decodes into control
	// characters rather than failing, so the result is checked the same way
	// a plain file is.
	if !looksTextual([]byte(s)) {
		return "", "", false
	}
	return s, enc, true
}

// encodeText writes the editor's UTF-8 back out in the file's own spelling.
func encodeText(s string, enc textEncoding) []byte {
	switch enc {
	case encUTF8BOM:
		return append(append([]byte{}, bomUTF8...), s...)
	case encUTF16LE, encUTF16BE:
		order := binary.ByteOrder(binary.LittleEndian)
		out := append([]byte{}, bomUTF16LE...)
		if enc == encUTF16BE {
			order = binary.BigEndian
			out = append([]byte{}, bomUTF16BE...)
		}
		units := utf16.Encode([]rune(s))
		buf := make([]byte, 2)
		for _, u := range units {
			order.PutUint16(buf, u)
			out = append(out, buf...)
		}
		return out
	default:
		return []byte(s)
	}
}

// looksTextual decides whether a run of characters can go in the editor. A NUL
// is the giveaway for binaries, and invalid UTF-8 would not survive the JSON
// round trip intact — writing it back would corrupt the file.
func looksTextual(b []byte) bool {
	head := b
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
	}
	for _, c := range head {
		if c == 0 {
			return false
		}
	}
	return utf8.Valid(b)
}

func handleNodeFileWrite(w http.ResponseWriter, r *http.Request) error {
	_, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	var payload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		// Whatever the read reported. Empty means plain UTF-8, which keeps
		// this compatible with a client that never sends it.
		Encoding string `json:"encoding"`
	}
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}
	p, err := requestPath(c, payload.Path)
	if err != nil {
		return err
	}

	// Written back in the spelling it was read in, so editing one line of a
	// UTF-16 desktop.ini does not quietly rewrite the whole file as UTF-8 and
	// leave Windows unable to read it.
	enc := textEncoding(payload.Encoding)
	switch enc {
	case encUTF8, encUTF8BOM, encUTF16LE, encUTF16BE:
	case "":
		enc = encUTF8
	default:
		return errf(400, "encoding tidak dikenal: "+payload.Encoding)
	}
	body := encodeText(payload.Content, enc)
	// Measured after encoding: UTF-16 doubles, and the limit is about what
	// lands on disk.
	if len(body) > maxEditBytes {
		return errf(413, "isi file melebihi batas editor")
	}

	// Existing mode is preserved: saving a shell script should not quietly
	// take its execute bit away.
	var mode os.FileMode
	st, statErr := c.Stat(p)
	exists := statErr == nil
	if exists {
		mode = st.Mode()
		if st.IsDir() {
			return errf(400, "itu folder, bukan file")
		}
	}

	// A file that already exists is opened without O_CREATE.
	//
	// sftp.Create asks for the equivalent of CREATE_ALWAYS, and Windows
	// refuses that on a file carrying the Hidden or System attribute unless
	// the same attributes are passed back with it — which SFTP has no way to
	// say. desktop.ini is exactly such a file, so saving one came back as
	// "permission denied" on a file the same session had just read.
	var f *sftp.File
	if exists {
		f, err = c.OpenFile(p, os.O_WRONLY|os.O_TRUNC)
	} else {
		f, err = c.Create(p)
	}
	if err != nil {
		return sftpErr("tulis", p, err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return sftpErr("tulis", p, err)
	}
	if err := f.Close(); err != nil {
		return sftpErr("tulis", p, err)
	}
	if mode != 0 {
		_ = c.Chmod(p, mode)
	}

	after, err := c.Stat(p)
	if err != nil {
		return sftpErr("tulis", p, err)
	}
	writeJSON(w, 200, map[string]any{"path": p, "size": after.Size(), "mtime": after.ModTime().Unix()})
	return nil
}

func handleNodeFileMkdir(w http.ResponseWriter, r *http.Request) error {
	_, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}
	p, err := requestPath(c, payload.Path)
	if err != nil {
		return err
	}
	if err := c.Mkdir(p); err != nil {
		return sftpErr("bikin folder", p, err)
	}
	writeJSON(w, 200, map[string]string{"path": p})
	return nil
}

func handleNodeFileMove(w http.ResponseWriter, r *http.Request) error {
	dev, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	from, to, err := fromTo(r, c)
	if err != nil {
		return err
	}
	// Rename is atomic and instant, but only within one filesystem. Dragging
	// something from / to /mnt is exactly the case that fails, and there the
	// shell's mv — which falls back to copy-then-delete — is what people
	// expect to happen.
	if err := c.Rename(from, to); err != nil {
		if serr := shellMove(dev, from, to); serr != nil {
			return errf(502, fmt.Sprintf("pindah %s gagal: %v", path.Base(from), serr))
		}
	}
	writeJSON(w, 200, map[string]string{"path": to})
	return nil
}

func handleNodeFileCopy(w http.ResponseWriter, r *http.Request) error {
	dev, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	from, to, err := fromTo(r, c)
	if err != nil {
		return err
	}
	// SFTP has no copy: doing it here would mean pulling every byte to this
	// server and pushing it back, over the same link, for a file that never
	// needed to leave the machine. The shell copies it in place instead —
	// the one operation that pays for a Windows branch.
	if err := shellCopy(dev, from, to); err != nil {
		return errf(502, fmt.Sprintf("salin %s gagal: %v", path.Base(from), err))
	}
	writeJSON(w, 200, map[string]string{"path": to})
	return nil
}

func fromTo(r *http.Request, c *sftp.Client) (string, string, error) {
	var payload struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(r, &payload); err != nil {
		return "", "", err
	}
	from, err := requestPath(c, payload.From)
	if err != nil {
		return "", "", err
	}
	to, err := requestPath(c, payload.To)
	if err != nil {
		return "", "", err
	}
	if from == to {
		return "", "", errf(400, "asal dan tujuan sama")
	}
	// Moving a directory inside itself leaves both unreachable, and neither
	// mv nor rename refuses it on every platform.
	if strings.HasPrefix(to+"/", from+"/") {
		return "", "", errf(400, "tujuan ada di dalam folder asalnya sendiri")
	}
	return from, to, nil
}

func shellCopy(d *Device, from, to string) error {
	if strings.EqualFold(d.OS, "windows") {
		return runShellOp(d, psCommand(fmt.Sprintf(
			"Copy-Item -LiteralPath %s -Destination %s -Recurse -Force -ErrorAction Stop",
			psQuote(winPath(from)), psQuote(winPath(to)))))
	}
	return runShellOp(d, fmt.Sprintf("cp -a -- %s %s", shQuote(from), shQuote(to)))
}

func shellMove(d *Device, from, to string) error {
	if strings.EqualFold(d.OS, "windows") {
		return runShellOp(d, psCommand(fmt.Sprintf(
			"Move-Item -LiteralPath %s -Destination %s -Force -ErrorAction Stop",
			psQuote(winPath(from)), psQuote(winPath(to)))))
	}
	return runShellOp(d, fmt.Sprintf("mv -- %s %s", shQuote(from), shQuote(to)))
}

func runShellOp(d *Device, cmd string) error {
	code, out, err := runRemote(d, cmd, fileOpTimeout)
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		return fmt.Errorf("%s", firstLine(msg))
	}
	return nil
}

// winPath converts the SFTP spelling of a Windows path back to the one the
// shell there understands: "/C:/Users/x" is what the subsystem reports, and
// "C:\Users\x" is what Copy-Item wants.
func winPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	return strings.ReplaceAll(p, "/", `\`)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func handleNodeFileDelete(w http.ResponseWriter, r *http.Request) error {
	_, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	p, err := requestPath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	if p == "/" {
		return errf(400, "tidak bisa menghapus root")
	}
	st, err := c.Stat(p)
	if err != nil {
		return sftpErr("hapus", p, err)
	}
	if st.IsDir() {
		// A non-empty directory needs the caller to have said so, because the
		// UI asks a different question for it than for a single file.
		if r.URL.Query().Get("recursive") != "1" {
			return errf(409, "folder tidak kosong — konfirmasi dulu")
		}
		if err := c.RemoveAll(p); err != nil {
			return sftpErr("hapus", p, err)
		}
	} else if err := c.Remove(p); err != nil {
		return sftpErr("hapus", p, err)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func handleNodeFileDownload(w http.ResponseWriter, r *http.Request) error {
	_, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	p, err := requestPath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	st, err := c.Stat(p)
	if err != nil {
		return sftpErr("ambil", p, err)
	}
	if st.IsDir() {
		return errf(400, "folder tidak bisa di-download langsung")
	}
	f, err := c.Open(p)
	if err != nil {
		return sftpErr("buka", p, err)
	}
	defer f.Close()

	// Streamed rather than buffered: a disk image should not have to fit in
	// this process's memory on its way to the browser.
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))

	// ?inline=1 is what the picture viewer asks for. An <img> needs a real
	// image type and a disposition that does not tell the browser to save the
	// file instead of drawing it; everything else stays a download, typed as
	// bytes so nothing renders in a tab by accident.
	name := url.PathEscape(path.Base(p))
	if ct := imageType(p); ct != "" && r.URL.Query().Get("inline") == "1" {
		w.Header().Set("Content-Type", ct)
		// SVG is the one image format that is also a document, and one read
		// off a machine's filesystem is not content this app vouches for. The
		// sandbox keeps any script inside it inert even if the URL is opened
		// directly rather than through an <img>.
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+name)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+name)
	}
	if _, err := io.Copy(w, f); err != nil {
		// The response is already going out, so this can only be logged.
		fmt.Printf("⚠️  download %s terputus: %v\n", p, err)
	}
	return nil
}

func handleNodeFileUpload(w http.ResponseWriter, r *http.Request) error {
	_, c, err := nodeSFTP(r)
	if err != nil {
		return err
	}
	dir, err := requestPath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}

	// Streamed for the same reason as download, which rules out ParseMultipartForm:
	// that one spools the whole upload to disk before the handler sees any of it.
	mr, err := r.MultipartReader()
	if err != nil {
		return errf(400, "upload harus multipart/form-data")
	}
	var written []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errf(400, "upload rusak: "+err.Error())
		}
		name := path.Base(part.FileName())
		if name == "" || name == "." || name == "/" {
			part.Close()
			continue
		}
		dst := path.Join(dir, name)
		f, err := c.Create(dst)
		if err != nil {
			part.Close()
			return sftpErr("tulis", dst, err)
		}
		if _, err := io.Copy(f, part); err != nil {
			f.Close()
			part.Close()
			return sftpErr("tulis", dst, err)
		}
		f.Close()
		part.Close()
		written = append(written, name)
	}
	if len(written) == 0 {
		return errf(400, "tidak ada file di upload")
	}
	writeJSON(w, 200, map[string]any{"path": dir, "written": written})
	return nil
}

// imageType reports the MIME type to serve a picture under, or "" when the
// name is not one.
//
// Extension only: sniffing the first bytes would mean an extra round trip to
// the machine before the viewer can even build a URL, and a photo whose name
// lies about its format is not a case worth paying for on every listing.
func imageType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".bmp":
		return "image/bmp"
	case ".ico":
		return "image/x-icon"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
