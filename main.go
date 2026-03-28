package main

import (
	"fmt"
	"log"
	"net/http"

	"travel-english-backend/config"
	"travel-english-backend/ws"
)

func main() {
	cfg := config.Load()

	http.HandleFunc("/ws", ws.NewHandler(cfg))
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
