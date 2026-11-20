package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type plainBox struct{}

func (plainBox) Seal(s string) (string, error) { return "sealed:" + s, nil }
func (plainBox) Open(s string) (string, error) { return strings.TrimPrefix(s, "sealed:"), nil }

func TestChatFallsBackWithoutResponseFormat(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k-123" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["response_format"]; ok {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"response_format is not supported by this model"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Sure: {\"ok\": true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	st, _ := OpenStore(t.TempDir(), plainBox{})
	p, err := st.Add(Provider{Kind: "compatible", BaseURL: srv.URL + "/v1/", Model: "m"}, "k-123")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Default || p.BaseURL != srv.URL+"/v1" {
		t.Fatalf("provider: %+v", p)
	}
	c, _ := st.Client(p)
	if err := c.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	st2, _ := OpenStore(st.path[:len(st.path)-len("/ai.json")], plainBox{})
	if got, ok := st2.Get(""); !ok || got.ID != p.ID {
		t.Fatal("not persisted")
	}
}

func TestPresetsAndErrors(t *testing.T) {
	st, _ := OpenStore(t.TempDir(), plainBox{})
	p, err := st.Add(Provider{Kind: "gemini"}, "g")
	if err != nil || !strings.Contains(p.BaseURL, "generativelanguage.googleapis.com") || p.Model != "" || p.Usable() {
		t.Fatalf("gemini preset: %+v %v", p, err)
	}
	if p, err = st.SetModel(p.ID, " gemini-x "); err != nil || p.Model != "gemini-x" || !p.Usable() {
		t.Fatalf("set model: %+v %v", p, err)
	}
	if _, err := st.Add(Provider{Kind: "compatible"}, "k"); err == nil {
		t.Fatal("compatible without url must fail")
	}
	if _, err := st.Add(Provider{Kind: "openai"}, ""); err == nil {
		t.Fatal("empty key must fail")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m", Key: "k"}
	if _, _, err := c.Chat(context.Background(), "s", "u", true); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("error not surfaced: %v", err)
	}
}

func TestModelsList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"models/gemini-embedding-1"},{"id":"models/gemini-3.8-flash","name":"Gemini Flash"},{"id":"models/aaa-pro"}]}`))
	}))
	defer srv.Close()
	ms, err := (&Client{BaseURL: srv.URL + "/v1", Key: "k"}).Models(context.Background())
	if err != nil || len(ms) != 3 || ms[0].ID != "aaa-pro" || ms[1].ID != "gemini-3.8-flash" || ms[2].Chat {
		t.Fatalf("models: %+v %v", ms, err)
	}
	if _, err := (&Client{BaseURL: srv.URL + "/v1", Key: "bad"}).Models(context.Background()); err == nil {
		t.Fatal("401 must fail")
	}
}
