package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/handler/oapigen"
	"github.com/dleiferives/tifl/internal/reader"
	"github.com/dleiferives/tifl/internal/speech"
)

const maxReaderWordTTSRunes = 256

type readerSpeechAsset struct {
	Audio     speech.Audio
	Alignment speech.Alignment
	Aligned   bool
}

type readerSpeechCache struct {
	mu      sync.Mutex
	limit   int
	entries map[string]readerSpeechAsset
	order   []string
}

func newReaderSpeechCache(limit int) *readerSpeechCache {
	return &readerSpeechCache{limit: limit, entries: make(map[string]readerSpeechAsset)}
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
	if _, exists := c.entries[key]; !exists {
		if c.limit > 0 && len(c.entries) >= c.limit {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.order = append(c.order, key)
	}
	c.entries[key] = asset
}

type readerSentenceSpeechTarget struct {
	cacheKey string
	story    domain.Story
	tokens   []domain.StoryToken
	span     reader.SentenceSpan
	model    string
}

type readerWordSpeechTarget struct {
	cacheKey string
	story    domain.Story
	token    domain.StoryToken
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
	cacheKey := fmt.Sprintf("%s\x00%s\x00sentence:%d\x00%s", h.currentUserID(r), story.StoryID, span.Index, model)
	return readerSentenceSpeechTarget{cacheKey: cacheKey, story: story, tokens: tokens, span: span, model: model}, true
}

func (h *Handler) readerWordSpeechTarget(w http.ResponseWriter, r *http.Request) (readerWordSpeechTarget, bool) {
	story, tokens, model, position, ok := h.readerSpeechContext(w, r)
	if !ok {
		return readerWordSpeechTarget{}, false
	}
	for _, token := range tokens {
		if token.Position != position || !token.IsWord {
			continue
		}
		if len([]rune(token.Surface)) > maxReaderWordTTSRunes {
			writeError(w, http.StatusBadRequest, errors.New("word is too long for speech"))
			return readerWordSpeechTarget{}, false
		}
		cacheKey := fmt.Sprintf("%s\x00%s\x00word:%d\x00%s", h.currentUserID(r), story.StoryID, token.Position, model)
		return readerWordSpeechTarget{cacheKey: cacheKey, story: story, token: token, model: model}, true
	}
	writeError(w, http.StatusNotFound, errors.New("word not found"))
	return readerWordSpeechTarget{}, false
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
	audio, err := h.speech.Synthesize(ctx, speech.SynthesisInput{
		Text: target.span.Text, Language: target.story.Language, Model: target.model,
	})
	if err != nil {
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
	writeJSON(w, http.StatusOK, oapigen.ReaderSentenceAlignment{
		SentenceIndex: target.span.Index,
		Words:         readerWordTimings(target.tokens, target.span, asset.Alignment.Words),
	})
}

func (h *Handler) storyWordAudio(w http.ResponseWriter, r *http.Request) {
	target, ok := h.readerWordSpeechTarget(w, r)
	if !ok {
		return
	}
	if asset, found := h.readerSpeech.get(target.cacheKey); found {
		writeReaderAudio(w, asset.Audio)
		return
	}
	audio, err := h.speech.Synthesize(r.Context(), speech.SynthesisInput{
		Text: target.token.Surface, Language: target.story.Language, Model: target.model,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	h.readerSpeech.put(target.cacheKey, readerSpeechAsset{Audio: audio})
	writeReaderAudio(w, audio)
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
