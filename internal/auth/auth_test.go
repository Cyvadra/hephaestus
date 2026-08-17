package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginIssuesAndParsesJWT(t *testing.T) {
	svc := newTestService(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	token, err := svc.Login("admin", now.UnixMilli(), "0123456789abcdef0123456789abcdef", loginDigest("password", now.UnixMilli(), "0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Username != "admin" || claims.ExpiresAt != now.Add(TokenLifetime).Unix() {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestLoginRejectsInvalidAndReplayedProof(t *testing.T) {
	svc := newTestService(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	salt := "0123456789abcdef0123456789abcdef"
	digest := loginDigest("password", now.UnixMilli(), salt)
	if _, err := svc.Login("admin", now.UnixMilli(), salt, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login("admin", now.UnixMilli(), salt, digest); err != ErrInvalidCredentials {
		t.Fatalf("replay error = %v", err)
	}
	if _, err := svc.Login("admin", now.Add(LoginWindow+time.Millisecond).UnixMilli(), salt, digest); err != ErrInvalidCredentials {
		t.Fatalf("stale proof error = %v", err)
	}
	if _, err := svc.Login("admin", now.UnixMilli(), "UPPERCASE0123456789abcdef01234567", digest); err != ErrInvalidCredentials {
		t.Fatalf("bad salt error = %v", err)
	}
}

func TestRefreshAndCookie(t *testing.T) {
	svc := newTestService(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	refreshClaims := &Claims{Username: "admin", ExpiresAt: now.Add(RefreshThreshold).Unix()}
	if token, refreshed, err := svc.RefreshIfNeeded(refreshClaims); err != nil || !refreshed || token == "" {
		t.Fatalf("refresh = %q, %t, %v", token, refreshed, err)
	}
	noRefreshClaims := &Claims{Username: "admin", ExpiresAt: now.Add(RefreshThreshold + time.Second).Unix()}
	if _, refreshed, err := svc.RefreshIfNeeded(noRefreshClaims); err != nil || refreshed {
		t.Fatalf("unexpected refresh: %t, %v", refreshed, err)
	}
	recorder := httptest.NewRecorder()
	svc.SetCookie(recorder, "token", false)
	if recorder.Header().Get(TokenHeader) != "token" || len(recorder.Result().Cookies()) != 1 || !recorder.Result().Cookies()[0].HttpOnly {
		t.Fatalf("unexpected response headers: %v", recorder.Header())
	}
}

func TestAuthenticateRejectsBadBearerAndAcceptsCookie(t *testing.T) {
	svc := newTestService(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	token, err := svc.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	if _, err := svc.Authenticate(request); err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer invalid")
	if _, err := svc.Authenticate(request); err != ErrInvalidToken {
		t.Fatalf("invalid bearer error = %v", err)
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := New(Config{Username: "admin", Password: "password", Secret: "12345678901234567890123456789012"})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func loginDigest(password string, timestamp int64, salt string) string {
	digest := sha256.Sum256([]byte(password + fmt.Sprintf("%d", timestamp) + salt))
	return hex.EncodeToString(digest[:])
}
