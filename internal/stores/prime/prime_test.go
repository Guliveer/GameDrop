package prime

import (
	"context"
	"errors"
	"testing"

	"github.com/Guliveer/GameDrop/internal/model"
	"github.com/Guliveer/GameDrop/internal/stores"
)

func TestFetchReportsDocumentedLimitation(t *testing.T) {
	adapter := New()
	if adapter.Store() != model.StorePrime {
		t.Fatalf("store = %q", adapter.Store())
	}

	offers, err := adapter.Fetch(context.Background(), stores.Query{Country: "PL", Locale: "pl-PL"})
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}
	if len(offers) != 0 {
		t.Fatalf("expected no offers, got %d", len(offers))
	}
}
