// tt-tray — lanceur systray pour TurningTrade (mode headless)
// Démarre hfq-server + rithmic-live + tt-agent sans fenêtre hfq-desktop,
// expose une icône dans la barre système pour ouvrir le Terminal / arrêter.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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
	payloadDir   = "/usr/lib/TurningTrade Terminal/payload"
	terminalURL  = "http://127.0.0.1:8765/hfq-terminal.html"
	agentURL     = "http://127.0.0.1:9199/"
	downloadURL  = "https://www.turningtrade.com/telecharger"
	manifestBase = "https://pub-ce01ba38458d4cf3a4355adbb56de991.r2.dev/desktop/terminal"
)

// manifests Tauri publiés par plateforme — linux-x86_64 n'existe pas encore,
// on le sonde quand même : le jour où le canal s'ouvre, il est pris en compte.
var manifestNames = []string{
	"latest-linux-x86_64.json",
	"latest-windows-x86_64.json",
	"latest-darwin-aarch64.json",
}

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

// killTree tue le PID — et tout son process-group seulement s'il en est le
// chef (groupe dédié, ex. nos services lancés avec Setpgid). Si le PID partage
// le groupe d'un lanceur ou de la session (hfq-desktop ouvert via appfinder…),
// on ne tue que le PID : un kill groupé emporterait des processus sans rapport
// (xfsettingsd → raccourcis clavier morts).
func killTree(pid int) {
	target := pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid && pgid != pidPgidSelf() {
		target = -pgid // chef d'un groupe dédié → kill groupé
	}
	syscall.Kill(target, syscall.SIGTERM)
	time.Sleep(400 * time.Millisecond)
	syscall.Kill(target, syscall.SIGKILL)
}

// killPorts tue tout ce qui écoute encore sur les ports donnés, quel que soit
// le nom du processus — couvre les fils renommés, les orphelins hors du
// process-group tué et les éventuels respawns (cause du « doublon :8765 »).
func killPorts(ports ...int) {
	for _, p := range ports {
		for i := 0; i < 8; i++ {
			pid := pidOnPort(p)
			if pid == 0 || pid == os.Getpid() {
				break
			}
			log.Printf("port %d encore pris par pid=%d → kill", p, pid)
			killTree(pid)
		}
	}
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

// ppidOf lit le PPID d'un PID depuis /proc/<pid>/stat.
func ppidOf(pid int) int {
	st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	if i := strings.LastIndexByte(string(st), ')'); i >= 0 {
		if f := strings.Fields(string(st[i+2:])); len(f) >= 2 {
			p, _ := strconv.Atoi(f[1])
			return p
		}
	}
	return 0
}

// sweepAll tue tous les processus TurningTrade, y compris ceux qui n'ont pas
// été lancés par tt-tray (fenêtre hfq-desktop, node du tt-agent, enfants du
// serveur) ainsi que tous leurs descendants — webview WebKit & co — même sous
// un autre nom. Ne protège que le tray, ses ancêtres et SES descendants :
// « même process-group » est trop large — hfq-desktop lancé depuis le même
// shell partage le pgid du tray et n'était jamais tué.
func sweepAll() {
	self := os.Getpid()
	arbre := arbreProcessus()
	proteges := map[int]bool{self: true}
	for _, d := range descendants(self, arbre) {
		proteges[d] = true
	}
	for p := ppidOf(self); p > 1 && !proteges[p]; { // ancêtres jusqu'à init
		proteges[p] = true
		p = ppidOf(p)
	}
	// table parent -> enfants pour attraper les fils renommés (ex. WebKitWebProcess)
	var racines []int
	noms := map[int]string{}
	for _, e := range mustReadDir("/proc") {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || proteges[pid] {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		name := strings.TrimSpace(string(comm))
		noms[pid] = name
		estTT := ttCommNames[name]
		if !estTT {
			// le binaire du tt-agent se renomme "MainThread" — on matche sa cmdline
			if cl, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline")); strings.Contains(string(cl), "tt-agent/server/index.js") {
				estTT = true
				name = "tt-agent"
			}
		}
		if estTT {
			racines = append(racines, pid)
		}
	}
	// élargit aux descendants avant toute exécution (sinon ils seraient orphelins)
	cibles := map[int]bool{}
	for _, r := range racines {
		cibles[r] = true
		for _, d := range descendants(r, arbre) {
			cibles[d] = true
		}
	}
	for pid := range cibles {
		if proteges[pid] {
			continue
		}
		log.Printf("nettoyage : %s pid=%d", noms[pid], pid)
		killTree(pid)
	}
}

func mustReadDir(name string) []os.DirEntry {
	entries, _ := os.ReadDir(name)
	return entries
}

// arbreProcessus : table parent -> enfants construite depuis /proc/*/stat.
func arbreProcessus() map[int][]int {
	arbre := map[int][]int{}
	for _, e := range mustReadDir("/proc") {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		if i := strings.LastIndexByte(string(st), ')'); i >= 0 {
			if f := strings.Fields(string(st[i+2:])); len(f) >= 2 {
				if ppid, err := strconv.Atoi(f[1]); err == nil {
					arbre[ppid] = append(arbre[ppid], pid)
				}
			}
		}
	}
	return arbre
}

// descendants : tous les descendants de pid (fils, petits-fils…), quel que
// soit leur nom ou leur process-group.
func descendants(pid int, arbre map[int][]int) []int {
	var out []int
	vu := map[int]bool{}
	var marcher func(int)
	marcher = func(p int) {
		for _, c := range arbre[p] {
			if !vu[c] {
				vu[c] = true
				out = append(out, c)
				marcher(c)
			}
		}
	}
	marcher(pid)
	return out
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
	arbre := arbreProcessus()
	killTree(pid)
	for _, d := range descendants(pid, arbre) { // service externe : groupe partagé → tuer les fils un par un
		if d != os.Getpid() {
			killTree(d)
		}
	}
	killPorts(s.port) // le port doit être réellement libéré
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

// ---------- check de version ----------

func installedVersion() string {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Version}", "turning-trade-terminal").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// versionNewer : a strictement plus récente que b (comparaison par segments numériques).
func versionNewer(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// latestPublished : plus haute version publiée parmi les manifests de toutes
// les plateformes (le .deb du site est l'app *journal*, pas le terminal).
func latestPublished() string {
	cli := &http.Client{Timeout: 10 * time.Second}
	best := ""
	for _, name := range manifestNames {
		resp, err := cli.Get(manifestBase + "/" + name)
		if err != nil {
			continue
		}
		var m struct {
			Version string `json:"version"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m)
		resp.Body.Close()
		if m.Version != "" && (best == "" || versionNewer(m.Version, best)) {
			best = m.Version
		}
	}
	return best
}

var (
	updLastPub  string // dernière version publiée vue
	updNotified string // version déjà notifiée (une notif par version)
	updChecking bool
)

func checkUpdate(item *systray.MenuItem) {
	if updChecking {
		return
	}
	updChecking = true
	defer func() { updChecking = false }()
	pub, inst := latestPublished(), installedVersion()
	updLastPub = pub
	switch {
	case pub == "":
		item.SetTitle("Màj : site injoignable")
	case inst == "":
		item.SetTitle(fmt.Sprintf("Màj : %s publiée (paquet non installé)", pub))
	case versionNewer(pub, inst):
		item.SetTitle(fmt.Sprintf("Màj : %s dispo (installée %s) — télécharger", pub, inst))
		if updNotified != pub {
			updNotified = pub
			log.Printf("nouvelle version publiée : %s (installée %s)", pub, inst)
			exec.Command("notify-send", "TurningTrade",
				fmt.Sprintf("Nouvelle version publiée : %s (installée : %s)", pub, inst)).Run()
		}
	case versionNewer(inst, pub):
		item.SetTitle(fmt.Sprintf("Màj : %s installée, site à %s (build non publié)", inst, pub))
	default:
		item.SetTitle("Màj : à jour (" + inst + ")")
	}
}

// updateAvailable : vrai si la dernière version publiée dépasse l'installée.
func updateAvailable() bool {
	return updLastPub != "" && installedVersion() != "" && versionNewer(updLastPub, installedVersion())
}

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
	mServices := systray.AddMenuItem("Services", "Démarrer/arrêter les processus")
	mStartAll := mServices.AddSubMenuItem("Tout démarrer", "Démarre hfq-server + tt-agent (rithmic-live suit à la connexion)")
	mStopAll := mServices.AddSubMenuItem("Tout arrêter", "Tue tous les processus TurningTrade, même ceux lancés hors du tray")
	for _, s := range svcs {
		s.item = mServices.AddSubMenuItem(s.name+" : …", s.bin)
	}
	mLogs := systray.AddMenuItem("Logs", logDir)
	type logEntry struct {
		label, file string
	}
	logFiles := []logEntry{{"Dossier", ""}, {"tt-tray", "tt-tray.log"}}
	for _, s := range svcs {
		logFiles = append(logFiles, logEntry{s.name, s.name + ".log"})
	}
	for _, lf := range logFiles {
		it := mLogs.AddSubMenuItem(lf.label, lf.file)
		p := logDir
		if lf.file != "" {
			p = filepath.Join(logDir, lf.file)
		}
		go func(item *systray.MenuItem, path string, isDir bool) {
			for range item.ClickedCh {
				if !isDir {
					if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND, 0o644); err == nil {
						f.Close()
					}
				}
				openURL("file://" + path)
			}
		}(it, p, lf.file == "")
	}
	mExt := systray.AddMenuItem("Ouvrir les indicateurs", filepath.Join(dataDir, "extensions"))
	mUpdate := systray.AddMenuItem("Màj : vérification…", "Compare la version installée à la dernière publiée")
	systray.AddSeparator()
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
			case <-mExt.ClickedCh:
				os.MkdirAll(filepath.Join(dataDir, "extensions"), 0o755)
				openURL("file://" + filepath.Join(dataDir, "extensions"))
			case <-mUpdate.ClickedCh:
				if updateAvailable() {
					openURL(downloadURL)
				} else {
					go checkUpdate(mUpdate)
				}
			case <-mStartAll.ClickedCh:
				for _, s := range svcs {
					s.start()
				}
			case <-mStopAll.ClickedCh:
				log.Println("nettoyage complet demandé")
				sweepAll()
				for _, s := range svcs {
					s.cmd = nil
				}
				// balayage par port : tue aussi ce que le sweep par nom a raté
				// (8766 = journal hfq-server, 9200 = webhook tt-agent)
				killPorts(8765, 8766, 8792, 9199, 9200)
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

	go func() { // check de version : 15 s après le démarrage, puis toutes les 2 h
		time.Sleep(15 * time.Second)
		checkUpdate(mUpdate)
		for range time.Tick(2 * time.Hour) {
			checkUpdate(mUpdate)
		}
	}()

}
