# Travel English Backend

旅行英语口语练习 App 的 Go 后台服务，串联 STT → LLM → TTS 全链路，Flutter 客户端只需连接一个 WebSocket。

## 架构概览

```
Flutter Client
    ↓ WebSocket (JSON + Binary)
Go Backend (本服务)
    ├── ElevenLabs Realtime STT (WebSocket) — 语音识别
    ├── OpenRouter LLM (SSE Streaming) — 对话生成
    └── ElevenLabs TTS (REST → MP3) — 语音合成
```

**核心设计**：API Key 全部收归后台，客户端零感知。后台换模型/换 TTS 引擎对客户端无影响。

## 功能

### WebSocket 实时对话 (`/ws`)

- **全链路流式**：PCM 音频输入 → 流式 ASR → 流式 LLM → 逐句 TTS → MP3 输出
- **LLM + TTS 并行**：LLM 流式输出同时触发 TTS 合成（goroutine + channel），显著降低端到端延迟
- **双 STT 模式**：Realtime（WebSocket 流式）/ Batch（REST 录完再识别），可配置
- **上下文管理**：最多 40 条消息（20 轮 QA），自动裁剪
- **Mock 模式**：无 API Key 时返回固定响应，用于协议测试

### REST API

| 端点 | 方法 | 功能 |
|------|------|------|
| `/ws` | GET→WebSocket | 实时对话（STT → LLM → TTS） |
| `/hint` | POST | 空闲引导提示（LLM 根据上下文生成建议） |
| `/evaluate` | POST | 对话质量评价（评分 + 纠正 + 反馈） |
| `/health` | GET | 健康检查 `{"status":"ok"}` |

## 快速开始

### 环境要求

- Go 1.21+

### 配置

复制 `.env.example` 为 `.env`，填入 API 密钥：

```bash
cp .env.example .env
```

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | 8080 | 监听端口 |
| `ELEVENLABS_API_KEY` | *(必填)* | ElevenLabs STT/TTS |
| `OPENROUTER_API_KEY` | *(必填)* | OpenRouter LLM |
| `DEFAULT_MODEL` | `deepseek/deepseek-chat-v3.1` | LLM 模型（中国区可用） |
| `DEFAULT_VOICE_ID` | `21m00Tcm4TlvDq8ikWAM` | TTS 语音（Rachel） |

不填 API Key 则以 Mock 模式运行（固定响应，不调外部 API）。

### 运行

```bash
# 安装依赖
go mod download

# 运行
go run main.go

# 编译
go build -o travel-english-backend .

# 测试（Mock 模式，无需 API Key）
go test ./test/ -v
```

## WebSocket 协议

### Client → Server

| 类型 | 说明 | 字段 |
|------|------|------|
| `session.start` | 初始化会话 | `config: {system_role, speaking_style, tts_voice_id, stt_language, stt_mode}` |
| Binary frame | PCM 音频（16kHz/16-bit mono，640字节/20ms） | 原始字节 |
| `audio.end` | 结束录音，触发 STT→LLM→TTS 链路 | — |
| `text.query` | 文本输入（跳过 STT） | `text` |
| `tts.synthesize` | 纯 TTS 合成（欢迎语/消息重播） | `text` |
| `conversation.history` | 注入历史上下文 | `items: [{role, text}]` |
| `session.end` | 结束会话 | — |

### Server → Client

| 类型 | 说明 | 字段 |
|------|------|------|
| `session.started` | 会话就绪 | `session_id` |
| `asr.result` | 语音识别结果 | `text, is_final` |
| `chat.delta` | LLM 流式 token | `text`（增量） |
| `chat.done` | LLM 回复完成 | — |
| `tts.start` | 音频流开始 | — |
| Binary frame | MP3 音频（逐句） | 原始字节 |
| `tts.end` | 音频流结束 | — |
| `error` | 错误 | `code, message` |

## REST API 详情

### POST /hint

用户沉默 10 秒后，客户端调用此接口获取上下文相关的英文建议。

```json
// 请求
{
  "scene_id": "airport",
  "messages": [
    {"role": "assistant", "text": "Welcome! How can I help you?"},
    {"role": "user", "text": "I need to find my gate."}
  ]
}

// 响应
{
  "hint_en": "Could you tell me where gate 12 is?",
  "hint_cn": "你可以试着问: Could you tell me where gate 12 is?（请问12号登机口在哪？）"
}
```

### POST /evaluate

评价用户英语表达质量，返回评分、纠正和反馈。

```json
// 请求
{
  "user_text": "I want check in please",
  "scene_id": "airport",
  "context": [
    {"role": "assistant", "text": "Welcome to the airport. How can I help you?"}
  ]
}

// 响应
{
  "score": 2,
  "correction": "I would like to check in, please.",
  "feedback": "缺少了动词不定式to。使用I would like会显得更有礼貌。"
}
```

## 项目结构

```
├── main.go                    # 入口，注册 HTTP 路由
├── config/
│   └── config.go              # 环境变量加载（.env 支持）
├── ws/
│   ├── handler.go             # WebSocket 升级 + 读取循环
│   ├── session.go             # 核心状态机 + STT→LLM→TTS 管线
│   └── protocol.go            # JSON 消息类型定义
├── stt/
│   └── elevenlabs.go          # ElevenLabs STT（Realtime WebSocket + Batch REST）
├── llm/
│   ├── openrouter.go          # OpenRouter SSE 流式调用
│   └── context.go             # 对话历史管理（最多 40 条）
├── tts/
│   ├── elevenlabs.go          # ElevenLabs TTS（REST → MP3）
│   └── sentence_splitter.go   # 句子边界检测（流式分句）
├── hint/
│   └── handler.go             # POST /hint 空闲引导
├── evaluate/
│   └── handler.go             # POST /evaluate 对话评价
├── test/
│   └── ws_integration_test.go # 集成测试（6 个用例）
├── .env.example               # 配置模板
└── go.mod / go.sum
```

## 测试

```bash
# 运行全部测试（Mock 模式，无需 API Key）
go test ./test/ -v
```

6 个集成测试用例：

| 测试 | 验证内容 |
|------|---------|
| TestHealthEndpoint | /health 返回 200 OK |
| TestSessionLifecycle | session.start → session.ended 生命周期 |
| TestAudioEndMockPipeline | 完整 Mock 对话链路（Binary→ASR→Chat→TTS→MP3） |
| TestTextQuery | 文本输入（跳过 STT）→ Chat → TTS |
| TestTtsSynthesize | 纯 TTS 合成请求 |
| TestConversationHistory | 历史上下文注入 |

## 部署

当前部署在 `ws.reikly.com`，通过 nginx 反向代理（SSL + WebSocket upgrade）：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 86400s;
}
```

使用 systemd 管理服务进程：

```bash
systemctl start travel-english-backend
systemctl status travel-english-backend
```

## 技术参数

| 参数 | 值 |
|------|-----|
| 音频输入 | PCM 16kHz 16-bit mono（640字节/20ms） |
| 音频输出 | MP3（逐句） |
| LLM max_tokens | 100（保持回复简短） |
| TTS 模型 | eleven_multilingual_v2 |
| 上下文上限 | 40 条消息（自动裁剪） |
| STT 超时 | 10s（Realtime）/ 30s（Batch） |
| LLM 超时 | 60s |
| Hint 超时 | 10s |
| Evaluate 超时 | 10s |
