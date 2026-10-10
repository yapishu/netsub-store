package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	nockchain "github.com/yapishu/nockchain-go"
	bolt "go.etcd.io/bbolt"
)

type nockFixture struct {
	mu                                                       sync.Mutex
	height                                                   uint64
	transactions                                             []map[string]any
	blocks                                                   map[string]nockBlock
	wrongGenesis, stale, wrongLookup, reorg, omitTx, offline bool
}

func nockDigest(n uint64) string { d, _ := nockchain.HashWords([]uint64{n}); return d.String() }

func nockTestServer(t *testing.T) (*Server, *nockFixture) {
	t.Helper()
	s := testServer(t)
	f := &nockFixture{height: 500, transactions: []map[string]any{}, blocks: map[string]nockBlock{}}
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("x-api-key") != "synthetic-api-key" || r.Header.Get("User-Agent") == "" {
			t.Error("missing Nockblocks authentication or user-agent")
		}
		if f.offline {
			w.WriteHeader(503)
			return
		}
		var req struct {
			ID, Method string
			Params     []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var result any
		var price any
		switch req.Method {
		case "getTip":
			if r.URL.Path == "/rpc" {
				result = map[string]any{"height": f.height}
				price = json.Number("0.025")
			} else {
				stamp := time.Now().Unix()
				if f.stale {
					stamp -= 3 * 3600
				}
				result = nockBlock{Digest: nockDigest(500), Height: f.height, Timestamp: stamp}
			}
		case "getTransactionsByAddress":
			var in struct {
				Address       string
				Limit, Offset int
			}
			json.Unmarshal(req.Params[0], &in)
			if in.Limit != 100 {
				t.Error("pagination limit")
			}
			end := in.Offset + in.Limit
			if end > len(f.transactions) {
				end = len(f.transactions)
			}
			lookup := in.Address
			if f.wrongLookup {
				lookup = nockDigest(88)
			}
			result = map[string]any{"transactions": f.transactions[in.Offset:end], "totalTransactions": len(f.transactions), "lookup": map[string]any{"value": lookup}}
		case "getBlocksByHeight":
			var in struct{ Heights []uint64 }
			json.Unmarshal(req.Params[0], &in)
			blocks := []nockBlock{}
			for _, h := range in.Heights {
				if h == 0 {
					genesis := nockGenesis
					if f.wrongGenesis {
						genesis = nockDigest(89)
					}
					blocks = append(blocks, nockBlock{Digest: genesis})
				} else {
					for _, b := range f.blocks {
						if b.Height == h {
							if f.reorg {
								b.Digest = nockDigest(90)
							}
							blocks = append(blocks, b)
						}
					}
				}
			}
			result = blocks
		case "getBlocksById":
			var in struct{ BlockIDs []string }
			json.Unmarshal(req.Params[0], &in)
			blocks := []nockBlock{}
			for _, id := range in.BlockIDs {
				b := f.blocks[id]
				if f.omitTx {
					b.TxIDs = nil
				}
				blocks = append(blocks, b)
			}
			result = blocks
		default:
			t.Error("unexpected Nockblocks method", req.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result, "price": price})
	}))
	t.Cleanup(rpc.Close)
	k, err := nockchain.Master(make([]byte, 64))
	if err != nil {
		t.Fatal(err)
	}
	zpub, _ := k.Zpub()
	s.cfg.Nock = NockConfig{BaseEnabled: true, Zpub: zpub, RPC: rpc.URL + "/rpc/v1", PriceRPC: rpc.URL + "/rpc", APIKey: "synthetic-api-key", Confirmations: 2}
	s.nock, err = newNock(s.cfg.Nock)
	if s.nock != nil {
		s.nock.requestInterval = 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}
func createNock(t *testing.T, s *Server, asset string) Order {
	t.Helper()
	chain := 0
	if asset == "NOCK_BASE" {
		chain = 8453
	}
	w := request(s, "POST", "/api/orders", fmt.Sprintf(`{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":%d,"asset":%q}`, chain, asset), "buyer")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "zpub") || strings.Contains(w.Body.String(), "NockKeyID") || strings.Contains(w.Body.String(), "NockLockRoot") {
		t.Fatal("internal receiving metadata exposed")
	}
	var o Order
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	return readOrder(t, s, o.ID)
}
func (f *nockFixture) payment(o Order, index uint64, nicks uint64, height uint64, stamp time.Time, lock string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := nockDigest(1000 + index)
	blockID := nockDigest(2000 + index)
	tx := map[string]any{"id": id, "blockId": blockID, "version": 1, "outputs": []any{map[string]any{"firstName": nockDigest(3000 + index), "lastName": nockDigest(4000 + index), "seeds": []any{map[string]any{"lockRoot": lock, "gift": nicks, "isCoinbase": false}}}}}
	f.transactions = append(f.transactions, tx)
	f.blocks[blockID] = nockBlock{Digest: blockID, Height: height, Timestamp: stamp.Unix(), TxIDs: []string{id}}
}

func TestNockQuotes(t *testing.T) {
	for _, tc := range []struct {
		usd, rate string
		base      bool
		want      string
	}{{"10", "0.025", false, "26214400"}, {"10", "0.03", false, "21845334"}, {"10", "0.03", true, "3333333333333333334"}, {"5", "0.025", true, "2000000000000000000"}} {
		got, err := nockQuote(tc.usd, tc.rate, tc.base)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	for _, rate := range []string{"0", "-1", "NaN", "1e-1000000000", "1e999999", "1/2", "0x10"} {
		if _, err := nockQuote("10", rate, false); err == nil {
			t.Errorf("invalid rate %q", rate)
		}
	}
	if got := displayPrice("1", "NOCK"); got != "0.0000152587890625" {
		t.Fatal(got)
	}
	if got := displayPrice("1", "NOCK_BASE"); got != "0.0000000000000001" {
		t.Fatal(got)
	}
}

func TestNockAddressPersistenceAndConfirmation(t *testing.T) {
	s, f := nockTestServer(t)
	o := createNock(t, s, "NOCK")
	if o.Amount != "26214400" || o.NockRate != "0.025" || o.NockRequired != 2 || o.NockLockRoot == "" || o.NockKeyID == "" || o.NockIndex != 0 {
		t.Fatal(o)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		second := Order{ID: "second"}
		if err := s.nock.allocate(tx, &second); err != nil {
			return err
		}
		if second.Recipient == o.Recipient || second.NockIndex != 1 {
			t.Fatal("address/index reuse")
		}
		return putOrder(tx, second)
	}); err != nil {
		t.Fatal(err)
	}
	n, err := newNock(s.cfg.Nock)
	if err != nil {
		t.Fatal(err)
	}
	n.requestInterval = 0
	s.nock = n
	if err = s.db.Update(func(tx *bolt.Tx) error {
		third := Order{ID: "third"}
		if err := s.nock.allocate(tx, &third); err != nil {
			return err
		}
		if third.NockIndex != 2 {
			t.Fatal("index not durable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.payment(o, 1, 10000000, 498, o.Created, o.NockLockRoot)
	f.payment(o, 2, 16214400, 500, o.Created, o.NockLockRoot)
	s.checkNockPayment(context.Background(), o)
	pending := readOrder(t, s, o.ID)
	if pending.Status != "confirming" || pending.NockReceived != o.Amount || pending.NockConfirmations != 1 || !pending.PaidAt.IsZero() {
		t.Fatal(pending)
	}
	f.mu.Lock()
	f.height++
	f.mu.Unlock()
	s.checkNockPayment(context.Background(), pending)
	paid := readOrder(t, s, o.ID)
	if paid.Status != "paid" || paid.PaidAt.IsZero() || len(paid.NockTxIDs) != 2 || paid.ReceiptNext.IsZero() {
		t.Fatal(paid)
	}
	replay := o
	replay.ID = "replay"
	s.save(replay)
	s.checkNockPayment(context.Background(), replay)
	if readOrder(t, s, replay.ID).Status != "review_payment" {
		t.Fatal("output replay accepted")
	}
	w := request(s, "POST", "/api/orders/"+o.ID+"/payment", `{"hash":"`+hash+`"}`, "buyer")
	if w.Code != 409 {
		t.Fatal("manual EVM claim accepted", w.Code)
	}
}

func TestNockPaymentFailureModes(t *testing.T) {
	for _, name := range []string{"wrong-lock", "late", "underpaid", "wrong-genesis", "stale", "wrong-lookup", "reorg", "missing-membership", "offline", "mismatched-lock", "zero-confirmations"} {
		t.Run(name, func(t *testing.T) {
			s, f := nockTestServer(t)
			o := createNock(t, s, "NOCK")
			lock := o.NockLockRoot
			at := o.Created
			amount := uint64(26214400)
			if name == "wrong-lock" {
				lock = nockDigest(99)
			}
			if name == "late" {
				at = o.Expires.Add(time.Second)
			}
			if name == "underpaid" {
				amount = 1
				o.Expires = time.Now().Add(-time.Second)
				o.Created = o.Expires.Add(-time.Minute)
				at = o.Created
				s.save(o)
			}
			f.payment(o, 1, amount, 498, at, lock)
			switch name {
			case "wrong-genesis":
				f.wrongGenesis = true
			case "stale":
				f.stale = true
			case "wrong-lookup":
				f.wrongLookup = true
			case "reorg":
				f.reorg = true
			case "missing-membership":
				f.omitTx = true
			case "offline":
				f.offline = true
			case "zero-confirmations":
				o.NockRequired = 0
			case "mismatched-lock":
				o.NockLockRoot = nockDigest(99)
			}
			s.checkNockPayment(context.Background(), o)
			got := readOrder(t, s, o.ID)
			if got.Status == "paid" || !got.PaidAt.IsZero() {
				t.Fatal("invalid payment accepted", got)
			}
			if (name == "late" || name == "underpaid") && (got.Status != "review_payment" || got.NockReceived == "0") {
				t.Fatal("funds not held for review", got)
			}
		})
	}
}

func TestNockOptionalAndPrivateConfig(t *testing.T) {
	t.Setenv("BASE_NOCK_ENABLED", "false")
	t.Setenv("NOCKCHAIN_ZPUB", "")
	if _, err := nockFromEnv(); err != nil {
		t.Fatal(err)
	}
	s := testServer(t)
	for _, asset := range []string{"NOCK", "NOCK_BASE"} {
		w := request(s, "POST", "/api/orders", fmt.Sprintf(`{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":8453,"asset":%q}`, asset), "buyer")
		if w.Code != 400 {
			t.Fatal("disabled NOCK accepted")
		}
	}
	k, _ := nockchain.Master(make([]byte, 64))
	prv, _ := k.Zprv()
	t.Setenv("NOCKCHAIN_ZPUB", prv)
	t.Setenv("NOCKBLOCKS_API_KEY", "test")
	if _, err := nockFromEnv(); err == nil {
		t.Fatal("private receiving key accepted")
	}
	pub, _ := k.Zpub()
	t.Setenv("NOCKCHAIN_ZPUB", pub)
	if _, err := nockFromEnv(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOCKCHAIN_CONFIRMATIONS", "1")
	if _, err := nockFromEnv(); err == nil {
		t.Fatal("single confirmation accepted")
	}
}

func TestBaseNockDirectTransfer(t *testing.T) {
	s, _ := nockTestServer(t)
	o := createNock(t, s, "NOCK_BASE")
	if o.Amount != "4000000000000000000" || o.ChainID != 8453 || o.NockRate != "0.025" {
		t.Fatal(o)
	}
	o.TxHash = hash
	blockHash := "0x" + strings.Repeat("b", 64)
	tx := chainTx{Hash: hash, From: o.Wallet, To: baseNockToken, BlockHash: blockHash, BlockNumber: "0x10"}
	receipt := chainReceipt{TransactionHash: hash, BlockHash: blockHash, BlockNumber: "0x10", Status: "0x1", Logs: []chainLog{{Address: baseNockToken, Topics: []string{crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)")).Hex(), common.BytesToHash(common.HexToAddress(o.Wallet).Bytes()).Hex(), common.BytesToHash(common.HexToAddress(o.Recipient).Bytes()).Hex()}, Data: "0x3782dace9d900000"}}}
	block := chainBlock{Hash: blockHash, Number: "0x10", Timestamp: fmt.Sprintf("0x%x", o.Created.Unix())}
	ch := s.cfg.Chains[8453]
	if err := validateTransfer(ch, o, tx, receipt, block); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wrong-contract", "wrong-emitter", "wrong-sender", "wrong-recipient", "wrong-amount", "wrong-chain", "expired"} {
		t.Run(name, func(t *testing.T) {
			tr := tx
			r := receipt
			r.Logs = append([]chainLog{}, receipt.Logs...)
			r.Logs[0].Topics = append([]string{}, r.Logs[0].Topics...)
			b := block
			c := ch
			switch name {
			case "wrong-contract":
				tr.To = c.USDC
			case "wrong-emitter":
				r.Logs[0].Address = c.USDC
			case "wrong-sender":
				tr.From = o.Recipient
			case "wrong-recipient":
				r.Logs[0].Topics[2] = r.Logs[0].Topics[1]
			case "wrong-amount":
				r.Logs[0].Data = "0x1"
			case "wrong-chain":
				c.ID = 1
			case "expired":
				b.Timestamp = fmt.Sprintf("0x%x", o.Expires.Add(time.Second).Unix())
			}
			if err := validateTransfer(c, o, tr, r, b); err == nil {
				t.Fatal("invalid Base NOCK accepted")
			}
		})
	}
}

func TestNockPublicConfigAndNetworkValidation(t *testing.T) {
	s, _ := nockTestServer(t)
	w := request(s, "GET", "/api/config", "", "")
	for _, secret := range []string{s.cfg.Nock.Zpub, s.cfg.Nock.APIKey, s.cfg.Nock.RPC} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("private configuration exposed")
		}
	}
	for _, tc := range []struct {
		asset string
		chain int
	}{{"NOCK", 8453}, {"NOCK_BASE", 1}, {"NOCK_BASE", 0}} {
		w = request(s, "POST", "/api/orders", fmt.Sprintf(`{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":%d,"asset":%q}`, tc.chain, tc.asset), "buyer")
		if w.Code != 400 {
			t.Fatal("wrong network accepted", tc, w.Code)
		}
	}
}
