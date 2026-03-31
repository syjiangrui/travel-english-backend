# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run Commands

```bash
# 启动后台服务 (默认端口 8080)
go run .

# 运行测试 (mock 模式，不需要 API key)
go test ./test/ -v

# 运行集成测试 (需要 .env 中的真实 API key)
go test ./test/ -tags=integration -v

# 手动 WebSocket 调试
go run . &
wscat -c ws://localhost:8080/ws
```

## Architecture

Go 后台服务，替代火山引擎 S2S 直连，串联 STT → OpenRouter LLM (streaming) → ElevenLabs TTS。Flutter 客户端只连此后台 WebSocket，API key 全部收归后台管理。另提供 3 个 REST API 端点供不同功能使用。

### STT 引擎

支持两个 STT 提供商，通过环境变量 `STT_PROVIDER` 设全局默认，客户端可在 `session.start` 的 `config.stt_provider` 覆盖：

| 引擎 | 模式 | 说明 |
|------|------|------|
| **ElevenLabs** (默认) | Realtime + Batch | Scribe v2 流式 WebSocket + REST fallback |
| **DeepInfra Whisper** | 仅 Batch | Whisper large-v3，`language=zh` + `task=transcribe`，中英混合音频正确转写 |

**DeepInfra Whisper 参数调试结论**（实测验证）：
- `language=zh` 是必需的：不传则 Whisper 自动检测为 en，会将中文翻译成英文
- `whisper-large-v3` + `language=zh`：中文正确转写，英文也保留原文 ✅
- `whisper-large-v3-turbo` + `language=zh`：中文正确，但英文会翻译成中文 ❌
- `initial_prompt` 对 DeepInfra 的 Whisper API 无任何效果（已实测验证）

### 文件结构

```
travel-english-backend/
├── main.go                   // HTTP server: /ws + /hint + /evaluate + /memory + /health
├── config/config.go          // 环境变量加载 (.env → Config struct)
├── hint/handler.go           // POST /hint: 空闲引导提示 (LLM 生成上下文引导语)
├── evaluate/handler.go       // POST /evaluate: 对话质量评价 (评分+纠正+反馈)
├── memory/handler.go         // POST /memory: 长期记忆提取 (temperature:0.1, max_tokens:300)
├── ws/
│   ├── handler.go            // WebSocket upgrade + read loop (text/binary 路由)
│   ├── session.go            // 会话状态 + 消息路由 + pipeline 编排 + session.update (核心)
│   └── protocol.go           // JSON 消息类型定义 (ClientMessage/ServerMessage)
├── stt/elevenlabs.go         // ElevenLabs STT: 实时 WebSocket + batch REST fallback
├── stt/deepinfra.go          // DeepInfra Whisper large-v3 batch STT (language=zh)
├── llm/
│   ├── openrouter.go         // OpenRouter SSE streaming (支持 MaxTokens/Temperature 可选参数)
│   └── context.go            // 对话历史管理 (max 40 条, 自动 trim)
├── tts/
│   ├── elevenlabs.go         // ElevenLabs TTS: POST → MP3 bytes
│   └── sentence_splitter.go  // 句子边界检测 (.!?。！？) → 分句给 TTS
├── test/
│   └── ws_integration_test.go // WebSocket 集成测试 (6 个测试用例)
├── .env                      // 真实 API keys (不入 git)
└── .env.example              // 配置模板
```

### 核心 Pipeline (session.go → streamLLMWithTTS)

```
Client PCM → ElevenLabs Realtime STT (WebSocket)
         → asr.result (partial + final)
         → OpenRouter SSE stream
         → chat.delta 逐 token 转发客户端
         → SentenceSplitter 按句拆分
         → goroutine: 每句 → ElevenLabsTTS.Synthesize() → MP3 binary frame
         → chat.done + tts.end
```

LLM streaming 和 TTS 合成**并行**：goroutine 从 `sentenceCh` channel 消费完整句子，边生成边发。

### Mock/Live 双模式

- **Live 模式**: `.env` 中配置了至少一个 STT API key (`ELEVENLABS_API_KEY` 或 `DEEPINFRA_API_KEY`) + `OPENROUTER_API_KEY` 时自动启用
- **Mock 模式**: 无 API key 时返回固定 mock 响应（`mockAudioEnd/mockTextQuery/sendMockTTS`），用于测试协议正确性
- STT 实时 WebSocket 断线后自动 fallback 到 batch REST API（`handleAudioEnd` 中的 `batchFallback` label）
- **STT 双模式**: `session.start` 的 `config.stt_mode` 支持 `"realtime"`（默认，流式 WebSocket STT）和 `"batch"`（非流式，录完后一次性 REST API 识别）。`isBatchSTT()` 辅助方法控制三处分支：`handleSessionStart` 跳过 realtime STT 连接、`HandleBinary` 仅缓冲不转发、`handleAudioEnd` 直接走 batch REST 路径
- **STT 引擎切换**: `config.stt_provider` 支持 `"elevenlabs"`（默认）和 `"deepinfra"`，batch fallback 中 `switch provider` 分发到对应客户端。优先级：客户端 session config > 服务端环境变量 > 默认 elevenlabs

## WebSocket 协议

WebSocket 原生帧类型区分：Text frame = JSON 控制消息，Binary frame = 音频数据。

### Client → Server

| type | 说明 | 额外字段 |
|------|------|---------|
| `session.start` | 建立会话，初始化 STT/Context | `config: {system_role, speaking_style, tts_voice_id, stt_language, stt_mode, stt_provider}` |
| `session.update` | 中途更新 session 配置（不断连重连） | `config: {system_role}` — 直接更新 ContextManager.SystemRole |
| *(binary frame)* | PCM 音频块 (16kHz 16bit mono) | 服务端实时转发给 ElevenLabs STT |
| `audio.end` | 录音结束，commit STT → 触发 LLM→TTS 链 | — |
| `text.query` | 文本输入 (跳过 STT，直接进 LLM) | `text` |
| `tts.synthesize` | 纯 TTS 请求 (问候/消息重播) | `text` |
| `conversation.history` | 注入历史上下文到 ContextManager | `items: [{role, text}]` |
| `session.end` | 结束会话，关闭 STT WebSocket | — |

### Server → Client

| type | 说明 | 额外字段 |
|------|------|---------|
| `session.started` | 会话就绪 | `session_id` |
| `asr.result` | 语音识别结果 | `text, is_final` (partial: false, final: true) |
| `chat.delta` | LLM 流式 token | `text` (增量) |
| `chat.done` | LLM 完成 | — |
| `tts.start` | TTS 音频即将发送 | — |
| *(binary frame)* | MP3 音频块 (按句发送) | — |
| `tts.end` | TTS 全部发送完毕 | — |
| `error` | 错误 | `code, message` |

## 关键类型与方法

### config.Config
```go
Port, ElevenLabsKey, OpenRouterKey, DefaultModel, DefaultVoiceID, DeepInfraKey, STTProvider
```
环境变量名: `PORT`, `ELEVENLABS_API_KEY`, `OPENROUTER_API_KEY`, `DEFAULT_MODEL`, `DEFAULT_VOICE_ID`, `DEEPINFRA_API_KEY`, `STT_PROVIDER`

### ws.Session (核心状态机)
```
sendJSON(v) / sendBinary(data)     // 线程安全写入 (mu sync.Mutex)
HandleMessage(raw) / HandleBinary(data)  // 读 loop 路由
handleSessionStart → connectSTT    // 初始化 + 连接实时 STT
handleSessionUpdate                // 中途更新 system_role (不断连)
handleAudioEnd → streamLLMWithTTS  // 核心 pipeline (realtime commit 或 batch REST)
handleTextQuery → streamLLMWithTTS // 文本输入路径
handleTTSSynthesize                // 独立 TTS 请求
isLive()                           // API key 是否配置
isBatchSTT()                       // stt_mode == "batch"
```

### stt.RealtimeSTT (实时 WebSocket STT)
```
Connect(ctx, language)   // 连接 ElevenLabs realtime endpoint
SendAudio(pcm []byte)    // base64 编码发送 PCM 块
Commit()                 // 提交当前语音段，等待 committed_transcript
Close()                  // 关闭 WebSocket
OnPartial / OnCommitted / OnError  // 回调
```

### stt.ElevenLabsSTT (批量 REST STT, fallback)
```
Transcribe(ctx, pcmAudio) → text  // multipart POST, 自动加 WAV header
```

### stt.DeepInfraSTT (DeepInfra Whisper batch STT)
```
Transcribe(ctx, pcmAudio) → text  // multipart POST, PCM→WAV, language=zh, task=transcribe
// 默认模型: openai/whisper-large-v3
// 端点: https://api.deepinfra.com/v1/inference/{model}
```

### llm.OpenRouterLLM
```
StreamChat(ctx, messages, onDelta) → (fullResponse, err)
// SSE 解析, OpenAI 兼容格式, 累积完整回复
// MaxTokens: 0 = default (100), 可自定义 (e.g. 300 for memory extraction)
// Temperature: 0 = omit (API default), >0 = include in request
```

### llm.ContextManager
```
AddUserMessage(text) / AddAssistantMessage(text)  // 自动 trim 到 40 条
SetHistory(items) / BuildMessages(userText)
SystemRole  // 可通过 session.update 中途更新
```

### tts.ElevenLabsTTS
```
Synthesize(ctx, text) → mp3Bytes  // eleven_multilingual_v2 模型
```

### tts.SentenceSplitter
```
Feed(delta)  // 缓冲 token, 句子边界时触发 OnSentence 回调
Flush()      // 发送剩余缓冲文本
// 边界: ". " "! " "? " ".\n" "!\n" "?\n" "。" "！" "？"
```

### memory.HandleMemory (POST /memory)
```
// 请求: {"messages": [{role, text}]}
// LLM: system prompt (记忆总结助手) + user (对话历史)
// 参数: temperature=0.1, max_tokens=300
// 解析: 正则 \[.*\] → JSON parse → fallback 空数组
// 响应: {"memories": ["...", "..."]}
```

## 默认配置

| 配置项 | 默认值 |
|--------|--------|
| Port | 8080 |
| LLM Model | google/gemini-3.1-flash-lite-preview (通过 .env DEFAULT_MODEL 配置) |
| LLM max_tokens | 100 (对话, 默认) / 300 (记忆提取, handler 覆盖) |
| LLM temperature | API default (对话) / 0.1 (记忆提取, 稳定 JSON 输出) |
| TTS Voice | 21m00Tcm4TlvDq8ikWAM (Rachel) |
| TTS Model | eleven_multilingual_v2 |
| TTS previous_text | 自动传递上一句文本，保持语气连贯 |
| STT Model | scribe_v2_realtime |
| STT language | 自动检测 (支持中英混说) |
| STT VAD | vad_threshold=0.5, min_speech=200ms, min_silence=200ms |
| STT previous_text | 自动传递上一轮 AI 回复，提高识别准确度 |
| 音频格式 | PCM 16kHz 16-bit mono (输入) / MP3 (输出) |
| 上下文上限 | 40 条消息 (20 轮 QA) |
| STT 超时 | commit 10s, batch 30s |
| LLM 超时 | 60s |
| Hint/Evaluate/Memory 超时 | 10s |

## 测试

6 个测试用例 (mock 模式, 不需要 API key):
1. `TestHealthEndpoint` — /health 返回 200
2. `TestSessionLifecycle` — session.start → session.ended
3. `TestAudioEndMockPipeline` — binary audio → asr.result → chat.delta × N → chat.done → tts.start → binary → tts.end
4. `TestTextQuery` — text.query → chat.delta × N → chat.done → tts
5. `TestTtsSynthesize` — tts.synthesize → tts.start → binary → tts.end
6. `TestConversationHistory` — conversation.history 注入

## 客户端配合

Flutter 客户端通过 `BackendConversationService` (lib/services/backend_conversation_service.dart) 连接此后台：
- 发送: JSON text frame + PCM binary frame
- 接收: JSON 控制消息 + MP3 binary frame (队列逐句播放)
- `sendSessionUpdate(systemRole:)`: 发送 `session.update` 消息中途刷新 system_role
- 后台换模型/换 TTS 引擎对客户端零影响

BingoScreen 通过 `BingoMemoryService` 调用 `POST /memory`：
- 每 2 轮 AI 回复后触发，发送最近 4 条消息
- 返回的记忆数组存入本地 + 通过 `session.update` 刷新 server 端 system_role
