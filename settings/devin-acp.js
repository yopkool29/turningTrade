#!/usr/bin/env node
// Wrapper ACP Devin pour TT Agent (driver customAcp d'openmausbot).
//
// 1. Sonde de dispo : le serveur exécute "<cli> --version", or
//    `devin acp --version` est invalide → on relaie vers `devin version`.
// 2. Auth : `devin acp` exige que le host ACP appelle `authenticate`
//    ("host is the sole source of credentials" — credentials.toml et
//    WINDSURF_API_KEY sont volontairement ignorés en mode ACP), mais le
//    driver customAcp n'appelle jamais authenticate (pickAuthMethod=null).
//    → ce wrapper shimme le JSON-RPC : à la réponse `initialize`, il
//    injecte `authenticate` avec la clé de %APPDATA%\devin\credentials.toml
//    (méthode windsurf-api-key + meta.api_key/api_server_url, validé à la
//    main : session/new passe ensuite).
// 3. Permissions : chaque turn spawn un NOUVEAU processus `devin acp`, et
//    les "toujours autoriser" restent en mémoire dans le processus (le
//    `sessionAllows` du driver et les grants natifs de devin ne survivent
//    pas à session/load dans un nouveau process). → après chaque
//    session/new, session/load et session/resume réussi, on injecte
//    `session/set_mode` vers DEVIN_ACP_MODE (défaut "bypass" : devin ne
//    demande plus rien ; "smart" = auto-sauf-risqué, "accept-edits"/"ask"…).
//
// Résolution du binaire : `devin.exe` sur le PATH d'abord (install CLI
// indépendante via `irm https://static.devin.ai/cli/setup.ps1 | iex`),
// sinon le binaire embarqué dans Devin Desktop.
const { spawn, spawnSync } = require("node:child_process");
const { existsSync, readFileSync } = require("node:fs");
const { join } = require("node:path");
const readline = require("node:readline");

const BUNDLED = join(
    process.env.LOCALAPPDATA ?? "",
    "Programs", "Devin", "resources", "app", "extensions", "windsurf", "devin", "bin", "devin.exe");
const CREDS = join(process.env.APPDATA ?? "", "devin", "credentials.toml");

function devinExe() {
    if (!spawnSync("devin.exe", ["version"], { stdio: "ignore" }).error)
        return "devin.exe";
    if (existsSync(BUNDLED))
        return BUNDLED;
    return "devin.exe"; // le ENOENT remontera dans le message d'erreur
}

const args = process.argv.slice(2);
if (args.includes("--version")) {
    const r = spawnSync(devinExe(), ["version"], { stdio: "inherit" });
    process.exit(r.error ? 1 : (r.status ?? 1));
}

function readCreds() {
    try {
        const t = readFileSync(CREDS, "utf8");
        const api_key = /^\s*windsurf_api_key\s*=\s*["']([^"']+)["']/m.exec(t)?.[1];
        const api_server_url = /^\s*api_server_url\s*=\s*["']([^"']+)["']/m.exec(t)?.[1];
        return api_key ? { api_key, ...(api_server_url ? { api_server_url } : {}) } : null;
    }
    catch {
        return null;
    }
}

const creds = readCreds();
const child = spawn(devinExe(), ["acp", ...args], { stdio: ["pipe", "pipe", "pipe"] });
child.on("error", (err) => {
    console.error(`devin-acp: ${err.message}`);
    process.exit(1);
});
child.on("exit", (code) => process.exit(code ?? 1));
for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"])
    process.on(sig, () => child.kill(sig));
child.stderr.on("data", (d) => process.stderr.write(d));

const SHIM_ID = "devin-auth-shim";
const MODE = process.env.DEVIN_ACP_MODE === undefined ? "bypass" : process.env.DEVIN_ACP_MODE;
// Modèle épinglé (configOption "model" de devin : swe-2-high, claude-opus-5-5-medium…).
// Vide/absent = le modèle persistant de la session / le défaut devin.
const MODEL = process.env.DEVIN_ACP_MODEL ?? "";
const SESSION_METHODS = new Set(["session/new", "session/load", "session/resume"]);
const isShimId = (id) => typeof id === "string" && id.startsWith("devin-");
const pendingSess = new Map(); // id de requête host → method, pour repérer les réponses
let modeSeq = 0;
const held = [];
let initId;
let holding = false;
const toAgent = (line) => child.stdin.write(line + "\n");
const flush = () => { while (held.length) toAgent(held.shift()); };

// host → agent : forward, en tenant les messages entre `initialize` et la
// fin de notre `authenticate` injecté (session/new passerait sinon avant).
readline.createInterface({ input: process.stdin, terminal: false }).on("line", (line) => {
    let msg;
    try { msg = JSON.parse(line); } catch { /* passthrough */ }
    if (msg?.id !== undefined && SESSION_METHODS.has(msg.method))
        pendingSess.set(String(msg.id), msg.params?.sessionId);
    if (holding) { held.push(line); return; }
    if (msg?.method === "initialize" && msg.id !== undefined) {
        initId = msg.id;
        toAgent(line);
        if (creds) holding = true;
        return;
    }
    toAgent(line);
});

// agent → host : forward ; à la réponse initialize, on injecte
// `authenticate` ; la réponse à notre requête injectée n'est pas relayée.
readline.createInterface({ input: child.stdout, terminal: false }).on("line", (line) => {
    let msg;
    try { msg = JSON.parse(line); } catch { /* passthrough */ }
    if (isShimId(msg?.id)) {
        if (msg?.id === SHIM_ID) {
            holding = false;
            flush();
        }
        else if (msg?.error)
            console.error(`devin-acp: set_mode a échoué: ${msg.error.message ?? msg.error.code}`);
        return;
    }
    process.stdout.write(line + "\n");
    // session/new|load|resume réussi → imposer le mode demandé avant tout
    // prompt. `session/new` rend le sessionId dans result ; `session/load`
    // et `session/resume` ne le rendent pas → on reprend celui de la requête.
    const reqSid = msg?.id !== undefined ? pendingSess.get(String(msg.id)) : undefined;
    if (msg?.id !== undefined) pendingSess.delete(String(msg.id));
    const sid = msg?.result?.sessionId ?? (msg?.result ? reqSid : undefined);
    if (sid && MODE) {
        toAgent(JSON.stringify({
            jsonrpc: "2.0", id: `devin-mode-shim:${modeSeq++}`, method: "session/set_mode",
            params: { sessionId: sid, modeId: MODE },
        }));
    }
    if (sid && MODEL) {
        toAgent(JSON.stringify({
            jsonrpc: "2.0", id: `devin-mode-shim:${modeSeq++}`, method: "session/set_config_option",
            params: { sessionId: sid, configId: "model", value: MODEL },
        }));
    }
    if (initId === undefined || msg?.id !== initId)
        return;
    if (msg.result && creds) {
        toAgent(JSON.stringify({
            jsonrpc: "2.0", id: SHIM_ID, method: "authenticate",
            params: { methodId: "windsurf-api-key", _meta: creds },
        }));
    }
    else {
        holding = false; // initialize a échoué ou pas de creds : pas d'injection
        flush();
    }
});
