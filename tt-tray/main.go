// tt-tray — lanceur systray pour TurningTrade (mode headless)
// Démarre hfq-server + rithmic-live + tt-agent sans fenêtre hfq-desktop,
// expose une icône dans la barre système pour ouvrir le Terminal / arrêter.
package main

import (
	_ "embed"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getlantern/systray"
)

//go:embed icon.png
var iconPNG []byte

const (
	payloadDir  = "/usr/lib/TurningTrade Terminal/payload"
	terminalURL = "http://127.0.0.1:8765/hfq-terminal.html"
	agentURL    = "http://127.0.0.1:9199/"
)

var (
	homeDir = os.Getenv("HOME")
	dataDir = filepath.Join(homeDir, ".local", "share", "TurningTrade")
	logDir  = filepath.Join(dataDir, "logs")
)

type service struct {
	name    string
	port    int
	bin     string
	args    []string
	dir     string
	env     []string
	managed bool // false = lancé par hfq-server lui-même (ex. rithmic-live à la connexion)
	cmd     *exec.Cmd
	item    *systray.MenuItem
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// pidOnPort retrouve le PID qui écoute sur un port TCP (via /proc, sans dépendance).
func pidOnPort(port int) int {
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

// killTree tue le process-group du PID (repli : le PID seul).
func killTree(pid int) {
	target := pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 0 && pgid != pidPgidSelf() {
		target = -pgid // ne jamais tuer notre propre groupe
	}
	syscall.Kill(target, syscall.SIGTERM)
	time.Sleep(400 * time.Millisecond)
	syscall.Kill(target, syscall.SIGKILL)
}

func pidPgidSelf() int {
	if g, err := syscall.Getpgid(os.Getpid()); err == nil {
		return g
	}
	return 0
}

// ttCommNames : noms de processus (comm exacts) appartenant à TurningTrade.
var ttCommNames = map[string]bool{
	"hfq-desktop":  true,
	"hfq-server":   true,
	"rithmic-live": true,
}

// sweepAll tue tous les processus TurningTrade, y compris ceux qui n'ont pas
// été lancés par tt-tray (fenêtre hfq-desktop, node du tt-agent, enfants du
// serveur). Ne touche jamais notre propre process-group.
func sweepAll() {
	self := os.Getpid()
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		if g, err := syscall.Getpgid(pid); err == nil && g == pidPgidSelf() {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		name := strings.TrimSpace(string(comm))
		estTT := ttCommNames[name]
		if !estTT {
			// le binaire du tt-agent se renomme "MainThread" — on matche sa cmdline
			if cl, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline")); strings.Contains(string(cl), "tt-agent/server/index.js") {
				estTT = true
				name = "tt-agent"
			}
		}
		if estTT {
			log.Printf("nettoyage : %s pid=%d", name, pid)
			killTree(pid)
		}
	}
}

func (s *service) running() bool {
	if s.cmd != nil && s.cmd.ProcessState == nil {
		return true
	}
	return portOpen(s.port)
}

// rithmicEnvHasPassword : vrai si l'utilisateur a mis son mot de passe en clair
// dans rithmic.env → permet de lancer rithmic-live sans passer par hfq-server.
func rithmicEnvHasPassword() bool {
	data, err := os.ReadFile(dataDir + "/native/rithmic/config/rithmic.env")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "RITHMIC_PASSWORD="); ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func (s *service) start() {
	launchable := s.managed || (s.name == "rithmic-live" && rithmicEnvHasPassword())
	if !launchable {
		return // rithmic-live sans mot de passe : démarré par hfq-server à la connexion
	}
	if portOpen(s.port) {
		log.Printf("%s : port %d déjà en écoute, service externe réutilisé", s.name, s.port)
		return
	}
	if _, err := os.Stat(s.bin); err != nil {
		log.Printf("%s : binaire absent (%s)", s.name, s.bin)
		return
	}
	lf, err := os.OpenFile(filepath.Join(logDir, s.name+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("%s : log %v", s.name, err)
		return
	}
	c := exec.Command(s.bin, s.args...)
	c.Dir = s.dir
	c.Env = append(os.Environ(), s.env...)
	c.Stdout, c.Stderr = lf, lf
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		log.Printf("%s : lancement %v", s.name, err)
		lf.Close()
		return
	}
	s.cmd = c
	log.Printf("%s : démarré pid=%d", s.name, c.Process.Pid)
	go func() {
		c.Wait()
		lf.Close()
		log.Printf("%s : terminé", s.name)
	}()
}

func (s *service) stop() {
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil {
		pid = s.cmd.Process.Pid
	} else {
		pid = pidOnPort(s.port) // service externe (lancé par hfq-desktop / ailleurs)
	}
	if pid == 0 {
		return
	}
	log.Printf("%s : arrêt pid=%d", s.name, pid)
	killTree(pid)
}

func services() []*service {
	p := payloadDir
	return []*service{
		{
			name: "hfq-server", port: 8765, managed: true,
			bin: p + "/hfq-server", dir: p,
			env: []string{
				"HFQ_DATA_DIR=" + dataDir, "HFQ_BUREAU=1",
				// hist_archive.py:182 honore cet override → _hist_live dans le data dir
				"HFQ_HIST_LIVE=" + dataDir + "/_hist_live",
				// amont live du proxy /live/* (capture_live.py:59) — sans elle,
				// hfq-server envoie un Host vide à rithmic-live (refus DNS-rebinding)
				"HFQ_HUB=http://127.0.0.1:8792",
				"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
				"SSL_CERT_DIR=/etc/ssl/certs",
			},
		},
		{
			name: "rithmic-live", port: 8792, managed: false,
			bin: p + "/native/target/release/rithmic-live",
			args: []string{"--env", dataDir + "/native/rithmic/config/rithmic.env"},
			dir:  p,
			env:  []string{"HFQ_DATA_DIR=" + dataDir},
		},
		{
			name: "tt-agent", port: 9199, managed: true,
			bin:  p + "/tt-agent/node/node",
			args: []string{p + "/tt-agent/server/index.js"},
			dir:  p + "/tt-agent",
			env: []string{
				"HFQ_DATA_DIR=" + dataDir,
				"OMB_PORT=9199", "OMB_WEBHOOK_PORT=9200",
				"OMB_STATIC_DIR=" + p + "/tt-agent/ui",
				"OMB_DATA_DIR=" + dataDir + "/tt-agent",
				"OMB_EXTRA_PATH=" + p + "/tt-agent/node",
				"OMB_FORBIDDEN_ROOTS=" + dataDir + ":" + p,
			},
		},
	}
}

func singleInstance() {
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

func openURL(u string) { exec.Command("xdg-open", u).Start() }

func main() {
	singleInstance()
	os.MkdirAll(logDir, 0o755)
	lf, _ := os.OpenFile(filepath.Join(logDir, "tt-tray.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if lf != nil {
		log.SetOutput(lf)
	}
	svcs := services()
	systray.Run(func() { onReady(svcs) }, func() {
		for _, s := range svcs {
			s.stop()
		}
	})
}

func onReady(svcs []*service) {
	systray.SetIcon(iconPNG)
	systray.SetTitle("")
	systray.SetTooltip("TurningTrade")

	mTerm := systray.AddMenuItem("Ouvrir le Terminal", "http://127.0.0.1:8765")
	mAgent := systray.AddMenuItem("Ouvrir TT Agent", "http://127.0.0.1:9199")
	systray.AddSeparator()
	for _, s := range svcs {
		s.item = systray.AddMenuItem(s.name+" : …", s.bin)
	}
	mLogs := systray.AddMenuItem("Ouvrir les logs", logDir)
	systray.AddSeparator()
	mStartAll := systray.AddMenuItem("Tout démarrer", "Démarre hfq-server + tt-agent (rithmic-live suit à la connexion)")
	mStopAll := systray.AddMenuItem("Tout arrêter", "Tue tous les processus TurningTrade, même ceux lancés hors du tray")
	mQuit := systray.AddMenuItem("Quitter", "Arrête les services lancés par tt-tray")

	for _, s := range svcs {
		s.start()
		go func(s *service) { // clic = toggle start/stop
			for range s.item.ClickedCh {
				if s.running() {
					s.stop()
				} else {
					s.start()
				}
			}
		}(s)
	}

	go func() {
		for {
			select {
			case <-mTerm.ClickedCh:
				openURL(terminalURL)
			case <-mAgent.ClickedCh:
				openURL(agentURL)
			case <-mLogs.ClickedCh:
				openURL("file://" + logDir)
			case <-mStartAll.ClickedCh:
				for _, s := range svcs {
					s.start()
				}
				go func() { // rouvre le terminal quand le serveur répond
					for i := 0; i < 30; i++ {
						if portOpen(8765) {
							openURL(terminalURL)
							return
						}
						time.Sleep(500 * time.Millisecond)
					}
				}()
			case <-mStopAll.ClickedCh:
				log.Println("nettoyage complet demandé")
				sweepAll()
				for _, s := range svcs {
					s.cmd = nil
				}
				log.Println("nettoyage terminé")
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	go func() {
		for range time.Tick(2 * time.Second) {
			for _, s := range svcs {
				// auto-heal : un fils gelé (SIGSTOP/SIGTSTP → état T) est réveillé
				if s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil {
					if st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", s.cmd.Process.Pid)); err == nil {
						if i := strings.LastIndexByte(string(st), ')'); i >= 0 && i+2 < len(st) && st[i+2] == 'T' {
							log.Printf("%s : processus suspendu (état T) → SIGCONT", s.name)
							s.cmd.Process.Signal(syscall.SIGCONT)
						}
					}
				}
				var label string
				switch {
				case s.running():
					label = "en cours — cliquer pour arrêter"
				case !s.managed && !rithmicEnvHasPassword():
					label = "inactif (démarré par l'app à la connexion)"
				default:
					label = "arrêté — cliquer pour démarrer"
				}
				s.item.SetTitle(fmt.Sprintf("%s : %s (:%d)", s.name, label, s.port))
			}
		}
	}()

	go func() { // ouvre le navigateur quand le terminal répond
		for i := 0; i < 60; i++ {
			if portOpen(8765) {
				openURL(terminalURL)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}
