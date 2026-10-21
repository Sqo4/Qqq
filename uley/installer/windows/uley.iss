; Inno Setup script for the Uley Windows installer.
; Built by .github/workflows/uley-release.yml:
;   iscc /DAppVersion=0.1.0 /DSourceDir=..\..\dist\windows uley.iss
; Installs per user (no admin rights needed) into %LOCALAPPDATA%\Programs\Uley.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\..\dist\windows"
#endif

[Setup]
AppId={{6B3E2C55-7F0B-4C3A-9D3A-5E1A2C0B7F11}
AppName=Улей
AppVersion={#AppVersion}
AppVerName=Улей {#AppVersion}
AppPublisher=Владимир
AppCopyright=© 2026 Владимир
AppPublisherURL=https://github.com/Sqo4/Qqq
DefaultDirName={localappdata}\Programs\Uley
DisableDirPage=yes
DisableProgramGroupPage=yes
DefaultGroupName=Улей
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\..\dist
OutputBaseFilename=Uley-Setup-windows-x64
SetupIconFile=uley.ico
UninstallDisplayIcon={app}\uley.exe
WizardStyle=modern
Compression=lzma2/max
SolidCompression=yes
CloseApplications=force
RestartApplications=no
InfoBeforeFile=info-ru.txt
#ifdef Sign
SignTool=uleysign
#endif

[Languages]
Name: "ru"; MessagesFile: "compiler:Languages\Russian.isl"
Name: "en"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Ярлык на рабочем столе"; Flags: unchecked
Name: "autostart"; Description: "Запускать Улей вместе с Windows (можно изменить потом в настройках)"; Flags: unchecked

[Files]
Source: "{#SourceDir}\uley.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\engine\*"; DestDir: "{app}\engine"; Flags: ignoreversion recursesubdirs
Source: "{#SourceDir}\NOTICE.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\Улей"; Filename: "{app}\uley.exe"
Name: "{userdesktop}\Улей"; Filename: "{app}\uley.exe"; Tasks: desktopicon

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "Uley"; ValueData: """{app}\uley.exe"" --background"; Tasks: autostart; Flags: uninsdeletevalue
; Remove a stale autostart entry on uninstall even if it was set from the app.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueName: "Uley"; Flags: uninsdeletevalue dontcreatekey

[Run]
Filename: "{app}\uley.exe"; Parameters: "{code:LaunchParams}"; Description: "Запустить Улей"; Flags: nowait postinstall

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM uley.exe"; Flags: runhidden; RunOnceId: "KillUley"

[UninstallDelete]
; Settings, node token, logs and downloaded models (they can be several GB).
Type: filesandordirs; Name: "{userappdata}\Uley"
Type: filesandordirs; Name: "{localappdata}\Uley"

[Code]
function LaunchParams(Param: String): String;
begin
  // After a silent auto-update, start quietly instead of opening the panel.
  if WizardSilent then Result := '--background' else Result := '';
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Code: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM uley.exe', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Result := '';
end;
