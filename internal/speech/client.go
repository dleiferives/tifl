// Package speech is the API-server client for the separately deployed
// OpenAI-compatible audio-server. Browser clients reach it only through
// authenticated Tifl conversation endpoints.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	maxSynthesisBytes = 32 << 20
	maxErrorBytes     = 64 << 10
)

var ErrEmptyTranscript = errors.New("speech: transcription is empty")

type Config struct {
	BaseURL  string
	APIKey   string
	TTSModel string
	TTSVoice string
	TTSSpeed float64
	STTModel string
	HTTP     *http.Client
}

type Audio struct {
	Data        []byte
	ContentType string
}

type TranscriptionInput struct {
	Data        []byte
	Filename    string
	ContentType string
	Language    string
}

// Gateway is the handler-facing audio service contract. *Client and test fakes
// satisfy it.
type Gateway interface {
	Synthesize(ctx context.Context, text, language string) (Audio, error)
	Transcribe(ctx context.Context, input TranscriptionInput) (string, error)
}

type Client struct {
	baseURL  string
	apiKey   string
	ttsModel string
	ttsVoice string
	ttsSpeed float64
	sttModel string
	http     *http.Client
}

var _ Gateway = (*Client)(nil)

func New(cfg Config) *Client {
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.TTSModel == "" {
		cfg.TTSModel = "auto"
	}
	if cfg.TTSVoice == "" {
		cfg.TTSVoice = "auto"
	}
	if cfg.TTSSpeed == 0 {
		cfg.TTSSpeed = 0.9
	}
	if cfg.STTModel == "" {
		cfg.STTModel = "auto"
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey,
		ttsModel: cfg.TTSModel, ttsVoice: cfg.TTSVoice, ttsSpeed: cfg.TTSSpeed,
		sttModel: cfg.STTModel, http: httpClient,
	}
}

func (c *Client) Synthesize(ctx context.Context, text, language string) (Audio, error) {
	if c.baseURL == "" {
		return Audio{}, errors.New("speech: audio server is not configured")
	}
	body, err := json.Marshal(map[string]any{
		"model": c.ttsModel, "input": text, "voice": c.ttsVoice,
		"language": language, "response_format": "mp3", "speed": c.ttsSpeed,
	})
	if err != nil {
		return Audio{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return Audio{}, fmt.Errorf("speech: synthesize: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Audio{}, upstreamError("synthesize", resp)
	}
	data, err := readLimited(resp.Body, maxSynthesisBytes)
	if err != nil {
		return Audio{}, fmt.Errorf("speech: synthesize response: %w", err)
	}
	if len(data) == 0 {
		return Audio{}, errors.New("speech: synthesize returned empty audio")
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if !strings.HasPrefix(contentType, "audio/") && contentType != "application/octet-stream" {
		return Audio{}, fmt.Errorf("speech: synthesize returned unexpected content type %q", contentType)
	}
	return Audio{Data: data, ContentType: contentType}, nil
}

func (c *Client) Transcribe(ctx context.Context, input TranscriptionInput) (string, error) {
	if c.baseURL == "" {
		return "", errors.New("speech: audio server is not configured")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filename := input.Filename
	if filename == "" {
		filename = "recording.webm"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	if input.ContentType != "" {
		header.Set("Content-Type", input.ContentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(input.Data); err != nil {
		return "", err
	}
	for name, value := range map[string]string{
		"model": c.sttModel, "language": input.Language, "response_format": "json",
	} {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return "", err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("speech: transcribe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", upstreamError("transcribe", resp)
	}
	var result struct {
		Text string `json:"text"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBytes))
	if err := dec.Decode(&result); err != nil {
		return "", fmt.Errorf("speech: decode transcription: %w", err)
	}
	result.Text = strings.TrimSpace(result.Text)
	if result.Text == "" {
		return "", ErrEmptyTranscript
	}
	return result.Text, nil
}

func (c *Client) authorize(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func upstreamError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("speech: %s: upstream status %d: %s", action, resp.StatusCode, message)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return data, nil
}
