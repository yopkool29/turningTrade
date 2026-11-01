//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type windowsPlatform struct{}

var plat platform = windowsPlatform{}

//go:embed icon.ico
var trayIcon []byte

func (windowsPlatform) payloadDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "TurningTrade Terminal", "payload")
}
func (windowsPlatform) dataDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "TurningTrade")
}
func (windowsPlatform) exeName(base string) string { return base + ".exe" }
func (windowsPlatform) sslCertEnv() []string       { return nil }
func (windowsPlatform) sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

// updateCheckEnabled : pas d'installateur Windows publié sur le bucket de
// releases — le manifest windows y est stale (0.4.5), le check est désactivé.
func (windowsPlatform) updateCheckEnabled() bool { return false }

// ---------- processus ----------

// procs : liste des processus via Toolhelp32 (pid, ppid, nom de l'exe).
func (windowsPlatform) procs() []procInfo {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil
	}
	var out []procInfo
	for {
		out = append(out, procInfo{
			pid:  int(pe.ProcessID),
			ppid: int(pe.ParentProcessID),
			name: windows.UTF16ToString(pe.ExeFile[:]),
		})
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return out
}

// ttProcNames : exécutables TurningTrade (sans le suffixe .exe).
var ttProcNames = map[string]bool{
	"hfq-desktop":  true,
	"hfq-server":   true,
	"rithmic-live": true,
}

func (windowsPlatform) isTTProc(p procInfo) bool {
	name := strings.ToLower(strings.TrimSuffix(p.name, ".exe"))
	if ttProcNames[name] {
		return true
	}
	// le binaire du tt-agent est node.exe — son chemin d'image le trahit
	if name == "node" {
		if path := procImagePath(p.pid); strings.Contains(strings.ToLower(path), `\tt-agent\`) {
			return true
		}
	}
	return false
}

// procImagePath : chemin complet de l'exécutable d'un processus.
func procImagePath(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var buf [windows.MAX_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// pidOnPort : PID écoutant sur un port TCP via GetExtendedTcpTable (iphlpapi).
func (windowsPlatform) pidOnPort(port int) int {
	for _, af := range []uintptr{windows.AF_INET, windows.AF_INET6} {
		if pid := tcpListenerPid(port, af); pid != 0 {
			return pid
		}
	}
	return 0
}

var procGetExtendedTcpTable = windows.NewLazyDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const (
	tcpTableOwnerPidListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	mibTCPStateListen        = 2 // MIB_TCP_STATE_LISTEN
)

func ntohs(v uint32) uint32 { return (v&0xFF)<<8 | (v>>8)&0xFF }

func tcpListenerPid(port int, af uintptr) int {
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, af, tcpTableOwnerPidListener, 0)
	if size == 0 {
		return 0
	}
	buf := make([]byte, size)
	r1, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
		0, af, tcpTableOwnerPidListener, 0)
	if r1 != 0 {
		return 0
	}
	n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
	rows := buf[4:]
	// MIB_TCPROW_OWNER_PID : state@0, localPort@8, owningPid@20 (24 o)
	// MIB_TCP6ROW_OWNER_PID : localPort@20, state@48, owningPid@52 (56 o)
	rowSize, portOff, stateOff, pidOff := 24, 8, 0, 20
	if af == windows.AF_INET6 {
		rowSize, portOff, stateOff, pidOff = 56, 20, 48, 52
	}
	for i := 0; i < n && i*rowSize+rowSize <= len(rows); i++ {
		row := rows[i*rowSize:]
		state := *(*uint32)(unsafe.Pointer(&row[stateOff]))
		lport := *(*uint32)(unsafe.Pointer(&row[portOff]))
		if state == mibTCPStateListen && ntohs(lport) == uint32(port) {
			return int(*(*uint32)(unsafe.Pointer(&row[pidOff])))
		}
	}
	return 0
}

// killTree : taskkill /T tue le PID et toute sa descendance — couvre les fils
// de services externes lancés hors du tray (pas de process-group sous Windows).
func (windowsPlatform) killTree(pid int) {
	hidden(exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")).Run()
	time.Sleep(300 * time.Millisecond)
}

// resumeIfSuspended : pas d'équivalent SIGCONT/SIGSTOP sous Windows — no-op.
func (windowsPlatform) resumeIfSuspended(pid int) {}

// hidden évite le flash d'une fenêtre console quand le tray est lancé en GUI.
func hidden(c *exec.Cmd) *exec.Cmd {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c
}

// ---------- instance unique ----------

var instanceMutex windows.Handle

func (windowsPlatform) singleInstance() {
	name, err := windows.UTF16PtrFromString(`Local\tt-tray-turningtrade`)
	if err != nil {
		return
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		fmt.Fprintln(os.Stderr, "tt-tray tourne déjà")
		os.Exit(1)
	}
	instanceMutex = h
}

// ---------- bureau ----------

func (windowsPlatform) openURL(u string) {
	hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", u)).Start()
}
func (windowsPlatform) openLocal(path string) {
	hidden(exec.Command("explorer", path)).Start()
}
func (windowsPlatform) notify(title, msg string) {
	log.Printf("%s : %s", title, msg)
}

// ---------- versions ----------

// installedVersion : DisplayVersion de « TurningTrade Terminal » dans les clés
// Uninstall du registre (HKCU puis HKLM, vues 64 et 32 bits).
func (windowsPlatform) installedVersion() string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			if v := uninstallVersion(root, view); v != "" {
				return v
			}
		}
	}
	return ""
}

func uninstallVersion(root registry.Key, view uint32) string {
	k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|view)
	if err != nil {
		return ""
	}
	defer k.Close()
	subs, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return ""
	}
	for _, s := range subs {
		sk, err := registry.OpenKey(k, s, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		dn, _, _ := sk.GetStringValue("DisplayName")
		v := ""
		if strings.EqualFold(dn, "TurningTrade Terminal") {
			v, _, _ = sk.GetStringValue("DisplayVersion")
		}
		sk.Close()
		if v != "" {
			return v
		}
	}
	return ""
}

// latestPublished : manifests Tauri seulement — pas de .deb côté Windows.
func (windowsPlatform) latestPublished() string { return latestManifestPublished() }

