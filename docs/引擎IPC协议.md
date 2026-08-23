# AI_MIDI 引擎 IPC 协议（v1）

> 版本：1 ｜ 日期：2026-08-23
> 适用双方：`AI_MIDI.exe`（Go 主进程，客户端）↔ `aimidi-engine.exe`（JUCE 引擎，服务端）
> 实现镜像：Go 侧 `internal/engine/protocol.go`（M1 后续落地）；C++ 侧 `engine/Source/Ipc/Protocol.h`
> **本文为协议唯一权威来源**，两侧实现与 engine/README 摘要如有出入，以本文为准。

---

## 1. 管道命名与连接规则

- 管道名：`\\.\pipe\AI_MIDI_ENGINE_<引擎进程PID>`
- PID 为**引擎自身**的进程 PID。主进程经 `os/exec` 启动引擎后从 `cmd.Process.Pid` 获得该值并拼接连接；
- 单实例单客户端：引擎以 `PIPE_ACCESS_DUPLEX | FILE_FLAG_FIRST_PIPE_INSTANCE | PIPE_REJECT_REMOTE_CLIENTS`
  创建字节模式管道，仅允许主进程连接；名字被占用（残留实例/抢占）时每 500ms 重试；
- 断开重连：客户端断开后引擎回到监听状态等待重连，不退出；**重连后握手状态复位，必须重新 hello**。

## 2. 帧格式

```
[uint32 LE 长度 n][n 字节 payload]        n ≤ 16 MiB（kMaxFrameSize）
payload = [1 字节消息类型][消息体]
```

| 类型 | 值 | 方向 | 消息体 |
|---|---|---|---|
| Request | 0x01 | Go → Engine | UTF-8 JSON：`{"id":<num>,"method":"...","params":{...}}` |
| Response | 0x02 | Engine → Go | UTF-8 JSON：`{"id":<num>,"ok":true,"result":...}` 或 `{"id":<num>,"ok":false,"error":{"message":"..."}}` |
| Event | 0x03 | Engine → Go | UTF-8 JSON：`{"event":"...","data":{...}}` |
| Midi | 0x04 | Go → Engine | 预留（二进制，M2 定义） |
| Timecode | 0x05 | Engine → Go | 预留（二进制定长，M2/M3 定义） |

JSON 约定：
- 编码 UTF-8，无 BOM；**载荷不含结尾 NUL 字节**（C++ 侧注意 `CharPointer_UTF8::sizeInBytes()` 含终止符，发送时须 -1）；
- 控制类消息一律走 JSON；实时高频消息（Midi/Timecode）必须走预留的二进制类型，禁止 JSON 化；
- 长度为 0 的帧视为传输错误，接收方断开连接。

## 3. 会话状态机

```
[启动] → 监听 → 客户端连接 → 未握手
未握手：仅放行 hello；其余请求返回 ok=false "engine not ready: send hello first"
hello 成功 → 已握手：全部方法可用
连接断开 → 回到监听（状态复位），重连后须重新 hello
shutdown → 回复后进程优雅退出
```

版本协商：Request 的 hello params **可携带** `"protocolVersion": <int>`；
引擎校验：缺失视为接受当前版本；不匹配则拒绝握手（ok=false, message 含双方版本号）。
响应侧 `result.protocolVersion` 总是返回引擎实际版本。

## 4. 方法字典（v1）

### 4.1 `hello`
| | |
|---|---|
| params | `{}` 或 `{"protocolVersion":1}` |
| result | `{"name":"aimidi-engine","version":"0.1.0","protocolVersion":1,"capabilities":["devices","testTone"]}` |
| 错误 | 版本不匹配 |

### 4.2 `ping`
| | |
|---|---|
| params | `{}` |
| result | `"pong"` |

### 4.3 `listDevices`
枚举全部驱动类型的输出设备。**只做扫描与名称枚举，不打开任何设备**
（打开任意物理设备可能长时间阻塞；采样率/通道数留待 M4 经 applySetup 后的
summary 与扩展方法提供）。
| | |
|---|---|
| params | `{}` |
| result | `{"drivers":[{"driver":"ASIO","devices":[{"name":"..."}]}]}` |
说明：`driver` 取值即 JUCE 设备类型名（如 `ASIO`、`Windows Audio`、`DirectSound`），
applySetup 的 driver 参数须与之精确匹配；此方法在 JUCE 消息线程执行，耗时可能达数秒。

### 4.4 `applySetup`
切换设备/采样率/缓冲区；成功后附带推送 `deviceChanged` 事件。
| | |
|---|---|
| params | `{"driver?":"ASIO","device?":"...","sampleRate?":48000,"bufferSize?":128}`（均可省略，省略项保持现状） |
| result | `{"summary":"<设备> @ <采样率>Hz, buffer=<N>"}` |
| event | `{"event":"deviceChanged","data":{"summary":"..."}}` |
| 错误 | JUCE setAudioDeviceSetup 返回的错误文本 |

### 4.5 `testTone`
正弦测试音开关（M1 验收用），带电平渐变防咔哒声。
| | |
|---|---|
| params | `{"on":true,"freq?":440.0}`（freq 合法域 20–20000 Hz，越界保持原值） |
| result | `{"on":true}` |

### 4.6 `shutdown`
| | |
|---|---|
| params | `{}` |
| result | `"bye"`；引擎随后退出（主进程可等待进程结束或超时强杀） |

### 4.7 未知方法
返回 `ok=false`，`error.message = "unknown method: <method>"`。

## 5. 事件（Engine → Go，单向推送）

| 事件 | data | 触发 |
|---|---|---|
| `deviceChanged` | `{"summary":"..."}` | applySetup 成功后 |

（v2 预留：`deviceListChanged` 热插拔、`xrun`、电平表、错误上报）

## 6. 超时与其他约定

- 主进程对每个请求应设超时（建议：ping/listDevices ≥ 3s，applySetup ≥ 5s，hello 握手 3s）；
- 引擎对畸形 JSON 不回复（请求方靠超时兜底），后续版本可在 Response 中引入显式 parse error；
- 心跳由**主进程侧**负责（1s ping × 连丢 3 次判崩溃），引擎不主动探测客户端；
- 本协议不含鉴权。本地信任边界收紧（客户端 PID 校验/DACL）列入 M5 前加固项。
