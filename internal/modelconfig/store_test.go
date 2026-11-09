package modelconfig

import (
	"os"
	"strings"
	"testing"
)

func TestStorePersistsCredentialWithoutPublicExposure(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	route := Route{ID: "personal", Label: "个人模型", BaseURL: "https://example.com/v1", APIKey: "secret-key", Model: "chat", Vision: true, Thinking: []string{"off"}}
	if _, err = store.Put(route); err != nil {
		t.Fatal(err)
	}
	public := Public(route)
	if !public.Configured || strings.Contains(public.BaseURL+public.Model+public.Label, route.APIKey) {
		t.Fatal("public projection exposed or lost credential state")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(route.ID)
	if !ok || got.APIKey != route.APIKey {
		t.Fatal("credential did not survive restart")
	}
	b, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), route.APIKey) {
		t.Fatal("server store did not retain credential")
	}
}

func TestStoreRejectsUnsafeRoute(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(Route{ID: "bad", Label: "bad", BaseURL: "http://example.com/v1", APIKey: "secret", Model: "chat", Thinking: []string{"off"}})
	if err == nil {
		t.Fatal("plaintext remote endpoint accepted")
	}
}
