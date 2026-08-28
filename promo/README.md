# AI_MIDI 宣传视频制作管线

本目录包含宣传视频的完整制作管线：作曲、音效合成、演示导播、虚拟舞台、录屏与后期剪辑。
成片输出：
- `out/AI_MIDI_promo.mp4`（1920×1080@25fps 主版本，87.5s = 31.25 小节 @85 BPM，双语字幕）
- `out/AI_MIDI_promo_4K.mp4`（3840×2160 lanczos 超采样 + 轻锐化衍生版）

## 成片结构（v5 · AI 交互为主角）

intro(3) → **A 虚拟舞台·对话写歌**(7，孤立输入卡→发送→3D whip 运镜→流式回复先慢后快→
结构化提问卡→结果卡定格) → 过场1(1.25) → **B 快速任务**(4，真实弹窗：MIDI 拖入解析/
任务 chips/表单自动填写/SSE 回放) → 过场2(1.25) → **C 编曲窗**(5，API 预置四轨编排→
开窗→轨道依次点亮→播放头+电平表→鼓组独奏) → 过场3(1.25) → **D 卷帘快剪**(1+1) →
过场4(1.25) → **E 闭环导出**(3，音符飞回聊天→编曲扩展→导出徽章砸落) → outro(2)

过场字卡 = 右侧应用画面 3D 透视薄片 + 左侧步骤圆点/中英文标题/要点胶囊/水印序号。

## 制作系统

- **虚拟舞台** `frontend/demo/stage.html`：复用产品 style.css 的消息气泡/工具块/
  问题卡/印章样式，UI 元素作为独立演员摆上蓝图舞台；摄像机系统对页面施加
  scale/translate/rotateX 运镜（M3 emphasized 缓动）+ 快速运动时的运动模糊。
  注意：3D 姿态仅作瞬态——Chromium 对 3D 变换做纹理映射不重新栅格化，
  保持姿态整页会发糊；运镜结束需解除合成层（will-change auto）强制重栅格化。
- **导播** `frontend/demo/showrunner.js`：SSE 确定性回放、拟真输入（贝塞尔光标+涟漪）、
  页面级 cam() 运镜、快速任务分镜（DataTransfer 合成 drop 走真实 /api/parse）、
  编排预置（PUT preset_arrangement.json）+ 编曲窗分镜。
- **录制** `record.js`：CDP Page.screencast 逐帧 JPEG(q93) + 按帧时间戳 concat →
  CFR H.264（crf17）。静止收尾页必须把末帧保持到目标时长，否则成片被截短。
- **音频** render_audio.py v2（ADSR 尾音 + FFT 混响 + 丰富鼓组/FX）；
  混音：垫乐只负责片头/过场/片尾，应用内音乐按段 0.3s 淡入出，
  riser→impact 接编曲窗全奏高潮，全部落点吸附 85 BPM 节拍。

## 旧版结构说明（v3-v4，钢琴卷帘为主角，已被 v5 取代）

过场字卡与镜头 2-6 的实机演示逻辑保留在 showrunner 中（按键 2-6 可单独触发）。

## 目录结构

```
promo/
├── compose_music.py      # 作曲：85 BPM D 多利亚 Lo-Fi，生成 3 个 .mid 到 frontend/demo/assets/
├── make_sfx.py           # numpy 合成 10 个 SFX/垫乐到 promo/sfx/（零版权风险）
├── render_audio.py       # 离线渲染配乐音轨（与画面中 MIDI 内容同源）到 promo/audio/
├── record.js             # playwright-core + 系统 Chrome 无头录制（1080p 与屏幕分辨率无关）
├── build_video.py        # 后期：剪辑清单 + 双语字幕 + 混音 + 拼接混流
├── msyhbd.ttc            # 字幕用粗体雅黑（避免盘符冒号转义问题）
├── sfx/ audio/ out/      # 中间产物与成片
frontend/demo/
├── showrunner.js         # 演示导播引擎（仅 ?demo=1 激活，普通用户零影响）
├── script_data.js        # 确定性 SSE 剧本 + 各镜头编排参数
├── intro.html outro.html # 片头/片尾字卡（产品同源蓝图美学）
└── assets/*.mid          # 演示用乐曲（前端静态服务直出）
```

## 核心设计

**不克隆 UI，直接驱动真实产品**：`go run -browser` 启动真实后端，导播脚本
1. 拦截 `/api/chat` 的 SSE，按预写剧本确定性回放（消除大模型随机性）；
2. 用合成鼠标/键盘事件驱动真实 UI 代码路径（打字/画笔/扫弦/切片/擦除/幽灵轨/键盘弹奏）；
3. 预制 .mid 通过真实上传接口进入工程，卷帘用真实 `openFile` 加载。

## 重制流程

```bash
# 0) 依赖：Go、Python312+numpy、Node+playwright-core（在 promo/ 下 npm i playwright-core）、ffmpeg(winget Gyan.FFmpeg)
# 1) 资产
python promo/compose_music.py && python promo/make_sfx.py && python promo/render_audio.py
# 2) 启动产品（浏览器模式，端口 7860）
go build -o build/promo_test.exe . && build/promo_test.exe -browser -port 7860
# 3) 录制（首次需先访问 shot=0 建工程/传素材；clean=1 隐藏导播 UI；产物统一放 promo/out/）
node promo/record.js "http://127.0.0.1:7860/demo/intro.html"  promo/out/intro.webm 9
node promo/record.js "http://127.0.0.1:7860/chat.html?demo=1&shot=0"  promo/out/prep.webm 12
node promo/record.js "http://127.0.0.1:7860/chat.html?demo=1&auto=1&clean=1" promo/out/main.webm 78 "http://127.0.0.1:7860/chat.html?demo=1&shot=0"
node promo/record.js "http://127.0.0.1:7860/chat.html?demo=1&shot=4&clean=1" promo/out/shot4.webm 34
node promo/record.js "http://127.0.0.1:7860/demo/outro.html" promo/out/outro.webm 6
# 4) 合成（剪辑点见 build_video.py 的 CUTS，按 85 BPM 卡点；源目录用 PROMO_SRC 覆盖）
python promo/build_video.py
```

## 导播按键/URL 参数

- 按键：`0` 准备 / `1` 进工程 / `2-6` 各分镜 / `H` 隐藏导播 UI
- URL：`?demo=1&auto=1` 全自动连播；`?demo=1&shot=N` 单镜；`&clean=1` 隐藏导播 UI

## 注意事项

- 录制机系统盘需留少量空间给命令行临时文件；可用 `TMP_DIR` 环境变量把 Chrome/playwright 临时目录重定向到其它盘。
- `record.js` 依赖 `promo/node_modules/playwright-core`（`cd promo && npm i playwright-core`）；如需指定浏览器目录，设置 `PLAYWRIGHT_BROWSERS_PATH` 环境变量（内含 ffmpeg-win64.exe，可由 winget 版 ffmpeg 复制充当）。
- 演示工程的 AI 回复为确定性剧本，与真实后端 `llm.FormatSingleToolEntry` 的工具块格式逐字段对齐。
