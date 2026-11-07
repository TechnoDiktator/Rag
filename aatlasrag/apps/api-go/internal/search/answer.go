package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxExcerptRunes = 2000

type AnswerSource struct {
	ID int `json:"id"`
	Result
	TextTruncated        bool `json:"text_truncated"`
	DescriptionTruncated bool `json:"description_truncated"`
}

type GeneratedAnswer struct {
	Answer               string `json:"answer"`
	InsufficientEvidence bool   `json:"insufficient_evidence"`
}

type AnswerGenerator interface {
	Generate(context.Context, string, []AnswerSource) (GeneratedAnswer, error)
}

type AnswerHandler struct {
	search    *Handler
	generator AnswerGenerator
}

func NewAnswerHandler(search *Handler, generator AnswerGenerator) *AnswerHandler {
	return &AnswerHandler{search: search, generator: generator}
}

func excerpt(text string) (string, bool) {
	runes := []rune(text)
	if len(runes) <= maxExcerptRunes {
		return text, false
	}
	return string(runes[:maxExcerptRunes]), true
}

var citationPattern = regexp.MustCompile(`\[(\d+)\]`)

func validateAnswer(answer GeneratedAnswer, count int) error {
	if strings.TrimSpace(answer.Answer) == "" {
		return fmt.Errorf("empty generated answer")
	}
	citations := citationPattern.FindAllStringSubmatch(answer.Answer, -1)
	if !answer.InsufficientEvidence && len(citations) == 0 {
		return fmt.Errorf("answer is missing citations")
	}
	for _, citation := range citations {
		id, err := strconv.Atoi(citation[1])
		if err != nil || id < 1 || id > count || strconv.Itoa(id) != citation[1] {
			return fmt.Errorf("answer contains an invalid citation")
		}
	}
	return nil
}

func (h *AnswerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.generator == nil {
		writeError(w, http.StatusServiceUnavailable, "answer generation requires OPENAI_API_KEY on the API server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	query, results, ok := h.search.retrieve(w, r.WithContext(ctx))
	if !ok {
		return
	}
	sources := make([]AnswerSource, 0, len(results))
	for i, result := range results {
		var textCut, descCut bool
		result.Text, textCut = excerpt(result.Text)
		if result.Description != nil {
			description, truncated := excerpt(*result.Description)
			result.Description, descCut = &description, truncated
		}
		sources = append(sources, AnswerSource{ID: i + 1, Result: result, TextTruncated: textCut, DescriptionTruncated: descCut})
	}
	answer := GeneratedAnswer{Answer: "No indexed chunks were found for this search.", InsufficientEvidence: true}
	if len(sources) > 0 {
		var err error
		answer, err = h.generator.Generate(ctx, query, sources)
		if err == nil {
			err = validateAnswer(answer, len(sources))
		}
		if err != nil {
			h.search.logger.Printf("answer generation failed: %v", err)
			writeError(w, http.StatusBadGateway, "could not generate a valid answer from the retrieved sources")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(struct {
		GeneratedAnswer
		Sources []AnswerSource `json:"sources"`
	}{answer, sources}); err != nil {
		h.search.logger.Printf("write answer response: %v", err)
	}
}
