# turningTrade

Outils autour de TurningTrade sous Linux et Windows.

## tt-tray

Lanceur en barre système (Go + systray) qui démarre `hfq-server`, `tt-agent` et `rithmic-live` sans la fenêtre desktop — le Terminal s'ouvre dans le navigateur sur `http://127.0.0.1:8765/hfq-terminal.html`.

### Linux

```bash
cd tt-tray
go build -o tt-tray .   # binaire seul
./build-deb.sh          # paquet .deb dans dist/
```

### Windows

Installeur dans [Releases](https://github.com/yopkool29/turningTrade/releases) — `tt-tray_<version>_win-x64_setup.exe` : installation par utilisateur (pas d'admin), raccourci menu Démarrer, option « lancer au démarrage », désinstalleur.

Build depuis les sources (Go + Inno Setup 6 requis) :

```powershell
cd tt-tray
.\build-win.ps1         # installeur dans dist\
```
