package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	nockchain "github.com/yapishu/nockchain-go"
	bolt "go.etcd.io/bbolt"
)

const baseNockToken = "0x9b5e262cf9bb04869ab40b19af91d2dc85761722"
const nockGenesis = "7pR2bvzoMvfFcxXaHv4ERm8AgEnExcZLuEsjNgLkJziBkqBLidLg39Y"

type NockConfig struct {
	BaseEnabled                 bool
	Zpub, RPC, PriceRPC, APIKey string
	Confirmations               int
}

func nockFromEnv() (NockConfig, error) {
	c := NockConfig{Zpub: os.Getenv("NOCKCHAIN_ZPUB"), RPC: env("NOCKBLOCKS_RPC_V1_URL", "https://nockblocks.com/rpc/v1"), PriceRPC: env("NOCKBLOCKS_RPC_URL", "https://nockblocks.com/rpc"), APIKey: os.Getenv("NOCKBLOCKS_API_KEY")}
	var err error
	c.BaseEnabled, err = strconv.ParseBool(env("BASE_NOCK_ENABLED", "false"))
	if err != nil {
		return c, errors.New("BASE_NOCK_ENABLED must be true or false")
	}
	if !c.BaseEnabled && c.Zpub == "" {
		return c, nil
	}
	for name, endpoint := range map[string]string{"NOCKBLOCKS_RPC_V1_URL": c.RPC, "NOCKBLOCKS_RPC_URL": c.PriceRPC} {
		u, e := url.Parse(endpoint)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("%s must be an HTTP(S) URL without credentials or query parameters", name)
		}
	}
	if c.APIKey == "" {
		return c, errors.New("NOCKBLOCKS_API_KEY is required for NOCK payments")
	}
	if c.Zpub != "" {
		if _, err = nockchain.ParsePublicKey(c.Zpub); err != nil {
			return c, errors.New("NOCKCHAIN_ZPUB must be a valid V1 public extended key")
		}
	}
	c.Confirmations, err = strconv.Atoi(env("NOCKCHAIN_CONFIRMATIONS", "2"))
	if err != nil || c.Confirmations < 2 || c.Confirmations > 144 {
		return c, errors.New("NOCKCHAIN_CONFIRMATIONS must be between 2 and 144")
	}
	return c, nil
}

type Nock struct {
	cfg             NockConfig
	http            *http.Client
	account         nockchain.ExtendedKey
	id              atomic.Uint64
	rateMu          sync.Mutex
	rate            string
	rateAt          time.Time
	pacer           requestPacer
	requestInterval time.Duration
}

func newNock(c NockConfig) (*Nock, error) {
	n := &Nock{cfg: c, requestInterval: 5 * time.Second, http: &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.Zpub != "" {
		var err error
		n.account, err = nockchain.ParsePublicKey(c.Zpub)
		if err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (n *Nock) call(ctx context.Context, price bool, method string, params []any, out any) (json.Number, error) {
	id := strconv.FormatUint(n.id.Add(1), 10)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return "", err
	}
	endpoint := n.cfg.RPC
	if price {
		endpoint = n.cfg.PriceRPC
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("Invalid Nockblocks endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "netsub-store/1.0")
	req.Header.Set("x-api-key", n.cfg.APIKey)
	if err := n.pacer.wait(ctx, n.requestInterval); err != nil {
		return "", err
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return "", errors.New("Nockblocks is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		n.pacer.deferFor(time.Minute)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Nockblocks HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		ID      string
		JSONRPC string
		Price   json.Number
		Result  json.RawMessage
		Error   *struct{ Code int }
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&envelope); err != nil {
		return "", errors.New("Invalid Nockblocks response")
	}
	if envelope.ID != id || envelope.JSONRPC != "2.0" {
		return "", errors.New("Nockblocks response ID or version mismatch")
	}
	if envelope.Error != nil {
		return "", fmt.Errorf("Nockblocks RPC error %d", envelope.Error.Code)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return "", errors.New("Nockblocks result is unavailable")
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return "", errors.New("Invalid Nockblocks result")
	}
	return envelope.Price, nil
}

type nockBlock struct {
	Digest    string
	Height    uint64
	Timestamp int64
	TxIDs     []string `json:"txids"`
}

func (n *Nock) tip(ctx context.Context) (nockBlock, error) {
	var genesis []nockBlock
	_, err := n.call(ctx, false, "getBlocksByHeight", []any{map[string]any{"heights": []uint64{0}, "includeTransactions": false}}, &genesis)
	if err != nil {
		return nockBlock{}, err
	}
	if len(genesis) != 1 || genesis[0].Height != 0 || genesis[0].Digest != nockGenesis {
		return nockBlock{}, errors.New("Nockblocks is not on Nockchain mainnet")
	}
	var tip nockBlock
	_, err = n.call(ctx, false, "getTip", []any{map[string]any{"includeTransactions": false}}, &tip)
	if err != nil {
		return tip, err
	}
	if _, err = nockchain.ParseDigest(tip.Digest); err != nil || tip.Height == 0 || tip.Timestamp > time.Now().Add(2*time.Minute).Unix() || tip.Timestamp < time.Now().Add(-2*time.Hour).Unix() {
		return tip, errors.New("Nockchain index is stale or invalid")
	}
	return tip, nil
}
func (n *Nock) usdRate(ctx context.Context) (string, error) {
	n.rateMu.Lock()
	defer n.rateMu.Unlock()
	if n.rate != "" && time.Since(n.rateAt) < time.Minute {
		return n.rate, nil
	}
	var result struct{ Height uint64 }
	price, err := n.call(ctx, true, "getTip", []any{}, &result)
	if err != nil {
		return "", err
	}
	if !validNockRate(string(price)) {
		return "", errors.New("NOCK exchange rate is unavailable")
	}
	v, ok := new(big.Rat).SetString(string(price))
	if !ok || v.Sign() <= 0 || result.Height == 0 {
		return "", errors.New("Invalid NOCK exchange rate")
	}
	n.rate, n.rateAt = string(price), time.Now()
	return n.rate, nil
}
func nockQuote(usd, rate string, base bool) (string, error) {
	dollars, err := units(usd, 6)
	if err != nil {
		return "", err
	}
	if !validNockRate(rate) {
		return "", errors.New("Invalid NOCK exchange rate")
	}
	price, ok := new(big.Rat).SetString(rate)
	if !ok || price.Sign() <= 0 {
		return "", errors.New("Invalid NOCK exchange rate")
	}
	scale := new(big.Int).SetUint64(nockchain.NicksPerNock)
	if base {
		scale.Exp(big.NewInt(10), big.NewInt(16), nil)
	}
	amount := new(big.Rat).SetFrac(new(big.Int).Mul(dollars, scale), big.NewInt(1_000_000))
	amount.Quo(amount, price)
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(amount.Num(), amount.Denom(), r)
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Sign() <= 0 || (!base && !q.IsUint64()) || (base && q.BitLen() > 256) {
		return "", errors.New("NOCK quote is out of range")
	}
	return q.String(), nil
}

// Address allocation and the order are committed together before exposure.
func (n *Nock) allocate(tx *bolt.Tx, o *Order) error {
	root, err := tx.CreateBucketIfNotExists([]byte("nockchain-accounts"))
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(n.cfg.Zpub))
	accountID := hex.EncodeToString(hash[:])
	account, err := root.CreateBucketIfNotExists([]byte(accountID))
	if err != nil {
		return err
	}
	if err = account.Put([]byte("zpub"), []byte(n.cfg.Zpub)); err != nil {
		return err
	}
	seq, err := account.NextSequence()
	if err != nil {
		return err
	}
	if seq == 0 || seq > 1<<31 {
		return errors.New("Nockchain receiving branch is exhausted")
	}
	child, err := n.account.Child(uint32(seq - 1))
	if err != nil {
		return errors.New("Cannot derive Nockchain receiving address")
	}
	o.Recipient, err = child.Address()
	if err != nil {
		return err
	}
	o.NockLockRoot, err = child.LockAddress()
	if err != nil {
		return err
	}
	addresses, err := tx.CreateBucketIfNotExists([]byte("nockchain-addresses"))
	if err != nil {
		return err
	}
	if addresses.Get([]byte(o.Recipient)) != nil {
		return errors.New("Nockchain receiving address is already assigned")
	}
	if err = addresses.Put([]byte(o.Recipient), []byte(o.ID)); err != nil {
		return err
	}
	o.NockKeyID = accountID
	o.NockIndex = uint32(seq - 1)
	return nil
}

type nockTransaction struct {
	ID, BlockID string
	Version     *uint64
	Outputs     []struct {
		FirstName, LastName string
		Seeds               []struct {
			LockRoot   string
			Gift       *uint64
			IsCoinbase *bool
		}
	}
}
type nockOutput struct {
	Key, TxID string
	Nicks     *big.Int
}
type nockPayment struct {
	Received, Confirmed, Observed *big.Int
	Confirmations                 int
	Late                          bool
	Outputs                       []nockOutput
}

func (n *Nock) payment(ctx context.Context, o Order) (nockPayment, error) {
	p := nockPayment{Received: new(big.Int), Confirmed: new(big.Int), Observed: new(big.Int)}
	if o.NockRequired < 2 || o.NockRequired > 144 {
		return p, errors.New("Invalid Nockchain confirmation requirement")
	}
	pkh, err := nockchain.ParseDigest(o.Recipient)
	if err != nil {
		return p, errors.New("Invalid Nockchain order address")
	}
	lock, err := nockchain.LockRoot(pkh)
	if err != nil || lock.String() != o.NockLockRoot {
		return p, errors.New("Nockchain receiving lock mismatch")
	}
	tip, err := n.tip(ctx)
	if err != nil {
		return p, err
	}
	txs := []nockTransaction{}
	seenTx := map[string]bool{}
	for offset := 0; offset < 1000; offset += 100 {
		var page struct {
			Transactions      []nockTransaction
			TotalTransactions *uint64
			Lookup            struct{ Value string }
		}
		_, err = n.call(ctx, false, "getTransactionsByAddress", []any{map[string]any{"address": o.NockLockRoot, "limit": 100, "offset": offset}}, &page)
		if err != nil {
			return p, err
		}
		if page.Lookup.Value != o.NockLockRoot || page.TotalTransactions == nil || *page.TotalTransactions > 1000 || len(page.Transactions) > 100 {
			return p, errors.New("Nockchain address response is incomplete")
		}
		for _, tx := range page.Transactions {
			if _, err = nockchain.ParseDigest(tx.ID); err != nil {
				return p, errors.New("Invalid Nockchain transaction ID")
			}
			if seenTx[tx.ID] {
				return p, errors.New("Nockchain transaction pagination is inconsistent")
			}
			seenTx[tx.ID] = true
			txs = append(txs, tx)
		}
		if uint64(len(txs)) >= *page.TotalTransactions {
			if uint64(len(txs)) != *page.TotalTransactions {
				return p, errors.New("Nockchain transaction count mismatch")
			}
			break
		}
		if len(page.Transactions) != 100 || offset == 900 {
			return p, errors.New("Nockchain transaction page is incomplete")
		}
	}
	// Batch block proofs while retaining exact identity and canonical-height checks.
	blockIDs := []string{}
	seenBlocks := map[string]bool{}
	for _, tx := range txs {
		if tx.Version == nil || *tx.Version != 1 {
			return p, errors.New("Nockchain transaction version needs review")
		}
		if _, err = nockchain.ParseDigest(tx.BlockID); err != nil {
			return p, errors.New("Nockchain transaction is not in a valid block")
		}
		if !seenBlocks[tx.BlockID] {
			seenBlocks[tx.BlockID] = true
			blockIDs = append(blockIDs, tx.BlockID)
		}
	}
	blocks := map[string]nockBlock{}
	for start := 0; start < len(blockIDs); start += 100 {
		end := start + 100
		if end > len(blockIDs) {
			end = len(blockIDs)
		}
		ids := blockIDs[start:end]
		var found []nockBlock
		_, err = n.call(ctx, false, "getBlocksById", []any{map[string]any{"blockIds": ids, "includeTransactions": false}}, &found)
		if err != nil {
			return p, err
		}
		if len(found) != len(ids) {
			return p, errors.New("Nockchain block proof is incomplete")
		}
		expected := map[string]bool{}
		for _, id := range ids {
			expected[id] = true
		}
		heights := []uint64{}
		byHeight := map[uint64]string{}
		for _, block := range found {
			if !expected[block.Digest] || block.Height == 0 || block.Height > tip.Height || byHeight[block.Height] != "" {
				return p, errors.New("Nockchain block cannot be verified")
			}
			delete(expected, block.Digest)
			heights = append(heights, block.Height)
			byHeight[block.Height] = block.Digest
			blocks[block.Digest] = block
		}
		var canonical []nockBlock
		_, err = n.call(ctx, false, "getBlocksByHeight", []any{map[string]any{"heights": heights, "includeTransactions": false}}, &canonical)
		if err != nil {
			return p, err
		}
		if len(canonical) != len(heights) {
			return p, errors.New("Nockchain canonical block proof is incomplete")
		}
		for _, block := range canonical {
			if byHeight[block.Height] != block.Digest || block.Digest == "" {
				return p, errors.New("Nockchain block is not canonical")
			}
			delete(byHeight, block.Height)
		}
	}
	seenOutput := map[string]bool{}
	for _, tx := range txs {
		block := blocks[tx.BlockID]
		included := false
		for _, id := range block.TxIDs {
			if id == tx.ID {
				included = true
				break
			}
		}
		if !included {
			return p, errors.New("Nockchain transaction is absent from its block")
		}
		confirmations := int(tip.Height - block.Height + 1)
		for _, output := range tx.Outputs {
			amount := new(big.Int)
			for _, seed := range output.Seeds {
				if seed.LockRoot != o.NockLockRoot {
					continue
				}
				if seed.Gift == nil || seed.IsCoinbase == nil || *seed.IsCoinbase {
					return p, errors.New("Nockchain output needs review")
				}
				amount.Add(amount, new(big.Int).SetUint64(*seed.Gift))
			}
			if amount.Sign() == 0 {
				continue
			}
			for _, name := range []string{output.FirstName, output.LastName} {
				if _, err = nockchain.ParseDigest(name); err != nil {
					return p, errors.New("Invalid Nockchain output name")
				}
			}
			key := "nockchain:" + output.FirstName + ":" + output.LastName
			if seenOutput[key] {
				return p, errors.New("Nockchain output appears more than once")
			}
			seenOutput[key] = true
			p.Observed.Add(p.Observed, amount)
			if block.Timestamp < o.Created.Unix() || block.Timestamp > o.Expires.Unix() {
				p.Late = true
				continue
			}
			if p.Received.Sign() == 0 || confirmations < p.Confirmations {
				p.Confirmations = confirmations
			}
			p.Received.Add(p.Received, amount)
			if confirmations >= o.NockRequired {
				p.Confirmed.Add(p.Confirmed, amount)
				p.Outputs = append(p.Outputs, nockOutput{key, tx.ID, amount})
			}
		}
	}
	return p, nil
}

func (s *Server) checkNockPayment(ctx context.Context, o Order) {
	o.Checks++
	o.NextCheck = time.Now().Add(30 * time.Second)
	if s.nock == nil {
		o.Error = "Nockchain payments are unavailable; contact the store"
		_ = s.save(o)
		return
	}
	p, err := s.nock.payment(ctx, o)
	if err != nil {
		o.Error = "Nockchain payment cannot be verified yet; the store will retry"
		_ = s.save(o)
		return
	}
	o.Error = ""
	o.NockReceived = p.Observed.String()
	o.NockConfirmations = p.Confirmations
	due, ok := new(big.Int).SetString(o.Amount, 10)
	if !ok || due.Sign() <= 0 {
		o.Status = "review_payment"
		o.Error = "Nockchain quote needs operator review"
		_ = s.save(o)
		return
	}
	if p.Received.Sign() > 0 {
		o.Status = "confirming"
	}
	if p.Confirmed.Cmp(due) < 0 {
		if time.Now().After(o.Expires.Add(24*time.Hour)) && p.Observed.Sign() == 0 {
			o.Status = "expired"
		}
		if p.Late || (time.Now().After(o.Expires) && p.Received.Sign() > 0 && p.Received.Cmp(due) < 0) {
			o.Status = "review_payment"
			o.Error = "Nockchain payment arrived late or is below the quote; contact the store"
		}
		_ = s.save(o)
		return
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		payments := tx.Bucket([]byte("payments"))
		ids := map[string]bool{}
		for _, output := range p.Outputs {
			if assigned := payments.Get([]byte(output.Key)); assigned != nil && string(assigned) != o.ID {
				o.Status = "review_payment"
				o.Error = "Nockchain payment output is assigned to another order"
				return putOrder(tx, o)
			}
		}
		for _, output := range p.Outputs {
			if err := payments.Put([]byte(output.Key), []byte(o.ID)); err != nil {
				return err
			}
			ids[output.TxID] = true
		}
		o.NockTxIDs = nil
		for id := range ids {
			o.NockTxIDs = append(o.NockTxIDs, id)
		}
		sort.Strings(o.NockTxIDs)
		o.TxHash = o.NockTxIDs[0]
		o.Status = "paid"
		o.PaidAt = time.Now().UTC()
		o.ReceiptNext = o.PaidAt
		o.NextCheck = time.Now()
		o.Checks = 0
		return putOrder(tx, o)
	})
	if err != nil {
		s.LogError("Nockchain payment persistence failed", err)
	}
}

func paymentAssetName(asset string) string {
	if asset == "NOCK_BASE" {
		return "$NOCK"
	}
	return asset
}
func addressPaymentAsset(asset string) bool {
	return asset == "BTC" || asset == "XMR" || asset == "NOCK"
}

// Bound decimal exponents before asking big.Rat to allocate their powers of ten.
var nockRatePattern = regexp.MustCompile(`^[0-9]{1,20}(\.[0-9]{1,20})?([eE][+-]?[0-9]{1,2})?$`)

func validNockRate(rate string) bool {
	return len(rate) <= 40 && nockRatePattern.MatchString(rate)
}
