package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeGenerator struct {
	answer   GeneratedAnswer
	err      error
	calls    int
	question string
	sources  []AnswerSource
}

func (g *fakeGenerator) Generate(_ context.Context, question string, sources []AnswerSource) (GeneratedAnswer, error) {
	g.calls++
	g.question, g.sources = question, sources
	return g.answer, g.err
}

func TestAnswerHandlerGroundingAndTruncation(t *testing.T) {
	description := strings.Repeat("x", maxExcerptRunes+1)
	reader := &fakeReader{results: []Result{{DocumentID: "doc", Text: strings.Repeat("界", maxExcerptRunes+1), Description: &description, Pages: []int{2}}}}
	generator := &fakeGenerator{answer: GeneratedAnswer{Answer: "The result is 42 [1]."}}
	handler := NewAnswerHandler(NewHandler(reader, &fakeEmbedder{vector: goodVector()}, nil), generator)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"query":" test ","top_k":1}`)))
	if response.Code != 200 || generator.calls != 1 || generator.question != "test" ||
		len(generator.sources) != 1 || generator.sources[0].ID != 1 ||
		!generator.sources[0].TextTruncated || !generator.sources[0].DescriptionTruncated ||
		len([]rune(generator.sources[0].Text)) != maxExcerptRunes {
		t.Fatalf("unexpected answer: %d %s %+v", response.Code, response.Body, generator.sources)
	}
	var result struct {
		Answer  string         `json:"answer"`
		Sources []AnswerSource `json:"sources"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Answer != "The result is 42 [1]." ||
		len(result.Sources) != 1 || result.Sources[0].Pages[0] != 2 {
		t.Fatalf("unexpected response: %s (%v)", response.Body, err)
	}
}

func TestAnswerNoSourcesDoesNotCallGenerator(t *testing.T) {
	generator := &fakeGenerator{}
	reader := &fakeReader{results: []Result{}}
	handler := NewAnswerHandler(NewHandler(reader, &fakeEmbedder{vector: goodVector()}, nil), generator)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"query":"test"}`)))
	if response.Code != 200 || generator.calls != 0 || !strings.Contains(response.Body.String(), `"insufficient_evidence":true`) ||
		!strings.Contains(response.Body.String(), `"sources":[]`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body)
	}
}

func TestAnswerRetrievalFailureDoesNotCallGenerator(t *testing.T) {
	for _, reader := range []*fakeReader{{err: errors.New("database")}} {
		generator := &fakeGenerator{}
		handler := NewAnswerHandler(NewHandler(reader, &fakeEmbedder{vector: goodVector()}, nil), generator)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"query":"test"}`)))
		if response.Code != 500 || generator.calls != 0 {
			t.Fatalf("unexpected response: %d %s", response.Code, response.Body)
		}
	}
}

func TestValidateAnswerMultipleCitations(t *testing.T) {
	if err := validateAnswer(GeneratedAnswer{Answer: "First [1], second [2]."}, 2); err != nil {
		t.Fatal(err)
	}
	if _, cut := excerpt(strings.Repeat("界", maxExcerptRunes)); cut {
		t.Fatal("exactly bounded excerpt was truncated")
	}
}

func TestAnswerErrorCases(t *testing.T) {
	cases := []struct {
		name      string
		generator *fakeGenerator
		body      string
		code      int
	}{
		{"missing key", nil, `{"query":"x"}`, 503},
		{"invalid query", &fakeGenerator{}, `{"query":""}`, 400},
		{"provider failure", &fakeGenerator{err: errors.New("provider")}, `{"query":"x"}`, 502},
		{"missing citation", &fakeGenerator{answer: GeneratedAnswer{Answer: "answer"}}, `{"query":"x"}`, 502},
		{"unknown citation", &fakeGenerator{answer: GeneratedAnswer{Answer: "answer [2]"}}, `{"query":"x"}`, 502},
		{"noncanonical citation", &fakeGenerator{answer: GeneratedAnswer{Answer: "answer [01]"}}, `{"query":"x"}`, 502},
		{"empty answer", &fakeGenerator{answer: GeneratedAnswer{Answer: " "}}, `{"query":"x"}`, 502},
		{"insufficient", &fakeGenerator{answer: GeneratedAnswer{Answer: "Not enough evidence.", InsufficientEvidence: true}}, `{"query":"x"}`, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{results: []Result{{Text: "source"}}}
			var generator AnswerGenerator
			if tc.generator != nil {
				generator = tc.generator
			}
			response := httptest.NewRecorder()
			NewAnswerHandler(NewHandler(reader, &fakeEmbedder{vector: goodVector()}, nil), generator).
				ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body)))
			if response.Code != tc.code {
				t.Fatalf("got %d: %s", response.Code, response.Body)
			}
		})
	}
}

func TestOpenAIAnswerRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat map[string]string `json:"response_format"`
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authorization")
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != answerModel || len(request.Messages) != 2 || request.ResponseFormat["type"] != "json_object" ||
			!strings.Contains(request.Messages[0].Content, "untrusted") ||
			!strings.Contains(request.Messages[1].Content, "source text") ||
			!strings.Contains(request.Messages[1].Content, "question") {
			t.Errorf("unexpected request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "stop", "message": map[string]any{"content": `{"answer":"Evidence [1].","insufficient_evidence":false}`},
		}}})
	}))
	defer server.Close()
	generator := &OpenAIAnswerGenerator{apiKey: "test-key", client: server.Client(), endpoint: server.URL}
	answer, err := generator.Generate(context.Background(), "question", []AnswerSource{{ID: 1, Result: Result{Text: "source text"}}})
	if err != nil || answer.Answer != "Evidence [1]." || answer.InsufficientEvidence {
		t.Fatalf("unexpected response: %+v %v", answer, err)
	}
}

func TestOpenAIAnswerInvalidResponses(t *testing.T) {
	cases := []string{
		`{"choices":[]}`,
		`{"choices":[{"finish_reason":"length","message":{"content":"{}"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"refusal":"no","content":"{}"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":"{\"answer\":\"x\"}"}}]}`,
	}
	for _, payload := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(payload)) }))
		generator := &OpenAIAnswerGenerator{apiKey: "test", client: server.Client(), endpoint: server.URL}
		_, err := generator.Generate(context.Background(), "question", nil)
		server.Close()
		if err == nil {
			t.Errorf("accepted %s", payload)
		}
	}

}

func TestOpenAIAnswerProviderErrorDoesNotExposeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider secret details", http.StatusTooManyRequests)
	}))
	defer server.Close()
	generator := &OpenAIAnswerGenerator{apiKey: "test", client: server.Client(), endpoint: server.URL}
	_, err := generator.Generate(context.Background(), "question", nil)
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "provider secret details") {
		t.Fatalf("unexpected error: %v", err)
	}
}
