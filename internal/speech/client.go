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
	"net/url"
	"strings"
	"time"
)

const (
	maxSynthesisBytes      = 32 << 20
	maxAlignmentBytes      = 4 << 20
	maxBatchAlignmentBytes = 16 << 20
	maxErrorBytes          = 64 << 10
	alignmentTimeout       = 90 * time.Second
	batchAlignmentTimeout  = 10 * time.Minute
	alignmentPollWait      = 500 * time.Millisecond
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

type SynthesisInput struct {
	Text     string
	Language string
	Model    string
}

type AlignmentInput struct {
	Audio      Audio
	Filename   string
	Transcript string
	Language   string
}

type AlignmentBatchInput struct {
	Items    []AlignmentBatchItem
	Language string
}

type AlignmentBatchItem struct {
	ID         string
	Audio      Audio
	Filename   string
	Transcript string
}

type WordTiming struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Alignment struct {
	Words []WordTiming `json:"words"`
}

type AlignmentBatch struct {
	Items []AlignmentBatchResult `json:"items"`
}

type AlignmentBatchResult struct {
	ID        string    `json:"id"`
	Alignment Alignment `json:"alignment"`
}

type BatchAligner interface {
	AlignBatch(ctx context.Context, input AlignmentBatchInput) (AlignmentBatch, error)
}

// Gateway is the handler-facing audio service contract. *Client and test fakes
// satisfy it.
type Gateway interface {
	Synthesize(ctx context.Context, input SynthesisInput) (Audio, error)
	Align(ctx context.Context, input AlignmentInput) (Alignment, error)
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

func (c *Client) Synthesize(ctx context.Context, input SynthesisInput) (Audio, error) {
	if c.baseURL == "" {
		return Audio{}, errors.New("speech: audio server is not configured")
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = c.ttsModel
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "input": input.Text, "voice": c.ttsVoice,
		"language": input.Language, "response_format": "mp3", "speed": c.ttsSpeed,
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

// Align queues MFA forced alignment on the audio server and polls the short-
// lived job until its word timings are available.
func (c *Client) Align(ctx context.Context, input AlignmentInput) (Alignment, error) {
	if c.baseURL == "" {
		return Alignment{}, errors.New("speech: audio server is not configured")
	}
	if len(input.Audio.Data) == 0 || strings.TrimSpace(input.Transcript) == "" || strings.TrimSpace(input.Language) == "" {
		return Alignment{}, errors.New("speech: alignment audio, transcript, and language are required")
	}
	ctx, cancel := context.WithTimeout(ctx, alignmentTimeout)
	defer cancel()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filename := strings.TrimSpace(input.Filename)
	if filename == "" {
		filename = "speech.mp3"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	if input.Audio.ContentType != "" {
		header.Set("Content-Type", input.Audio.ContentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		return Alignment{}, err
	}
	if _, err := part.Write(input.Audio.Data); err != nil {
		return Alignment{}, err
	}
	if err := writer.WriteField("transcript", input.Transcript); err != nil {
		return Alignment{}, err
	}
	if err := writer.WriteField("language", input.Language); err != nil {
		return Alignment{}, err
	}
	if err := writer.Close(); err != nil {
		return Alignment{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audio/alignments", &body)
	if err != nil {
		return Alignment{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return Alignment{}, fmt.Errorf("speech: align: %w", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		defer resp.Body.Close()
		return Alignment{}, upstreamError("align", resp)
	}
	job, err := decodeAlignmentJob(resp)
	if err != nil {
		return Alignment{}, err
	}
	for {
		switch job.Status {
		case "succeeded":
			if len(job.Result.Words) > 0 {
				return job.Result, nil
			}
			return c.alignmentResult(ctx, job.ID)
		case "failed":
			if strings.TrimSpace(job.Error) == "" {
				job.Error = "alignment job failed"
			}
			return Alignment{}, errors.New("speech: align: " + job.Error)
		case "queued", "running":
		default:
			return Alignment{}, fmt.Errorf("speech: align: unexpected job status %q", job.Status)
		}

		timer := time.NewTimer(alignmentPollWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Alignment{}, fmt.Errorf("speech: align: %w", ctx.Err())
		case <-timer.C:
		}
		job, err = c.alignmentJob(ctx, job.ID)
		if err != nil {
			return Alignment{}, err
		}
	}
}

// AlignBatch sends independent clips as one MFA corpus and polls one job. This
// avoids repeating Python/Kaldi/model initialization for every reader sentence.
func (c *Client) AlignBatch(ctx context.Context, input AlignmentBatchInput) (AlignmentBatch, error) {
	if c.baseURL == "" {
		return AlignmentBatch{}, errors.New("speech: audio server is not configured")
	}
	if len(input.Items) == 0 || strings.TrimSpace(input.Language) == "" {
		return AlignmentBatch{}, errors.New("speech: batch alignment items and language are required")
	}
	ctx, cancel := context.WithTimeout(ctx, batchAlignmentTimeout)
	defer cancel()

	manifest := struct {
		Language string `json:"language"`
		Items    []struct {
			ID         string `json:"id"`
			Transcript string `json:"transcript"`
		} `json:"items"`
	}{Language: input.Language, Items: make([]struct {
		ID         string `json:"id"`
		Transcript string `json:"transcript"`
	}, len(input.Items))}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	seen := make(map[string]struct{}, len(input.Items))
	for i, item := range input.Items {
		item.ID = strings.TrimSpace(item.ID)
		item.Transcript = strings.TrimSpace(item.Transcript)
		if item.ID == "" || item.Transcript == "" || len(item.Audio.Data) == 0 {
			return AlignmentBatch{}, fmt.Errorf("speech: batch alignment item %d requires id, audio, and transcript", i)
		}
		if _, exists := seen[item.ID]; exists {
			return AlignmentBatch{}, fmt.Errorf("speech: duplicate batch alignment id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		manifest.Items[i].ID = item.ID
		manifest.Items[i].Transcript = item.Transcript
		filename := strings.TrimSpace(item.Filename)
		if filename == "" {
			filename = fmt.Sprintf("sentence-%06d.mp3", i)
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
		if item.Audio.ContentType != "" {
			header.Set("Content-Type", item.Audio.ContentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			return AlignmentBatch{}, err
		}
		if _, err := part.Write(item.Audio.Data); err != nil {
			return AlignmentBatch{}, err
		}
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return AlignmentBatch{}, err
	}
	if err := writer.WriteField("manifest", string(manifestJSON)); err != nil {
		return AlignmentBatch{}, err
	}
	if err := writer.Close(); err != nil {
		return AlignmentBatch{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audio/alignments/batch", &body)
	if err != nil {
		return AlignmentBatch{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.authorize(req)
	resp, err := c.batchHTTPClient().Do(req)
	if err != nil {
		return AlignmentBatch{}, fmt.Errorf("speech: align batch: %w", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		defer resp.Body.Close()
		return AlignmentBatch{}, upstreamError("align batch", resp)
	}
	job, err := decodeAlignmentBatchJob(resp)
	if err != nil {
		return AlignmentBatch{}, err
	}
	for {
		switch job.Status {
		case "succeeded":
			if len(job.Result.Items) > 0 {
				return job.Result, nil
			}
			return c.alignmentBatchResult(ctx, job.ID)
		case "failed":
			if strings.TrimSpace(job.Error) == "" {
				job.Error = "batch alignment job failed"
			}
			return AlignmentBatch{}, errors.New("speech: align batch: " + job.Error)
		case "queued", "running":
		default:
			return AlignmentBatch{}, fmt.Errorf("speech: align batch: unexpected job status %q", job.Status)
		}

		timer := time.NewTimer(alignmentPollWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return AlignmentBatch{}, fmt.Errorf("speech: align batch: %w", ctx.Err())
		case <-timer.C:
		}
		job, err = c.alignmentBatchJob(ctx, job.ID)
		if err != nil {
			return AlignmentBatch{}, err
		}
	}
}

func (c *Client) batchHTTPClient() *http.Client {
	if c.http.Timeout == 0 || c.http.Timeout >= batchAlignmentTimeout {
		return c.http
	}
	clone := *c.http
	clone.Timeout = batchAlignmentTimeout
	return &clone
}

type alignmentJob struct {
	ID     string    `json:"id"`
	Status string    `json:"status"`
	Error  string    `json:"error"`
	Result Alignment `json:"result"`
}

type alignmentBatchJob struct {
	ID     string         `json:"id"`
	Status string         `json:"status"`
	Error  string         `json:"error"`
	Result AlignmentBatch `json:"result"`
}

func decodeAlignmentJob(resp *http.Response) (alignmentJob, error) {
	defer resp.Body.Close()
	var job alignmentJob
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAlignmentBytes)).Decode(&job); err != nil {
		return alignmentJob{}, fmt.Errorf("speech: decode alignment job: %w", err)
	}
	if strings.TrimSpace(job.ID) == "" {
		return alignmentJob{}, errors.New("speech: alignment job returned no id")
	}
	return job, nil
}

func decodeAlignmentBatchJob(resp *http.Response) (alignmentBatchJob, error) {
	defer resp.Body.Close()
	var job alignmentBatchJob
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBatchAlignmentBytes)).Decode(&job); err != nil {
		return alignmentBatchJob{}, fmt.Errorf("speech: decode batch alignment job: %w", err)
	}
	if strings.TrimSpace(job.ID) == "" {
		return alignmentBatchJob{}, errors.New("speech: batch alignment job returned no id")
	}
	return job, nil
}

func (c *Client) alignmentJob(ctx context.Context, id string) (alignmentJob, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/audio/alignments/"+url.PathEscape(id), nil)
	if err != nil {
		return alignmentJob{}, err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return alignmentJob{}, fmt.Errorf("speech: poll alignment: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return alignmentJob{}, upstreamError("poll alignment", resp)
	}
	return decodeAlignmentJob(resp)
}

func (c *Client) alignmentBatchJob(ctx context.Context, id string) (alignmentBatchJob, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/audio/alignments/"+url.PathEscape(id), nil)
	if err != nil {
		return alignmentBatchJob{}, err
	}
	c.authorize(req)
	resp, err := c.batchHTTPClient().Do(req)
	if err != nil {
		return alignmentBatchJob{}, fmt.Errorf("speech: poll batch alignment: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return alignmentBatchJob{}, upstreamError("poll batch alignment", resp)
	}
	return decodeAlignmentBatchJob(resp)
}

func (c *Client) alignmentResult(ctx context.Context, id string) (Alignment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/audio/alignments/"+url.PathEscape(id)+"/result", nil)
	if err != nil {
		return Alignment{}, err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return Alignment{}, fmt.Errorf("speech: fetch alignment result: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Alignment{}, upstreamError("fetch alignment result", resp)
	}
	var alignment Alignment
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAlignmentBytes)).Decode(&alignment); err != nil {
		return Alignment{}, fmt.Errorf("speech: decode alignment result: %w", err)
	}
	return alignment, nil
}

func (c *Client) alignmentBatchResult(ctx context.Context, id string) (AlignmentBatch, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/audio/alignments/"+url.PathEscape(id)+"/result", nil)
	if err != nil {
		return AlignmentBatch{}, err
	}
	c.authorize(req)
	resp, err := c.batchHTTPClient().Do(req)
	if err != nil {
		return AlignmentBatch{}, fmt.Errorf("speech: fetch batch alignment result: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AlignmentBatch{}, upstreamError("fetch batch alignment result", resp)
	}
	var alignment AlignmentBatch
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBatchAlignmentBytes)).Decode(&alignment); err != nil {
		return AlignmentBatch{}, fmt.Errorf("speech: decode batch alignment result: %w", err)
	}
	return alignment, nil
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
