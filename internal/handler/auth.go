package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	authn "github.com/dleiferives/tifl/internal/auth"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
)

const refreshCookieName = "tifl_refresh"

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userDTO struct {
	UserID    string   `json:"user_id"`
	Email     string   `json:"email"`
	CreatedAt float64  `json:"created_at"`
	LastLogin *float64 `json:"last_login,omitempty"`
}

type authResponse struct {
	AccessToken string  `json:"access_token"`
	TokenType   string  `json:"token_type"`
	ExpiresIn   int     `json:"expires_in"`
	User        userDTO `json:"user"`
}

func (h *Handler) registerAPI(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.Handle(pattern, h.requireUser(fn))
}

func (h *Handler) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.auth == nil {
			next.ServeHTTP(w, r.WithContext(authn.WithUserID(r.Context(), domain.LocalUserID)))
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) || strings.TrimSpace(strings.TrimPrefix(header, prefix)) == "" {
			writeUnauthorized(w)
			return
		}
		raw := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if strings.Contains(raw, " ") {
			writeUnauthorized(w)
			return
		}
		claims, err := h.auth.ValidateAccess(raw)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(authn.WithUserID(r.Context(), claims.Subject)))
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.allowAuthAttempt(w, r, domain.AuthFlowRegister) {
		return
	}
	var req credentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request body"))
		return
	}
	session, err := h.auth.Register(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, authn.ErrInvalidEmail), errors.Is(err, authn.ErrPasswordLength):
		writeError(w, http.StatusBadRequest, err)
		return
	case errors.Is(err, authn.ErrEmailUnavailable):
		writeError(w, http.StatusConflict, err)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, errors.New("unable to create account"))
		return
	}
	h.setRefreshCookie(w, session)
	writeJSON(w, http.StatusCreated, sessionResponseBody(session))
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.allowAuthAttempt(w, r, domain.AuthFlowLogin) {
		return
	}
	var req credentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request body"))
		return
	}
	session, err := h.auth.Login(r.Context(), req.Email, req.Password)
	if errors.Is(err, authn.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("unable to log in"))
		return
	}
	h.setRefreshCookie(w, session)
	writeJSON(w, http.StatusOK, sessionResponseBody(session))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil {
		writeUnauthorized(w)
		return
	}
	session, err := h.auth.Refresh(r.Context(), cookie.Value)
	if err != nil {
		h.clearRefreshCookie(w)
		writeUnauthorized(w)
		return
	}
	h.setRefreshCookie(w, session)
	writeJSON(w, http.StatusOK, sessionResponseBody(session))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(refreshCookieName); err == nil {
		if err := h.auth.Logout(r.Context(), cookie.Value); err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("unable to log out"))
			return
		}
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.LogoutAll(r.Context(), h.currentUserID(r)); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("unable to log out"))
		return
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user, err := h.auth.User(r.Context(), h.currentUserID(r))
	if errors.Is(err, db.ErrNotFound) {
		writeUnauthorized(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("unable to load user"))
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(user))
}

func (h *Handler) allowAuthAttempt(w http.ResponseWriter, r *http.Request, flow domain.AuthFlow) bool {
	if h.authLimiter.Allow(r) {
		return true
	}
	h.recordThrottledAuthAttempt(r, flow)
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, errors.New("too many authentication attempts"))
	return false
}

func (h *Handler) recordThrottledAuthAttempt(r *http.Request, flow domain.AuthFlow) {
	var req credentialsRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	_, _, _ = h.repo.InsertAuthSecurityEvent(r.Context(), domain.AuthSecurityEvent{
		EventType:           domain.AuthSecurityEventThrottledAttempt,
		Flow:                flow,
		EmailHash:           authn.SecurityEmailHash(req.Email),
		SourceAddressBucket: authSourceAddressBucket(r),
		Details: map[string]any{
			"reason": "auth_limiter",
		},
	})
}

func authSourceAddressBucket(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "unknown"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "remote:" + hashString(host)
	}
	if v4 := ip.To4(); v4 != nil {
		masked := net.IP(v4).Mask(net.CIDRMask(24, 32))
		return fmt.Sprintf("ip:%s/24", masked.String())
	}
	masked := ip.Mask(net.CIDRMask(64, 128))
	return fmt.Sprintf("ip:%s/64", masked.String())
}

func hashString(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) setRefreshCookie(w http.ResponseWriter, session authn.Session) {
	maxAge := int(time.Until(session.RefreshExpiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: session.RefreshToken, Path: "/api/v1/auth",
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
		MaxAge: maxAge, Expires: session.RefreshExpiresAt,
	})
}

func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: "", Path: "/api/v1/auth",
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
		MaxAge: -1, Expires: time.Unix(1, 0).UTC(),
	})
}

func sessionResponseBody(session authn.Session) authResponse {
	return authResponse{
		AccessToken: session.AccessToken, TokenType: "Bearer", ExpiresIn: session.ExpiresIn,
		User: toUserDTO(session.User),
	}
}

func toUserDTO(user domain.User) userDTO {
	return userDTO{UserID: user.UserID, Email: user.Email, CreatedAt: user.CreatedAt, LastLogin: user.LastLogin}
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="tifl"`)
	writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
}
