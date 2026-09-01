package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
	"github.com/dleiferives/tifl/internal/objectstore"
	"github.com/dleiferives/tifl/internal/reader"
	"github.com/dleiferives/tifl/internal/speech"
)

const (
	maxPersistedReaderSpeechBytes    = 32 << 20
	maxPersistedReaderAlignmentBytes = 4 << 20
)

type readerSpeechAsset struct {
	Audio     speech.Audio
	Alignment speech.Alignment
	Aligned   bool
}

type readerSpeechCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	usedBytes  int
	entries    map[string]readerSpeechAsset
	order      []string
}

func newReaderSpeechCache(maxEntries, maxBytes int) *readerSpeechCache {
	return &readerSpeechCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		entries:    make(map[string]readerSpeechAsset),
	}
}

func (c *readerSpeechCache) get(key string) (readerSpeechAsset, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	asset, ok := c.entries[key]
	return asset, ok
}

func (c *readerSpeechCache) put(key string, asset readerSpeechAsset) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, exists := c.entries[key]; exists {
		c.usedBytes -= readerSpeechAssetSize(existing)
		delete(c.entries, key)
		for i, orderedKey := range c.order {
			if orderedKey == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}

	assetBytes := readerSpeechAssetSize(asset)
	if c.maxBytes > 0 && assetBytes > c.maxBytes {
		return
	}
	for len(c.order) > 0 && ((c.maxEntries > 0 && len(c.entries) >= c.maxEntries) ||
		(c.maxBytes > 0 && c.usedBytes+assetBytes > c.maxBytes)) {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.usedBytes -= readerSpeechAssetSize(c.entries[oldest])
		delete(c.entries, oldest)
	}

	c.entries[key] = asset
	c.order = append(c.order, key)
	c.usedBytes += assetBytes
}

func readerSpeechAssetSize(asset readerSpeechAsset) int {
	size := len(asset.Audio.Data) + len(asset.Audio.ContentType)
	for _, word := range asset.Alignment.Words {
		size += len(word.Text) + 16 // Two float64 timing values.
	}
	return size
}

func readerSpeechObjectKey(cacheKey, suffix string) string {
	digest := sha256.Sum256([]byte(cacheKey))
	return "reader_speech/v1/" + hex.EncodeToString(digest[:]) + suffix
}

func (h *Handler) loadPersistedReaderSpeech(ctx context.Context, cacheKey string) (readerSpeechAsset, bool, error) {
	if h.media == nil {
		return readerSpeechAsset{}, false, nil
	}
	reader, info, err := h.media.Get(ctx, readerSpeechObjectKey(cacheKey, ".audio"))
	if errors.Is(err, objectstore.ErrNotFound) {
		return readerSpeechAsset{}, false, nil
	}
	if err != nil {
		return readerSpeechAsset{}, false, fmt.Errorf("load persisted reader audio: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxPersistedReaderSpeechBytes+1))
	if err != nil {
		return readerSpeechAsset{}, false, fmt.Errorf("read persisted reader audio: %w", err)
	}
	if len(data) == 0 || len(data) > maxPersistedReaderSpeechBytes {
		return readerSpeechAsset{}, false, errors.New("persisted reader audio is empty or too large")
	}
	asset := readerSpeechAsset{Audio: speech.Audio{Data: data, ContentType: info.ContentType}}

	alignmentReader, _, err := h.media.Get(ctx, readerSpeechObjectKey(cacheKey, ".alignment.json"))
	if errors.Is(err, objectstore.ErrNotFound) {
		return asset, true, nil
	}
	if err != nil {
		return readerSpeechAsset{}, false, fmt.Errorf("load persisted reader alignment: %w", err)
	}
	defer alignmentReader.Close()
	if err := json.NewDecoder(io.LimitReader(alignmentReader, maxPersistedReaderAlignmentBytes)).Decode(&asset.Alignment); err != nil {
		return readerSpeechAsset{}, false, fmt.Errorf("decode persisted reader alignment: %w", err)
	}
	asset.Aligned = len(asset.Alignment.Words) > 0
	return asset, true, nil
}

func (h *Handler) persistReaderAudio(ctx context.Context, cacheKey string, audio speech.Audio) error {
	if h.media == nil {
		return nil
	}
	_, err := h.media.Put(ctx, readerSpeechObjectKey(cacheKey, ".audio"), bytes.NewReader(audio.Data), audio.ContentType)
	if err != nil {
		return fmt.Errorf("persist reader audio: %w", err)
	}
	return nil
}

func (h *Handler) persistReaderAlignment(ctx context.Context, cacheKey string, alignment speech.Alignment) error {
	if h.media == nil {
		return nil
	}
	data, err := json.Marshal(alignment)
	if err != nil {
		return fmt.Errorf("encode reader alignment: %w", err)
	}
	_, err = h.media.Put(ctx, readerSpeechObjectKey(cacheKey, ".alignment.json"), bytes.NewReader(data), "application/json")
	if err != nil {
		return fmt.Errorf("persist reader alignment: %w", err)
	}
	return nil
}

type readerSentenceSpeechTarget struct {
	cacheKey string
	story    domain.Story
	tokens   []domain.StoryToken
	span     reader.SentenceSpan
	model    string
}

func (h *Handler) readerSentenceSpeechTarget(w http.ResponseWriter, r *http.Request) (readerSentenceSpeechTarget, bool) {
	story, tokens, model, position, ok := h.readerSpeechContext(w, r)
	if !ok {
		return readerSentenceSpeechTarget{}, false
	}
	span, found := reader.SentenceAt(tokens, position)
	if !found {
		writeError(w, http.StatusNotFound, errors.New("sentence not found"))
		return readerSentenceSpeechTarget{}, false
	}
	if len([]rune(span.Text)) > maxReaderSentenceTTSRunes {
		writeError(w, http.StatusBadRequest, errors.New("sentence is too long for speech"))
		return readerSentenceSpeechTarget{}, false
	}
	cacheKey := readerSentenceSpeechCacheKey(h.currentUserID(r), story.StoryID, span, model)
	return readerSentenceSpeechTarget{cacheKey: cacheKey, story: story, tokens: tokens, span: span, model: model}, true
}

func readerSentenceSpeechCacheKey(userID, storyID string, span reader.SentenceSpan, model string) string {
	return fmt.Sprintf("%s\x00%s\x00sentence:%d\x00%s\x00%s", userID, storyID, span.Index, model, span.Text)
}

func (h *Handler) readerSpeechContext(w http.ResponseWriter, r *http.Request) (domain.Story, []domain.StoryToken, string, int, bool) {
	if h.speech == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("reader audio is not configured"))
		return domain.Story{}, nil, "", 0, false
	}
	position, err := strconv.Atoi(r.PathValue("position"))
	if err != nil || position < 0 {
		writeError(w, http.StatusBadRequest, errors.New("position must be a non-negative integer"))
		return domain.Story{}, nil, "", 0, false
	}
	story, err := h.repo.GetStory(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeStoryLookupError(w, err)
		return domain.Story{}, nil, "", 0, false
	}
	if story.UserID != h.currentUserID(r) {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return domain.Story{}, nil, "", 0, false
	}
	tokens, err := h.repo.ListStoryTokens(r.Context(), story.StoryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return domain.Story{}, nil, "", 0, false
	}
	profile, err := h.currentProfile(r)
	if err != nil {
		h.writeProfileError(w, err)
		return domain.Story{}, nil, "", 0, false
	}
	return story, tokens, profile.TTSModel, position, true
}

func (h *Handler) synthesizeReaderSentence(ctx context.Context, target readerSentenceSpeechTarget) (readerSpeechAsset, error) {
	if asset, ok := h.readerSpeech.get(target.cacheKey); ok {
		return asset, nil
	}
	if asset, ok, err := h.loadPersistedReaderSpeech(ctx, target.cacheKey); err != nil {
		return readerSpeechAsset{}, err
	} else if ok {
		h.readerSpeech.put(target.cacheKey, asset)
		return asset, nil
	}
	audio, err := h.speech.Synthesize(ctx, speech.SynthesisInput{
		Text: target.span.Text, Language: target.story.Language, Model: target.model,
	})
	if err != nil {
		return readerSpeechAsset{}, err
	}
	if err := h.persistReaderAudio(ctx, target.cacheKey, audio); err != nil {
		return readerSpeechAsset{}, err
	}
	asset := readerSpeechAsset{Audio: audio}
	h.readerSpeech.put(target.cacheKey, asset)
	return asset, nil
}

func (h *Handler) alignReaderSentence(ctx context.Context, target readerSentenceSpeechTarget) (readerSpeechAsset, error) {
	asset, err := h.synthesizeReaderSentence(ctx, target)
	if err != nil || asset.Aligned {
		return asset, err
	}
	alignment, err := h.speech.Align(ctx, speech.AlignmentInput{
		Audio: asset.Audio, Filename: "sentence.mp3", Transcript: target.span.Text, Language: target.story.Language,
	})
	if err != nil {
		return readerSpeechAsset{}, err
	}
	asset.Alignment = alignment
	asset.Aligned = true
	if err := h.persistReaderAlignment(ctx, target.cacheKey, alignment); err != nil {
		return readerSpeechAsset{}, err
	}
	h.readerSpeech.put(target.cacheKey, asset)
	return asset, nil
}

func (h *Handler) storySentenceAlignment(w http.ResponseWriter, r *http.Request) {
	target, ok := h.readerSentenceSpeechTarget(w, r)
	if !ok {
		return
	}
	asset, err := h.alignReaderSentence(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	writeJSON(w, http.StatusOK, oapigen.ReaderSentenceAlignment{
		SentenceIndex: target.span.Index,
		Words:         readerWordTimings(target.tokens, target.span, asset.Alignment.Words),
	})
}

type readerSpeechBatchEntry struct {
	target readerSentenceSpeechTarget
	asset  readerSpeechAsset
}

func (h *Handler) storySentenceAlignments(w http.ResponseWriter, r *http.Request) {
	if h.speech == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("reader audio is not configured"))
		return
	}
	var request oapigen.ReaderAlignmentBatchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid alignment batch request"))
		return
	}
	if len(request.Positions) == 0 || len(request.Positions) > 1000 {
		writeError(w, http.StatusBadRequest, errors.New("positions must contain between 1 and 1000 items"))
		return
	}

	story, err := h.repo.GetStory(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeStoryLookupError(w, err)
		return
	}
	if story.UserID != h.currentUserID(r) {
		writeError(w, http.StatusNotFound, errors.New("story not found"))
		return
	}
	tokens, err := h.repo.ListStoryTokens(r.Context(), story.StoryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	profile, err := h.currentProfile(r)
	if err != nil {
		h.writeProfileError(w, err)
		return
	}

	entries := make([]readerSpeechBatchEntry, 0, len(request.Positions))
	seenSentences := make(map[int]struct{}, len(request.Positions))
	for _, position := range request.Positions {
		if position < 0 {
			writeError(w, http.StatusBadRequest, errors.New("positions must be non-negative"))
			return
		}
		span, found := reader.SentenceAt(tokens, position)
		if !found {
			writeError(w, http.StatusNotFound, fmt.Errorf("sentence not found at position %d", position))
			return
		}
		if _, exists := seenSentences[span.Index]; exists {
			continue
		}
		seenSentences[span.Index] = struct{}{}
		if len([]rune(span.Text)) > maxReaderSentenceTTSRunes {
			writeError(w, http.StatusBadRequest, fmt.Errorf("sentence %d is too long for speech", span.Index))
			return
		}
		cacheKey := readerSentenceSpeechCacheKey(h.currentUserID(r), story.StoryID, span, profile.TTSModel)
		target := readerSentenceSpeechTarget{
			cacheKey: cacheKey, story: story, tokens: tokens, span: span, model: profile.TTSModel,
		}
		asset, err := h.synthesizeReaderSentence(r.Context(), target)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		entries = append(entries, readerSpeechBatchEntry{target: target, asset: asset})
	}

	unaligned := make([]int, 0, len(entries))
	batchInput := speech.AlignmentBatchInput{Language: story.Language}
	for i, entry := range entries {
		if entry.asset.Aligned {
			continue
		}
		unaligned = append(unaligned, i)
		batchInput.Items = append(batchInput.Items, speech.AlignmentBatchItem{
			ID:    strconv.Itoa(entry.target.span.Index),
			Audio: entry.asset.Audio, Filename: fmt.Sprintf("sentence-%d.mp3", entry.target.span.Index),
			Transcript: entry.target.span.Text,
		})
	}
	if len(batchInput.Items) > 0 {
		batcher, ok := h.speech.(speech.BatchAligner)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, errors.New("batch alignment is not configured"))
			return
		}
		batch, err := batcher.AlignBatch(r.Context(), batchInput)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		results := make(map[string]speech.Alignment, len(batch.Items))
		for _, item := range batch.Items {
			results[item.ID] = item.Alignment
		}
		for _, entryIndex := range unaligned {
			entry := &entries[entryIndex]
			alignment, ok := results[strconv.Itoa(entry.target.span.Index)]
			if !ok || len(alignment.Words) == 0 {
				writeError(w, http.StatusBadGateway, fmt.Errorf("batch alignment omitted sentence %d", entry.target.span.Index))
				return
			}
			if err := h.persistReaderAlignment(r.Context(), entry.target.cacheKey, alignment); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			entry.asset.Alignment = alignment
			entry.asset.Aligned = true
			h.readerSpeech.put(entry.target.cacheKey, entry.asset)
		}
	}

	response := oapigen.ReaderAlignmentBatchResponse{Alignments: make([]oapigen.ReaderSentenceAlignment, 0, len(entries))}
	for _, entry := range entries {
		response.Alignments = append(response.Alignments, oapigen.ReaderSentenceAlignment{
			SentenceIndex: entry.target.span.Index,
			Words:         readerWordTimings(entry.target.tokens, entry.target.span, entry.asset.Alignment.Words),
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func writeReaderAudio(w http.ResponseWriter, audio speech.Audio) {
	w.Header().Set("Content-Type", audio.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(audio.Data)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio.Data)
}

func readerWordTimings(tokens []domain.StoryToken, span reader.SentenceSpan, aligned []speech.WordTiming) []oapigen.ReaderWordTiming {
	storyWords := make([]domain.StoryToken, 0, len(aligned))
	for _, token := range tokens {
		if token.IsWord && token.Position >= span.StartPosition && token.Position < span.EndPosition {
			storyWords = append(storyWords, token)
		}
	}
	result := make([]oapigen.ReaderWordTiming, 0, min(len(storyWords), len(aligned)))
	if len(storyWords) == len(aligned) {
		for i, timing := range aligned {
			result = append(result, timingDTO(storyWords[i], timing))
		}
		return result
	}

	// MFA normally emits exactly one item per transcript word. If a provider
	// normalizes or drops a token, match forward by normalized text so one
	// mismatch does not offset every subsequent highlight.
	next := 0
	for _, timing := range aligned {
		if next >= len(storyWords) {
			break
		}
		match := -1
		want := normalizedAlignmentWord(timing.Text)
		for i := next; i < len(storyWords); i++ {
			if normalizedAlignmentWord(storyWords[i].Surface) == want {
				match = i
				break
			}
		}
		if match < 0 {
			match = next
		}
		result = append(result, timingDTO(storyWords[match], timing))
		next = match + 1
	}
	return result
}

func timingDTO(token domain.StoryToken, timing speech.WordTiming) oapigen.ReaderWordTiming {
	return oapigen.ReaderWordTiming{Position: token.Position, Surface: token.Surface, Start: timing.Start, End: timing.End}
}

func normalizedAlignmentWord(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}
