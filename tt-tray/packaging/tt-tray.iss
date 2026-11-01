; Installeur Windows de tt-tray — équivalent du paquet .deb.
; Version passée par build-win.ps1 via /DAppVersion=…
#ifndef AppVersion
  #define AppVersion "0.1.0"
#endif

[Setup]
AppId={{B7E4A1C9-3F2D-4E8A-9C5B-6D1F2A3E4B5C}}
AppName=TurningTrade Tray
AppVersion={#AppVersion}
AppPublisher=patrice
AppComments=Lanceur systray pour TurningTrade Terminal (mode headless)
DefaultDirName={localappdata}\Programs\tt-tray
DefaultGroupName=TurningTrade Tray
PrivilegesRequired=lowest
OutputDir=..\dist
OutputBaseFilename=tt-tray_{#AppVersion}_win-x64_setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\icon.ico
UninstallDisplayIcon={app}\tt-tray.exe
DisableProgramGroupPage=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
CloseApplications=yes

[Languages]
Name: "french"; MessagesFile: "compiler:Languages\French.isl"

[Tasks]
Name: "autostart"; Description: "Lancer tt-tray au démarrage de Windows"; GroupDescription: "Options :"
Name: "desktopicon"; Description: "Créer un raccourci sur le Bureau"; GroupDescription: "Options :"; Flags: unchecked

[Files]
Source: "..\tt-tray.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\icon.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\TurningTrade Tray"; Filename: "{app}\tt-tray.exe"; IconFilename: "{app}\icon.ico"
Name: "{group}\Désinstaller TurningTrade Tray"; Filename: "{uninstallexe}"
Name: "{autodesktop}\TurningTrade Tray"; Filename: "{app}\tt-tray.exe"; IconFilename: "{app}\icon.ico"; Tasks: desktopicon
Name: "{userstartup}\TurningTrade Tray"; Filename: "{app}\tt-tray.exe"; Tasks: autostart

[Run]
Filename: "{app}\tt-tray.exe"; Description: "Lancer TurningTrade Tray"; Flags: nowait postinstall skipifsilent
