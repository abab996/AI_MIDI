; AI_MIDI v3 安装脚本 — Inno Setup 6
; 编译命令: ISCC.exe AI_MIDI.iss
; 产物: dist\installer\AI_MIDI_Setup_3.0.3.exe
;
; 发布物约定（长期规范）:
;   - Windows 版仅上传本安装包（AI_MIDI_Setup_<ver>.exe），不再分发便携压缩包
;   - Linux 版仅上传 tar.gz 压缩包（AI_MIDI_v<ver>_linux_amd64.tar.gz）
;
; 设计说明（沿用 v2.0.1 安装脚本约定）:
;   - per-user 安装（默认 {localappdata}\Programs\AI_MIDI）：运行数据
;     （settings.json / projects / output）写在 exe 同级目录，装到
;     Program Files 会因普通用户无写权限导致数据保存失败（UAC 虚拟化），
;     故采用免管理员、数据目录可写的 per-user 模式（与 VSCode 等一致）。
;   - AppId 与 v2.0.1 相同：覆盖安装时系统可识别旧版本并原位升级。
;   - 卸载时保留用户数据（设置/项目/输出），不主动删除。

#define MyAppName "AI_MIDI"
#define MyAppVersion "3.0.3"
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
; 文件名带平台标识，与 Linux 包（_linux_amd64）命名规则对齐
OutputBaseFilename=AI_MIDI_Setup_{#MyAppVersion}_windows_amd64
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
; 启动画面与主题资源（splash_windows.go / dialogs_windows.go 从 exe 目录读取，
; 缺失时启动画面与冷暖主题图标切换静默失效）
Source: "splash.png"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "splash_dark.png"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "splash_warm.png"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "theme.txt"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "app_icon_dark.ico"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "app_icon_warm.ico"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "window_icon.ico"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
; 文档
Source: "README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "README_zh.md"; DestDir: "{app}"; Flags: ignoreversion
; 乐理知识库（read_library_file 工具的数据源；缺失时 AI 每次读取知识文件
; 都会失败——此前安装包漏装该目录，打包版 AI 无法引用乐理指南）
Source: "Library\*.md"; DestDir: "{app}\Library"; Flags: ignoreversion
; 音色库不分发（Library\soundfonts\* 体积大且为测试音色，沿用既有约定：
; 用户自备 sf2 放入 {app}\Library\soundfonts，无音色时原生引擎降级 WebAudio）

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{group}\{#MyAppName} 浏览器模式"; Filename: "{app}\{#MyAppExeName}"; Parameters: "-browser"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "启动 {#MyAppName}"; Flags: nowait postinstall skipifsilent

[Code]
// 安装前结束运行中的主程序与引擎，避免文件被占用导致覆盖失败
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Result := '';
  // 先尝试优雅关闭（不带 /F，投递 WM_CLOSE）：给主程序落盘设置与草稿的机会
  Exec('taskkill.exe', '/IM AI_MIDI.exe', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Sleep(800);
  // 再强制终止残留进程（无消息队列的引擎进程对不带 /F 的 taskkill 无响应）
  Exec('taskkill.exe', '/F /IM AI_MIDI.exe /IM aimidi-engine.exe', '',
       SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if ResultCode = 128 then // 128 = 进程未找到，属正常
    Sleep(300);
end;

[UninstallRun]
; 卸载前先终止运行中的主程序与引擎（数据文件保留）
Filename: "taskkill.exe"; Parameters: "/F /IM {#MyAppExeName} /IM aimidi-engine.exe"; Flags: runhidden; RunOnceId: "KillApps"

[UninstallDelete]
; 清理运行期生成的空壳目录（用户数据文件不在删除列表，升级/重装可保留）
Type: dirifempty; Name: "{app}\bin"
Type: dirifempty; Name: "{app}\Library"
Type: dirifempty; Name: "{app}"
