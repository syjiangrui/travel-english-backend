// Package main is the entry point for the travel-english-backend server.
// It exposes HTTP endpoints for both conversation and utility features:
//   - /ws      — WebSocket endpoint for real-time conversation (STT → LLM → TTS)
//   - /tts     — standalone TTS synthesis (MP3)
//   - /health  — JSON health check for load balancers and monitoring
package main

import (
	"fmt"
	"log"
	"net/http"

	"travel-english-backend/config"
	"travel-english-backend/evaluate"
	"travel-english-backend/hint"
	"travel-english-backend/memory"
	"travel-english-backend/translate"
	"travel-english-backend/tts"
	"travel-english-backend/ws"
)

func main() {
	cfg := config.Load()

	http.HandleFunc("/ws", ws.NewHandler(cfg))
	http.HandleFunc("/hint", hint.HandleHint(cfg))
	http.HandleFunc("/evaluate", evaluate.HandleEvaluate(cfg))
	http.HandleFunc("/memory", memory.HandleMemory(cfg))
	http.HandleFunc("/translate", translate.HandleTranslate(cfg))
	http.HandleFunc("/tts", tts.HandleSynthesize(cfg))
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	addr := ":" + cfg.Port
	log.Printf("Backend ready on port %s (ElevenLabs=%v, OpenRouter=%v)",
		cfg.Port, cfg.ElevenLabsKey != "", cfg.OpenRouterKey != "")
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
