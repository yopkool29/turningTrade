# Devin comme moteur d'agent dans TT Agent

TT Agent (openmausbot) possède un driver « bring-your-own engine » : `customAcp`.
Tout CLI qui parle **ACP** (Agent Client Protocol, JSON-RPC sur stdio) peut
être ajouté comme moteur via une entrée `instances` dans `config.json` —
aucune modification du code de l'outil n'est nécessaire.

Devin CLI expose `devin acp`, exactement ce protocole. Ce document décrit
l'installation complète (testée sur Windows, devin 3000.11.238).

## Prérequis

1. **Un binaire `devin`**, au choix :
   - le CLI standalone : `irm https://static.devin.ai/cli/setup.ps1 | iex`
     dans PowerShell (met `devin` sur le PATH), ou l'installeur x86_64 ;
   - ou le binaire embarqué dans Devin Desktop :
     `%LOCALAPPDATA%\Programs\Devin\resources\app\extensions\windsurf\devin\bin\devin.exe`.
2. **Des credentials** : `devin auth login` une fois dans un terminal
   (OAuth navigateur ; écrit `%APPDATA%\devin\credentials.toml`).

## Le wrapper `devin-acp.js`

Deux particularités de `devin acp` obligent un petit shim Node
(`settings/devin-acp.js` dans ce dépôt — à copier dans
`%LOCALAPPDATA%\TurningTrade\tt-agent\`) :

- **Sonde de disponibilité** : TT Agent teste `<cli> --version`, or
  `devin acp --version` est invalide → l'engine s'afficherait
  « unavailable ». Le wrapper répond `devin version` à la sonde.
- **Auth** : `devin acp` exige que le host appelle `authenticate`
  (policy « host is the sole source of credentials » — `credentials.toml`
  et `WINDSURF_API_KEY` sont ignorés en mode ACP), mais `customAcp` n'appelle
  jamais `authenticate`. Le wrapper shimme le JSON-RPC : à la réponse
  `initialize`, il injecte `authenticate`
  (`methodId: "windsurf-api-key"`, `_meta.api_key`/`api_server_url` lus dans
  `credentials.toml`) en retenant les messages host entre-temps.
- **Permissions** : chaque turn lance un **nouveau** processus `devin acp`
  et « toujours autoriser pendant cette session » ne survit pas au
  processus (ni le `sessionAllows` du driver, en mémoire, ni les grants
  natifs de devin après `session/load`). Le wrapper injecte donc
  `session/set_mode` après chaque `session/new`, `session/load` et
  `session/resume` réussi → la session démarre directement dans le mode
  voulu et devin ne redemande plus les autorisations. Mode choisi par
  `DEVIN_ACP_MODE` (env / `environment` de l'instance) : **`bypass`** par
  défaut (tout auto-approuvé), `smart` (routine auto, risqué demandé),
  `accept-edits`, `ask`, `plan` — chaîne vide pour désactiver l'injection.
- **Modèle** : le picker de modèle de TT Agent est figé à « Agent default »
  (customAcp est passthrough par design), mais devin expose un
  `configOption "model"` (≈20 choix : `swe-2-high` par défaut, `adaptive`,
  `claude-opus-5-5-medium`, `gpt-6-*-medium`, `gemini-*`, `kimi-k3-high`,
  `glm-*`…). `DEVIN_ACP_MODEL=<id>` le pousse via `set_config_option` après
  chaque session → pour « switcher de modèle » dans TT, créer des
  instances séparées qui apparaissent comme moteurs distincts :

```json
"devin-opus": {
  "driver": "customAcp",
  "displayName": "Devin (Opus)",
  "config": { "cli": "C:/Users/<user>/AppData/Local/TurningTrade/tt-agent/devin-acp.js" },
  "environment": { "DEVIN_ACP_MODEL": "claude-opus-5-5-medium" }
}
```

Le wrapper préfère `devin.exe` sur le PATH et retombe sur le binaire
embarqué du Desktop — une install standalone ultérieure est donc prise en
compte sans reconfig.

## `config.json`

Dans `%LOCALAPPDATA%\TurningTrade\tt-agent\config.json`.

> ⚠️ Dès que `instances` existe, il **remplace** la flotte par défaut.
> Si seul `devin` est déclaré, tous les autres moteurs disparaissent :
> il faut redéclarer la flotte complète.

```json
"instances": {
  "grok":         { "driver": "grokAgent" },
  "kimi":         { "driver": "kimiAgent" },
  "droid":        { "driver": "droidAgent" },
  "cursor":       { "driver": "cursorAgent" },
  "claude":       { "driver": "claudeAgent" },
  "codex":        { "driver": "codex" },
  "antigravity":  { "driver": "antigravityAgent" },
  "opencodeGo":   { "driver": "opencodeGo" },
  "computer":     { "driver": "boxAgent" },
  "openaiCompat": { "driver": "openai-compat" },
  "qwen":         { "driver": "qwenAgent" },
  "hermes":       { "driver": "hermesAgent" },
  "pi":           { "driver": "piAgent" },
  "devin": {
    "driver": "customAcp",
    "displayName": "Devin",
    "config": {
      "cli": "C:/Users/<user>/AppData/Local/TurningTrade/tt-agent/devin-acp.js"
    }
  }
},
"defaultModelSelection": { "instanceId": "devin", "model": "agent-default" }
```

`defaultModelSelection` est optionnel : sans lui, le défaut reste Claude.

Une copie de référence complète est dans `settings/config.json`.

## Prise en compte

La flotte est chargée au démarrage du serveur ; un edit manuel de
`config.json` n'est pas rechargé à chaud. Deux options :

- **Redémarrer tt-agent** : tray → Services → tt-agent (off/on).
- **Ou recharger via l'API** : quand le serveur a un owner token actif,
  les mutations exigent l'en-tête `x-openmausbot-desktop-owner`, dont la
  valeur est servie dans le `<meta>` de la page `http://127.0.0.1:9199/` :

```sh
TOKEN=$(curl -s http://127.0.0.1:9199/ | grep -o 'owner" content="[^"]*"' | cut -d'"' -f3)
# déclenche Object.assign(cfg, loadConfig()) + reloadProviders()
curl -X PATCH http://127.0.0.1:9199/api/config \
  -H "Content-Type: application/json" -H "x-openmausbot-desktop-owner: $TOKEN" \
  -d '{"defaultModelSelection":{"instanceId":"devin","model":"agent-default"}}'
```

(Le reload tue les turns en vol, comme un restart.)

## Basculer les bots existants

Chaque bot garde sa propre `modelSelection` (ex. `claude/claude-sonnet-5`)
— le défaut ne les touche pas. Pour les passer sur Devin :

```sh
# ids : %LOCALAPPDATA%\TurningTrade\tt-agent\bots.json
curl -X PATCH http://127.0.0.1:9199/api/bots/<botId>/tasks/<threadId> \
  -H "Content-Type: application/json" -H "x-openmausbot-desktop-owner: $TOKEN" \
  -d '{"modelSelection":{"instanceId":"devin","model":"agent-default"},"updateBotDefault":true}'
```

## Notes

- **Modèle** : `customAcp` annonce un seul modèle passthrough
  (`agent-default`) — le choix du modèle reste côté Devin (`/model`).
  Pour figer un modèle au lancement, ajouter `--model <nom>` à la fin du
  `cli` (ex. `"...devin-acp.js" --model opus` → relayé à `devin acp`).
- **Sécurité** : `credentials.toml` est lu par le wrapper à chaque
  lancement ; ne pas le versionner. Aucun secret dans `config.json`.
- **Réversibilité** : retirer le bloc `instances` rend la flotte par
  défaut ; la sélection d'un bot se change aussi dans l'UI.
