package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Native-Planet/perigee/roller"
	"github.com/Native-Planet/perigee/types"
	"github.com/deelawn/urbit-gob/co"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	bolt "go.etcd.io/bbolt"
)

const buyer = "0x1111111111111111111111111111111111111111"
const recipient = "0x2222222222222222222222222222222222222222"
const hash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testServer(t *testing.T) *Server {
	t.Helper()
	c := Config{Demo: true, DBPath: filepath.Join(t.TempDir(), "store.db"), Star: "~marzod", Origin: "https://store.test", AdminWallet: buyer, EncryptionKey: bytes.Repeat([]byte{1}, 32), Chains: map[int]Chain{1: {ID: 1, USDC: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"}, 8453: {ID: 8453, USDC: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"}}}
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string
			Method string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "getSpawned" {
			t.Errorf("unexpected availability method %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": []any{}})
	}))
	t.Cleanup(rpc.Close)
	s.roller = roller.New(roller.Config{Endpoint: rpc.URL})
	s.cfg.ReceiptTo = "matwet@subject.network"
	s.cfg.Demo = false
	s.rates = &MarketRates{rates: map[string]string{"BTC": "100000", "ETH": "2500", "XMR": "200"}, next: time.Now().Add(time.Hour)}
	s.auth.sessions["buyer"] = session{Origin: c.Origin, Wallet: buyer, CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.auth.sessions["other"] = session{Origin: c.Origin, Wallet: recipient, CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.auth.sessions["admin"] = session{Origin: c.Origin, Admin: true, CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	e = s.db.Update(func(tx *bolt.Tx) error {
		v := settings(tx)
		v.Open = true
		v.Recipient = recipient
		b, _ := json.Marshal(v)
		if e := tx.Bucket([]byte("settings")).Put([]byte("store"), b); e != nil {
			return e
		}
		return tx.Bucket([]byte("inventory")).Put([]byte("updated"), []byte(time.Now().UTC().Format(time.RFC3339)))
	})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func request(s *Server, method, path, body, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.cfg.Origin)
	r.Header.Set("X-CSRF-Token", "csrf")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "netsub_session", Value: cookie})
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func create(t *testing.T, s *Server, planet string) Order {
	t.Helper()
	w := request(s, "POST", "/api/orders", fmt.Sprintf(`{"planet":%q,"email":"buyer@example.com","chainId":8453,"asset":"USDC"}`, planet), "buyer")
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var o Order
	if e := json.Unmarshal(w.Body.Bytes(), &o); e != nil {
		t.Fatal(e)
	}
	return o
}
func readOrder(t *testing.T, s *Server, id string) Order {
	t.Helper()
	var o Order
	e := s.db.View(func(tx *bolt.Tx) error { var e error; o, e = getOrder(tx, id); return e })
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func TestPricePrecision(t *testing.T) {
	for _, s := range []string{"0", "-1", "1e6", "NaN", "1.0000001", "", ".1", "1..2"} {
		if _, e := units(s, 6); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
	n, e := units("10.000001", 6)
	if e != nil || n.String() != "10000001" {
		t.Fatal(n, e)
	}
}
func TestPlanetCodeEncryptionAndPersistence(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	c := types.PlanetCode{Planet: o.Planet, Code: "activation-secret", URL: "https://bridge.urbit.org/#activation-secret"}
	var e error
	o.Secret, e = encrypt(s.cfg.EncryptionKey, o.ID, c)
	if e != nil {
		t.Fatal(e)
	}
	o.Status = "delivered"
	o.Checks = 17
	o.EmailAttempts = 3
	o.NextCheck = time.Now().UTC().Truncate(time.Second)
	if e = s.save(o); e != nil {
		t.Fatal(e)
	}
	copy := readOrder(t, s, o.ID)
	if copy.Secret != o.Secret || copy.Checks != 17 || copy.EmailAttempts != 3 || !copy.NextCheck.Equal(o.NextCheck) {
		t.Fatal("lost durable worker state")
	}
	plain, e := decrypt(s.cfg.EncryptionKey, o.ID, copy.Secret)
	if e != nil || plain.Code != c.Code {
		t.Fatal(plain, e)
	}
	if _, e = decrypt(s.cfg.EncryptionKey, "other-order", copy.Secret); e == nil {
		t.Fatal("ciphertext not bound to order")
	}
	for _, who := range []string{"", "other", "admin"} {
		w := request(s, "GET", "/api/orders/"+o.ID+"/planet-code", "", who)
		if w.Code == 200 {
			t.Fatalf("credentials exposed to %q", who)
		}
	}
	w := request(s, "GET", "/api/orders/"+o.ID+"/planet-code", "", "buyer")
	if w.Code != 200 || !strings.Contains(w.Body.String(), c.Code) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/orders", "/api/admin"} {
		who := "buyer"
		if path == "/api/admin" {
			who = "admin"
		}
		w = request(s, "GET", path, "", who)
		for _, secret := range []string{c.Code, c.URL, o.Secret} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret leaked in order metadata")
			}
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cache policy")
	}
}
func TestReservationPricingAndPaymentClaim(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-sampel")
	if o.Tier != "premium" || o.Amount != "30000000" {
		t.Fatal("premium quote", o)
	}
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-sampel","email":"buyer@example.com","chainId":8453,"asset":"USDC"}`, "buyer")
	if w.Code != 409 {
		t.Fatal("double reservation", w.Code)
	}
	e := s.db.Update(func(tx *bolt.Tx) error {
		v := settings(tx)
		v.Prices.PremiumUSDC = "100"
		b, _ := json.Marshal(v)
		return tx.Bucket([]byte("settings")).Put([]byte("store"), b)
	})
	if e != nil {
		t.Fatal(e)
	}
	if readOrder(t, s, o.ID).Amount != "30000000" {
		t.Fatal("quote mutated")
	}
	w = request(s, "POST", "/api/orders/"+o.ID+"/payment", `{"hash":"`+hash+`"}`, "other")
	if w.Code == 200 {
		t.Fatal("foreign wallet mutated order")
	}
	w = request(s, "POST", "/api/orders/"+o.ID+"/payment", `{"hash":"`+hash+`"}`, "buyer")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = s.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("payments")).Stats().KeyN != 0 {
			t.Fatal("unverified hash consumes payment")
		}
		return nil
	})
}
func TestCookieCSRFOriginAndLoginRateLimit(t *testing.T) {
	s := testServer(t)
	r := httptest.NewRequest("PUT", "/api/admin/settings", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.test")
	r.AddCookie(&http.Cookie{Name: "netsub_session", Value: "admin"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	r.Header.Set("Origin", s.cfg.Origin)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	w = request(s, "POST", "/api/auth/login", `{"password":"anything"}`, "")
	if w.Code < 400 || len(w.Result().Cookies()) != 0 {
		t.Fatal("password grants access")
	}
	for i := 0; i < 12; i++ {
		w = request(s, "POST", "/api/auth/login", `{"Password":"wrong"}`, "")
	}
	if w.Code != 429 {
		t.Fatal("login not limited", w.Code)
	}
}
func TestWalletChallengeSingleUse(t *testing.T) {
	s := testServer(t)
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	w := request(s, "POST", "/api/auth/challenge", fmt.Sprintf(`{"address":%q}`, addr), "")
	var c map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if !strings.Contains(c["message"], s.cfg.Origin) {
		t.Fatal("challenge missing domain")
	}
	sig, _ := crypto.Sign(accounts.TextHash([]byte(c["message"])), key)
	body := fmt.Sprintf(`{"challenge":%q,"signature":"0x%x"}`, c["id"], sig)
	w = request(s, "POST", "/api/auth/login", body, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	if request(s, "POST", "/api/auth/login", body, "").Code != 401 {
		t.Fatal("replayed signature accepted")
	}
}
func transferFixture() (Chain, Order, chainTx, chainReceipt, chainBlock) {
	now := time.Now().UTC().Truncate(time.Second)
	ch := Chain{ID: 8453, USDC: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"}
	o := Order{Wallet: buyer, Recipient: recipient, Asset: "ETH", Amount: "100", TxHash: hash, Created: now.Add(-time.Minute), Expires: now.Add(time.Minute)}
	tx := chainTx{Hash: hash, From: buyer, To: recipient, Value: "0x64", BlockHash: hash, BlockNumber: "0x10"}
	r := chainReceipt{TransactionHash: hash, BlockHash: hash, BlockNumber: "0x10", Status: "0x1"}
	b := chainBlock{Hash: hash, Number: "0x10", Timestamp: fmt.Sprintf("0x%x", now.Unix())}
	return ch, o, tx, r, b
}
func TestPaymentValidation(t *testing.T) {
	ch, o, tx, r, b := transferFixture()
	if e := validateTransfer(ch, o, tx, r, b); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		change func(*Order, *chainTx, *chainReceipt, *chainBlock)
	}{{"wrong sender", func(_ *Order, t *chainTx, _ *chainReceipt, _ *chainBlock) { t.From = recipient }}, {"wrong recipient", func(_ *Order, t *chainTx, _ *chainReceipt, _ *chainBlock) { t.To = buyer }}, {"wrong amount", func(_ *Order, t *chainTx, _ *chainReceipt, _ *chainBlock) { t.Value = "0x63" }}, {"reverted", func(_ *Order, _ *chainTx, r *chainReceipt, _ *chainBlock) { r.Status = "0x0" }}, {"reorg", func(_ *Order, _ *chainTx, _ *chainReceipt, b *chainBlock) { b.Hash = "0x123" }}, {"old payment", func(o *Order, _ *chainTx, _ *chainReceipt, _ *chainBlock) { o.Created = time.Now().Add(time.Hour) }}, {"late payment", func(o *Order, _ *chainTx, _ *chainReceipt, _ *chainBlock) { o.Expires = time.Now().Add(-time.Hour) }}} {
		t.Run(tc.name, func(t *testing.T) {
			a, c, d, e := o, tx, r, b
			tc.change(&a, &c, &d, &e)
			if validateTransfer(ch, a, c, d, e) == nil {
				t.Fatal("invalid transfer accepted")
			}
		})
	}
	o.Asset = "USDC"
	tx.To = ch.USDC
	tx.Value = "0x0"
	r.Logs = []chainLog{{Address: ch.USDC, Data: "0x64", Topics: []string{crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)")).Hex(), common.BytesToHash(common.HexToAddress(buyer).Bytes()).Hex(), common.BytesToHash(common.HexToAddress(recipient).Bytes()).Hex()}}}
	if e := validateTransfer(ch, o, tx, r, b); e != nil {
		t.Fatal(e)
	}
	r.Logs[0].Address = buyer
	if validateTransfer(ch, o, tx, r, b) == nil {
		t.Fatal("counterfeit USDC accepted")
	}
	r.Logs[0].Address = ch.USDC
	r.Logs[0].Removed = true
	if validateTransfer(ch, o, tx, r, b) == nil {
		t.Fatal("removed log accepted")
	}
}
func TestFinalityAndReplayConsumption(t *testing.T) {
	s := testServer(t)
	ch, _, tx, receipt, block := transferFixture()
	finalized := false
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params []json.RawMessage
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "eth_chainId":
			result = "0x2105"
		case "eth_getTransactionByHash":
			result = tx
		case "eth_getTransactionReceipt":
			result = receipt
		case "eth_getBlockByNumber":
			if string(req.Params[0]) == `"finalized"` {
				number := "0xf"
				if finalized {
					number = "0x10"
				}
				result = chainBlock{Number: number}
			} else {
				result = block
			}
		default:
			t.Errorf("unexpected RPC: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	}))
	defer rpc.Close()
	ch.RPC = rpc.URL
	s.cfg.Chains[8453] = ch
	o := create(t, s, "~sampel-palnet")
	o.Asset = "ETH"
	o.Amount = "100"
	o.Created = time.Now().Add(-time.Minute)
	o.TxHash = hash
	o.Status = "confirming"
	_ = s.save(o)
	s.checkPayment(context.Background(), o)
	if readOrder(t, s, o.ID).Status != "confirming" {
		t.Fatal("unfinalized payment accepted")
	}
	finalized = true
	s.checkPayment(context.Background(), o)
	if readOrder(t, s, o.ID).Status != "paid" {
		t.Fatal("finalized payment not accepted")
	}
	other := o
	other.ID = token()
	other.Planet = "~ravmel-ropdyl"
	_ = s.save(other)
	s.checkPayment(context.Background(), other)
	if readOrder(t, s, other.ID).Status != "review_payment" {
		t.Fatal("payment replay accepted")
	}
}
func TestAmbiguousSpawnIsNotResubmitted(t *testing.T) {
	s := testServer(t)
	calls := 0
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer rpc.Close()
	old := roller.Client
	roller.Client = roller.New(roller.Config{Endpoint: rpc.URL})
	defer func() { roller.Client = old }()
	o := create(t, s, "~sampel-palnet")
	o.Status = "prepared"
	o.Secret, _ = encrypt(s.cfg.EncryptionKey, o.ID, types.PlanetCode{Planet: o.Planet, Code: "~doznec-binwes-samper-siglet-sampel-palnet", Owner: "0x5EfED9f04129342e83c92dd8eB26332b05afb989"})
	_ = s.save(o)
	s.fulfill(context.Background(), o)
	saved := readOrder(t, s, o.ID)
	if saved.Status != "spawn_submitted" || saved.Secret == "" {
		t.Fatal("intent and credentials not durable")
	}
	first := calls
	s.fulfill(context.Background(), saved)
	if calls != first+1 {
		t.Fatal("expected only reconciliation read")
	}
	if readOrder(t, s, o.ID).Status != "spawn_submitted" {
		t.Fatal("uncertain spawn became retryable")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSendGridDelivery(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	o.Status = "delivered"
	o.Secret, _ = encrypt(s.cfg.EncryptionKey, o.ID, types.PlanetCode{Planet: o.Planet, Code: "private-planet-code", URL: "https://bridge.urbit.org/#private-planet-code"})
	_ = s.save(o)
	s.cfg.SendGridKey = "test-api-key"
	s.cfg.EmailFrom = "store@example.com"
	s.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.sendgrid.com/v3/mail/send" || r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Fatal("bad SendGrid request")
		}
		b, _ := io.ReadAll(r.Body)
		var data map[string]any
		if e := json.Unmarshal(b, &data); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(b), "private-planet-code") || !strings.Contains(string(b), "https://bridge.urbit.org/#private-planet-code") || data["attachments"] != nil || strings.Contains(string(b), "Master ticket (ownership secret)") {
			t.Fatal("incorrect planet code email")
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	s.email(context.Background(), o)
	if !readOrder(t, s, o.ID).EmailSent {
		t.Fatal("delivery not recorded")
	}
}
func TestDatabaseBinding(t *testing.T) {
	s := testServer(t)
	s.cfg.Demo = true
	s.cfg.EncryptionKey = bytes.Repeat([]byte{2}, 32)
	if s.bindDatabase() == nil {
		t.Fatal("wrong encryption key accepted")
	}
	s.cfg.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	s.cfg.Star = "~sampel"
	if s.bindDatabase() == nil {
		t.Fatal("wrong star accepted")
	}
	s.cfg.Star = "~marzod"
	s.cfg.Demo = false
	if s.bindDatabase() == nil {
		t.Fatal("preview database accepted in production")
	}
}

func TestPlanetCodeFulfillment(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	o.Status = "paid"
	if e := s.save(o); e != nil {
		t.Fatal(e)
	}
	_, point, e := types.ValidateAndNormalizePatp(o.Planet)
	if e != nil {
		t.Fatal(e)
	}
	s.cfg.Star, e = co.Point2Patp(new(big.Int).SetUint64(uint64(point & 65535)))
	if e != nil {
		t.Fatal(e)
	}
	s.cfg.SpawnKey = strings.Repeat("1", 64)
	key, _ := crypto.HexToECDSA(s.cfg.SpawnKey)
	var parent, child types.Point
	parent.Dominion = "l2"
	parent.Ownership.SpawnProxy.Address = crypto.PubkeyToAddress(key.PublicKey).Hex()
	child.Dominion = "l2"
	var mu sync.Mutex
	submitted := map[string]int{}
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req struct {
			Method string
			ID     string
			Params json.RawMessage
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
			return
		}
		var result any
		switch req.Method {
		case "getPoint":
			var p struct{ Ship string }
			_ = json.Unmarshal(req.Params, &p)
			if p.Ship == s.cfg.Star {
				result = parent
			} else {
				result = child
			}
		case "getNonce":
			result = 0
		case "getUnsignedTx":
			result = "0x" + strings.Repeat("0", 64)
		case "spawn":
			var p types.SpawnRequest
			_ = json.Unmarshal(req.Params, &p)
			saved := readOrder(t, s, o.ID)
			if saved.Secret == "" || saved.Status != "spawn_submitted" {
				t.Error("spawn attempted without durable planet code and intent")
			}
			child.Ownership.TransferProxy.Address = p.Data.Address
			submitted[req.Method]++
			result = "spawn-tx"
		case "transferPoint":
			var p types.TransferRequest
			_ = json.Unmarshal(req.Params, &p)
			child.Ownership.Owner.Address = p.Data.Address
			child.Ownership.TransferProxy.Address = ""
			submitted[req.Method]++
			result = "accept-tx"
		default:
			t.Errorf("unexpected roller method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer rpc.Close()
	old := roller.Client
	roller.Client = roller.New(roller.Config{Endpoint: rpc.URL})
	defer func() { roller.Client = old }()
	for i := 0; i < 4; i++ {
		s.fulfill(context.Background(), readOrder(t, s, o.ID))
	}
	done := readOrder(t, s, o.ID)
	if done.Status != "delivered" {
		t.Fatalf("fulfillment stopped at %s: %s", done.Status, done.Error)
	}
	c, e := decrypt(s.cfg.EncryptionKey, o.ID, done.Secret)
	if e != nil || c.Code == "" || !strings.HasPrefix(c.URL, "https://bridge.urbit.org/#") {
		t.Fatal("missing planet code", e)
	}
	if strings.EqualFold(c.Owner, buyer) || !strings.EqualFold(child.Ownership.Owner.Address, c.Owner) {
		t.Fatal("planet ownership must belong to the generated ticket")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, method := range []string{"spawn", "transferPoint"} {
		if submitted[method] != 1 {
			t.Errorf("%s submitted %d times", method, submitted[method])
		}
	}
}

func TestOfflineRecovery(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	o.Status = "spawn_submitted"
	o.Secret, _ = encrypt(s.cfg.EncryptionKey, o.ID, types.PlanetCode{Code: "recovery-planet-code"})
	if e := s.save(o); e != nil {
		t.Fatal(e)
	}
	c := s.cfg
	c.DBPath = filepath.Join(t.TempDir(), "backup.db")
	if e := s.db.View(func(tx *bolt.Tx) error { return tx.CopyFile(c.DBPath, 0600) }); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "recovery.json")
	if e := Recover(c, o.ID, path); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(path)
	if e != nil || !bytes.Contains(data, []byte("recovery-planet-code")) {
		t.Fatal("missing recovery ticket", e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe recovery permissions", e)
	}
	if Recover(c, o.ID, path) == nil {
		t.Fatal("overwrote recovery file")
	}
}

func TestAdminWalletSignatureAuthorization(t *testing.T) {
	s := testServer(t)
	key, _ := crypto.GenerateKey()
	admin := crypto.PubkeyToAddress(key.PublicKey).Hex()
	s.cfg.AdminWallet = admin
	signIn := func(adminIntent bool) (*httptest.ResponseRecorder, string) {
		t.Helper()
		w := request(s, "POST", "/api/auth/challenge", fmt.Sprintf(`{"address":%q,"admin":%t}`, strings.ToLower(admin), adminIntent), "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var c map[string]string
		json.Unmarshal(w.Body.Bytes(), &c)
		if adminIntent && !strings.Contains(c["message"], "administer this store") {
			t.Fatal("admin privilege absent from signed message")
		}
		sig, _ := crypto.Sign(accounts.TextHash([]byte(c["message"])), key)
		body := fmt.Sprintf(`{"challenge":%q,"signature":"0x%x"}`, c["id"], sig)
		return request(s, "POST", "/api/auth/login", body, ""), body
	}
	w, _ := signIn(false)
	var buyerSession struct{ Admin bool }
	json.Unmarshal(w.Body.Bytes(), &buyerSession)
	if buyerSession.Admin {
		t.Fatal("buyer signature grants admin access")
	}
	if request(s, "POST", "/api/auth/challenge", fmt.Sprintf(`{"address":%q,"admin":true}`, buyer), "").Code != 403 {
		t.Fatal("other wallet offered admin challenge")
	}
	w, body := signIn(true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var granted struct {
		Admin  bool
		Wallet string
	}
	json.Unmarshal(w.Body.Bytes(), &granted)
	if !granted.Admin || granted.Wallet != strings.ToLower(admin) {
		t.Fatal("signed admin wallet not authorized")
	}
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", s.cfg.Origin+"/api/admin", nil)
	r.AddCookie(cookie)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, r)
	if response.Code != 200 {
		t.Fatal("admin session cannot read settings", response.Code)
	}
	if request(s, "POST", "/api/auth/login", body, "").Code != 401 {
		t.Fatal("admin signature replay accepted")
	}
	// A valid signature from another key cannot answer the admin wallet's challenge.
	w = request(s, "POST", "/api/auth/challenge", fmt.Sprintf(`{"address":%q,"admin":true}`, admin), "")
	var c map[string]string
	json.Unmarshal(w.Body.Bytes(), &c)
	other, _ := crypto.GenerateKey()
	bad, _ := crypto.Sign(accounts.TextHash([]byte(c["message"])), other)
	if request(s, "POST", "/api/auth/login", fmt.Sprintf(`{"challenge":%q,"signature":"0x%x"}`, c["id"], bad), "").Code != 401 {
		t.Fatal("wrong signer granted access")
	}
}
