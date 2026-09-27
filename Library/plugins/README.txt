这个目录存放已安装的 MM 插件（每插件一个子目录：<id>@<版本>/）。

- 命令行安装：mm install <包.mm> --dest "<数据根>\Library\plugins" [--grant files,network]
- 界面安装：设置页 → 声音管理 → 「安装 .mm 插件」→ 预览能力 → 勾选授权 → 安装
- AI 安装：对话里让 AI 用 mm_create_plugin / mm_build_plugin / mm_install_plugin

安装后如果界面里没看到，点设置页的「↻ 同步已安装插件」把它们登记进音源注册表。
卸载：删除对应子目录后同样点一次「同步已安装插件」。