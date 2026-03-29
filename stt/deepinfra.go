// Package stt – DeepInfra Whisper large-v3 batch speech-to-text.
//
// DeepInfra hosts OpenAI Whisper models via a simple REST API.
// This client uploads WAV audio and returns the transcribed text.
// Only batch mode is supported (no streaming).
package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
)

// DeepInfraSTT handles batch speech-to-text via DeepInfra's Whisper API.
type DeepInfraSTT struct {
	APIKey  string
	Model   string // default "openai/whisper-large-v3"
	BaseURL string // default "https://api.deepinfra.com/v1/inference"
}

// Transcribe converts raw PCM audio (16 kHz, 16-bit, mono) to text using
// the DeepInfra Whisper REST API. Uses the official /v1/inference/ endpoint
// with "audio" field name, matching DeepInfra's documented curl format.
//
// Language is set to "zh" which correctly handles mixed Chinese+English audio:
//   - whisper-large-v3 + language=zh: Chinese transcribed correctly, English preserved as-is.
//   - Without language hint, Whisper auto-detects "en" and translates Chinese to English.
//   - initial_prompt has no effect on DeepInfra's Whisper API (tested empirically).
func (d *DeepInfraSTT) Transcribe(ctx context.Context, pcmAudio []byte) (string, error) {
	if len(pcmAudio) == 0 {
		return "", fmt.Errorf("empty audio data")
	}

	model := d.Model
	if model == "" {
		model = "openai/whisper-large-v3"
	}
	baseURL := d.BaseURL
	if baseURL == "" {
		baseURL = "https://api.deepinfra.com/v1/inference"
	}

	wavData := deepinfraWAVHeader(pcmAudio, 16000, 16, 1)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("audio", "audio.wav")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(wavData); err != nil {
		return "", fmt.Errorf("write audio data: %w", err)
	}
	_ = writer.WriteField("task", "transcribe")
	_ = writer.WriteField("language", "zh")
	writer.Close()

	reqURL := baseURL + "/" + model
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+d.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	log.Printf("[DeepInfra STT] POST %s (%d bytes audio)", reqURL, len(wavData))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("DeepInfra STT request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("DeepInfra STT API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	log.Printf("[DeepInfra STT] result: %q", result.Text)
	return result.Text, nil
}

// deepinfraWAVHeader prepends a standard 44-byte RIFF/WAV header to raw PCM data.
// This is a local copy to avoid coupling with the ElevenLabs STT implementation.
func deepinfraWAVHeader(pcmData []byte, sampleRate, bitsPerSample, channels int) []byte {
	dataSize := len(pcmData)
	fileSize := 36 + dataSize
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, uint32(fileSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, uint32(16))
	binary.Write(buf, binary.LittleEndian, uint16(1))
	binary.Write(buf, binary.LittleEndian, uint16(channels))
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(buf, binary.LittleEndian, uint32(byteRate))
	binary.Write(buf, binary.LittleEndian, uint16(blockAlign))
	binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(pcmData)
	return buf.Bytes()
}
