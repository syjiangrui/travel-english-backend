package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port            string
	ElevenLabsKey   string
	OpenRouterKey   string
	DefaultModel    string
	DefaultVoiceID  string
}

func Load() *Config {
	// Best-effort load .env; ignore error if missing
	_ = godotenv.Load()

	return &Config{
		Port:           getEnv("PORT", "8080"),
		ElevenLabsKey:  getEnv("ELEVENLABS_API_KEY", ""),
		OpenRouterKey:  getEnv("OPENROUTER_API_KEY", ""),
		DefaultModel:   getEnv("DEFAULT_MODEL", "deepseek/deepseek-chat-v3.1"),
		DefaultVoiceID: getEnv("DEFAULT_VOICE_ID", "21m00Tcm4TlvDq8ikWAM"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
