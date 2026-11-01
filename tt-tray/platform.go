package main

import "syscall"

// procInfo : instantané d'un processus — nom court (comm / exe) et chemin de
// l'image ou cmdline selon l'OS (vide si non renseigné).
type procInfo struct {
	pid  int
	ppid int
	name string
	path string
}

// platform : tout ce qui diffère entre Linux et Windows. Une seule
// implémentation est compilée, choisie par build tag — le reste du code ne
// connaît que cette interface (aucun test sur GOOS dans main.go).
type platform interface {
	// --- processus ---
	procs() []procInfo                 // liste des processus (pid, ppid, nom, chemin)
	pidOnPort(port int) int            // PID écoutant sur un port TCP, 0 si aucun
	killTree(pid int)                  // tue le processus et sa descendance
	isTTProc(p procInfo) bool          // processus appartenant à TurningTrade
	resumeIfSuspended(pid int)         // réveille un fils gelé (Linux : état T → SIGCONT ; Windows : no-op)
	sysProcAttr() *syscall.SysProcAttr // isolation du processus lancé

	// --- instance unique ---
	singleInstance()

	// --- bureau ---
	openURL(u string)           // URL dans le navigateur
	openLocal(path string)      // fichier/dossier dans l'explorateur de fichiers
	notify(title, msg string)   // notification utilisateur
	installedVersion() string   // version du paquet/app installé, "" si inconnue
	latestPublished() string    // dernière version publiée sur le site
	updateCheckEnabled() bool   // check de Màj disponible sur cette plateforme

	// --- config ---
	payloadDir() string      // binaires du Terminal (hfq-server, tt-agent…)
	dataDir() string         // données utilisateur (_hist_live, tt-agent…)
	exeName(base string) string // nom du binaire (Windows : +".exe")
	sslCertEnv() []string    // variables SSL_CERT_* (Linux seulement)
}

// plat : implémentation de l'OS courant — déclarée et initialisée dans le
// fichier platform_<os>.go correspondant (ex. var plat platform = linuxPlatform{}).
