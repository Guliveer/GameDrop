package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Guliveer/GameDrop/internal/httpclient"
)

// DefaultTelegramBaseURL is the Telegram Bot API root.
const DefaultTelegramBaseURL = "https://api.telegram.org"

// TelegramSender delivers messages with the Telegram Bot API sendMessage call.
//
// Delivery is a single attempt per call: bounded retries live in the outbox
// processor so intent survives a process restart. Telegram offers no idempotency
// key for sendMessage, so a timeout after Telegram accepted the request can
// still produce a duplicate; the outbox records the attempt either way.
type TelegramSender struct {
	client  *httpclient.Client
	token   string
	chatID  string
	baseURL string
	// minInterval spaces consecutive sends to respect Telegram's per-chat rate
	// limit (~1 message per second).
	minInterval time.Duration

	mu       sync.Mutex
	lastSent time.Time
}

// NewTelegram builds a Telegram sender. The token is never logged.
func NewTelegram(client *httpclient.Client, token, chatID string) *TelegramSender {
	return &TelegramSender{
		client:      client,
		token:       strings.TrimSpace(token),
		chatID:      strings.TrimSpace(chatID),
		baseURL:     DefaultTelegramBaseURL,
		minInterval: time.Second,
	}
}

// SetBaseURL overrides the API root (tests).
func (t *TelegramSender) SetBaseURL(url string) { t.baseURL = strings.TrimSuffix(url, "/") }

// Name identifies the channel.
func (t *TelegramSender) Name() string { return "telegram" }

// Send delivers one message. It returns a *DeliveryError describing whether a
// retry is worthwhile.
func (t *TelegramSender) Send(ctx context.Context, msg Message) error {
	if t.token == "" || t.chatID == "" {
		return &DeliveryError{Err: fmt.Errorf("telegram: bot token and chat ID are required")}
	}
	if err := t.throttle(ctx); err != nil {
		return &DeliveryError{Err: err}
	}

	body, err := json.Marshal(map[string]any{
		"chat_id":                  t.chatID,
		"text":                     msg.Text,
		"disable_web_page_preview": true,
		"disable_notification":     false,
	})
	if err != nil {
		return &DeliveryError{Err: fmt.Errorf("telegram: encode request: %w", err)}
	}

	rawURL := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return &DeliveryError{Err: fmt.Errorf("telegram: build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	// The shared client never retries POST requests; the request is sent once.
	resp, err := t.client.Do(ctx, req)
	if err != nil {
		return &DeliveryError{Retryable: true, Err: fmt.Errorf("telegram: send message: %w", err)}
	}
	defer resp.Body.Close()

	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if readErr != nil {
		return &DeliveryError{Retryable: true, Err: fmt.Errorf("telegram: read response: %w", readErr)}
	}

	var result telegramResponse
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &result)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := result.Parameters.RetryAfter
		return &DeliveryError{
			Retryable:  true,
			RetryAfter: time.Duration(retryAfter) * time.Second,
			Err:        fmt.Errorf("telegram: rate limited (retry after %ds)", retryAfter),
		}
	}
	if resp.StatusCode >= 500 {
		return &DeliveryError{
			Retryable: true,
			Err:       fmt.Errorf("telegram: server error %d", resp.StatusCode),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || !result.OK {
		description := strings.TrimSpace(result.Description)
		if description == "" {
			description = strings.TrimSpace(string(payload))
		}
		if len(description) > 200 {
			description = description[:200]
		}
		return &DeliveryError{Err: fmt.Errorf("telegram: status %d: %s", resp.StatusCode, description)}
	}

	t.mu.Lock()
	t.lastSent = time.Now()
	t.mu.Unlock()
	return nil
}

// throttle waits if the previous send was less than minInterval ago.
func (t *TelegramSender) throttle(ctx context.Context) error {
	if t.minInterval <= 0 {
		return nil
	}
	t.mu.Lock()
	wait := time.Until(t.lastSent.Add(t.minInterval))
	t.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}
