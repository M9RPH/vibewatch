package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Manager struct {
	Password string
	Secret   []byte
	Secure   bool
}

type Identity struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Expires  int64  `json:"exp"`
}

func New(password, secret string, secure bool) *Manager {
	return &Manager{Password: password, Secret: []byte(secret), Secure: secure}
}
func (m *Manager) Enabled() bool { return m.Password != "" }
func (m *Manager) CheckAdminPassword(password string) bool {
	return subtle.ConstantTimeCompare([]byte(password), []byte(m.Password)) == 1
}

func (m *Manager) LoginAdmin(w http.ResponseWriter, password string) (Identity, bool) {
	if !m.CheckAdminPassword(password) {
		return Identity{}, false
	}
	id := Identity{UserID: 0, Username: "admin", Role: "owner"}
	m.SetSession(w, id)
	return id, true
}

func (m *Manager) SetSession(w http.ResponseWriter, id Identity) {
	id.Expires = time.Now().Add(7 * 24 * time.Hour).Unix()
	payloadBytes, _ := json.Marshal(id)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := m.sign(payload)
	value := payload + "." + sig
	http.SetCookie(w, &http.Cookie{Name: "wtui_session", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: m.Secure, MaxAge: 7 * 24 * 3600})
}
func (m *Manager) Logout(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "wtui_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: m.Secure, MaxAge: -1})
}
func (m *Manager) Session(r *http.Request) (Identity, bool) {
	if !m.Enabled() {
		return Identity{Username: "admin", Role: "owner", Expires: time.Now().Add(time.Hour).Unix()}, true
	}
	c, err := r.Cookie("wtui_session")
	if err != nil {
		return Identity{}, false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(m.sign(parts[0]))) {
		return Identity{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, false
	}
	var id Identity
	if err := json.Unmarshal(payload, &id); err != nil {
		return Identity{}, false
	}
	if id.Expires <= time.Now().Unix() || id.Username == "" || (id.Role != "owner" && id.Role != "admin" && id.Role != "user") {
		return Identity{}, false
	}
	return id, true
}
func (m *Manager) Valid(r *http.Request) bool { _, ok := m.Session(r); return ok }
func (m *Manager) sign(v string) string {
	h := hmac.New(sha256.New, m.Secret)
	_, _ = h.Write([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
	})
}

const passwordIterations = 210000

// HashPassword uses PBKDF2-HMAC-SHA256 implemented with the standard library
// so the controller keeps its dependency-free build while regular user
// passwords are never stored in plaintext.
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", fmt.Errorf("password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := pbkdf2SHA256([]byte(password), salt, passwordIterations, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, hex.EncodeToString(salt), hex.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 10000 || iterations > 1000000 {
		return false
	}
	salt, err1 := hex.DecodeString(parts[2])
	want, err2 := hex.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	hLen := sha256.Size
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
