// Package auth provides single-user login proof validation and JWT sessions.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	CookieName               = "hephaestus_session"
	TokenHeader              = "X-Hephaestus-Token"
	RequestKeyHeader         = "X-Hephaestus-Request-Key"
	SignatureVersionHeader   = "X-Hephaestus-Signature-Version"
	SignatureSessionHeader   = "X-Hephaestus-Signature-Session"
	SignatureTimestampHeader = "X-Hephaestus-Signature-Timestamp"
	SignatureNonceHeader     = "X-Hephaestus-Signature-Nonce"
	SignatureBodyHashHeader  = "X-Hephaestus-Signature-Body-SHA256"
	SignatureHeader          = "X-Hephaestus-Signature"
	SignatureVersion         = "1"
	TokenLifetime            = 14 * 24 * time.Hour
	RefreshThreshold         = 7 * 24 * time.Hour
	LoginWindow              = 5 * time.Minute
	RequestWindow            = 2 * time.Minute
	MaxRequestNonces         = 4096
	FailureWindow            = 10 * time.Minute
	FailureThreshold         = 5
	ProofLifetime            = 2 * time.Minute
	ProofDifficulty          = 18
)

var (
	ErrInvalidCredentials      = errors.New("invalid credentials")
	ErrInvalidToken            = errors.New("invalid token")
	ErrInvalidRequestSignature = errors.New("invalid request signature")
	ErrRequestTimestamp        = errors.New("request timestamp outside accepted window")
	ErrProofRequired           = errors.New("proof of work required")
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

type ProofOfWork struct {
	Challenge  string `json:"challenge"`
	Difficulty int    `json:"difficulty"`
	ExpiresAt  int64  `json:"expires_at"`
}

type pendingProof struct {
	ProofOfWork
	expiresAt time.Time
}

type sessionState struct {
	requestKey []byte
	expiresAt  time.Time
	nonces     map[string]time.Time
}

type Service struct {
	username string
	password string
	secret   []byte
	now      func() time.Time
	mu       sync.Mutex
	replays  map[string]time.Time
	sessions map[string]*sessionState
	failures []time.Time
	proof    *pendingProof
}

func New(config Config) (*Service, error) {
	if strings.TrimSpace(config.Username) == "" || config.Password == "" {
		return nil, fmt.Errorf("auth: username and password are required")
	}
	if len(config.Secret) < 32 {
		return nil, fmt.Errorf("auth: secret must be at least 32 bytes")
	}
	return &Service{username: strings.TrimSpace(config.Username), password: config.Password, secret: []byte(config.Secret), now: time.Now, replays: make(map[string]time.Time), sessions: make(map[string]*sessionState)}, nil
}

func (s *Service) Login(username string, timestamp int64, salt, digest string) (string, error) {
	token, _, err := s.LoginWithProof(username, timestamp, salt, digest, "")
	return token, err
}

// LoginWithRequestKey authenticates a login proof and returns the JWT plus a
// per-session key used to authenticate every subsequent HTTP request.
func (s *Service) LoginWithRequestKey(username string, timestamp int64, salt, digest, nonce string) (string, string, *ProofOfWork, error) {
	token, proof, err := s.LoginWithProof(username, timestamp, salt, digest, nonce)
	if err != nil {
		return "", "", proof, err
	}
	claims, err := s.Parse(token)
	if err != nil {
		return "", "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[claims.ID]
	if state == nil {
		return "", "", nil, ErrInvalidToken
	}
	return token, hex.EncodeToString(state.requestKey), nil, nil
}

func (s *Service) LoginWithProof(username string, timestamp int64, salt, digest, nonce string) (string, *ProofOfWork, error) {
	now := s.now()
	s.mu.Lock()
	s.pruneFailures(now)
	if len(s.failures) >= FailureThreshold && !s.consumeProof(now, nonce) {
		proof, err := s.activeProof(now)
		s.mu.Unlock()
		if err != nil {
			return "", nil, err
		}
		return "", proof, ErrProofRequired
	}
	s.mu.Unlock()

	if strings.TrimSpace(username) == "" || !validSalt(salt) || !validDigest(digest) {
		s.recordFailure(now)
		return "", nil, ErrInvalidCredentials
	}
	issuedAt := time.UnixMilli(timestamp)
	if issuedAt.Before(now.Add(-LoginWindow)) || issuedAt.After(now.Add(LoginWindow)) {
		s.recordFailure(now)
		return "", nil, ErrInvalidCredentials
	}
	expected := sha256.Sum256([]byte(s.password + fmt.Sprintf("%d", timestamp) + salt))
	provided, _ := hex.DecodeString(digest)
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(s.username))
	digestMatch := subtle.ConstantTimeCompare(provided, expected[:])
	if usernameMatch != 1 || digestMatch != 1 {
		s.recordFailure(now)
		return "", nil, ErrInvalidCredentials
	}

	replayKey := username + "\x00" + fmt.Sprintf("%d", timestamp) + "\x00" + salt + "\x00" + digest
	s.mu.Lock()
	for key, expiry := range s.replays {
		if !expiry.After(now) {
			delete(s.replays, key)
		}
	}
	if _, used := s.replays[replayKey]; used {
		s.failures = append(s.failures, now)
		s.mu.Unlock()
		return "", nil, ErrInvalidCredentials
	}
	s.replays[replayKey] = now.Add(LoginWindow)
	s.failures = nil
	s.proof = nil
	s.mu.Unlock()
	token, err := s.issue(now)
	return token, nil, err
}

func (s *Service) recordFailure(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneFailures(now)
	s.failures = append(s.failures, now)
}

func (s *Service) pruneFailures(now time.Time) {
	cutoff := now.Add(-FailureWindow)
	first := 0
	for first < len(s.failures) && s.failures[first].Before(cutoff) {
		first++
	}
	s.failures = append(s.failures[:0], s.failures[first:]...)
	if len(s.failures) < FailureThreshold {
		s.proof = nil
	}
}

func (s *Service) activeProof(now time.Time) (*ProofOfWork, error) {
	if s.proof == nil || !s.proof.expiresAt.After(now) {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return nil, fmt.Errorf("auth: create proof of work: %w", err)
		}
		expiresAt := now.Add(ProofLifetime)
		s.proof = &pendingProof{
			ProofOfWork: ProofOfWork{Challenge: hex.EncodeToString(random), Difficulty: ProofDifficulty, ExpiresAt: expiresAt.UnixMilli()},
			expiresAt:   expiresAt,
		}
	}
	proof := s.proof.ProofOfWork
	return &proof, nil
}

func (s *Service) consumeProof(now time.Time, nonce string) bool {
	if s.proof == nil || !s.proof.expiresAt.After(now) || nonce == "" || len(nonce) > 64 {
		return false
	}
	digest := sha256.Sum256([]byte(s.proof.Challenge + ":" + nonce))
	if !hasLeadingZeroBits(digest[:], s.proof.Difficulty) {
		return false
	}
	s.proof = nil
	return true
}

func hasLeadingZeroBits(value []byte, bits int) bool {
	for bits >= 8 {
		if len(value) == 0 || value[0] != 0 {
			return false
		}
		value = value[1:]
		bits -= 8
	}
	return bits == 0 || (len(value) > 0 && value[0]>>(8-bits) == 0)
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
	s.mu.Lock()
	state := s.sessions[claims.ID]
	if state == nil || !state.expiresAt.After(s.now()) {
		s.mu.Unlock()
		return "", false, ErrInvalidToken
	}
	state.expiresAt = s.now().Add(TokenLifetime)
	s.mu.Unlock()
	token, err := s.issueForSession(s.now(), claims.ID)
	return token, err == nil, err
}

// VerifyRequest validates the signed headers for an authenticated request and
// atomically consumes its nonce after all integrity checks pass.
func (s *Service) VerifyRequest(request *http.Request, claims *Claims, body []byte) error {
	if request.Header.Get(SignatureVersionHeader) != SignatureVersion || request.Header.Get(SignatureSessionHeader) != claims.ID {
		return ErrInvalidRequestSignature
	}
	timestamp, err := strconv.ParseInt(request.Header.Get(SignatureTimestampHeader), 10, 64)
	if err != nil {
		return ErrInvalidRequestSignature
	}
	now := s.now()
	issuedAt := time.UnixMilli(timestamp)
	if issuedAt.Before(now.Add(-RequestWindow)) || issuedAt.After(now.Add(RequestWindow)) {
		return ErrRequestTimestamp
	}
	nonce := request.Header.Get(SignatureNonceHeader)
	providedBodyHash := request.Header.Get(SignatureBodyHashHeader)
	providedSignature := request.Header.Get(SignatureHeader)
	if !validNonce(nonce) || !validDigest(providedBodyHash) || !validDigest(providedSignature) {
		return ErrInvalidRequestSignature
	}
	bodyHash := sha256.Sum256(body)
	if subtle.ConstantTimeCompare([]byte(providedBodyHash), []byte(hex.EncodeToString(bodyHash[:]))) != 1 {
		return ErrInvalidRequestSignature
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSessions(now)
	state := s.sessions[claims.ID]
	if state == nil || !state.expiresAt.After(now) {
		return ErrInvalidRequestSignature
	}
	canonical := canonicalRequest(request, claims.ID, timestamp, nonce, providedBodyHash)
	mac := hmac.New(sha256.New, state.requestKey)
	_, _ = mac.Write([]byte(canonical))
	expectedSignature := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(providedSignature), []byte(expectedSignature)) != 1 {
		return ErrInvalidRequestSignature
	}
	for seenNonce, expiry := range state.nonces {
		if !expiry.After(now) {
			delete(state.nonces, seenNonce)
		}
	}
	if _, used := state.nonces[nonce]; used || len(state.nonces) >= MaxRequestNonces {
		return ErrInvalidRequestSignature
	}
	state.nonces[nonce] = now.Add(RequestWindow)
	return nil
}

// Revoke invalidates the request key and replay state for a session.
func (s *Service) Revoke(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

func (s *Service) SetCookie(writer http.ResponseWriter, token string, secure bool) {
	http.SetCookie(writer, &http.Cookie{Name: CookieName, Value: token, Path: "/", MaxAge: int(TokenLifetime.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure})
	writer.Header().Set(TokenHeader, token)
}

func (s *Service) ClearCookie(writer http.ResponseWriter, secure bool) {
	http.SetCookie(writer, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure})
}

func (s *Service) issue(now time.Time) (string, error) {
	id := uuid.NewString()
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("auth: create request key: %w", err)
	}
	s.mu.Lock()
	s.pruneSessions(now)
	s.sessions[id] = &sessionState{requestKey: key, expiresAt: now.Add(TokenLifetime), nonces: make(map[string]time.Time)}
	s.mu.Unlock()
	return s.issueForSession(now, id)
}

func (s *Service) issueForSession(now time.Time, sessionID string) (string, error) {
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(Claims{Username: s.username, Subject: s.username, ID: sessionID, IssuedAt: now.Unix(), ExpiresAt: now.Add(TokenLifetime).Unix()})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return payload + "." + s.signature(payload), nil
}

func (s *Service) pruneSessions(now time.Time) {
	for sessionID, state := range s.sessions {
		if !state.expiresAt.After(now) {
			delete(s.sessions, sessionID)
		}
	}
}

func canonicalRequest(request *http.Request, sessionID string, timestamp int64, nonce, bodyHash string) string {
	target := request.URL.EscapedPath()
	if target == "" {
		target = "/"
	}
	if request.URL.RawQuery != "" {
		target += "?" + request.URL.RawQuery
	}
	return strings.Join([]string{SignatureVersion, sessionID, strings.ToUpper(request.Method), target, strconv.FormatInt(timestamp, 10), nonce, bodyHash}, "\n")
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

func validNonce(value string) bool {
	return len(value) == 32 && isLowerHex(value)
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
