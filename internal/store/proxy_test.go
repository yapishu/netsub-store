package store

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestWalletAdminThroughTwoProxies(t *testing.T) {
	s := testServer(t)
	key, _ := crypto.GenerateKey()
	s.cfg.AdminWallet = crypto.PubkeyToAddress(key.PublicKey).Hex()
	app := httptest.NewServer(s.Handler())
	defer app.Close()
	endpoint, _ := url.Parse(app.URL)
	home := httptest.NewServer(httputil.NewSingleHostReverseProxy(endpoint))
	defer home.Close()
	endpoint, _ = url.Parse(home.URL)
	public := httptest.NewServer(httputil.NewSingleHostReverseProxy(endpoint))
	defer public.Close()
	call := func(method, path, body, host string, cookie *http.Cookie) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, public.URL+path, strings.NewReader(body))
		r.Host = host
		if method == "POST" {
			r.Header.Set("Origin", s.cfg.Origin)
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		return response
	}
	response := call("POST", "/api/auth/challenge", fmt.Sprintf(`{"address":%q,"admin":true}`, s.cfg.AdminWallet), "store.test", nil)
	var challenge map[string]string
	json.NewDecoder(response.Body).Decode(&challenge)
	signature, _ := crypto.Sign(accounts.TextHash([]byte(challenge["message"])), key)
	response = call("POST", "/api/auth/login", fmt.Sprintf(`{"challenge":%q,"signature":"0x%x"}`, challenge["id"], signature), "store.test", nil)
	if response.StatusCode != 200 {
		b, _ := io.ReadAll(response.Body)
		t.Fatal(response.StatusCode, string(b))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatal("public HTTPS session lost behind HTTP proxies")
	}
	if r := call("GET", "/api/admin", "", "store.test", cookies[0]); r.StatusCode != 200 {
		t.Fatal("GET without Origin loses session", r.StatusCode)
	}
	if r := call("GET", "/api/admin", "", "home.internal", cookies[0]); r.StatusCode != 401 {
		t.Fatal("internal Host should not match public session", r.StatusCode)
	}
}
