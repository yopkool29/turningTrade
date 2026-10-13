# turningTrade

Outils autour de TurningTrade sous Linux.

## tt-tray

Lanceur en barre système (Go + systray) qui démarre `hfq-server`, `tt-agent` et `rithmic-live` sans la fenêtre desktop — le Terminal s'ouvre dans le navigateur sur `http://127.0.0.1:8765/hfq-terminal.html`.

```bash
cd tt-tray
go build -o tt-tray .   # binaire seul
./build-deb.sh          # paquet .deb dans dist/
```

La doc et les posts du projet sont gérés dans campaign-pilot (namespace `TurningTrade`).
