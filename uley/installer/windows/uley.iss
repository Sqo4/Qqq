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
AppPublisher=Владимир Ракул (oracool)
AppCopyright=© 2026 Владимир Ракул (oracool)
AppPublisherURL=https://github.com/Sqo4/Qqq
DefaultDirName={localappdata}\Programs\Uley
DisableDirPage=yes
DisableProgramGroupPage=yes
DefaultGroupName=Улей
PrivilegesRequired=lowest
; Setup runs without admin rights, so the guard gains nothing, while it
; breaks shortcuts for people whose Desktop was moved through a junction
; (error 0x800701C0, "untrusted mount point").
RedirectionGuard=no
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
Name: "hostserver"; Description: "Этот компьютер будет сервером Улья для своей сети (ярлык «Улей — сервер»)"; Flags: unchecked
Name: "autostart"; Description: "Запускать Улей вместе с Windows (можно изменить потом в настройках)"; Flags: unchecked

[Files]
Source: "{#SourceDir}\uley.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\uley-coordinator.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\engine\*"; DestDir: "{app}\engine"; Flags: ignoreversion recursesubdirs
Source: "{#SourceDir}\NOTICE.txt"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\Улей"; Filename: "{app}\uley.exe"
Name: "{userdesktop}\Улей"; Filename: "{app}\uley.exe"; Tasks: desktopicon
; Hosting a hive for a local network (classroom, dorm, a few friends).
Name: "{userprograms}\Улей — сервер для своей сети"; Filename: "{app}\uley-coordinator.exe"; Parameters: "-addr 0.0.0.0:8080 -model qwen3-4b-chain -db ""{localappdata}\Uley\server\uley.db"""; Tasks: hostserver
Name: "{userdesktop}\Улей — сервер"; Filename: "{app}\uley-coordinator.exe"; Parameters: "-addr 0.0.0.0:8080 -model qwen3-4b-chain -db ""{localappdata}\Uley\server\uley.db"""; Tasks: hostserver

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "Uley"; ValueData: """{app}\uley.exe"" --background"; Tasks: autostart; Flags: uninsdeletevalue
; Remove a stale autostart entry on uninstall even if it was set from the app.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueName: "Uley"; Flags: uninsdeletevalue dontcreatekey

[Run]
Filename: "{app}\uley.exe"; Parameters: "{code:LaunchParams}"; Description: "Запустить Улей"; Flags: nowait postinstall

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM uley.exe"; Flags: runhidden; RunOnceId: "KillUley"
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM uley-coordinator.exe"; Flags: runhidden; RunOnceId: "KillUleyServer"

[UninstallDelete]
; Settings, node token, logs and downloaded models (they can be several GB).
Type: filesandordirs; Name: "{userappdata}\Uley"
Type: filesandordirs; Name: "{localappdata}\Uley"
; Only our own folders inside the chosen data folder.
Type: filesandordirs; Name: "{code:DataDir}\models"
Type: filesandordirs; Name: "{code:DataDir}\chain-cache"
Type: filesandordirs; Name: "{code:DataDir}\server-models"
Type: dirifempty; Name: "{code:DataDir}"
Type: files; Name: "{app}\data-dir.txt"

[Code]
var
  DataPage: TInputDirWizardPage;

// The system disk is often full, so suggest the disk with the most room
// when C: has less than 10 GB free.
function DefaultDataDir: String;
var
  Free, Total, BestFree: Int64;
  I: Integer;
begin
  Result := ExpandConstant('{localappdata}\Uley');
  if not GetSpaceOnDisk64(AddBackslash(ExtractFileDrive(Result)), Free, Total) then
    Free := 0;
  if Free > 10737418240 then
    Exit;
  BestFree := Free;
  for I := Ord('D') to Ord('Z') do
    if GetSpaceOnDisk64(Chr(I) + ':\', Free, Total) and (Free > BestFree) and (Total > 21474836480) then
    begin
      BestFree := Free;
      Result := Chr(I) + ':\Uley';
    end;
end;

procedure InitializeWizard;
begin
  DataPage := CreateInputDirPage(wpSelectTasks,
    'Где хранить модели',
    'Языковые модели занимают несколько гигабайт.',
    'Выберите папку на диске, где свободно хотя бы 10 ГБ. Компьютеру-серверу ' +
    'с большой моделью нужно около 25 ГБ. Улей создаст в ней свою папку Uley.',
    False, '');
  DataPage.Add('');
  DataPage.Values[0] := GetPreviousData('DataDir', DefaultDataDir);
end;

// DataDir always ends in its own Uley folder, so uninstalling never touches
// anything else on a disk the person picked.
function DataDir(Param: String): String;
begin
  Result := RemoveBackslashUnlessRoot(DataPage.Values[0]);
  if CompareText(ExtractFileName(Result), 'Uley') <> 0 then
    Result := AddBackslash(Result) + 'Uley';
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  Free, Total: Int64;
begin
  Result := True;
  if (CurPageID = DataPage.ID) and GetSpaceOnDisk64(AddBackslash(ExtractFileDrive(DataDir(''))), Free, Total)
     and (Free < 5368709120) then
    Result := MsgBox('На этом диске свободно меньше 5 ГБ, модель может не поместиться. Всё равно оставить эту папку?',
      mbConfirmation, MB_YESNO) = IDYES;
end;

procedure RegisterPreviousData(PreviousDataKey: Integer);
begin
  SetPreviousData(PreviousDataKey, 'DataDir', DataPage.Values[0]);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Lines: TArrayOfString;
begin
  if CurStep = ssPostInstall then
  begin
    ForceDirectories(DataDir(''));
    SetArrayLength(Lines, 1);
    Lines[0] := DataDir('');
    SaveStringsToUTF8File(ExpandConstant('{app}\data-dir.txt'), Lines, False);
  end;
end;

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
