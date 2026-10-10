package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	bolt "go.etcd.io/bbolt"
)

type bitcoinFixture struct {
	mu                  sync.Mutex
	labels              map[string]string
	txs                 map[string]map[string]any
	network             string
	syncing             bool
	allocated           int
	loseAddressResponse bool
}

func bitcoinTestServer(t *testing.T) (*Server, *bitcoinFixture) {
	t.Helper()
	s := testServer(t)
	f := &bitcoinFixture{labels: map[string]string{}, txs: map[string]map[string]any{}, network: "regtest"}
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		user, password, ok := r.BasicAuth()
		if !ok || user != "test-user" || password != "test-password" {
			t.Error("RPC authentication missing")
		}
		if r.URL.Path != "/wallet/netsub-test" {
			t.Error("incorrect wallet path", r.URL.Path)
		}
		var req struct {
			ID     uint64
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var result any
		var rpcError any
		first := ""
		if len(req.Params) > 0 {
			json.Unmarshal(req.Params[0], &first)
		}
		switch req.Method {
		case "getblockchaininfo":
			result = map[string]any{"chain": f.network, "blocks": 500, "headers": 500, "initialblockdownload": f.syncing}
		case "getwalletinfo":
			result = map[string]any{"scanning": false, "private_keys_enabled": false}
		case "getaddressesbylabel":
			if addr := f.labels[first]; addr != "" {
				result = map[string]any{addr: map[string]string{"purpose": "receive"}}
			} else {
				rpcError = map[string]any{"code": -11, "message": "private provider message"}
			}
		case "getnewaddress":
			f.allocated++
			address, _ := btcutil.NewAddressWitnessPubKeyHash(btcutil.Hash160([]byte(first)), &chaincfg.RegressionNetParams)
			f.labels[first] = address.EncodeAddress()
			result = f.labels[first]
			if f.loseAddressResponse {
				f.loseAddressResponse = false
				w.WriteHeader(503)
				return
			}
		case "listreceivedbyaddress":
			var address string
			json.Unmarshal(req.Params[3], &address)
			ids := []string{}
			for id := range f.txs {
				ids = append(ids, id)
			}
			result = []any{map[string]any{"address": address, "txids": ids}}
		case "gettransaction":
			result = f.txs[first]
		default:
			t.Error("unexpected Bitcoin RPC method", req.Method)
			rpcError = map[string]any{"code": -32601}
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result, "error": rpcError})
	}))
	t.Cleanup(rpc.Close)
	cfg := BitcoinConfig{RPC: rpc.URL, User: "test-user", Password: "test-password", Wallet: "netsub-test", Network: "regtest", Confirmations: 6}
	client := &http.Client{Timeout: 15 * time.Second}
	s.cfg.Bitcoin = cfg
	s.bitcoin = &Bitcoin{cfg: cfg, http: client}
	return s, f
}
func createBitcoin(t *testing.T, s *Server) Order {
	t.Helper()
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":0,"asset":"BTC"}`, "buyer")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var o Order
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	return o
}
func (f *bitcoinFixture) payment(o Order, id string, amount json.Number, confirmations int, at time.Time, vout uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txs[id] = map[string]any{"txid": id, "confirmations": confirmations, "timereceived": at.Unix(), "details": []any{map[string]any{"address": o.Recipient, "category": "receive", "amount": amount, "vout": vout, "abandoned": false}}}
}
func TestBitcoinQuotePrecision(t *testing.T) {
	for _, test := range []struct{ usd, rate, want string }{{"10", "100000", "10000"}, {"5", "99999", "5001"}, {"0.01", "1000000000", "1"}} {
		got, err := bitcoinQuote(test.usd, test.rate)
		if err != nil || got != test.want {
			t.Fatal(test, got, err)
		}
	}
	for _, value := range []string{"-1", "0.000000001", "NaN", "21000001"} {
		if _, err := bitcoinSats(json.Number(value)); err == nil {
			t.Fatal("invalid satoshi amount", value)
		}
	}
	if n, err := bitcoinSats("1e-8"); err != nil || n.String() != "1" {
		t.Fatal(n, err)
	}
}
func TestBitcoinOptionalConfiguration(t *testing.T) {
	t.Setenv("BITCOIN_RPC_URL", "")
	if cfg, err := bitcoinFromEnv(); err != nil || cfg.RPC != "" {
		t.Fatal(cfg, err)
	}
	s := testServer(t)
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":0,"asset":"BTC"}`, "buyer")
	if w.Code != 400 {
		t.Fatal("disabled Bitcoin checkout accepted")
	}
	t.Setenv("BITCOIN_RPC_URL", "http://127.0.0.1:8332")
	t.Setenv("BITCOIN_RPC_USER", "u")
	t.Setenv("BITCOIN_RPC_PASSWORD", "p")
	t.Setenv("BITCOIN_CONFIRMATIONS", "0")
	if _, err := bitcoinFromEnv(); err == nil {
		t.Fatal("zero-confirmation configuration accepted")
	}
	t.Setenv("BITCOIN_CONFIRMATIONS", "6")
	if cfg, err := bitcoinFromEnv(); err != nil || cfg.Confirmations != 6 {
		t.Fatal(err)
	}
}
func TestBitcoinCheckoutAndConfirmedOutputs(t *testing.T) {
	s, f := bitcoinTestServer(t)
	o := createBitcoin(t, s)
	if o.Status != "awaiting_payment" || o.Recipient == "" || o.Amount != "10000" || o.BitcoinRate != "100000" || o.BitcoinRequired != 6 {
		t.Fatal(o)
	}
	id1, id2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	f.payment(o, id1, "0.00004", 6, o.Created.Add(time.Second), 0)
	f.payment(o, id2, "0.00006", 1, o.Created.Add(time.Second), 1)
	s.checkBitcoinPayment(context.Background(), o)
	pending := readOrder(t, s, o.ID)
	if pending.Status != "confirming" || pending.BitcoinReceived != "10000" || pending.BitcoinConfirmations != 1 || !pending.PaidAt.IsZero() {
		t.Fatal(pending)
	}
	f.payment(o, id2, "0.00006", 6, o.Created.Add(time.Second), 1)
	s.checkBitcoinPayment(context.Background(), pending)
	paid := readOrder(t, s, o.ID)
	if paid.Status != "paid" || paid.PaidAt.IsZero() || len(paid.BitcoinTxIDs) != 2 || paid.ReceiptNext.IsZero() {
		t.Fatal(paid)
	}
	replay := o
	replay.ID = "another-order"
	s.save(replay)
	s.checkBitcoinPayment(context.Background(), replay)
	if readOrder(t, s, replay.ID).Status != "review_payment" {
		t.Fatal("Bitcoin output replay accepted")
	}
	w := request(s, "POST", "/api/orders/"+o.ID+"/payment", `{"hash":"`+hash+`"}`, "buyer")
	if w.Code != 409 {
		t.Fatal("manual transaction claim accepted for Bitcoin")
	}
}
func TestBitcoinAddressRecoveryAndRestart(t *testing.T) {
	s, f := bitcoinTestServer(t)
	f.loseAddressResponse = true
	o := createBitcoin(t, s)
	if o.Status != "awaiting_address" || o.Recipient != "" {
		t.Fatal(o)
	}
	path := s.cfg.DBPath
	s.db.Close()
	var err error
	s.db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	o = s.prepareBitcoinAddress(context.Background(), readOrder(t, s, o.ID))
	if o.Status != "awaiting_payment" || o.Recipient == "" || f.allocated != 1 {
		t.Fatal("address allocation repeated", o, f.allocated)
	}
	if saved := readOrder(t, s, o.ID); saved.Recipient != o.Recipient {
		t.Fatal("address not durable")
	}
}
func TestBitcoinRejectsLateConflictedAndWrongNetwork(t *testing.T) {
	for _, mode := range []string{"late", "conflicted", "wrong-network", "syncing", "underpaid"} {
		t.Run(mode, func(t *testing.T) {
			s, f := bitcoinTestServer(t)
			o := createBitcoin(t, s)
			at := o.Created.Add(time.Second)
			confirmations := 6
			amount := json.Number("0.0001")
			if mode == "late" {
				at = o.Expires.Add(time.Second)
				o.Expires = time.Now().Add(-time.Second)
			}
			if mode == "conflicted" {
				confirmations = -1
			}
			if mode == "wrong-network" {
				f.network = "main"
			}
			if mode == "syncing" {
				f.syncing = true
			}
			if mode == "underpaid" {
				amount = "0.00009"
				at = o.Created
				o.Expires = time.Now().Add(-time.Second)
			}
			f.payment(o, strings.Repeat("c", 64), amount, confirmations, at, 0)
			s.save(o)
			s.checkBitcoinPayment(context.Background(), o)
			saved := readOrder(t, s, o.ID)
			if saved.Status == "paid" || !saved.PaidAt.IsZero() {
				t.Fatal("invalid Bitcoin payment accepted")
			}
			if (mode == "late" || mode == "underpaid") && saved.Status != "review_payment" {
				t.Fatal(saved.Status)
			}
		})
	}
}

// Run against an isolated, funded regtest wallet; no public Bitcoin network is used.
func TestBitcoinCoreRegtest(t *testing.T) {
	endpoint := os.Getenv("BITCOIN_TEST_RPC_URL")
	if endpoint == "" {
		t.Skip("set BITCOIN_TEST_RPC_URL for Bitcoin Core integration")
	}
	s := testServer(t)
	cfg := BitcoinConfig{RPC: endpoint, User: "netsub-test", Password: "regtest-only", Wallet: "netsub-store", Network: "regtest", Confirmations: 2}
	s.cfg.Bitcoin = cfg
	s.bitcoin = &Bitcoin{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
	o := createBitcoin(t, s)
	minerCfg := cfg
	minerCfg.Wallet = "miner"
	miner := &Bitcoin{cfg: minerCfg, http: s.bitcoin.http}
	var txid string
	if err := miner.call(context.Background(), "sendtoaddress", []any{o.Recipient, json.Number("0.0001")}, &txid); err != nil {
		t.Fatal(err)
	}
	s.checkBitcoinPayment(context.Background(), o)
	pending := readOrder(t, s, o.ID)
	if pending.Status != "confirming" || pending.BitcoinReceived != "10000" {
		t.Fatal("mempool payment not detected", pending.Status, pending.Error)
	}
	var miningAddress string
	if err := miner.call(context.Background(), "getnewaddress", []any{}, &miningAddress); err != nil {
		t.Fatal(err)
	}
	var blocks []string
	if err := miner.call(context.Background(), "generatetoaddress", []any{2, miningAddress}, &blocks); err != nil {
		t.Fatal(err)
	}
	s.checkBitcoinPayment(context.Background(), pending)
	paid := readOrder(t, s, o.ID)
	if paid.Status != "paid" || paid.TxHash != txid {
		t.Fatal("confirmed Core payment not accepted", paid.Status, paid.Error)
	}
	var used int
	s.db.View(func(tx *bolt.Tx) error { used = tx.Bucket([]byte("payments")).Stats().KeyN; return nil })
	if used != 1 {
		t.Fatal(fmt.Sprintf("expected one consumed output, got %d", used))
	}
}
