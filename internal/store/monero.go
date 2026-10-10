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

	"github.com/icholy/digest"
	bolt "go.etcd.io/bbolt"
)

type MoneroConfig struct {
	WalletRPC, WalletUser, WalletPassword string
	DaemonRPC, DaemonUser, DaemonPassword string
	Network                               string
	Account                               uint32
	Confirmations                         int
}

func moneroFromEnv() (MoneroConfig, error) {
	c := MoneroConfig{WalletRPC: os.Getenv("MONERO_WALLET_RPC_URL"), WalletUser: os.Getenv("MONERO_WALLET_RPC_USER"), WalletPassword: os.Getenv("MONERO_WALLET_RPC_PASSWORD"), DaemonRPC: os.Getenv("MONERO_DAEMON_RPC_URL"), DaemonUser: os.Getenv("MONERO_DAEMON_RPC_USER"), DaemonPassword: os.Getenv("MONERO_DAEMON_RPC_PASSWORD"), Network: env("MONERO_NETWORK", "mainnet")}
	if c.WalletRPC == "" {
		return c, nil
	}
	for name, endpoint := range map[string]string{"MONERO_WALLET_RPC_URL": c.WalletRPC, "MONERO_DAEMON_RPC_URL": c.DaemonRPC} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return c, fmt.Errorf("%s must be an HTTP(S) base URL without credentials or a path", name)
		}
	}
	if c.WalletUser == "" || c.WalletPassword == "" {
		return c, errors.New("MONERO_WALLET_RPC_USER and MONERO_WALLET_RPC_PASSWORD are required")
	}
	if (c.DaemonUser == "") != (c.DaemonPassword == "") {
		return c, errors.New("set both MONERO_DAEMON_RPC_USER and MONERO_DAEMON_RPC_PASSWORD")
	}
	if c.Network != "mainnet" && c.Network != "stagenet" && c.Network != "testnet" {
		return c, errors.New("MONERO_NETWORK must be mainnet, stagenet, or testnet")
	}
	account, err := strconv.ParseUint(env("MONERO_ACCOUNT_INDEX", "0"), 10, 32)
	if err != nil {
		return c, errors.New("MONERO_ACCOUNT_INDEX must be a nonnegative account index")
	}
	c.Account = uint32(account)
	c.Confirmations, err = strconv.Atoi(env("MONERO_CONFIRMATIONS", "10"))
	if err != nil || c.Confirmations < 10 || c.Confirmations > 144 {
		return c, errors.New("MONERO_CONFIRMATIONS must be between 10 and 144")
	}
	return c, nil
}

type Monero struct {
	cfg                    MoneroConfig
	wallet, daemon, prices *http.Client
	id                     atomic.Uint64
	rateMu                 sync.Mutex
	rate                   string
	rateAt                 time.Time
}

func newMonero(c MoneroConfig, prices *http.Client) *Monero {
	client := func(user, password string) *http.Client {
		var transport http.RoundTripper = http.DefaultTransport
		if user != "" {
			transport = &digest.Transport{Username: user, Password: password}
		}
		return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Monero{cfg: c, wallet: client(c.WalletUser, c.WalletPassword), daemon: client(c.DaemonUser, c.DaemonPassword), prices: prices}
}
func (m *Monero) call(ctx context.Context, daemon bool, method string, params any, out any) error {
	id := m.id.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": strconv.FormatUint(id, 10), "method": method, "params": params})
	if err != nil {
		return err
	}
	endpoint, client := m.cfg.WalletRPC, m.wallet
	if daemon {
		endpoint, client = m.cfg.DaemonRPC, m.daemon
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(endpoint, "/")+"/json_rpc", bytes.NewReader(body))
	if err != nil {
		return errors.New("Cannot prepare Monero RPC request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Monero RPC unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Monero RPC HTTP %d", resp.StatusCode)
	}
	var result struct {
		ID     string
		Result json.RawMessage
		Error  *struct{ Code int }
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&result); err != nil {
		return errors.New("Invalid Monero RPC response")
	}
	if result.ID != strconv.FormatUint(id, 10) {
		return errors.New("Monero RPC response ID mismatch")
	}
	if result.Error != nil {
		return fmt.Errorf("Monero RPC error %d", result.Error.Code)
	}
	if len(result.Result) == 0 || bytes.Equal(result.Result, []byte("null")) {
		return errors.New("Empty Monero RPC result")
	}
	if err = json.Unmarshal(result.Result, out); err != nil {
		return errors.New("Invalid Monero RPC result")
	}
	return nil
}
func (m *Monero) ready(ctx context.Context) error {
	var node struct {
		Nettype, Status    string
		Height             uint64
		TargetHeight       uint64 `json:"target_height"`
		Synchronized       bool
		BusySyncing        bool `json:"busy_syncing"`
		Offline, Untrusted bool
	}
	if err := m.call(ctx, true, "get_info", map[string]any{}, &node); err != nil {
		return err
	}
	if node.Nettype != m.cfg.Network || node.Status != "OK" || !node.Synchronized || node.BusySyncing || node.Offline || node.Untrusted || node.Height == 0 || node.TargetHeight > node.Height {
		return errors.New("Monero daemon is unavailable, synchronizing, or on another network")
	}
	var wallet struct{ Height uint64 }
	if err := m.call(ctx, false, "get_height", map[string]any{}, &wallet); err != nil {
		return err
	}
	if wallet.Height == 0 || wallet.Height+1 < node.Height || wallet.Height > node.Height+1 {
		return errors.New("Monero wallet is synchronizing")
	}
	return nil
}

type moneroAddress struct {
	Address, Label string
	Index          uint32 `json:"address_index"`
	Used           bool
}

func (m *Monero) addresses(ctx context.Context, indices []uint32) ([]moneroAddress, error) {
	params := map[string]any{"account_index": m.cfg.Account}
	if indices != nil {
		params["address_index"] = indices
	}
	var result struct{ Addresses []moneroAddress }
	err := m.call(ctx, false, "get_address", params, &result)
	return result.Addresses, err
}
func (m *Monero) address(ctx context.Context, orderID string) (moneroAddress, error) {
	var found moneroAddress
	if err := m.ready(ctx); err != nil {
		return found, err
	}
	addresses, err := m.addresses(ctx, nil)
	if err != nil {
		return found, err
	}
	label := "netsub-order:" + orderID
	for _, address := range addresses {
		if address.Label == label {
			if found.Address != "" {
				return found, errors.New("Multiple Monero subaddresses have the order label")
			}
			found = address
		}
	}
	if found.Address == "" {
		if err = m.call(ctx, false, "create_address", map[string]any{"account_index": m.cfg.Account, "label": label, "count": 1}, &found); err != nil {
			return found, err
		}
	}
	if found.Index == 0 || !regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{95}$`).MatchString(found.Address) {
		return found, errors.New("Invalid Monero receiving subaddress")
	}
	var validation struct {
		Valid, Integrated, Subaddress bool
		Nettype                       string
	}
	if err = m.call(ctx, false, "validate_address", map[string]any{"address": found.Address, "any_net_type": false, "allow_openalias": false}, &validation); err != nil {
		return found, err
	}
	if !validation.Valid || validation.Integrated || !validation.Subaddress || validation.Nettype != m.cfg.Network {
		return found, errors.New("Monero subaddress network mismatch")
	}
	// Persist the label before exposing the subaddress; retries recover it by label.
	var stored struct{}
	if err = m.call(ctx, false, "store", map[string]any{}, &stored); err != nil {
		return found, err
	}
	return found, nil
}
func (m *Monero) usdRate(ctx context.Context) (string, error) {
	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	if m.rate != "" && time.Since(m.rateAt) < time.Minute {
		return m.rate, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.kraken.com/0/public/Ticker?pair=XMRUSD", nil)
	if err != nil {
		return "", err
	}
	resp, err := m.prices.Do(req)
	if err != nil {
		return "", errors.New("Monero exchange rate unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("Monero exchange rate unavailable")
	}
	var data struct {
		Error  []string
		Result map[string]struct {
			Close []string `json:"c"`
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&data); err != nil || len(data.Error) > 0 {
		return "", errors.New("Invalid Monero exchange rate")
	}
	ticker, ok := data.Result["XXMRZUSD"]
	if !ok || len(ticker.Close) != 2 {
		return "", errors.New("Missing XMR/USD exchange rate")
	}
	if _, err = moneroQuote("1", ticker.Close[0]); err != nil {
		return "", err
	}
	m.rate, m.rateAt = ticker.Close[0], time.Now()
	return m.rate, nil
}
func moneroQuote(usd, rate string) (string, error) {
	dollars, err := units(usd, 6)
	if err != nil {
		return "", err
	}
	price, ok := new(big.Rat).SetString(rate)
	if !ok || price.Sign() <= 0 {
		return "", errors.New("Invalid Monero exchange rate")
	}
	atoms := new(big.Rat).Quo(new(big.Rat).SetInt(new(big.Int).Mul(dollars, big.NewInt(1_000_000))), price)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(atoms.Num(), atoms.Denom(), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if quotient.Sign() <= 0 || !quotient.IsUint64() {
		return "", errors.New("Monero amount is out of range")
	}
	return quotient.String(), nil
}

type moneroTransfer struct {
	Address, TxID, Type string
	Amount              uint64
	Confirmations       int
	Height              uint64
	UnlockTime          uint64 `json:"unlock_time"`
	Locked              *bool
	DoubleSpend         bool                          `json:"double_spend_seen"`
	Index               struct{ Major, Minor uint32 } `json:"subaddr_index"`
}
type moneroPayment struct {
	Observed, Received, Confirmed *big.Int
	Confirmations                 int
	Seen                          map[string]time.Time
	TxIDs                         []string
	Late, Unsafe                  bool
}

var moneroTxID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (m *Monero) payment(ctx context.Context, o Order) (moneroPayment, error) {
	p := moneroPayment{Observed: new(big.Int), Received: new(big.Int), Confirmed: new(big.Int), Seen: map[string]time.Time{}}
	if o.MoneroNetwork != m.cfg.Network || o.MoneroAccount != m.cfg.Account {
		return p, errors.New("Monero order configuration mismatch")
	}
	if err := m.ready(ctx); err != nil {
		return p, err
	}
	addresses, err := m.addresses(ctx, []uint32{o.MoneroIndex})
	if err != nil {
		return p, err
	}
	if len(addresses) != 1 || addresses[0].Address != o.Recipient || addresses[0].Index != o.MoneroIndex {
		return p, errors.New("Monero wallet does not contain the order subaddress")
	}
	var transfers struct{ In, Pool []moneroTransfer }
	if err = m.call(ctx, false, "get_transfers", map[string]any{"in": true, "pool": true, "account_index": o.MoneroAccount, "subaddr_indices": []uint32{o.MoneroIndex}}, &transfers); err != nil {
		return p, err
	}
	for id, seen := range o.MoneroSeen {
		p.Seen[id] = seen
	}
	if len(transfers.In)+len(transfers.Pool) > 100 {
		return p, errors.New("Monero payment needs operator review")
	}
	seen := map[string]bool{}
	for _, t := range append(transfers.In, transfers.Pool...) {
		if t.Address != o.Recipient || t.Index.Major != o.MoneroAccount || t.Index.Minor != o.MoneroIndex || (t.Type != "in" && t.Type != "pool") {
			continue
		}
		if !moneroTxID.MatchString(t.TxID) || t.Confirmations < 0 {
			return p, errors.New("Invalid Monero transfer")
		}
		if t.Amount == 0 || seen[t.TxID] {
			continue
		}
		seen[t.TxID] = true
		amount := new(big.Int).SetUint64(t.Amount)
		p.Observed.Add(p.Observed, amount)
		if t.DoubleSpend || t.UnlockTime != 0 {
			p.Unsafe = true
			continue
		}
		first, ok := p.Seen[t.TxID]
		if !ok {
			first = time.Now().UTC()
			p.Seen[t.TxID] = first
		}
		if first.After(o.Expires) {
			p.Late = true
			continue
		}
		confirmations := t.Confirmations
		if t.Type == "pool" || t.Height == 0 {
			confirmations = 0
		}
		if p.Received.Sign() == 0 || confirmations < p.Confirmations {
			p.Confirmations = confirmations
		}
		p.Received.Add(p.Received, amount)
		if t.Type == "in" && t.Height > 0 && t.Locked != nil && !*t.Locked && confirmations >= o.MoneroRequired {
			p.Confirmed.Add(p.Confirmed, amount)
			p.TxIDs = append(p.TxIDs, t.TxID)
		}
	}
	return p, nil
}
func (s *Server) prepareMoneroAddress(ctx context.Context, o Order) Order {
	o.NextCheck = time.Now().Add(time.Minute)
	if s.monero == nil {
		o.Error = "Monero payments are unavailable"
		_ = s.save(o)
		return o
	}
	address, err := s.monero.address(ctx, o.ID)
	if err != nil {
		o.Error = "Cannot prepare a Monero payment address; the store will retry"
		_ = s.save(o)
		return o
	}
	o.Recipient = address.Address
	o.MoneroIndex = address.Index
	o.Status = "awaiting_payment"
	o.Error = ""
	o.NextCheck = time.Now()
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("monero-addresses"))
		if err != nil {
			return err
		}
		key := []byte(o.MoneroNetwork + ":" + address.Address)
		if assigned := bucket.Get(key); assigned != nil && string(assigned) != o.ID {
			return errors.New("Monero subaddress is assigned to another order")
		}
		if err = bucket.Put(key, []byte(o.ID)); err != nil {
			return err
		}
		return putOrder(tx, o)
	})
	if err != nil {
		s.LogError("Monero address persistence failed", err)
		o.Status = "awaiting_address"
		o.Recipient = ""
		o.Error = "Cannot save Monero payment address; the store will retry"
		_ = s.save(o)
	}
	return o
}
func (s *Server) checkMoneroPayment(ctx context.Context, o Order) {
	o.Checks++
	o.NextCheck = time.Now().Add(30 * time.Second)
	if s.monero == nil {
		o.Error = "Monero payments are unavailable; contact the store"
		_ = s.save(o)
		return
	}
	p, err := s.monero.payment(ctx, o)
	if err != nil {
		o.Error = "Monero wallet or node is unavailable or synchronizing; payment verification will retry"
		_ = s.save(o)
		return
	}
	o.Error = ""
	o.MoneroReceived = p.Observed.String()
	o.MoneroConfirmations = p.Confirmations
	o.MoneroSeen = p.Seen
	due, ok := new(big.Int).SetString(o.Amount, 10)
	if !ok || due.Sign() <= 0 {
		o.Status = "review_payment"
		o.Error = "Monero quote needs operator review"
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
		if p.Late || p.Unsafe || (time.Now().After(o.Expires) && p.Received.Sign() > 0 && p.Received.Cmp(due) < 0) {
			o.Status = "review_payment"
			o.Error = "Monero payment is late, insufficient, locked by its sender, or conflicted; contact the store"
		}
		_ = s.save(o)
		return
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("payments"))
		for _, id := range p.TxIDs {
			key := []byte("monero:" + o.MoneroNetwork + ":" + id + ":" + o.Recipient)
			if used := bucket.Get(key); used != nil && string(used) != o.ID {
				o.Status = "review_payment"
				o.Error = "Monero payment is assigned to another order"
				return putOrder(tx, o)
			}
		}
		for _, id := range p.TxIDs {
			if err := bucket.Put([]byte("monero:"+o.MoneroNetwork+":"+id+":"+o.Recipient), []byte(o.ID)); err != nil {
				return err
			}
		}
		sort.Strings(p.TxIDs)
		o.MoneroTxIDs = p.TxIDs
		o.TxHash = p.TxIDs[0]
		o.Status = "paid"
		o.PaidAt = time.Now().UTC()
		o.ReceiptNext = o.PaidAt
		o.NextCheck = time.Now()
		o.Checks = 0
		return putOrder(tx, o)
	})
	if err != nil {
		s.LogError("Monero payment persistence failed", err)
	}
}
