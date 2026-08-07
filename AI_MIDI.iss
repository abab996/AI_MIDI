; AI_MIDI 安装脚本 — Inno Setup 6
; 编译命令: "C:\Program Files (x86)\Inno Setup 6\ISCC.exe" AI_MIDI.iss
; 产物: dist\installer\AI_MIDI_Setup_1.0.0.exe
;
; 设计说明:
;   - per-user 安装(默认 {localappdata}\Programs\AI_MIDI):打包版把运行数据
;     (settings.json / projects / output / doing)写在 exe 同级的 _internal 下,
;     装到 Program Files 会因普通用户无写权限导致数据保存失败(UAC 虚拟化),
;     故采用免管理员、数据目录可写的 per-user 模式(与 VSCode 等一致)。
;   - 卸载时保留用户数据(设置/项目/输出),可选择是否一并删除。

#define MyAppName "AI_MIDI"
#define MyAppVersion "2.0.0"
#define MyAppPublisher "abab996"
#define MyAppExeName "AI_MIDI.exe"
#define MyAppAssocName MyAppName + " File"

[Setup]
; 唯一应用标识(勿与其他应用重复;变更后无法检测到旧版卸载)
AppId={{0741ee2c-6a2d-4499-bd74-ca9eb1655ee0}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL=https://github.com/abab996/AI_MIDI
AppSupportURL=https://github.com/abab996/AI_MIDI
AppUpdatesURL=https://github.com/abab996/AI_MIDI

; per-user 安装:无需管理员权限,升级/卸载不依赖 UAC
PrivilegesRequired=lowest
DefaultDirName={localappdata}\Programs\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes

; 图标:安装程序与卸载项均使用 splash 图标(与 exe 一致)
SetupIconFile=splash.ico
UninstallDisplayIcon={app}\{#MyAppExeName}
UninstallDisplayName={#MyAppName} {#MyAppVersion}

; 输出与压缩
OutputDir=dist\installer
OutputBaseFilename=AI_MIDI_Setup_{#MyAppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern

; 许可页(Apache-2.0)
LicenseFile=LICENSE

; 版本资源(资源管理器"详细信息"页可见)
VersionInfoVersion={#MyAppVersion}.0
VersionInfoCompany={#MyAppPublisher}
VersionInfoDescription={#MyAppName} - AI 编曲助手
VersionInfoProductName={#MyAppName}
VersionInfoProductVersion={#MyAppVersion}.0
VersionInfoCopyright=Copyright (C) abab996

; 仅支持 64 位 Windows
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible

[Languages]
; 官方 Inno Setup 6 发行版不含简体中文, 使用项目 Languages/ 下的官方仓库版本
Name: "chinesesimplified"; MessagesFile: "Languages\ChineseSimplified.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
; 桌面快捷方式(默认勾选)
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked
; 安装完成后立即运行(仅全新安装时默认勾选,升级时不打扰)
Name: "runapp"; Description: "{cm:LaunchProgram,{#MyAppName}}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: checkedonce

[Files]
; 递归安装整个打包目录(127MB,含 _internal 全部依赖)
Source: "{#SourcePath}dist\AI_MIDI\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"
Name: "{group}\{cm:UninstallProgram,{#MyAppName}}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Tasks: desktopicon

[Run]
; 安装完成页"运行 AI_MIDI"(默认勾选,静默安装时自动跳过)
Filename: "{app}\{#MyAppExeName}"; Description: "{cm:LaunchProgram,{#MyAppName}}"; Flags: nowait postinstall skipifsilent; Tasks: runapp

[Code]
{ ===== 工具函数:检测进程是否在运行(按 exe 文件名) ===== }
function ProcessExists(const ExeName: string): Boolean;
var
  ResultCode: Integer;
begin
  { tasklist 列出同名进程,findstr 匹配到则退出码为 0(未找到为 1) }
  Exec('cmd.exe',
       '/C tasklist /FI "IMAGENAME eq ' + ExeName + '" /NH 2>NUL | findstr /I /C:"' + ExeName + '" >NUL',
       '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Result := ResultCode = 0;
end;

{ ===== 安装前:若程序正在运行则提示关闭 ===== }
function InitializeSetup(): Boolean;
begin
  Result := True;
  if ProcessExists('{#MyAppExeName}') then
  begin
    if MsgBox('检测到 {#MyAppName} 正在运行。' + #13#10 +
              '请先关闭程序后再继续安装。' + #13#10 + #13#10 +
              '现在继续安装吗？',
              mbConfirmation, MB_YESNO) = IDNO then
    begin
      Result := False;
    end;
  end;
end;

{ ===== 卸载时:结束进程 + 询问是否删除用户数据 ===== }
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  ErrorCode: Integer;
begin
  if CurUninstallStep = usUninstall then
  begin
    { 结束正在运行的程序,避免文件占用导致卸载失败 }
    if ProcessExists('{#MyAppExeName}') then
    begin
      ShellExec('open', 'taskkill.exe', '/F /IM {#MyAppExeName} /T', '',
        SW_HIDE, ewWaitUntilTerminated, ErrorCode);
    end;

    { 用户数据默认保留(下次重装时继续使用);
      选"是"则连同设置/项目/输出一起删除。
      MB_DEFBUTTON2:静默卸载(/VERYSILENT)时 MsgBox 自动取默认按钮"否",绝不误删数据 }
    if MsgBox('是否同时删除用户数据？' + #13#10 + #13#10 +
              '包括:设置文件 settings.json、项目库 projects、' + #13#10 +
              '输出 output、中间产物 doing。' + #13#10 + #13#10 +
              '选择"否"将保留这些数据。',
              mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
    begin
      DelTree(ExpandConstant('{app}\_internal\settings.json'), False, True, False);
      DelTree(ExpandConstant('{app}\_internal\projects'), True, True, False);
      DelTree(ExpandConstant('{app}\_internal\output'), True, True, False);
      DelTree(ExpandConstant('{app}\_internal\doing'), True, True, False);
    end;
  end;
end;
