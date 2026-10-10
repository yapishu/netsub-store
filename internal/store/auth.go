package store

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
)

type session struct {
	Wallet  string
	Origin  string
	Admin   bool
	CSRF    string
	Expires time.Time
}
type challenge struct {
	Address, Message, Origin string
	Admin                    bool
	Expires                  time.Time
}
type attempts struct {
	Count int
	Until time.Time
}
type auth struct {
	mu         sync.Mutex
	sessions   map[string]session
	challenges map[string]challenge
	rates      map[string]attempts
}

func newAuth() *auth {
	return &auth{sessions: map[string]session{}, challenges: map[string]challenge{}, rates: map[string]attempts{}}
}
func (a *auth) allow(key string, n int, d time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.rates {
		if now.After(v.Until) {
			delete(a.rates, k)
		}
	}
	v := a.rates[key]
	if v.Until.IsZero() {
		v.Until = now.Add(d)
	}
	if v.Count >= n || len(a.rates) > 10000 {
		return false
	}
	v.Count++
	a.rates[key] = v
	return true
}
func ip(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return r.RemoteAddr
	}
	return host
}
func (s *Server) current(r *http.Request) (session, bool) {
	c, e := r.Cookie("netsub_session")
	if e != nil {
		return session{}, false
	}
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	v, ok := s.auth.sessions[c.Value]
	if time.Now().After(v.Expires) {
		delete(s.auth.sessions, c.Value)
		return session{}, false
	}
	return v, ok && v.Origin == s.requestOrigin(r)
}
func (s *Server) setSession(w http.ResponseWriter, r *http.Request, v session) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	for k, a := range s.auth.sessions {
		if time.Now().After(a.Expires) {
			delete(s.auth.sessions, k)
		}
	}
	if c, e := r.Cookie("netsub_session"); e == nil {
		delete(s.auth.sessions, c.Value)
	}
	id := token()
	v.CSRF = token()
	v.Origin = s.requestOrigin(r)
	v.Expires = time.Now().Add(8 * time.Hour)
	s.auth.sessions[id] = v
	http.SetCookie(w, &http.Cookie{Name: "netsub_session", Value: id, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.requestOrigin(r), "https:"), SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
	respond(w, vPublic(v))
}
func vPublic(v session) any {
	return map[string]any{"wallet": v.Wallet, "admin": v.Admin, "csrf": v.CSRF}
}
func recoverWallet(message, sig string) (string, error) {
	b, e := hex.DecodeString(strings.TrimPrefix(sig, "0x"))
	if e != nil || len(b) != 65 {
		return "", errors.New("invalid signature")
	}
	if b[64] >= 27 {
		b[64] -= 27
	}
	if b[64] > 1 {
		return "", errors.New("invalid signature")
	}
	pub, e := crypto.SigToPub(accounts.TextHash([]byte(message)), b)
	if e != nil {
		return "", e
	}
	return strings.ToLower(crypto.PubkeyToAddress(*pub).Hex()), nil
}
func (s *Server) challenge(w http.ResponseWriter, r *http.Request) {
	if !s.auth.allow("challenge:"+ip(r), 20, 10*time.Minute) {
		fail(w, 429, "Try again later")
		return
	}
	var in struct {
		Address string `json:"address"`
		Admin   bool   `json:"admin"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !address(in.Address) {
		fail(w, 400, "Invalid wallet address")
		return
	}
	purpose := "buy planets and reveal your private planet code"
	if in.Admin {
		if s.cfg.AdminWallet == "" {
			fail(w, 503, "Admin wallet sign-in is not configured")
			return
		}
		if !strings.EqualFold(in.Address, s.cfg.AdminWallet) {
			fail(w, 403, "This wallet does not have admin access")
			return
		}
		purpose = "administer this store, including payment settings and planet inventory"
	}
	id := token()
	expires := time.Now().Add(5 * time.Minute)
	origin := s.requestOrigin(r)
	message := fmt.Sprintf("%s wants you to sign in to networked subject_.\n\nWallet: %s\nPurpose: %s.\nURI: %s\nNonce: %s\nIssued At: %s\nExpiration Time: %s\n\nThis signature does not authorize a payment.", origin, strings.ToLower(in.Address), purpose, origin, id, time.Now().UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339))
	s.auth.mu.Lock()
	for k, c := range s.auth.challenges {
		if time.Now().After(c.Expires) {
			delete(s.auth.challenges, k)
		}
	}
	if len(s.auth.challenges) >= 1000 {
		s.auth.mu.Unlock()
		fail(w, 429, "Try again later")
		return
	}
	s.auth.challenges[id] = challenge{Admin: in.Admin, Address: strings.ToLower(in.Address), Message: message, Origin: origin, Expires: expires}
	s.auth.mu.Unlock()
	respond(w, map[string]string{"id": id, "message": message})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.auth.allow("login:"+ip(r), 12, 15*time.Minute) {
		fail(w, 429, "Too many attempts. Try again in 15 minutes.")
		return
	}
	var in struct{ Challenge, Signature string }
	if !readJSON(w, r, &in) {
		return
	}
	s.auth.mu.Lock()
	c, ok := s.auth.challenges[in.Challenge]
	delete(s.auth.challenges, in.Challenge)
	s.auth.mu.Unlock()
	if !ok || time.Now().After(c.Expires) || c.Origin != s.requestOrigin(r) {
		fail(w, 401, "Sign-in challenge expired")
		return
	}
	wallet, e := recoverWallet(c.Message, in.Signature)
	if e != nil || wallet != c.Address {
		fail(w, 401, "Sign-in failed")
		return
	}
	if c.Admin && !strings.EqualFold(wallet, s.cfg.AdminWallet) {
		fail(w, 403, "This wallet does not have admin access")
		return
	}
	s.setSession(w, r, session{Wallet: wallet, Admin: c.Admin})
}

// Local access is allowed only when both the browser origin and request host are local.
func (s *Server) allowedOrigin(origin string, r *http.Request) bool {
	if origin == s.cfg.Origin {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) requestOrigin(r *http.Request) string {
	if origin := r.Header.Get("Origin"); origin != "" {
		if s.allowedOrigin(origin, r) {
			return origin
		}
		return ""
	}
	local := "http://" + r.Host
	if s.allowedOrigin(local, r) {
		return local
	}
	configured, err := url.Parse(s.cfg.Origin)
	if err == nil && strings.EqualFold(configured.Host, r.Host) {
		return s.cfg.Origin
	}
	return ""
}
