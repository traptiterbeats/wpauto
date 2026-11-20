// Package ai talks to chat-completion APIs. OpenAI, Gemini (through Google's OpenAI-compatible
// endpoint), OpenRouter and any other OpenAI-compatible service share one client; providers are
// kept in <config>/ai.json with their API keys sealed by secretbox.
package ai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind describes a provider preset.
type Kind struct {
	ID, Name, BaseURL, Model string
}

// Kinds are the presets offered in the panel. No model is preset: models come and go, so the panel
// loads the provider's own model list after the key is saved and the user picks one. "compatible" is any OpenAI-compatible service.
var Kinds = []Kind{
	{"openai", "OpenAI", "https://api.openai.com/v1", ""},
	{"gemini", "Google Gemini", "https://generativelanguage.googleapis.com/v1beta/openai", ""},
	{"openrouter", "OpenRouter", "https://openrouter.ai/api/v1", ""},
	{"compatible", "سرویس سازگار با OpenAI", "", ""},
}

// KindByID returns a preset.
func KindByID(id string) (Kind, bool) {
	for _, k := range Kinds {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// Provider is one configured API.
type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	SealedKey string `json:"key"`
	Default   bool   `json:"default,omitempty"`

	// Result of the last connection test (or real call).
	TestedAt time.Time `json:"tested_at,omitempty"`
	TestOK   bool      `json:"test_ok,omitempty"`
	TestErr  string    `json:"test_err,omitempty"`
}

// Tested reports whether the provider was ever tested.
func (p Provider) Tested() bool { return !p.TestedAt.IsZero() }

// Usable reports whether the provider may be used: a model is chosen, and it was never tested or
// the last test passed.
func (p Provider) Usable() bool { return p.Model != "" && (!p.Tested() || p.TestOK) }

// Sealer encrypts API keys (secretbox.Box).
type Sealer interface {
	Seal(string) (string, error)
	Open(string) (string, error)
}

// Store keeps providers.
type Store struct {
	mu   sync.Mutex
	path string
	box  Sealer
	list []Provider
}

// OpenStore loads <dir>/ai.json.
func OpenStore(dir string, box Sealer) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "ai.json"), box: box}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(b, &s.list); err != nil {
			return nil, fmt.Errorf("ai.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *Store) save() error {
	b, _ := json.MarshalIndent(s.list, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns providers (keys stay sealed).
func (s *Store) List() []Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Provider(nil), s.list...)
}

// Get returns one provider; id "" returns the default (or the first).
func (s *Store) Get(id string) (Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.list {
		if p.ID == id || (id == "" && p.Default) {
			return p, true
		}
	}
	if id == "" && len(s.list) > 0 {
		return s.list[0], true
	}
	return Provider{}, false
}

// Add stores a provider with its plain API key.
func (s *Store) Add(p Provider, key string) (Provider, error) {
	k, ok := KindByID(p.Kind)
	if !ok {
		return p, errors.New("نوع سرویس نامعتبر است")
	}
	if p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"); p.BaseURL == "" {
		p.BaseURL = k.BaseURL
	}
	if p.Model = strings.TrimSpace(p.Model); p.Model == "" {
		p.Model = k.Model
	}
	if p.Name = strings.TrimSpace(p.Name); p.Name == "" {
		p.Name = k.Name
	}
	if !strings.HasPrefix(p.BaseURL, "https://") && !strings.HasPrefix(p.BaseURL, "http://") {
		return p, errors.New("آدرس API باید با https:// شروع شود")
	}
	if strings.TrimSpace(key) == "" {
		return p, errors.New("کلید API را وارد کنید")
	}
	sealed, err := s.box.Seal(strings.TrimSpace(key))
	if err != nil {
		return p, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	p.ID, p.SealedKey = hex.EncodeToString(b), sealed
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) == 0 {
		p.Default = true
	}
	s.list = append(s.list, p)
	return p, s.save()
}

// Delete removes a provider.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list[:0]
	wasDefault := false
	for _, p := range s.list {
		if p.ID == id {
			wasDefault = p.Default
			continue
		}
		out = append(out, p)
	}
	s.list = out
	if wasDefault && len(s.list) > 0 {
		s.list[0].Default = true
	}
	return s.save()
}

// SetTest records the result of a connection test or a real call.
func (s *Store) SetTest(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].ID == id {
			s.list[i].TestedAt, s.list[i].TestOK, s.list[i].TestErr = time.Now(), err == nil, ""
			if err != nil {
				s.list[i].TestErr = err.Error()
			}
		}
	}
	_ = s.save()
}

// SetModel chooses the provider's model and clears the old test result.
func (s *Store) SetModel(id, model string) (Provider, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return Provider{}, errors.New("یک مدل انتخاب کنید")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].ID == id {
			s.list[i].Model, s.list[i].TestedAt, s.list[i].TestOK, s.list[i].TestErr = model, time.Time{}, false, ""
			return s.list[i], s.save()
		}
	}
	return Provider{}, errors.New("سرویس پیدا نشد")
}

// SetDefault marks one provider as the default.
func (s *Store) SetDefault(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		s.list[i].Default = s.list[i].ID == id
	}
	return s.save()
}

// Client returns a ready client for a provider.
func (s *Store) Client(p Provider) (*Client, error) {
	key, err := s.box.Open(p.SealedKey)
	if err != nil {
		return nil, errors.New("کلید API قابل خواندن نیست؛ سرویس را دوباره اضافه کنید")
	}
	return &Client{BaseURL: p.BaseURL, Model: p.Model, Key: key, Kind: p.Kind}, nil
}

// Client calls /chat/completions.
type Client struct {
	BaseURL, Model, Key, Kind string
	HTTP                      *http.Client
}

// Usage is the token count the API reported.
type Usage struct {
	Prompt, Completion int
}

// APIError is a non-2xx answer.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API خطا داد (HTTP %d): %s", e.Status, e.Msg)
}

// Chat sends a system and a user message and returns the reply text. With jsonMode it asks for a
// JSON object, and retries once without response_format when the API refuses that option.
func (c *Client) Chat(ctx context.Context, system, user string, jsonMode bool) (string, Usage, error) {
	body := map[string]any{
		"model":       c.Model,
		"temperature": 0.7,
		"messages":    []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	out, u, err := c.post(ctx, body)
	var ae *APIError
	if jsonMode && errors.As(err, &ae) && ae.Status == 400 && strings.Contains(strings.ToLower(ae.Msg), "response_format") {
		delete(body, "response_format")
		out, u, err = c.post(ctx, body)
	}
	return out, u, err
}

func (c *Client) post(ctx context.Context, body map[string]any) (string, Usage, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if c.Kind == "openrouter" {
		req.Header.Set("X-Title", "wpauto")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 4 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", Usage{}, fmt.Errorf("اتصال به API نشد: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if r.Error != nil && r.Error.Message != "" {
			msg = r.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", Usage{}, &APIError{Status: resp.StatusCode, Msg: msg}
	}
	if r.Error != nil {
		return "", Usage{}, &APIError{Status: resp.StatusCode, Msg: r.Error.Message}
	}
	if len(r.Choices) == 0 {
		return "", Usage{}, errors.New("API پاسخی برنگرداند")
	}
	u := Usage{r.Usage.Prompt, r.Usage.Completion}
	if r.Choices[0].FinishReason == "length" {
		return r.Choices[0].Message.Content, u, errors.New("پاسخ مدل نیمه‌کاره ماند (محدودیت طول)")
	}
	return r.Choices[0].Message.Content, u, nil
}

// JSONObject extracts the first JSON object from a reply (models sometimes wrap it in ```json).
func JSONObject(s string) (map[string]any, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("پاسخ مدل JSON معتبر نبود: %w", err)
	}
	return m, nil
}

// Model is one entry of the provider's model list.
type Model struct {
	ID, Name string
	Chat     bool // false for embedding, image, audio and similar models
}

var nonChat = []string{"embed", "tts", "whisper", "dall-e", "imagen", "veo", "moderation", "transcribe", "audio", "-image", "image-", "aqa", "rerank", "realtime", "search-preview", "computer-use", "lyria", "native-audio"}

// Models reads GET <base>/models. Chat models come first; Gemini's "models/" prefix is removed.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("اتصال به API نشد: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var r struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Models []struct { // Gemini native shape, just in case
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if r.Error != nil && r.Error.Message != "" {
			msg = r.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, &APIError{Status: resp.StatusCode, Msg: msg}
	}
	var out []Model
	seen := map[string]bool{}
	add := func(id, name string) {
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		m := Model{ID: id, Name: name, Chat: true}
		low := strings.ToLower(id)
		for _, w := range nonChat {
			if strings.Contains(low, w) {
				m.Chat = false
			}
		}
		out = append(out, m)
	}
	for _, d := range r.Data {
		add(d.ID, d.Name)
	}
	for _, d := range r.Models {
		add(d.Name, d.DisplayName)
	}
	if len(out) == 0 {
		return nil, errors.New("سرویس هیچ مدلی برنگرداند؛ نام مدل را دستی بنویسید")
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Chat != out[j].Chat {
			return out[i].Chat
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Test checks a provider with a tiny request.
func (c *Client) Test(ctx context.Context) error {
	if c.Model == "" {
		return errors.New("هنوز مدلی انتخاب نشده")
	}
	out, _, err := c.Chat(ctx, "Reply with a JSON object only.", `Return {"ok": true}`, true)
	if err != nil {
		return err
	}
	m, err := JSONObject(out)
	if err != nil {
		return err
	}
	if m["ok"] != true {
		return fmt.Errorf("پاسخ غیرمنتظره: %.100s", out)
	}
	return nil
}
