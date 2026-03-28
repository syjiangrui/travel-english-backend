# Travel English Backend

Go WebSocket server that proxies between the Flutter travel English app and AI services (ElevenLabs STT/TTS, OpenRouter LLM).

## Phase 1 (Current)

Mock responses only — no real API calls. The server implements the full WebSocket protocol with hardcoded responses for testing the Flutter client integration.

## Quick Start

```bash
# Install dependencies
go mod download

# Run the server
go run main.go

# Run tests
go test ./test/ -v

# With custom port
PORT=9090 go run main.go
```

## Endpoints

- `GET /ws` — WebSocket endpoint
- `GET /health` — Health check (`{"status":"ok"}`)

## WebSocket Protocol

### Client → Server

| Type | Description |
|------|-------------|
| `session.start` | Start a new session with config |
| Binary frames | PCM audio chunks (16kHz 16-bit mono) |
| `audio.end` | End audio input, trigger STT→LLM→TTS pipeline |
| `text.query` | Text input (skips STT) |
| `tts.synthesize` | Pure TTS synthesis |
| `conversation.history` | Load conversation context |
| `session.end` | End session |

### Server → Client

| Type | Description |
|------|-------------|
| `session.started` | Session created with session_id |
| `asr.result` | Speech recognition result |
| `chat.delta` | Streaming LLM token |
| `chat.done` | LLM response complete |
| `tts.start` | TTS audio starting |
| Binary frames | MP3 audio chunks |
| `tts.end` | TTS audio complete |
| `error` | Error with code and message |

## Project Structure

```
├── main.go                  # Entry point
├── config/config.go         # Environment configuration
├── ws/
│   ├── protocol.go          # Message type definitions
│   ├── handler.go           # WebSocket upgrade + read loop
│   └── session.go           # Session state + mock pipeline
├── stt/elevenlabs.go        # STT interface + stub (Phase 2)
├── llm/
│   ├── openrouter.go        # LLM interface + stub (Phase 2)
│   └── context.go           # Context management stub (Phase 2)
├── tts/
│   ├── elevenlabs.go        # TTS interface + stub (Phase 2)
│   └── sentence_splitter.go # Sentence splitting for streaming TTS
└── test/
    └── ws_integration_test.go
```
