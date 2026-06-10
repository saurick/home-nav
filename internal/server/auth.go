package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

const sessionCookieName = "home_nav_session"

const (
	defaultAuthUsername      = "admin"
	defaultAuthPassword      = "change-me"
	defaultAuthSessionSecret = "change-this-to-at-least-32-random-characters"
)

func (s *Server) authEnabled() bool {
	return s.currentConfig().Auth.Enabled
}

func (s *Server) setupRequired() bool {
	return configNeedsSetup(s.currentConfig())
}

func configNeedsSetup(cfg *Config) bool {
	return !cfg.Auth.Enabled &&
		cfg.Auth.Username == defaultAuthUsername &&
		cfg.Auth.Password == defaultAuthPassword &&
		cfg.Auth.SessionSecret == defaultAuthSessionSecret
}

func (s *Server) authenticated(r *http.Request) bool {
	cfg := s.currentConfig()
	if configNeedsSetup(cfg) {
		return false
	}
	if !cfg.Auth.Enabled {
		return true
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}

	username, ok := s.parseSession(cookie.Value)
	return ok && username == cfg.Auth.Username
}

func (s *Server) newSession(username string) string {
	payload := username
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	signature := s.sign(encodedPayload)
	return encodedPayload + "." + signature
}

func (s *Server) parseSession(value string) (string, bool) {
	payload, signature, ok := strings.Cut(value, ".")
	if !ok || payload == "" || signature == "" {
		return "", false
	}
	if !constantTimeEqual(signature, s.sign(payload)) {
		return "", false
	}

	rawPayload, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	username := string(rawPayload)
	if legacyUsername, rawExpires, ok := strings.Cut(username, ":"); ok {
		if _, err := strconv.ParseInt(rawExpires, 10, 64); err == nil {
			username = legacyUsername
		}
	}
	if username == "" {
		return "", false
	}
	return username, true
}

func (s *Server) sign(payload string) string {
	cfg := s.currentConfig()
	mac := hmac.New(sha256.New, []byte(cfg.Auth.SessionSecret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) validCredentials(username, password string) bool {
	cfg := s.currentConfig()
	expectedUsername := cfg.Auth.Username
	expectedPassword := cfg.Auth.Password
	userOK := constantTimeEqual(username, expectedUsername)
	passwordOK := constantTimeEqual(password, expectedPassword)
	return userOK && passwordOK
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomSessionSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, username string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.newSession(username),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
	})
}

func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
