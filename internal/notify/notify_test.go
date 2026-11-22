package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Guliveer/GameDrop/internal/httpclient"
	"github.com/Guliveer/GameDrop/internal/model"
)

func testSender(baseURL string) *TelegramSender {
	client := httpclient.New(httpclient.Options{
		Timeout:    2 * time.Second,
		MaxRetries: 2,
		BaseDelay:  time.Millisecond,
		UserAgent:  "GameDropTest/1.0",
	})
	sender := NewTelegram(client, "test-token", "42")
	sender.SetBaseURL(baseURL)
	sender.minInterval = 0
	return sender
}

func TestTelegramSendSuccess(t *testing.T) {
	var (
		gotPath string
		gotBody string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	sender := testSender(srv.URL)
	if err := sender.Send(context.Background(), Message{Text: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"chat_id":"42"`) || !strings.Contains(gotBody, `"text":"hello"`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestTelegramRateLimitIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":17}}`)
	}))
	defer srv.Close()

	err := testSender(srv.URL).Send(context.Background(), Message{Text: "hello"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsRetryable(err) {
		t.Error("429 must be retryable")
	}
	if delay, ok := RetryAfter(err); !ok || delay != 17*time.Second {
		t.Errorf("RetryAfter = %s, %v; want 17s", delay, ok)
	}
}

func TestTelegramClientErrorIsNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"chat not found"}`)
	}))
	defer srv.Close()

	err := testSender(srv.URL).Send(context.Background(), Message{Text: "hello"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsRetryable(err) {
		t.Error("400 must not be retryable")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error = %v", err)
	}
	if strings.Contains(err.Error(), "test-token") {
		t.Error("error must not leak the bot token")
	}
}

func TestTelegramServerErrorIsRetryable(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	err := testSender(srv.URL).Send(context.Background(), Message{Text: "hello"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsRetryable(err) {
		t.Error("5xx must be retryable")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("requests = %d, want 1: delivery retries belong to the outbox", got)
	}
}

func TestTelegramRequiresCredentials(t *testing.T) {
	sender := NewTelegram(nil, "", "")
	err := sender.Send(context.Background(), Message{Text: "x"})
	if err == nil || IsRetryable(err) {
		t.Fatalf("expected a non-retryable configuration error, got %v", err)
	}
}

func renderFixture() model.Offer {
	return model.Offer{
		Store:         model.StoreEpic,
		OfferID:       "abc",
		Country:       "PL",
		Title:         "Some Game",
		URL:           "https://store.epicgames.com/pl/p/some-game",
		Type:          model.TypeFreeToKeep,
		EndsAt:        time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC),
		OriginalPrice: &model.Money{AmountMinor: 9999, Currency: "PLN"},
	}
}

func TestRenderOffer(t *testing.T) {
	text := RenderOffer(renderFixture(), KindNew)
	for _, want := range []string{
		"Free game",
		"Epic Games Store",
		"Some Game",
		"Free to keep",
		"PL",
		"was 99.99 PLN",
		"Claim by: Fri, 27 Nov 2026 16:00 UTC",
		"https://store.epicgames.com/pl/p/some-game",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("message missing %q:\n%s", want, text)
		}
	}
}

func TestRenderOfferWithoutDeadlineOrPrice(t *testing.T) {
	offer := renderFixture()
	offer.EndsAt = time.Time{}
	offer.OriginalPrice = nil
	offer.Country = ""
	text := RenderOffer(offer, KindReappeared)
	if !strings.Contains(text, "Back again") {
		t.Errorf("message missing reappeared header:\n%s", text)
	}
	if !strings.Contains(text, "Claim by: unknown") {
		t.Errorf("message must state that the deadline is unknown:\n%s", text)
	}
	if strings.Contains(text, "Store region") {
		t.Errorf("message must omit an empty region:\n%s", text)
	}
}

func TestDeliveryErrorHelpers(t *testing.T) {
	plain := errors.New("boom")
	if IsRetryable(plain) {
		t.Error("plain errors are not retryable")
	}
	if _, ok := RetryAfter(plain); ok {
		t.Error("plain errors carry no retry delay")
	}
}
