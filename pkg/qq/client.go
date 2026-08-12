// Package qq implements the Tencent QQ bot C2C message API: app access-token
// acquisition with caching and expiry-aware refresh, plus markdown message
// delivery. It is a self-contained HTTP client with no dependency on the
// platform internals, mirroring pkg/weather and pkg/baidu.
package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL      = "https://api.bot.qq.com"
	maxResponseBytes    = 1 << 20
	tokenRefreshAdvance = time.Minute
)

// Config configures the QQ client.
type Config struct {
	AppID      string
	AppSecret  string
	UserOpenID string
	BaseURL    string
	HTTPClient *http.Client
}

// Message is the server response for a delivered C2C message.
type Message struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

// Client talks to the QQ bot API. It is safe for concurrent use: access-token
// acquisition is serialized and cached, and an expired token is refreshed once
// before retrying a rejected request.
type Client struct {
	mu          sync.Mutex
	appID       string
	appSecret   string
	userOpenID  string
	baseURL     string
	httpClient  *http.Client
	accessToken string
	tokenExpiry time.Time
}

type apiError struct {
	StatusCode int
	Code       int64
	Message    string
	TraceID    string
}

func (e *apiError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
	}
	if e.Code == 11255 {
		detail += "; verify HEPHAESTUS_QQ_USER_OPENID is this bot's C2C user_openid from FRIEND_ADD.openid or C2C_MESSAGE_CREATE.author.id, and that the user is a bot friend"
	}
	if e.TraceID != "" {
		return fmt.Sprintf("QQ API error %d: %s (trace_id: %s)", e.Code, detail, e.TraceID)
	}
	return fmt.Sprintf("QQ API error %d: %s", e.Code, detail)
}

// New builds a Client. An empty BaseURL falls back to the default endpoint.
func New(config Config) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		appID:      strings.TrimSpace(config.AppID),
		appSecret:  strings.TrimSpace(config.AppSecret),
		userOpenID: strings.TrimSpace(config.UserOpenID),
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// Configured reports whether the credentials needed to send are present.
func (c *Client) Configured() bool {
	return c != nil && c.appID != "" && c.appSecret != "" && c.userOpenID != ""
}

// SendMarkdown delivers a markdown C2C message and returns the server
// acknowledgment. On an unauthorized response it refreshes the access token
// once and retries.
func (c *Client) SendMarkdown(ctx context.Context, content string) (Message, error) {
	var message Message
	if !c.Configured() {
		return message, errors.New("QQ credentials are not initialized")
	}
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return message, err
	}
	err = c.postMarkdown(ctx, token, content, &message)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		return message, err
	}
	c.clearToken(token)
	token, err = c.getAccessToken(ctx)
	if err != nil {
		return message, err
	}
	err = c.postMarkdown(ctx, token, content, &message)
	return message, err
}

func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.Configured() {
		return "", errors.New("QQ credentials are not initialized")
	}
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}

	payload, err := json.Marshal(map[string]string{"appId": c.appID, "clientSecret": c.appSecret})
	if err != nil {
		return "", fmt.Errorf("marshal access token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/app/getAppAccessToken", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create access token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	var response struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := c.doJSON(req, &response); err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	expiresIn, err := parseExpiresIn(response.ExpiresIn)
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	if strings.TrimSpace(response.AccessToken) == "" {
		return "", errors.New("get access token: response did not contain an access token")
	}
	c.accessToken = response.AccessToken
	validFor := time.Duration(expiresIn) * time.Second
	if validFor > tokenRefreshAdvance {
		validFor -= tokenRefreshAdvance
	}
	c.tokenExpiry = time.Now().Add(validFor)
	return c.accessToken, nil
}

func parseExpiresIn(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("token response did not contain expires_in")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, parseErr := number.Int64()
		if parseErr == nil && value > 0 {
			return value, nil
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr == nil && value > 0 {
			return value, nil
		}
	}
	return 0, errors.New("token response contained invalid expires_in")
}

func (c *Client) clearToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken == token {
		c.accessToken = ""
		c.tokenExpiry = time.Time{}
	}
}

func (c *Client) postMarkdown(ctx context.Context, token, content string, target any) error {
	payload, err := json.Marshal(map[string]any{
		"msg_type": 2,
		"markdown": map[string]string{"content": content},
	})
	if err != nil {
		return fmt.Errorf("marshal message request: %w", err)
	}
	endpoint := c.baseURL + "/v2/users/" + url.PathEscape(c.userOpenID) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create message request: %w", err)
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("X-Union-Appid", c.appID)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	if err := c.doJSON(req, target); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

func (c *Client) doJSON(req *http.Request, target any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("response exceeds %d-byte limit", maxResponseBytes)
	}
	var errorBody struct {
		Code    int64  `json:"err_code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &errorBody); err != nil {
			return fmt.Errorf("invalid JSON response: %w", err)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || errorBody.Code != 0 {
		return &apiError{StatusCode: resp.StatusCode, Code: errorBody.Code, Message: errorBody.Message, TraceID: errorBody.TraceID}
	}
	if target != nil && len(body) > 0 {
		if err := json.Unmarshal(body, target); err != nil {
			return fmt.Errorf("invalid JSON response: %w", err)
		}
	}
	return nil
}
