package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// telegramAPI is the minimal part of the Bot API the wizard needs. It never logs
// the token and never echoes it in errors.
type telegramAPI struct {
	client  *http.Client
	baseURL string
	token   string
}

// botUsername verifies the token and returns the bot's @username.
func (t telegramAPI) botUsername(ctx context.Context) (string, error) {
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"result"`
	}
	if err := t.call(ctx, "getMe", nil, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("%s", strings.TrimSpace(resp.Description))
	}
	return resp.Result.Username, nil
}

// findChatID returns the chat of the most recent message sent to the bot. It is
// empty when nobody has written to the bot yet, which is the common first-run
// case: the user has to press Start once.
func (t telegramAPI) findChatID(ctx context.Context) (id, name string, err error) {
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      []struct {
			Message *struct {
				Chat struct {
					ID        int64  `json:"id"`
					Username  string `json:"username"`
					FirstName string `json:"first_name"`
					Title     string `json:"title"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := t.call(ctx, "getUpdates", nil, &resp); err != nil {
		return "", "", err
	}
	if !resp.OK {
		return "", "", fmt.Errorf("%s", strings.TrimSpace(resp.Description))
	}
	for i := len(resp.Result) - 1; i >= 0; i-- {
		message := resp.Result[i].Message
		if message == nil || message.Chat.ID == 0 {
			continue
		}
		label := message.Chat.FirstName
		if label == "" {
			label = message.Chat.Title
		}
		if message.Chat.Username != "" {
			label = "@" + message.Chat.Username
		}
		return fmt.Sprintf("%d", message.Chat.ID), strings.TrimSpace(label), nil
	}
	return "", "", nil
}

// sendTest delivers one short message so the user sees the channel working.
func (t telegramAPI) sendTest(ctx context.Context, chatID string) error {
	body := map[string]any{
		"chat_id":                  chatID,
		"text":                     "GameDrop is connected. Free-game alerts will arrive here.",
		"disable_web_page_preview": true,
	}
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := t.call(ctx, "sendMessage", body, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", strings.TrimSpace(resp.Description))
	}
	return nil
}

// call performs one Bot API request and decodes the JSON answer into out.
func (t telegramAPI) call(ctx context.Context, method string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	url := fmt.Sprintf("%s/bot%s/%s", strings.TrimSuffix(t.baseURL, "/"), t.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach Telegram: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if out != nil {
		// A failed call still carries a description worth showing.
		_ = json.Unmarshal(data, out)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s", describeTelegramError(resp.StatusCode, data))
	}
	return nil
}

// describeTelegramError turns an API error into an instruction a non-technical
// user can act on.
func describeTelegramError(status int, payload []byte) string {
	var api struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(payload, &api)
	description := strings.TrimSpace(api.Description)

	switch {
	case status == http.StatusUnauthorized || status == http.StatusNotFound:
		return "Telegram rejected the bot token. Copy the whole token from @BotFather and try again."
	case strings.Contains(description, "chat not found"):
		return "Telegram does not know that chat yet. Open your bot in Telegram, press Start, then try again."
	case strings.Contains(description, "bot was blocked"):
		return "You blocked this bot. Unblock it in Telegram and press Start."
	case strings.Contains(description, "chat_id is empty"):
		return "That chat ID is empty. Open your bot in Telegram, press Start, then try again."
	case description != "":
		return description
	default:
		return fmt.Sprintf("Telegram returned status %d", status)
	}
}
