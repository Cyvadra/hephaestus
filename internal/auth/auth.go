// Package auth provides single-user login proof validation and JWT sessions.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	CookieName       = "hephaestus_session"
	TokenHeader      = "X-Hephaestus-Token"
	TokenLifetime    = 14 * 24 * time.Hour
	RefreshThreshold = 7 * 24 * time.Hour
	LoginWindow      = 5 * time.Minute
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidToken       = errors.New("invalid token")
)

type Config struct {
	Username string
	Password string
	Secret   string
}

type Claims struct {
	Username  string `json:"username"`
	Subject   string `json:"sub"`
	ID        string `json:"jti"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type Service struct {
	username string
	password string
	secret   []byte
	now      func() time.Time
	mu       sync.Mutex
	replays  map[string]time.Time
}

func New(config Config) (*Service, error) {
	if strings.TrimSpace(config.Username) == "" || config.Password == "" {
		return nil, fmt.Errorf("auth: username and password are required")
	}
	if len(config.Secret) < 32 {
		return nil, fmt.Errorf("auth: secret must be at least 32 bytes")
	}
	return &Service{username: strings.TrimSpace(config.Username), password: config.Password, secret: []byte(config.Secret), now: time.Now, replays: make(map[string]time.Time)}, nil
}

func (s *Service) Login(username string, timestamp int64, salt, digest string) (string, error) {
	now := s.now()
	if strings.TrimSpace(username) == "" || !validSalt(salt) || !validDigest(digest) {
		return "", ErrInvalidCredentials
	}
	issuedAt := time.UnixMilli(timestamp)
	if issuedAt.Before(now.Add(-LoginWindow)) || issuedAt.After(now.Add(LoginWindow)) {
		return "", ErrInvalidCredentials
	}
	expected := sha256.Sum256([]byte(s.password + fmt.Sprintf("%d", timestamp) + salt))
	provided, _ := hex.DecodeString(digest)
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(s.username))
	digestMatch := subtle.ConstantTimeCompare(provided, expected[:])
	if usernameMatch != 1 || digestMatch != 1 {
		return "", ErrInvalidCredentials
	}

	replayKey := username + "\x00" + fmt.Sprintf("%d", timestamp) + "\x00" + salt + "\x00" + digest
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, expiry := range s.replays {
		if !expiry.After(now) {
			delete(s.replays, key)
		}
	}
	if _, used := s.replays[replayKey]; used {
		return "", ErrInvalidCredentials
	}
	s.replays[replayKey] = now.Add(LoginWindow)
	return s.issue(now)
}

func (s *Service) Parse(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || !s.verifySignature(parts[0]+"."+parts[1], parts[2]) {
		return nil, ErrInvalidToken
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if !decodeJSON(parts[0], &header) || header.Algorithm != "HS256" || header.Type != "JWT" {
		return nil, ErrInvalidToken
	}
	claims := &Claims{}
	if !decodeJSON(parts[1], claims) || claims.Username != s.username || claims.Subject != s.username || claims.ID == "" || claims.IssuedAt <= 0 || claims.ExpiresAt <= s.now().Unix() || claims.ExpiresAt <= claims.IssuedAt {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// Authenticate accepts an Authorization bearer token or the session cookie.
// An explicitly supplied malformed bearer token is never silently bypassed by
// a cookie, preventing header corruption from changing authentication mode.
func (s *Service) Authenticate(request *http.Request) (*Claims, error) {
	_, claims, err := s.authenticate(request)
	return claims, err
}

// Token returns the authenticated token selected from the request's bearer
// header and session cookie. It is intended for same-origin response headers.
func (s *Service) Token(request *http.Request) (string, error) {
	token, _, err := s.authenticate(request)
	return token, err
}

func (s *Service) authenticate(request *http.Request) (string, *Claims, error) {
	bearer := strings.TrimSpace(request.Header.Get("Authorization"))
	if bearer != "" {
		if !strings.HasPrefix(bearer, "Bearer ") {
			return "", nil, ErrInvalidToken
		}
		bearerToken := strings.TrimSpace(strings.TrimPrefix(bearer, "Bearer "))
		claims, err := s.Parse(bearerToken)
		if err != nil {
			return "", nil, err
		}
		if cookie, err := request.Cookie(CookieName); err == nil {
			cookieClaims, cookieErr := s.Parse(cookie.Value)
			if cookieErr == nil && cookieClaims.ExpiresAt > claims.ExpiresAt {
				return cookie.Value, cookieClaims, nil
			}
		}
		return bearerToken, claims, nil
	}
	cookie, err := request.Cookie(CookieName)
	if err != nil {
		return "", nil, ErrInvalidToken
	}
	claims, err := s.Parse(cookie.Value)
	if err != nil {
		return "", nil, err
	}
	return cookie.Value, claims, nil
}

func (s *Service) RefreshIfNeeded(claims *Claims) (string, bool, error) {
	if claims.ExpiresAt == 0 {
		return "", false, ErrInvalidToken
	}
	if time.Unix(claims.ExpiresAt, 0).Sub(s.now()) > RefreshThreshold {
		return "", false, nil
	}
	token, err := s.issue(s.now())
	return token, err == nil, err
}

func (s *Service) SetCookie(writer http.ResponseWriter, token string, secure bool) {
	http.SetCookie(writer, &http.Cookie{Name: CookieName, Value: token, Path: "/", MaxAge: int(TokenLifetime.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure})
	writer.Header().Set(TokenHeader, token)
}

func (s *Service) ClearCookie(writer http.ResponseWriter, secure bool) {
	http.SetCookie(writer, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure})
}

func (s *Service) issue(now time.Time) (string, error) {
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(Claims{Username: s.username, Subject: s.username, ID: uuid.NewString(), IssuedAt: now.Unix(), ExpiresAt: now.Add(TokenLifetime).Unix()})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return payload + "." + s.signature(payload), nil
}

func (s *Service) signature(payload string) string {
	signer := hmac.New(sha256.New, s.secret)
	_, _ = signer.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(signer.Sum(nil))
}

func (s *Service) verifySignature(payload, supplied string) bool {
	expected, err := base64.RawURLEncoding.DecodeString(s.signature(payload))
	if err != nil {
		return false
	}
	actual, err := base64.RawURLEncoding.DecodeString(supplied)
	return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1
}

func decodeJSON(encoded string, value any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && json.Unmarshal(raw, value) == nil
}

func validSalt(value string) bool {
	return len(value) == 32 && isLowerHex(value)
}

func validDigest(value string) bool {
	return len(value) == sha256.Size*2 && isLowerHex(value)
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
