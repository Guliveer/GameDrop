package stores

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Guliveer/GameDrop/internal/model"
)

type stubAdapter struct{ store model.Store }

func (s stubAdapter) Store() model.Store { return s.store }

func (s stubAdapter) Fetch(ctx context.Context, q Query) ([]model.Offer, error) {
	return nil, nil
}

func TestRegistrySkipsNilAndKeepsOrder(t *testing.T) {
	registry := NewRegistry(stubAdapter{model.StoreSteam}, nil, stubAdapter{model.StoreEpic})
	if registry.Len() != 2 {
		t.Fatalf("Len = %d, want 2", registry.Len())
	}
	all := registry.All()
	if len(all) != 2 || all[0].Store() != model.StoreSteam || all[1].Store() != model.StoreEpic {
		t.Fatalf("All = %v", all)
	}

	// All returns a copy: mutating it must not change the registry.
	all[0] = stubAdapter{model.StoreGOG}
	if registry.All()[0].Store() != model.StoreSteam {
		t.Error("All must return a defensive copy")
	}
}

func TestLocaleLanguage(t *testing.T) {
	cases := map[string]string{
		"pl-PL":    "pl",
		"en_US":    "en",
		"de":       "de",
		"":         "",
		"  FR-fr ": "fr",
	}
	for in, want := range cases {
		if got := LocaleLanguage(in); got != want {
			t.Errorf("LocaleLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWrapError(t *testing.T) {
	if WrapError(model.StoreSteam, "fetch", nil) != nil {
		t.Error("nil error must stay nil")
	}
	cause := errors.New("boom")
	wrapped := WrapError(model.StoreSteam, "fetch", cause)
	if !errors.Is(wrapped, cause) {
		t.Error("wrapped error must preserve the cause")
	}
	if !strings.Contains(wrapped.Error(), "steam: fetch") {
		t.Errorf("wrapped error = %v", wrapped)
	}
}
