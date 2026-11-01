// tt-tray — lanceur systray pour TurningTrade (mode headless)
// Démarre hfq-server + rithmic-live + tt-agent sans fenêtre hfq-desktop,
// expose une icône dans la barre système pour ouvrir le Terminal / arrêter.
package main

import (
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
	"time"

	"github.com/getlantern/systray"
)

const (
	terminalURL  = "http://127.0.0.1:8765/hfq-terminal.html"
	agentURL     = "http://127.0.0.1:9199/"
	downloadURL  = "https://www.turningtrade.com/telecharger"
	manifestBase = "https://pub-ce01ba38458d4cf3a4355adbb56de991.r2.dev/desktop/terminal"
)

// manifests Tauri historiques — périmés côté serveur (win 0.4.5, mac 0.4.2,
// linux 404), gardés en repli si la lecture du paquet publié échoue.
var manifestNames = []string{
	"latest-linux-x86_64.json",
	"latest-windows-x86_64.json",
	"latest-darwin-aarch64.json",
}

var (
	payloadDir = envOr("TT_PAYLOAD_DIR", plat.payloadDir())
	dataDir    = envOr("TT_DATA_DIR", plat.dataDir())
	logDir     = filepath.Join(dataDir, "logs")
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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

// killPorts tue tout ce qui écoute encore sur les ports donnés, quel que soit
// le nom du processus — couvre les fils renommés, les orphelins hors de
// l'arbre tué et les éventuels respawns (cause du « doublon :8765 »).
func killPorts(ports ...int) {
	for _, p := range ports {
		for i := 0; i < 8; i++ {
			pid := plat.pidOnPort(p)
			if pid == 0 || pid == os.Getpid() {
				break
			}
			log.Printf("port %d encore pris par pid=%d → kill", p, pid)
			plat.killTree(pid)
		}
	}
}

// arbreProcessus : table parent -> enfants construite depuis le snapshot OS.
func arbreProcessus() map[int][]int {
	arbre := map[int][]int{}
	for _, p := range plat.procs() {
		arbre[p.ppid] = append(arbre[p.ppid], p.pid)
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

// sweepAll tue tous les processus TurningTrade, y compris ceux qui n'ont pas
// été lancés par tt-tray (fenêtre hfq-desktop, node du tt-agent, enfants du
// serveur) ainsi que tous leurs descendants. Ne protège que le tray, ses
// ancêtres et SES descendants.
func sweepAll() {
	self := os.Getpid()
	procs := plat.procs()
	byPid := map[int]procInfo{}
	arbre := map[int][]int{}
	for _, p := range procs {
		byPid[p.pid] = p
		arbre[p.ppid] = append(arbre[p.ppid], p.pid)
	}
	proteges := map[int]bool{self: true}
	for _, d := range descendants(self, arbre) {
		proteges[d] = true
	}
	for p := byPid[self].ppid; p > 0 && !proteges[p]; { // ancêtres jusqu'à la racine
		proteges[p] = true
		a, ok := byPid[p]
		if !ok {
			break
		}
		p = a.ppid
	}
	var racines []int
	noms := map[int]string{}
	for _, p := range procs {
		if proteges[p.pid] || !plat.isTTProc(p) {
			continue
		}
		noms[p.pid] = p.name
		racines = append(racines, p.pid)
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
		plat.killTree(pid)
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
	data, err := os.ReadFile(filepath.Join(dataDir, "native", "rithmic", "config", "rithmic.env"))
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
	c.SysProcAttr = plat.sysProcAttr()
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
		pid = plat.pidOnPort(s.port) // service externe (lancé par hfq-desktop / ailleurs)
	}
	if pid == 0 {
		return
	}
	log.Printf("%s : arrêt pid=%d", s.name, pid)
	arbre := arbreProcessus()
	plat.killTree(pid)
	for _, d := range descendants(pid, arbre) { // service externe : fils tués un par un
		if d != os.Getpid() {
			plat.killTree(d)
		}
	}
	killPorts(s.port) // le port doit être réellement libéré
}

func services() []*service {
	p := payloadDir
	return []*service{
		{
			name: "hfq-server", port: 8765, managed: true,
			bin: filepath.Join(p, plat.exeName("hfq-server")), dir: p,
			env: append([]string{
				"HFQ_DATA_DIR=" + dataDir, "HFQ_BUREAU=1",
				// hist_archive.py:182 honore cet override → _hist_live dans le data dir
				"HFQ_HIST_LIVE=" + filepath.Join(dataDir, "_hist_live"),
				// amont live du proxy /live/* (capture_live.py:59) — sans elle,
				// hfq-server envoie un Host vide à rithmic-live (refus DNS-rebinding)
				"HFQ_HUB=http://127.0.0.1:8792",
			}, plat.sslCertEnv()...),
		},
		{
			name: "rithmic-live", port: 8792, managed: false,
			bin:   filepath.Join(p, "native", "target", "release", plat.exeName("rithmic-live")),
			args:  []string{"--env", filepath.Join(dataDir, "native", "rithmic", "config", "rithmic.env")},
			dir:   p,
			env:   []string{"HFQ_DATA_DIR=" + dataDir},
		},
		{
			name: "tt-agent", port: 9199, managed: true,
			bin:  filepath.Join(p, "tt-agent", "node", plat.exeName("node")),
			args: []string{filepath.Join(p, "tt-agent", "server", "index.js")},
			dir:  filepath.Join(p, "tt-agent"),
			env: []string{
				"HFQ_DATA_DIR=" + dataDir,
				"OMB_PORT=9199", "OMB_WEBHOOK_PORT=9200",
				"OMB_STATIC_DIR=" + filepath.Join(p, "tt-agent", "ui"),
				"OMB_DATA_DIR=" + filepath.Join(dataDir, "tt-agent"),
				"OMB_EXTRA_PATH=" + filepath.Join(p, "tt-agent", "node"),
				"OMB_FORBIDDEN_ROOTS=" + dataDir + string(os.PathListSeparator) + p,
			},
		},
	}
}

// ---------- check de version ----------

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

// latestManifestPublished : meilleure version trouvée dans les manifests
// Tauri — repli commun (stale côté serveur, cf. commentaire manifestNames).
func latestManifestPublished() string {
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
	pub := plat.latestPublished()
	updLastPub = pub
	updateLabel(item, pub, plat.installedVersion())
}

// refreshUpdateLabel : recalcule le libellé avec la version publiée en cache —
// pas de réseau, rattrape une install/désinstall faite entre deux checks.
func refreshUpdateLabel(item *systray.MenuItem) {
	if updLastPub == "" {
		return
	}
	updateLabel(item, updLastPub, plat.installedVersion())
}

func updateLabel(item *systray.MenuItem, pub, inst string) {
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
			plat.notify("TurningTrade",
				fmt.Sprintf("Nouvelle version publiée : %s (installée : %s)", pub, inst))
		}
	case versionNewer(inst, pub):
		item.SetTitle(fmt.Sprintf("Màj : %s installée, site à %s (build non publié)", inst, pub))
	default:
		item.SetTitle("Màj : à jour (" + inst + ")")
	}
}

// updateAvailable : vrai si la dernière version publiée dépasse l'installée.
func updateAvailable() bool {
	return updLastPub != "" && plat.installedVersion() != "" && versionNewer(updLastPub, plat.installedVersion())
}

func main() {
	plat.singleInstance()
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
	systray.SetIcon(trayIcon)
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
				plat.openLocal(path)
			}
		}(it, p, lf.file == "")
	}
	mExt := systray.AddMenuItem("Ouvrir les indicateurs", filepath.Join(dataDir, "extensions"))
	mUpdate := systray.AddMenuItem("Màj : vérification…", "Compare la version installée à la dernière publiée")
	if !plat.updateCheckEnabled() {
		mUpdate.SetTitle("Màj : non dispo sous Windows")
		mUpdate.Disable()
	}
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
				plat.openURL(terminalURL)
			case <-mAgent.ClickedCh:
				plat.openURL(agentURL)
			case <-mExt.ClickedCh:
				os.MkdirAll(filepath.Join(dataDir, "extensions"), 0o755)
				plat.openLocal(filepath.Join(dataDir, "extensions"))
			case <-mUpdate.ClickedCh:
				if plat.updateCheckEnabled() {
					if updateAvailable() {
						plat.openURL(downloadURL)
					} else {
						go checkUpdate(mUpdate)
					}
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
		n := 0
		for range time.Tick(2 * time.Second) {
			n++
			if n%30 == 0 && plat.updateCheckEnabled() { // chaque minute : libellé Màj (local, sans réseau)
				refreshUpdateLabel(mUpdate)
			}
			for _, s := range svcs {
				// auto-heal : un fils gelé est réveillé (no-op sous Windows)
				if s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil {
					plat.resumeIfSuspended(s.cmd.Process.Pid)
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

	if plat.updateCheckEnabled() { // check de version : 15 s après le démarrage, puis toutes les 2 h
		go func() {
			time.Sleep(15 * time.Second)
			checkUpdate(mUpdate)
			for range time.Tick(2 * time.Hour) {
				checkUpdate(mUpdate)
			}
		}()
	}

}
