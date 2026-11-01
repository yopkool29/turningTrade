//go:build windows

package main

import (
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPidOnPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if pid := plat.pidOnPort(port); pid != os.Getpid() {
		t.Fatalf("pidOnPort(%d) = %d, attendu %d (process de test)", port, pid, os.Getpid())
	}
	if got := plat.pidOnPort(9); got != 0 {
		t.Fatalf("pidOnPort(9) = %d, attendu 0 (port libre)", got)
	}
}

func TestProcsAndKill(t *testing.T) {
	// processus fils à tuer — powershell sleep survit sans console
	c := exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep", "-Seconds", "60")
	if err := c.Start(); err != nil {
		t.Skip("powershell indispo")
	}
	time.Sleep(1500 * time.Millisecond)
	pid := c.Process.Pid
	found := false
	var ppid int
	for _, p := range plat.procs() {
		if p.pid == pid {
			found, ppid = true, p.ppid
		}
	}
	if !found {
		t.Fatalf("procs() ne liste pas le fils pid=%d", pid)
	}
	t.Logf("fils pid=%d ppid=%d", pid, ppid)
	plat.killTree(pid)
	time.Sleep(500 * time.Millisecond)
	for _, p := range plat.procs() {
		if p.pid == pid {
			t.Fatalf("pid=%d encore vivant après killTree", pid)
		}
	}
}

func TestInstalledVersion(t *testing.T) {
	if v := plat.installedVersion(); v == "" {
		t.Skip("pas de clé Uninstall trouvée")
	} else {
		t.Logf("version installée = %s", v)
	}
}
