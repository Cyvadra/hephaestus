package ocr

import (
	"bytes"
	"context"
	"encoding/base64"
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
	defaultTokenEndpoint = "https://aip.baidubce.com/oauth/2.0/token"
	defaultOCREndpoint   = "https://aip.baidubce.com/rest/2.0/ocr/v1/general_basic"
)

var defaultClient = &Client{
	httpClient:    http.DefaultClient,
	tokenEndpoint: defaultTokenEndpoint,
	ocrEndpoint:   defaultOCREndpoint,
}

// Client calls Baidu's general basic OCR API.
type Client struct {
	mu            sync.Mutex
	apiKey        string
	secretKey     string
	accessToken   string
	tokenExpiry   time.Time
	httpClient    *http.Client
	tokenEndpoint string
	ocrEndpoint   string
}

// Options controls optional general basic OCR features.
type Options struct {
	DetectDirection bool
	DetectLanguage  bool
	Paragraph       bool
	Probability     bool
}

// Result is the response returned by Baidu's general basic OCR API.
type Result struct {
	LogID          uint64        `json:"log_id"`
	WordsResultNum int           `json:"words_result_num"`
	WordsResult    []WordsResult `json:"words_result"`
	Direction      int           `json:"direction,omitempty"`
	Language       int           `json:"language,omitempty"`
	Paragraphs     []Paragraph   `json:"paragraphs_result,omitempty"`
}

type WordsResult struct {
	Words       string       `json:"words"`
	Probability *Probability `json:"probability,omitempty"`
}

type Probability struct {
	Average  float64 `json:"average"`
	Variance float64 `json:"variance"`
	Minimum  float64 `json:"min"`
}

type Paragraph struct {
	WordsResultIndex []int `json:"words_result_idx"`
}

// APIError describes an error response from Baidu.
type APIError struct {
	Code    int    `json:"error_code"`
	Message string `json:"error_msg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("baidu ocr: API error %d: %s", e.Code, e.Message)
}

// Init injects the API key and secret key used by package-level OCR calls.
func Init(apiKey, secretKey string) {
	defaultClient.Init(apiKey, secretKey)
}

// Init updates this client's credentials and clears its cached access token.
func (c *Client) Init(apiKey, secretKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = strings.TrimSpace(apiKey)
	c.secretKey = strings.TrimSpace(secretKey)
	c.accessToken = ""
	c.tokenExpiry = time.Time{}
}

func RecognizeURL(ctx context.Context, imageURL string, options Options) (*Result, error) {
	return defaultClient.RecognizeURL(ctx, imageURL, options)
}

func RecognizeImage(ctx context.Context, image []byte, options Options) (*Result, error) {
	return defaultClient.RecognizeImage(ctx, image, options)
}

func (c *Client) RecognizeURL(ctx context.Context, imageURL string, options Options) (*Result, error) {
	if strings.TrimSpace(imageURL) == "" {
		return nil, errors.New("baidu ocr: image URL is required")
	}
	values := url.Values{"url": {imageURL}}
	return c.recognize(ctx, values, options)
}

func (c *Client) RecognizeImage(ctx context.Context, image []byte, options Options) (*Result, error) {
	if len(image) == 0 {
		return nil, errors.New("baidu ocr: image is required")
	}
	values := url.Values{"image": {base64.StdEncoding.EncodeToString(image)}}
	return c.recognize(ctx, values, options)
}

func (c *Client) recognize(ctx context.Context, values url.Values, options Options) (*Result, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	values.Set("detect_direction", strconv.FormatBool(options.DetectDirection))
	values.Set("detect_language", strconv.FormatBool(options.DetectLanguage))
	values.Set("paragraph", strconv.FormatBool(options.Paragraph))
	values.Set("probability", strconv.FormatBool(options.Probability))

	requestURL := c.ocrEndpoint + "?access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("baidu ocr: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	var result Result
	if err := c.doJSON(req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apiKey == "" || c.secretKey == "" {
		return "", errors.New("baidu ocr: credentials are not initialized")
	}
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}

	values := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.apiKey},
		"client_secret": {c.secretKey},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return "", fmt.Errorf("baidu ocr: create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	var response struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := c.doJSON(req, &response); err != nil {
		return "", fmt.Errorf("baidu ocr: get access token: %w", err)
	}
	if response.AccessToken == "" {
		return "", errors.New("baidu ocr: token response did not contain an access token")
	}
	c.accessToken = response.AccessToken
	validFor := time.Duration(response.ExpiresIn) * time.Second
	if validFor > time.Minute {
		validFor -= time.Minute
	}
	c.tokenExpiry = time.Now().Add(validFor)
	return c.accessToken, nil
}

func (c *Client) doJSON(req *http.Request, target any) error {
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("baidu ocr: send request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("baidu ocr: read response: %w", err)
	}
	var apiError APIError
	if err := json.Unmarshal(body, &apiError); err == nil && apiError.Code != 0 {
		return &apiError
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("baidu ocr: HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("baidu ocr: decode response: %w", err)
	}
	return nil
}
