package store

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icholy/digest"
	bolt "go.etcd.io/bbolt"
)

type moneroFixture struct {
	mu                                                        sync.Mutex
	addresses                                                 []moneroAddress
	transfers                                                 []moneroTransfer
	network                                                   string
	syncing, walletBehind, loseAddress, stored, changedWallet bool
	calls                                                     map[string]int
}

func moneroTestServer(t *testing.T) (*Server, *moneroFixture) {
	t.Helper()
	s := testServer(t)
	f := &moneroFixture{network: "stagenet", calls: map[string]int{}}
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json_rpc" {
			t.Error("unexpected RPC path")
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="netsub-test", nonce="fixed-test-nonce", qop="auth", algorithm=MD5`)
			w.WriteHeader(401)
			return
		}
		c, err := digest.ParseCredentials(r.Header.Get("Authorization"))
		hash := func(v string) string { sum := md5.Sum([]byte(v)); return hex.EncodeToString(sum[:]) }
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		expected := hash(hash("test-user:netsub-test:test-password") + ":" + c.Nonce + ":" + fmt.Sprintf("%08x", c.Nc) + ":" + c.Cnonce + ":auth:" + hash("POST:/json_rpc"))
		if c.Username != "test-user" || c.Response != expected {
			t.Error("invalid digest authentication")
			w.WriteHeader(401)
			return
		}
		var req struct {
			ID, Method string
			Params     struct {
				Account        uint32   `json:"account_index"`
				Indices        []uint32 `json:"address_index"`
				Subindices     []uint32 `json:"subaddr_indices"`
				Label, Address string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls[req.Method]++
		var result any
		switch req.Method {
		case "get_info":
			result = map[string]any{"nettype": f.network, "status": "OK", "height": 500, "target_height": 500, "synchronized": !f.syncing, "offline": false, "untrusted": false}
		case "get_height":
			height := 500
			if f.walletBehind {
				height = 400
			}
			result = map[string]any{"height": height}
		case "get_address":
			list := []moneroAddress{}
			for _, a := range f.addresses {
				if len(req.Params.Indices) == 0 || a.Index == req.Params.Indices[0] {
					if f.changedWallet {
						a.Address = "other-wallet"
					}
					list = append(list, a)
				}
			}
			result = map[string]any{"addresses": list}
		case "create_address":
			a := moneroAddress{Address: "7" + strings.Repeat("A", 92) + fmt.Sprintf("%02d", len(f.addresses)+11), Index: uint32(len(f.addresses) + 1), Label: req.Params.Label}
			f.addresses = append(f.addresses, a)
			result = a
			if f.loseAddress {
				f.loseAddress = false
				w.WriteHeader(503)
				return
			}
		case "validate_address":
			result = map[string]any{"valid": true, "integrated": false, "subaddress": true, "nettype": f.network}
		case "store":
			f.stored = true
			result = map[string]any{}
		case "get_transfers":
			if len(req.Params.Subindices) != 1 {
				t.Error("missing subaddress filter")
			}
			incoming, pool := []moneroTransfer{}, []moneroTransfer{}
			for _, tx := range f.transfers {
				if tx.Type == "pool" {
					pool = append(pool, tx)
				} else {
					incoming = append(incoming, tx)
				}
			}
			result = map[string]any{"in": incoming, "pool": pool}
		default:
			t.Error("unexpected Monero method", req.Method)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(rpc.Close)
	cfg := MoneroConfig{WalletRPC: rpc.URL, WalletUser: "test-user", WalletPassword: "test-password", DaemonRPC: rpc.URL, DaemonUser: "test-user", DaemonPassword: "test-password", Network: "stagenet", Confirmations: 10}
	prices := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.kraken.com/0/public/Ticker?pair=XMRUSD" {
			t.Error("unexpected price request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"error":[],"result":{"XXMRZUSD":{"c":["200","1"]}}}`))}, nil
	})}
	s.cfg.Monero = cfg
	s.monero = newMonero(cfg, prices)
	return s, f
}
func createMonero(t *testing.T, s *Server) Order {
	t.Helper()
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":0,"asset":"XMR"}`, "buyer")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var o Order
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	return readOrder(t, s, o.ID)
}
func (f *moneroFixture) payment(o Order, id string, amount uint64, confirmations int, locked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tx := moneroTransfer{Address: o.Recipient, TxID: id, Amount: amount, Confirmations: confirmations, Locked: &locked, Type: "in", Height: 490}
	tx.Index.Major = o.MoneroAccount
	tx.Index.Minor = o.MoneroIndex
	if confirmations == 0 {
		tx.Type = "pool"
		tx.Height = 0
	}
	for i, old := range f.transfers {
		if old.TxID == id {
			f.transfers[i] = tx
			return
		}
	}
	f.transfers = append(f.transfers, tx)
}
func TestMoneroOptionalConfiguration(t *testing.T) {
	t.Setenv("MONERO_WALLET_RPC_URL", "")
	if c, err := moneroFromEnv(); err != nil || c.WalletRPC != "" {
		t.Fatal(c, err)
	}
	s := testServer(t)
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":0,"asset":"XMR"}`, "buyer")
	if w.Code != 400 {
		t.Fatal("disabled Monero checkout accepted")
	}
	t.Setenv("MONERO_WALLET_RPC_URL", "http://127.0.0.1:18082")
	t.Setenv("MONERO_WALLET_RPC_USER", "user")
	t.Setenv("MONERO_WALLET_RPC_PASSWORD", "pass")
	t.Setenv("MONERO_DAEMON_RPC_URL", "http://127.0.0.1:18081")
	if c, err := moneroFromEnv(); err != nil || c.Confirmations != 10 {
		t.Fatal(c, err)
	}
	t.Setenv("MONERO_CONFIRMATIONS", "1")
	if _, err := moneroFromEnv(); err == nil {
		t.Fatal("unsafe confirmation threshold accepted")
	}
}
func TestMoneroQuotePrecision(t *testing.T) {
	for _, c := range []struct{ usd, rate, want string }{{"10", "200", "50000000000"}, {"5", "300", "16666666667"}, {"0.01", "100000000000", "1"}} {
		got, err := moneroQuote(c.usd, c.rate)
		if err != nil || got != c.want {
			t.Fatal(c, got, err)
		}
	}
	for _, rate := range []string{"0", "-1", "NaN", "0.00000000001"} {
		if _, err := moneroQuote("10", rate); err == nil {
			t.Fatal("invalid quote", rate)
		}
	}
	if displayPrice("1000000000001", "XMR") != "1.000000000001" {
		t.Fatal("receipt precision lost")
	}
}
func TestMoneroSubaddressRecovery(t *testing.T) {
	s, f := moneroTestServer(t)
	f.loseAddress = true
	o := createMonero(t, s)
	if o.Status != "awaiting_address" || o.Recipient != "" {
		t.Fatal("ambiguous address exposed")
	}
	o = s.prepareMoneroAddress(context.Background(), o)
	if o.Status != "awaiting_payment" || o.MoneroIndex != 1 || o.Amount != "50000000000" || len(f.addresses) != 1 || !f.stored {
		t.Fatal("subaddress recovery failed", o.Status, o.Error)
	}
	var saved map[string]any
	s.db.View(func(tx *bolt.Tx) error { return json.Unmarshal(tx.Bucket([]byte("orders")).Get([]byte(o.ID)), &saved) })
	if saved["MoneroIndex"] != float64(1) {
		t.Fatal("subaddress index is not durable")
	}
}
func TestMoneroPartialPaymentsWaitForUnlockAndSurviveExpiry(t *testing.T) {
	s, f := moneroTestServer(t)
	o := createMonero(t, s)
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	f.payment(o, a, 20_000_000_000, 0, true)
	f.payment(o, b, 30_000_000_000, 0, true)
	s.checkMoneroPayment(context.Background(), o)
	o = readOrder(t, s, o.ID)
	if o.Status != "confirming" || o.MoneroReceived != "50000000000" || len(o.MoneroSeen) != 2 {
		t.Fatal("pool payment not retained")
	}
	// First observation is retained when confirmations arrive after the quote expires.
	o.Expires = time.Now().Add(-time.Second)
	for id := range o.MoneroSeen {
		o.MoneroSeen[id] = o.Expires.Add(-time.Second)
	}
	s.save(o)
	f.payment(o, a, 20_000_000_000, 10, true)
	f.payment(o, b, 30_000_000_000, 10, false)
	s.checkMoneroPayment(context.Background(), o)
	o = readOrder(t, s, o.ID)
	if o.Status == "paid" {
		t.Fatal("locked funds accepted")
	}
	f.payment(o, a, 20_000_000_000, 11, false)
	s.checkMoneroPayment(context.Background(), o)
	o = readOrder(t, s, o.ID)
	if o.Status != "paid" || len(o.MoneroTxIDs) != 2 || o.PaidAt.IsZero() {
		t.Fatal("unlocked confirmed payment not accepted", o.Status, o.Error)
	}
	var used int
	s.db.View(func(tx *bolt.Tx) error { used = tx.Bucket([]byte("payments")).Stats().KeyN; return nil })
	if used != 2 {
		t.Fatal("payment consumption not durable")
	}
}
func TestMoneroRejectsInvalidPayments(t *testing.T) {
	for _, mode := range []string{"late", "wrong-address", "wrong-index", "wrong-network", "wallet-changed", "wallet-syncing", "node-syncing", "timelock", "double-spend", "locked", "missing-lock", "replay", "underpaid"} {
		t.Run(mode, func(t *testing.T) {
			s, f := moneroTestServer(t)
			o := createMonero(t, s)
			id := strings.Repeat("c", 64)
			f.payment(o, id, 50_000_000_000, 10, false)
			switch mode {
			case "late":
				o.Expires = time.Now().Add(-time.Second)
			case "wrong-address":
				f.transfers[0].Address = "other"
			case "wrong-index":
				f.transfers[0].Index.Minor++
			case "wrong-network":
				f.network = "mainnet"
			case "wallet-changed":
				f.changedWallet = true
			case "wallet-syncing":
				f.walletBehind = true
			case "node-syncing":
				f.syncing = true
			case "timelock":
				f.transfers[0].UnlockTime = 900000000
			case "double-spend":
				f.transfers[0].DoubleSpend = true
			case "locked":
				*f.transfers[0].Locked = true
			case "missing-lock":
				f.transfers[0].Locked = nil
			case "underpaid":
				f.transfers[0].Amount--
			case "replay":
				s.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket([]byte("payments")).Put([]byte("monero:"+o.MoneroNetwork+":"+id+":"+o.Recipient), []byte("different-order"))
				})
			}
			s.save(o)
			s.checkMoneroPayment(context.Background(), o)
			saved := readOrder(t, s, o.ID)
			if saved.Status == "paid" || !saved.PaidAt.IsZero() {
				t.Fatal("invalid payment accepted")
			}
			if mode == "late" || mode == "timelock" || mode == "double-spend" {
				if saved.Status != "review_payment" || saved.MoneroReceived != "50000000000" {
					t.Fatal("unsafe received funds not retained")
				}
			}
		})
	}
}

// Exercise a real view-only wallet RPC on an isolated offline Monero fakechain.
// The proxy only maps fakechain readiness to mainnet-shaped test metadata.
func TestMoneroWalletRPC(t *testing.T) {
	walletURL := os.Getenv("MONERO_TEST_WALLET_RPC_URL")
	if walletURL == "" {
		t.Skip("set MONERO_TEST_WALLET_RPC_URL for the isolated Monero integration")
	}
	daemonURL := os.Getenv("MONERO_TEST_DAEMON_RPC_URL")
	senderURL := os.Getenv("MONERO_TEST_SENDER_RPC_URL")
	cfg := MoneroConfig{WalletRPC: walletURL, WalletUser: "netsub-test", WalletPassword: "regtest-only", DaemonRPC: daemonURL, Network: "mainnet", Confirmations: 10}
	raw := newMonero(cfg, &http.Client{Timeout: 30 * time.Second})
	var info map[string]any
	if err := raw.call(context.Background(), true, "get_info", map[string]any{}, &info); err != nil {
		t.Fatal(err)
	}
	if info["nettype"] != "fakechain" || info["offline"] != true {
		t.Fatal("integration requires an isolated offline fakechain")
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		resp, err := http.Post(daemonURL+"/json_rpc", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			w.WriteHeader(503)
			return
		}
		defer resp.Body.Close()
		var data struct {
			ID      string
			JSONRPC string `json:"jsonrpc"`
			Result  map[string]any
		}
		if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
			t.Error(err)
			return
		}
		if data.Result["nettype"] != "fakechain" {
			t.Error("unexpected daemon request")
			w.WriteHeader(503)
			return
		}
		data.Result["nettype"] = "mainnet"
		data.Result["offline"] = false
		json.NewEncoder(w).Encode(data)
	}))
	defer proxy.Close()
	s := testServer(t)
	cfg.DaemonRPC = proxy.URL
	s.cfg.Monero = cfg
	s.monero = newMonero(cfg, s.http)
	s.monero.rate = "200"
	s.monero.rateAt = time.Now()
	var empty map[string]any
	if err := s.monero.call(context.Background(), false, "refresh", map[string]any{}, &empty); err != nil {
		t.Fatal(err)
	}
	o := createMonero(t, s)
	if o.Status != "awaiting_payment" {
		t.Fatal(o.Status, o.Error)
	}
	senderCfg := cfg
	senderCfg.WalletRPC = senderURL
	sender := newMonero(senderCfg, s.http)
	var sent struct {
		Hash string `json:"tx_hash"`
	}
	if err := sender.call(context.Background(), false, "transfer", map[string]any{"destinations": []any{map[string]any{"address": o.Recipient, "amount": json.Number(o.Amount)}}, "ring_size": 16}, &sent); err != nil {
		t.Fatal(err)
	}
	if err := s.monero.call(context.Background(), false, "refresh", map[string]any{}, &empty); err != nil {
		t.Fatal(err)
	}
	s.checkMoneroPayment(context.Background(), o)
	pending := readOrder(t, s, o.ID)
	if pending.Status != "confirming" || pending.MoneroReceived != o.Amount {
		t.Fatal("Monero pool payment not detected", pending.Status, pending.Error)
	}
	var miner struct{ Address string }
	if err := sender.call(context.Background(), false, "get_address", map[string]any{}, &miner); err != nil {
		t.Fatal(err)
	}
	if err := raw.call(context.Background(), true, "generateblocks", map[string]any{"wallet_address": miner.Address, "amount_of_blocks": 10}, &empty); err != nil {
		t.Fatal(err)
	}
	if err := s.monero.call(context.Background(), false, "refresh", map[string]any{}, &empty); err != nil {
		t.Fatal(err)
	}
	s.checkMoneroPayment(context.Background(), pending)
	paid := readOrder(t, s, o.ID)
	if paid.Status != "paid" || paid.TxHash != sent.Hash || paid.MoneroConfirmations < 10 {
		t.Fatal("Monero confirmed payment not accepted", paid.Status, paid.Error, paid.MoneroConfirmations)
	}
}
