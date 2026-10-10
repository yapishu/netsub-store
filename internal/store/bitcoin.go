package store

import (
	"bytes"
	"context"
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	bolt "go.etcd.io/bbolt"
)

type BitcoinConfig struct {
	RPC, User, Password, Wallet, Network string
	Confirmations                        int
}

func bitcoinFromEnv() (BitcoinConfig, error) {
	c := BitcoinConfig{RPC: os.Getenv("BITCOIN_RPC_URL"), User: os.Getenv("BITCOIN_RPC_USER"), Password: os.Getenv("BITCOIN_RPC_PASSWORD"), Wallet: env("BITCOIN_RPC_WALLET", "netsub-store"), Network: env("BITCOIN_NETWORK", "main")}
	if c.RPC == "" {
		return c, nil
	}
	u, err := url.Parse(c.RPC)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("BITCOIN_RPC_URL must be an HTTP(S) node URL without credentials or a path")
	}
	if c.User == "" || c.Password == "" {
		return c, errors.New("BITCOIN_RPC_USER and BITCOIN_RPC_PASSWORD are required when Bitcoin is enabled")
	}
	if bitcoinParams(c.Network) == nil {
		return c, errors.New("BITCOIN_NETWORK must be main, test, signet, or regtest")
	}
	c.Confirmations, err = strconv.Atoi(env("BITCOIN_CONFIRMATIONS", "6"))
	if err != nil || c.Confirmations < 1 || c.Confirmations > 144 {
		return c, errors.New("BITCOIN_CONFIRMATIONS must be between 1 and 144")
	}
	return c, nil
}
func bitcoinParams(network string) *chaincfg.Params {
	switch network {
	case "main":
		return &chaincfg.MainNetParams
	case "test":
		return &chaincfg.TestNet3Params
	case "signet":
		return &chaincfg.SigNetParams
	case "regtest":
		return &chaincfg.RegressionNetParams
	}
	return nil
}

type Bitcoin struct {
	cfg    BitcoinConfig
	http   *http.Client
	id     atomic.Uint64
	rateMu sync.Mutex
	rate   string
	rateAt time.Time
}
type bitcoinRPCError struct {
	Code int `json:"code"`
}

func (e *bitcoinRPCError) Error() string { return fmt.Sprintf("Bitcoin RPC error %d", e.Code) }
func (b *Bitcoin) call(ctx context.Context, method string, params any, out any) error {
	id := b.id.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(b.cfg.RPC, "/") + "/wallet/" + url.PathEscape(b.cfg.Wallet)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("Cannot prepare Bitcoin RPC request")
	}
	req.SetBasicAuth(b.cfg.User, b.cfg.Password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return errors.New("Bitcoin node unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 500 {
		return fmt.Errorf("Bitcoin node HTTP %d", resp.StatusCode)
	}
	var result struct {
		ID     uint64           `json:"id"`
		Result json.RawMessage  `json:"result"`
		Error  *bitcoinRPCError `json:"error"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return errors.New("Invalid Bitcoin node response")
	}
	if result.ID != id {
		return errors.New("Bitcoin RPC response ID mismatch")
	}
	if result.Error != nil {
		return result.Error
	}
	if resp.StatusCode != 200 || len(result.Result) == 0 || bytes.Equal(result.Result, []byte("null")) {
		return errors.New("Empty Bitcoin node response")
	}
	if err = json.Unmarshal(result.Result, out); err != nil {
		return errors.New("Invalid Bitcoin RPC result")
	}
	return nil
}
func (b *Bitcoin) ready(ctx context.Context) error {
	var info struct {
		Chain                string
		InitialBlockDownload bool `json:"initialblockdownload"`
		Blocks, Headers      int64
	}
	if err := b.call(ctx, "getblockchaininfo", []any{}, &info); err != nil {
		return err
	}
	if info.Chain != b.cfg.Network {
		return errors.New("Bitcoin node network does not match configuration")
	}
	if info.InitialBlockDownload || info.Headers > info.Blocks+1 {
		return errors.New("Bitcoin node is synchronizing")
	}
	var wallet struct {
		Scanning json.RawMessage `json:"scanning"`
	}
	if err := b.call(ctx, "getwalletinfo", []any{}, &wallet); err != nil {
		return err
	}
	if len(wallet.Scanning) > 0 && !bytes.Equal(wallet.Scanning, []byte("false")) {
		return errors.New("Bitcoin wallet is scanning")
	}
	return nil
}
func (b *Bitcoin) address(ctx context.Context, id string) (string, error) {
	if err := b.ready(ctx); err != nil {
		return "", err
	}
	label := "netsub-order:" + id
	var addresses map[string]json.RawMessage
	err := b.call(ctx, "getaddressesbylabel", []any{label}, &addresses)
	var rpcErr *bitcoinRPCError
	if err != nil && (!errors.As(err, &rpcErr) || rpcErr.Code != -11) {
		return "", err
	}
	var address string
	if err == nil {
		if len(addresses) != 1 {
			return "", errors.New("Bitcoin order label does not identify one address")
		}
		for a := range addresses {
			address = a
		}
	} else if err = b.call(ctx, "getnewaddress", []any{label, "bech32"}, &address); err != nil {
		return "", err
	}
	params := bitcoinParams(b.cfg.Network)
	decoded, err := btcutil.DecodeAddress(address, params)
	if err != nil || !decoded.IsForNet(params) {
		return "", errors.New("Bitcoin node returned an address for the wrong network")
	}
	return address, nil
}
func (b *Bitcoin) usdRate(ctx context.Context) (string, error) {
	b.rateMu.Lock()
	defer b.rateMu.Unlock()
	if b.rate != "" && time.Since(b.rateAt) < time.Minute {
		return b.rate, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.coinbase.com/v2/prices/BTC-USD/spot", nil)
	if err != nil {
		return "", err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return "", errors.New("Bitcoin exchange rate unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("Bitcoin exchange rate unavailable")
	}
	var price struct {
		Data struct{ Amount, Base, Currency string }
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&price); err != nil {
		return "", errors.New("Invalid Bitcoin exchange rate")
	}
	value, ok := new(big.Rat).SetString(price.Data.Amount)
	if !ok || value.Sign() <= 0 || price.Data.Currency != "USD" || price.Data.Base != "BTC" {
		return "", errors.New("Invalid Bitcoin exchange rate")
	}
	b.rate, b.rateAt = price.Data.Amount, time.Now()
	return b.rate, nil
}
func bitcoinQuote(usd, rate string) (string, error) {
	dollars, err := units(usd, 6)
	if err != nil {
		return "", err
	}
	price, ok := new(big.Rat).SetString(rate)
	if !ok || price.Sign() <= 0 {
		return "", errors.New("Invalid Bitcoin exchange rate")
	}
	sats := new(big.Rat).Quo(new(big.Rat).SetInt(new(big.Int).Mul(dollars, big.NewInt(100))), price)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(sats.Num(), sats.Denom(), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if quotient.Sign() <= 0 || !quotient.IsInt64() || quotient.Int64() > 21_000_000*100_000_000 {
		return "", errors.New("Bitcoin amount is out of range")
	}
	return quotient.String(), nil
}
func bitcoinSats(number json.Number) (*big.Int, error) {
	amount, ok := new(big.Rat).SetString(string(number))
	if !ok || amount.Sign() < 0 {
		return nil, errors.New("Invalid Bitcoin payment amount")
	}
	amount.Mul(amount, new(big.Rat).SetInt64(100_000_000))
	if !amount.IsInt() || !amount.Num().IsInt64() || amount.Num().Int64() > 21_000_000*100_000_000 {
		return nil, errors.New("Invalid Bitcoin payment precision")
	}
	return new(big.Int).Set(amount.Num()), nil
}

type bitcoinOutput struct {
	Key, TxID string
	Sats      *big.Int
}
type bitcoinPayment struct {
	Received, Confirmed, Observed *big.Int
	Confirmations                 int
	Outputs                       []bitcoinOutput
	Late                          bool
}

var bitcoinTxID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (b *Bitcoin) payment(ctx context.Context, o Order) (bitcoinPayment, error) {
	payment := bitcoinPayment{Received: new(big.Int), Confirmed: new(big.Int), Observed: new(big.Int)}
	if o.BitcoinNetwork != b.cfg.Network {
		return payment, errors.New("Bitcoin order network does not match configuration")
	}
	if err := b.ready(ctx); err != nil {
		return payment, err
	}
	var received []struct {
		Address string
		TxIDs   []string `json:"txids"`
	}
	if err := b.call(ctx, "listreceivedbyaddress", []any{0, true, true, o.Recipient}, &received); err != nil {
		return payment, err
	}
	seen := map[string]bool{}
	for _, entry := range received {
		if entry.Address != o.Recipient {
			continue
		}
		if len(entry.TxIDs) > 100 {
			return payment, errors.New("Bitcoin payment needs operator review")
		}
		for _, txid := range entry.TxIDs {
			if seen[txid] {
				continue
			}
			seen[txid] = true
			if !bitcoinTxID.MatchString(txid) {
				return payment, errors.New("Invalid Bitcoin transaction ID")
			}
			var tx struct {
				TxID          string `json:"txid"`
				Confirmations int
				TimeReceived  int64 `json:"timereceived"`
				Details       []struct {
					Address, Category string
					Amount            json.Number
					Vout              uint32
					Abandoned         bool
				}
			}
			if err := b.call(ctx, "gettransaction", []any{txid, true}, &tx); err != nil {
				return payment, err
			}
			if tx.TxID != txid {
				return payment, errors.New("Bitcoin transaction ID mismatch")
			}
			if tx.Confirmations < 0 {
				continue
			}
			outputs := map[uint32]bool{}
			for _, detail := range tx.Details {
				if detail.Address != o.Recipient || detail.Category != "receive" || detail.Abandoned || outputs[detail.Vout] {
					continue
				}
				outputs[detail.Vout] = true
				sats, err := bitcoinSats(detail.Amount)
				if err != nil {
					return payment, err
				}
				if sats.Sign() == 0 {
					continue
				}
				payment.Observed.Add(payment.Observed, sats)
				if tx.TimeReceived < o.Created.Unix()-120 || tx.TimeReceived > o.Expires.Unix() {
					payment.Late = true
					continue
				}
				if payment.Received.Sign() == 0 || tx.Confirmations < payment.Confirmations {
					payment.Confirmations = tx.Confirmations
				}
				payment.Received.Add(payment.Received, sats)
				if tx.Confirmations >= o.BitcoinRequired {
					payment.Confirmed.Add(payment.Confirmed, sats)
					payment.Outputs = append(payment.Outputs, bitcoinOutput{Key: fmt.Sprintf("bitcoin:%s:%s:%d", b.cfg.Network, txid, detail.Vout), TxID: txid, Sats: sats})
				}
			}
		}
	}
	return payment, nil
}

func (s *Server) prepareBitcoinAddress(ctx context.Context, o Order) Order {
	o.NextCheck = time.Now().Add(time.Minute)
	if s.bitcoin == nil {
		o.Error = "Bitcoin payments are unavailable"
		_ = s.save(o)
		return o
	}
	address, err := s.bitcoin.address(ctx, o.ID)
	if err != nil {
		o.Error = "Cannot prepare a Bitcoin payment address; the store will retry"
		_ = s.save(o)
		return o
	}
	o.Recipient = address
	o.Status = "awaiting_payment"
	o.Error = ""
	o.NextCheck = time.Now()
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("bitcoin-addresses"))
		if err != nil {
			return err
		}
		key := []byte(o.BitcoinNetwork + ":" + address)
		if assigned := bucket.Get(key); assigned != nil && string(assigned) != o.ID {
			return errors.New("Bitcoin address is assigned to another order")
		}
		if err = bucket.Put(key, []byte(o.ID)); err != nil {
			return err
		}
		return putOrder(tx, o)
	})
	if err != nil {
		s.LogError("Bitcoin address persistence failed", err)
		o.Status = "awaiting_address"
		o.Recipient = ""
		o.Error = "Cannot save Bitcoin payment address; the store will retry"
		_ = s.save(o)
	}
	return o
}
func (s *Server) checkBitcoinPayment(ctx context.Context, o Order) {
	o.Checks++
	o.NextCheck = time.Now().Add(30 * time.Second)
	if s.bitcoin == nil {
		o.Error = "Bitcoin payments are unavailable; contact the store"
		_ = s.save(o)
		return
	}
	payment, err := s.bitcoin.payment(ctx, o)
	if err != nil {
		o.Error = "Bitcoin node is unavailable or synchronizing; payment verification will retry"
		_ = s.save(o)
		return
	}
	o.Error = ""
	o.BitcoinReceived = payment.Observed.String()
	o.BitcoinConfirmations = payment.Confirmations
	due, ok := new(big.Int).SetString(o.Amount, 10)
	if !ok {
		o.Error = "Bitcoin quote needs operator review"
		o.Status = "review_payment"
		_ = s.save(o)
		return
	}
	if payment.Received.Sign() > 0 {
		o.Status = "confirming"
	}
	if payment.Confirmed.Cmp(due) < 0 {
		if time.Now().After(o.Expires.Add(24*time.Hour)) && payment.Received.Sign() == 0 && !payment.Late {
			o.Status = "expired"
		}
		if payment.Late || (time.Now().After(o.Expires) && payment.Received.Cmp(due) < 0 && payment.Received.Sign() > 0) {
			o.Status = "review_payment"
			o.Error = "Bitcoin payment arrived late or is below the quote; contact the store"
		}
		_ = s.save(o)
		return
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("payments"))
		for _, output := range payment.Outputs {
			if used := bucket.Get([]byte(output.Key)); used != nil && string(used) != o.ID {
				o.Status = "review_payment"
				o.Error = "Bitcoin payment output is assigned to another order"
				return putOrder(tx, o)
			}
		}
		ids := map[string]bool{}
		for _, output := range payment.Outputs {
			if err := bucket.Put([]byte(output.Key), []byte(o.ID)); err != nil {
				return err
			}
			ids[output.TxID] = true
		}
		o.BitcoinTxIDs = nil
		for id := range ids {
			o.BitcoinTxIDs = append(o.BitcoinTxIDs, id)
		}
		sort.Strings(o.BitcoinTxIDs)
		o.TxHash = o.BitcoinTxIDs[0]
		o.Status = "paid"
		o.PaidAt = time.Now().UTC()
		o.ReceiptNext = o.PaidAt
		o.NextCheck = time.Now()
		o.Checks = 0
		return putOrder(tx, o)
	})
	if err != nil {
		s.LogError("Bitcoin payment persistence failed", err)
	}
}
