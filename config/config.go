// Package config loads application configuration from environment variables
// with optional .env file support. When API keys are empty, the server runs
// in mock mode (no real STT/LLM/TTS calls).
package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration for the backend server.
type Config struct {
	Port           string // HTTP listen port (default "8080")
	ElevenLabsKey  string // ElevenLabs API key for STT and TTS
	OpenRouterKey  string // OpenRouter API key for LLM chat completions
	DefaultModel   string // LLM model identifier (default "deepseek/deepseek-chat-v3.1")
	DefaultVoiceID string // ElevenLabs voice ID for TTS (default "21m00Tcm4TlvDq8ikWAM" = Rachel)
}

// Load reads configuration from environment variables, with .env file support.
// Missing .env is silently ignored (common in production/Docker deployments).
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

// getEnv returns the environment variable value or a fallback default.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
