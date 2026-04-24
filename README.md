# asr_trans

基于 WebSocket 的音频转写客户端，通过 WSS 协议连接 ASR 服务端，支持长音频自动分割、离线识别、说话人分离（Speaker Diarization），并将识别结果合并输出。

## 功能特性

- **长音频自动分割** — 超过阈值的 WAV 文件自动按片切分，分别发送识别后合并
- **说话人分离** — 通过 session_id 实现跨片段说话人一致性
- **断线兜底** — 连接断开后自动重连服务端查询缓存结果（30秒重试，最长30分钟超时）
- **时间戳校准** — 自动对分割片段的时间戳进行偏移校正
- **结构化日志** — 基于 zap 的 JSON 日志，按天轮转，分 info/warn/error 文件

## 快速开始

### 编译

```bash
go build -o asr_trans .
```

### 运行

```bash
./asr_trans -host 127.0.0.1 -port 10096 -audio_in speaker123.wav
```

### 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-host` | `127.0.0.1` | ASR 服务端地址 |
| `-port` | `10096` | ASR 服务端端口 |
| `-audio_in` | `speaker123.wav` | 输入音频文件路径（WAV 格式） |

### 输出示例

```
========== 识别结果（带说话人） ==========
[spk1] 0ms - 3500ms: 你好，欢迎使用语音识别服务
[spk2] 3600ms - 7200ms: 谢谢，我来测试一下

========== 完整文本 ==========
你好，欢迎使用语音识别服务谢谢，我来测试一下
```

## 项目结构

```
.
├── main.go                 # 程序入口，日志初始化，参数解析
├── audioTranscriber.go     # 核心转写逻辑
├── pkg/log/
│   ├── logger.go           # 日志接口定义（Debug/Info/Warn/Error/WithFields）
│   └── zap.go              # zap 实现，支持 stdout + file 双输出
├── go.mod
└── go.sum
```

## 日志

日志默认输出到 `./logs/` 目录，按天轮转，保留 7 天：

| 文件 | 说明 |
|------|------|
| `asr_trans.log` | 全部日志（info 及以下级别） |
| `asr_trans.wf.log` | 警告日志 |
| `asr_trans.err.log` | 错误日志 |

- **控制台**：彩色文本格式，方便开发调试
- **文件**：JSON 格式，便于日志采集和分析

日志配置在 `main.go` 的 `initLogger()` 中修改：

```go
cfg := &log.Config{
    Writers:          "stdout,file",   // 输出目标：stdout, file
    LoggerLevel:      "debug",         // 最低日志级别
    LoggerFile:       "./logs/asr_trans.log",
    LoggerWarnFile:   "./logs/asr_trans.wf.log",
    LoggerErrorFile:  "./logs/asr_trans.err.log",
    LogRollingPolicy: "daily",         // 轮转策略：daily, hourly
    LogBackupCount:   7,               // 保留份数
    AppName:          "asr_trans",
}
```

## 工作流程

```
WAV 文件 → 判断是否需要分割 → 逐片段通过 WebSocket 发送 → 服务端识别
                                                          ↓
              时间戳偏移校正 ← 合并结果 ← 接收识别结果（含说话人信息）
                                                          ↓
                                                    发送 session_end
                                                    清理临时文件
```

1. **音频分割** — 读取 WAV header 获取采样率，计算每个 chunk 的 stride，超过 maxChunks 时按文件切分
2. **WebSocket 通信** — 每个片段建立独立 WSS 连接，发送配置消息 → 音频数据 → 结束标记
3. **结果接收** — 等待 `is_final=True` 的最终结果，对时间戳进行偏移校正
4. **断线恢复** — 连接异常断开后，通过 `getCachedResult` 重连查询服务端缓存
5. **会话结束** — 所有片段完成后发送 `session_end` 通知服务端清理说话人注册表

## 集成到 ray 项目

`audioTranscriber.go` 的日志调用使用 `log.Info/Error/Warn` 风格，与 ray 项目的 `hexmeet.com/ray/pkg/log` 接口一致。copy 到 ray 项目后，只需修改 import：

```go
// 改这一行即可
import "hexmeet.com/ray/pkg/log"
```

其余代码无需任何修改。

## 依赖

- [gorilla/websocket](https://github.com/gorilla/websocket) — WebSocket 客户端
- [go.uber.org/zap](https://go.uber.org/zap) — 高性能结构化日志
- [lestrrat-go/file-rotatelogs](https://github.com/lestrrat-go/file-rotatelogs) — 日志文件轮转
