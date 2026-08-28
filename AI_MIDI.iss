; AI_MIDI v3 安装脚本 — Inno Setup 6
; 编译命令: ISCC.exe AI_MIDI.iss
; 产物: dist\installer\AI_MIDI_Setup_3.0.0.exe
;
; 设计说明（沿用 v2.0.1 安装脚本约定）:
;   - per-user 安装（默认 {localappdata}\Programs\AI_MIDI）：运行数据
;     （settings.json / projects / output）写在 exe 同级目录，装到
;     Program Files 会因普通用户无写权限导致数据保存失败（UAC 虚拟化），
;     故采用免管理员、数据目录可写的 per-user 模式（与 VSCode 等一致）。
;   - AppId 与 v2.0.1 相同：覆盖安装时系统可识别旧版本并原位升级。
;   - 卸载时保留用户数据（设置/项目/输出），不主动删除。

#define MyAppName "AI_MIDI"
#define MyAppVersion "3.0.0"
#define MyAppPublisher "abab996"
#define MyAppExeName "AI_MIDI.exe"

[Setup]
; 唯一应用标识（与 v2.0.1 一致；变更后无法检测到旧版卸载）
AppId={{0741ee2c-6a2d-4499-bd74-ca9eb1655ee0}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL=https://github.com/abab996/AI_MIDI
AppSupportURL=https://github.com/abab996/AI_MIDI
AppUpdatesURL=https://github.com/abab996/AI_MIDI

; per-user 安装：无需管理员权限，升级/卸载不依赖 UAC
PrivilegesRequired=lowest
DefaultDirName={localappdata}\Programs\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes

; 图标：安装程序与卸载项均使用应用图标（与 exe 一致）
SetupIconFile=app_icon.ico
UninstallDisplayIcon={app}\{#MyAppExeName}
UninstallDisplayName={#MyAppName} {#MyAppVersion}

; 输出与压缩
OutputDir=dist\installer
OutputBaseFilename=AI_MIDI_Setup_{#MyAppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern

; 许可页（Apache-2.0）
LicenseFile=LICENSE

; 版本资源（资源管理器"详细信息"页可见）
VersionInfoVersion={#MyAppVersion}.0
VersionInfoCompany={#MyAppPublisher}
VersionInfoDescription={#MyAppName} - AI 编曲助手
VersionInfoProductName={#MyAppName}
VersionInfoProductVersion={#MyAppVersion}.0
VersionInfoCopyright=Copyright 2026 {#MyAppPublisher}

; 仅支持 64 位 Windows
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible

[Languages]
; 中文语言包随仓库分发（Inno 官方不含简体中文）
Name: "chinesesimplified"; MessagesFile: "Languages\ChineseSimplified.isl"

[Files]
; 主程序（wails build 产物）
Source: "build\bin\{#MyAppExeName}"; DestDir: "{app}"; Flags: ignoreversion
; 闭源 JUCE 音频引擎（随安装包分发，缺失时应用自动降级 Web Audio）
Source: "bin\aimidi-engine.exe"; DestDir: "{app}\bin"; Flags: ignoreversion skipifsourcedoesntexist
; 启动脚本与配置模板
Source: "RUN.bat"; DestDir: "{app}"; Flags: ignoreversion
Source: "settings.example.json"; DestDir: "{app}"; Flags: ignoreversion
; 文档
Source: "README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "README_EN.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{group}\{#MyAppName} 浏览器模式"; Filename: "{app}\{#MyAppExeName}"; Parameters: "-browser"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "启动 {#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; 卸载前先终止运行中的主程序与引擎（数据文件保留）
Filename: "taskkill.exe"; Parameters: "/F /IM {#MyAppExeName} /IM aimidi-engine.exe"; Flags: runhidden; RunOnceId: "KillApps"

[UninstallDelete]
; 清理运行期生成的空壳目录（用户数据文件不在删除列表，升级/重装可保留）
Type: dirifempty; Name: "{app}\bin"
Type: dirifempty; Name: "{app}"
