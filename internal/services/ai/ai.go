// Package ai ports the plain-REST Gemini/Grok clients from routes/ai.ts
// (apps/server/src/routes/ai.ts:14-20,293-343,348-383,432-521) - no SDK for
// either provider, just net/http + encoding/json. Both providers do two
// things: turn a free-text playlist prompt into a handful of Plex search
// queries, and turn the same prompt into a short playlist name. Per-user API
// keys are stored in the users_settings table (gemini_api_key/grok_api_key),
// not env vars - verified there is no server-wide GEMINI_API_KEY/GROK_API_KEY
// in the current .env, so internal/config needs no changes for this.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	geminiModel = "gemini-2.0-flash-exp"
	grokModel   = "grok-beta"
)

// geminiAPIBase/grokAPIBase are vars, not consts, purely so tests can point
// them at a fake httptest server instead of the real APIs - same pattern
// and reasoning as auth.SetPlexAPIBaseForTest. Never reassigned outside
// tests.
var (
	geminiAPIBase = "https://generativelanguage.googleapis.com/v1"
	grokAPIBase   = "https://api.x.ai/v1"
)

// SetAPIBasesForTest points both providers at fake servers for the rest of
// the calling test, returning a restore func to defer.
func SetAPIBasesForTest(gemini, grok string) (restore func()) {
	prevGemini, prevGrok := geminiAPIBase, grokAPIBase
	geminiAPIBase, grokAPIBase = gemini, grok
	return func() { geminiAPIBase, grokAPIBase = prevGemini, prevGrok }
}

// AuthError mirrors the TS code's "re-throw auth errors instead of falling
// back to keyword extraction" behavior for 400/401/403 responses.
type AuthError struct {
	Status int
	Body   string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("ai provider auth error: status %d: %s", e.Status, e.Body)
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// ---- Gemini ----

type geminiRequest struct {
	Contents         []geminiContent `json:"contents"`
	GenerationConfig *geminiGenCfg   `json:"generationConfig,omitempty"`
}
type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}
type geminiPart struct {
	Text string `json:"text"`
}
type geminiGenCfg struct {
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
}
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func geminiGenerate(ctx context.Context, apiKey, prompt string, temperature float64, maxTokens int) (string, error) {
	body, _ := json.Marshal(geminiRequest{
		Contents:         []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
		GenerationConfig: &geminiGenCfg{Temperature: temperature, MaxOutputTokens: maxTokens},
	})
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", geminiAPIBase, geminiModel, apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", &AuthError{Status: resp.StatusCode, Body: string(respBody)}
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("gemini api error: status %d: %s", resp.StatusCode, respBody)
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("gemini api: decode response: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", nil
	}
	return parsed.Candidates[0].Content.Parts[0].Text, nil
}

// GetSearchQueriesFromGemini ports getSearchQueriesFromGemini (ai.ts:293-343).
// Auth errors are returned as *AuthError so callers can re-throw them instead
// of falling back, matching the TS behavior.
func GetSearchQueriesFromGemini(ctx context.Context, prompt, apiKey string) ([]string, error) {
	text, err := geminiGenerate(ctx, apiKey, searchQueryPrompt(prompt), 0.7, 200)
	if err != nil {
		var authErr *AuthError
		if isAuthError(err, &authErr) {
			return nil, err
		}
		return extractKeywords(prompt), nil
	}
	if queries := parseStringArray(text); len(queries) > 0 {
		return queries, nil
	}
	return extractKeywords(prompt), nil
}

// GeneratePlaylistNameWithGemini ports generatePlaylistNameWithGemini (ai.ts:348-383).
func GeneratePlaylistNameWithGemini(ctx context.Context, prompt, apiKey string) (string, error) {
	text, err := geminiGenerate(ctx, apiKey, namePrompt(prompt), 0.8, 50)
	if err != nil {
		// TS: logs a warning and falls back, never propagates.
		return generatePlaylistName(prompt), nil
	}
	if name := cleanName(text); name != "" {
		return name, nil
	}
	return generatePlaylistName(prompt), nil
}

// TestGemini ports the Gemini branch of POST /api/import/ai/test (ai.ts:233-252).
func TestGemini(ctx context.Context, apiKey string) (string, error) {
	return geminiGenerate(ctx, apiKey, `Say "OK" if you can read this.`, 0, 0)
}

// ---- Grok ----

type grokRequest struct {
	Model       string        `json:"model"`
	Messages    []grokMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}
type grokMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type grokResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func grokChat(ctx context.Context, apiKey, content string, temperature float64, maxTokens int) (string, error) {
	body, _ := json.Marshal(grokRequest{
		Model:       grokModel,
		Messages:    []grokMessage{{Role: "user", Content: content}},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, grokAPIBase+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", &AuthError{Status: resp.StatusCode, Body: string(respBody)}
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("grok api error: status %d: %s", resp.StatusCode, respBody)
	}

	var parsed grokResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("grok api: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", nil
	}
	return parsed.Choices[0].Message.Content, nil
}

// GetSearchQueriesFromGrok ports getSearchQueriesFromGrok (ai.ts:432-481).
func GetSearchQueriesFromGrok(ctx context.Context, prompt, apiKey string) ([]string, error) {
	text, err := grokChat(ctx, apiKey, searchQueryPrompt(prompt), 0.7, 200)
	if err != nil {
		var authErr *AuthError
		if isAuthError(err, &authErr) {
			return nil, err
		}
		return extractKeywords(prompt), nil
	}
	if queries := parseStringArray(text); len(queries) > 0 {
		return queries, nil
	}
	return extractKeywords(prompt), nil
}

// GeneratePlaylistNameWithGrok ports generatePlaylistNameWithGrok (ai.ts:486-520).
func GeneratePlaylistNameWithGrok(ctx context.Context, prompt, apiKey string) (string, error) {
	text, err := grokChat(ctx, apiKey, namePrompt(prompt), 0.8, 50)
	if err != nil {
		return generatePlaylistName(prompt), nil
	}
	if name := cleanName(text); name != "" {
		return name, nil
	}
	return generatePlaylistName(prompt), nil
}

// TestGrok ports the Grok branch of POST /api/import/ai/test (ai.ts:212-232).
func TestGrok(ctx context.Context, apiKey string) (string, error) {
	return grokChat(ctx, apiKey, `Say "OK" if you can read this.`, 0, 10)
}

// ---- shared helpers ----

func isAuthError(err error, target **AuthError) bool {
	if ae, ok := err.(*AuthError); ok {
		*target = ae
		return true
	}
	return false
}

func searchQueryPrompt(prompt string) string {
	return "You are a music expert. Given a user's description of a playlist they want to create, " +
		"extract 5-10 specific search queries (artist names, song titles, genres, moods) that would " +
		`help find matching tracks in a music library. Return ONLY a JSON array of strings, nothing else. ` +
		`Example: ["rock", "The Beatles", "upbeat", "80s pop", "dance music"]` + "\n\nUser request: " + prompt
}

func namePrompt(prompt string) string {
	return "You are a creative playlist naming expert. Given a user's description of a playlist, " +
		"create a short, catchy playlist name (2-6 words). Return ONLY the playlist name, nothing else. " +
		"No quotes, no explanation.\n\nUser request: " + prompt
}

var quoteTrim = regexp.MustCompile(`^["']|["']$`)

func cleanName(text string) string {
	name := strings.TrimSpace(text)
	if len(name) == 0 || len(name) >= 100 {
		return ""
	}
	return quoteTrim.ReplaceAllString(name, "")
}

// parseStringArray mirrors the TS "JSON.parse then filter to non-empty
// strings" step; any parse failure or non-array result yields nil so the
// caller falls back to keyword extraction, same as the TS catch block.
func parseStringArray(text string) []string {
	var raw []interface{}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true, "in": true, "on": true,
	"at": true, "to": true, "for": true, "of": true, "with": true, "by": true, "from": true, "up": true,
	"about": true, "into": true, "through": true, "during": true, "create": true, "make": true,
	"playlist": true, "songs": true, "tracks": true, "music": true, "i": true, "want": true, "like": true,
	"that": true, "this": true, "some": true, "any": true, "all": true, "would": true, "could": true,
	"should": true,
}

var nonWord = regexp.MustCompile(`[^\w\s]`)

// extractKeywords ports the fallback keyword extractor (ai.ts:388-406).
func extractKeywords(prompt string) []string {
	cleaned := nonWord.ReplaceAllString(strings.ToLower(prompt), " ")
	seen := map[string]bool{}
	var out []string
	for _, word := range strings.Fields(cleaned) {
		if len(word) <= 2 || stopWords[word] || seen[word] {
			continue
		}
		seen[word] = true
		out = append(out, word)
	}
	return out
}

var leadingPhrase = regexp.MustCompile(`(?i)^(create|make|generate|build)\s+(a|an)?\s*(playlist\s+(of|with|for)?)?`)

// generatePlaylistName ports the fallback name generator (ai.ts:411-424).
func generatePlaylistName(prompt string) string {
	cleaned := strings.TrimSpace(leadingPhrase.ReplaceAllString(prompt, ""))
	if len(cleaned) > 0 && len(cleaned) < 100 {
		r := []rune(cleaned)
		r[0] = unicode.ToUpper(r[0])
		return string(r)
	}
	return "AI Generated Playlist"
}
