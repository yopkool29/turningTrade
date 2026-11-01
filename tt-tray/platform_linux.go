//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// debURL : .deb publié sur le site (nom fixe, écrasé à chaque release).
const debURL = manifestBase + "/TurningTrade-Terminal-Linux-amd64.deb"

type linuxPlatform struct{}

var plat platform = linuxPlatform{}

//go:embed icon.png
var trayIcon []byte

func (linuxPlatform) payloadDir() string { return "/usr/lib/TurningTrade Terminal/payload" }
func (linuxPlatform) dataDir() string {
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "TurningTrade")
}
func (linuxPlatform) exeName(base string) string { return base }
func (linuxPlatform) sslCertEnv() []string {
	return []string{
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"SSL_CERT_DIR=/etc/ssl/certs",
	}
}
func (linuxPlatform) sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
func (linuxPlatform) updateCheckEnabled() bool { return true }

// ---------- processus ----------

// procs liste les processus depuis /proc : nom = comm, path = cmdline.
// Le binaire du tt-agent se renomme "MainThread" — il est identifié par sa
// cmdline et normalisé en "tt-agent".
func (linuxPlatform) procs() []procInfo {
	entries, _ := os.ReadDir("/proc")
	out := make([]procInfo, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p := procInfo{pid: pid}
		if st, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat")); err == nil {
			if i := strings.LastIndexByte(string(st), ')'); i >= 0 {
				if f := strings.Fields(string(st[i+2:])); len(f) >= 2 {
					p.ppid, _ = strconv.Atoi(f[1])
				}
			}
		}
		if comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm")); err == nil {
			p.name = strings.TrimSpace(string(comm))
		}
		if cl, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline")); err == nil {
			p.path = string(cl)
			if strings.Contains(p.path, "tt-agent/server/index.js") {
				p.name = "tt-agent"
			}
		}
		out = append(out, p)
	}
	return out
}

var ttProcNames = map[string]bool{
	"hfq-desktop":  true,
	"hfq-server":   true,
	"rithmic-live": true,
	"tt-agent":     true,
}

func (linuxPlatform) isTTProc(p procInfo) bool { return ttProcNames[p.name] }

// pidOnPort retrouve le PID qui écoute sur un port TCP (via /proc, sans dépendance).
func (linuxPlatform) pidOnPort(port int) int {
	inode := listeningInode(port)
	if inode == "" {
		return 0
	}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			l, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
			if err == nil && l == "socket:["+inode+"]" {
				return pid
			}
		}
	}
	return 0
}

func listeningInode(port int) string {
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			// local_address = IP:PORT hex, st 0A = LISTEN, inode = champ 9
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			if strings.HasSuffix(fields[1], fmt.Sprintf(":%04X", port)) {
				return fields[9]
			}
		}
	}
	return ""
}

// killTree tue le PID — et tout son process-group seulement s'il en est le
// chef (groupe dédié, ex. nos services lancés avec Setpgid). Si le PID partage
// le groupe d'un lanceur ou de la session (hfq-desktop ouvert via appfinder…),
// on ne tue que le PID : un kill groupé emporterait des processus sans rapport
// (xfsettingsd → raccourcis clavier morts).
func (p linuxPlatform) killTree(pid int) {
	target := pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid && pgid != p.pgidSelf() {
		target = -pgid // chef d'un groupe dédié → kill groupé
	}
	syscall.Kill(target, syscall.SIGTERM)
	time.Sleep(400 * time.Millisecond)
	syscall.Kill(target, syscall.SIGKILL)
}

func (linuxPlatform) pgidSelf() int {
	if g, err := syscall.Getpgid(os.Getpid()); err == nil {
		return g
	}
	return 0
}

// resumeIfSuspended : un fils gelé (SIGSTOP/SIGTSTP → état T) est réveillé.
func (linuxPlatform) resumeIfSuspended(pid int) {
	st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return
	}
	if i := strings.LastIndexByte(string(st), ')'); i >= 0 && i+2 < len(st) && st[i+2] == 'T' {
		log.Printf("processus suspendu pid=%d (état T) → SIGCONT", pid)
		syscall.Kill(pid, syscall.SIGCONT)
	}
}

// ---------- instance unique ----------

func (linuxPlatform) singleInstance() {
	f, err := os.OpenFile(filepath.Join(dataDir, "tt-tray.lock"),
		os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		fmt.Fprintln(os.Stderr, "tt-tray tourne déjà")
		os.Exit(1)
	}
}

// ---------- bureau ----------

func (linuxPlatform) openURL(u string) { exec.Command("xdg-open", u).Start() }
func (p linuxPlatform) openLocal(path string) {
	p.openURL("file://" + path)
}
func (linuxPlatform) notify(title, msg string) {
	exec.Command("notify-send", title, msg).Run()
}

// ---------- versions ----------

func (linuxPlatform) installedVersion() string {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Version}", "turning-trade-terminal").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// latestPublished : version du .deb publié sur le site (nom de fichier fixe,
// écrasé à chaque release). Repli sur les manifests Tauri si la lecture échoue.
func (linuxPlatform) latestPublished() string {
	if v := linuxPublishedDebVersion(); v != "" {
		return v
	}
	return latestManifestPublished()
}

// linuxPublishedDebVersion : lit le champ Version du control.tar en tête du
// .deb publié — une requête Range de 256 Ko suffit, pas besoin des ~500 Mo.
func linuxPublishedDebVersion() string {
	req, err := http.NewRequest("GET", debURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Range", "bytes=0-262143")
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return ""
	}
	return debControlVersion(head)
}

// debControlVersion : parcourt les membres de l'archive ar et extrait la
// Version du fichier control (control.tar.gz uniquement — xz/zst non gérés).
func debControlVersion(buf []byte) string {
	if len(buf) < 68 || string(buf[:8]) != "!<arch>\n" {
		return ""
	}
	pos := 8
	for pos+60 <= len(buf) {
		hdr := buf[pos : pos+60]
		name := strings.TrimSpace(string(hdr[:16]))
		size, err := strconv.Atoi(strings.TrimSpace(string(hdr[48:58])))
		if err != nil || size < 0 {
			return ""
		}
		body := buf[pos+60:]
		if len(body) > size {
			body = body[:size]
		}
		if strings.HasPrefix(name, "control.tar") {
			return tarControlVersion(name, body)
		}
		pos += 60 + size + size%2
	}
	return ""
}

func tarControlVersion(name string, blob []byte) string {
	var r io.Reader = bytes.NewReader(blob)
	switch {
	case strings.HasSuffix(name, ".gz"):
		gz, err := gzip.NewReader(r)
		if err != nil {
			return ""
		}
		defer gz.Close()
		r = gz
	case !strings.HasSuffix(name, ".tar"):
		return ""
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			return ""
		}
		if filepath.Base(h.Name) != "control" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 64<<10))
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "Version:"); ok {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
}
