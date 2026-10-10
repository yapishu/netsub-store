package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Native-Planet/perigee/types"
	"github.com/ethereum/go-ethereum/common"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	Nock                                                                                                                                   NockConfig
	Monero                                                                                                                                 MoneroConfig
	Bitcoin                                                                                                                                BitcoinConfig
	Listen, Origin, DBPath, WebDir, Star, SpawnKey, AdminHash, AdminWallet, SendGridKey, EmailFrom, WalletConnectID, GonetiaDir, ReceiptTo string
	EncryptionKey                                                                                                                          []byte
	Chains                                                                                                                                 map[int]Chain
	Demo                                                                                                                                   bool
}
type Chain struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	USDC          string `json:"usdc"`
	RPC           string `json:"-"`
	ScanKey       string `json:"-"`
	Confirmations uint64 `json:"confirmations"`
}

func FromEnv() (Config, error) {
	c := Config{Listen: env("LISTEN", "127.0.0.1:8080"), Origin: env("PUBLIC_ORIGIN", "http://localhost:8080"), DBPath: env("DB_PATH", "data/store.db"), WebDir: env("WEB_DIR", "web/dist"), Star: os.Getenv("STAR"), SpawnKey: strings.TrimPrefix(os.Getenv("SPAWN_PRIVATE_KEY"), "0x"), AdminHash: os.Getenv("ADMIN_PASSWORD_HASH"), AdminWallet: strings.TrimSpace(os.Getenv("ADMIN_WALLET_ADDRESS")), SendGridKey: os.Getenv("SENDGRID_API_KEY"), EmailFrom: os.Getenv("EMAIL_FROM"), WalletConnectID: os.Getenv("WALLETCONNECT_PROJECT_ID"), GonetiaDir: os.Getenv("GONETIA_DIR"), ReceiptTo: env("RECEIPT_EMAIL", "matwet@subject.network"), Demo: os.Getenv("DEMO") == "true"}
	var err error
	c.Bitcoin, err = bitcoinFromEnv()
	if err != nil {
		return c, err
	}
	c.Monero, err = moneroFromEnv()
	if err != nil {
		return c, err
	}
	c.Nock, err = nockFromEnv()
	if err != nil {
		return c, err
	}
	c.EncryptionKey, err = hex.DecodeString(os.Getenv("DATA_ENCRYPTION_KEY"))
	if err != nil || len(c.EncryptionKey) != 32 {
		return c, errors.New("DATA_ENCRYPTION_KEY must be 32 bytes in hex")
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) || u.Path != "" {
		return c, errors.New("PUBLIC_ORIGIN must be an HTTPS origin (HTTP allowed on localhost)")
	}
	if c.AdminWallet != "" && !address(c.AdminWallet) {
		return c, errors.New("ADMIN_WALLET_ADDRESS must be a nonzero EVM wallet address")
	}
	if c.AdminHash != "" {
		cost, hashErr := bcrypt.Cost([]byte(c.AdminHash))
		if hashErr != nil || cost < 10 {
			return c, errors.New("ADMIN_PASSWORD_HASH must be a valid bcrypt hash; use store hash-password")
		}
	}
	if c.AdminWallet == "" && c.AdminHash == "" {
		return c, errors.New("configure ADMIN_WALLET_ADDRESS or ADMIN_PASSWORD_HASH")
	}
	c.Chains = map[int]Chain{1: {ID: 1, Name: "Ethereum", USDC: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", RPC: os.Getenv("ETH_RPC_URL"), ScanKey: os.Getenv("ETHERSCAN_API_KEY"), Confirmations: 32}, 8453: {ID: 8453, Name: "Base", USDC: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", RPC: os.Getenv("BASE_RPC_URL"), ScanKey: os.Getenv("ETHERSCAN_API_KEY"), Confirmations: 1800}}
	if c.Demo {
		c.Star = "~marzod"
		return c, nil
	}
	_, n, err := types.ValidateAndNormalizePatp(c.Star)
	if err != nil || n < 256 || n >= 65536 {
		return c, errors.New("STAR must be a star @p")
	}
	c.SpawnKey, err = signingKey(c.SpawnKey, os.Getenv("SPAWN_MNEMONIC"), os.Getenv("SPAWN_MNEMONIC_PASSPHRASE"))
	if err != nil {
		return c, err
	}
	if _, err := mail.ParseAddress(c.EmailFrom); err != nil || c.SendGridKey == "" {
		return c, errors.New("SENDGRID_API_KEY and EMAIL_FROM are required")
	}
	if receipt, err := mail.ParseAddress(c.ReceiptTo); err != nil || receipt.Address != c.ReceiptTo {
		return c, errors.New("RECEIPT_EMAIL must be an email address")
	}
	for _, ch := range c.Chains {
		if ch.RPC == "" && ch.ScanKey == "" {
			return c, fmt.Errorf("configure RPC or ETHERSCAN_API_KEY for %s", ch.Name)
		}
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func address(s string) bool {
	return common.IsHexAddress(s) && common.HexToAddress(s) != (common.Address{})
}

type Prices struct {
	PremiumSigilETH  string `json:"premiumSigilEth"`
	PremiumSigilUSDC string `json:"premiumSigilUsdc"`
	UniqueETH        string `json:"uniqueEth"`
	UniqueUSDC       string `json:"uniqueUsdc"`
	ETH              string `json:"eth"`
	USDC             string `json:"usdc"`
	RareETH          string `json:"rareEth"`
	RareUSDC         string `json:"rareUsdc"`
	PremiumETH       string `json:"premiumEth"`
	PremiumUSDC      string `json:"premiumUsdc"`
}
type Settings struct {
	UniqueStrategies  []string `json:"uniqueStrategies"`
	Open              bool     `json:"open"`
	Recipient         string   `json:"recipient"`
	Prices            Prices   `json:"prices"`
	RareStrategies    []string `json:"rareStrategies"`
	PremiumStrategies []string `json:"premiumStrategies"`
}
type Planet struct {
	NameAndSigil bool     `json:"nameAndSigil"`
	Name         string   `json:"name"`
	Tags         []string `json:"tags"`
	Tier         string   `json:"tier"`
}
type Order struct {
	NockRate            string               `json:"nockRate,omitempty"`
	NockRequired        int                  `json:"nockRequired,omitempty"`
	NockConfirmations   int                  `json:"nockConfirmations,omitempty"`
	NockReceived        string               `json:"nockReceived,omitempty"`
	NockTxIDs           []string             `json:"nockTxIds,omitempty"`
	NockLockRoot        string               `json:"-"`
	NockKeyID           string               `json:"-"`
	NockIndex           uint32               `json:"-"`
	NameAndSigil        bool                 `json:"nameAndSigil"`
	MoneroNetwork       string               `json:"moneroNetwork,omitempty"`
	MoneroRate          string               `json:"moneroRate,omitempty"`
	MoneroRequired      int                  `json:"moneroRequired,omitempty"`
	MoneroConfirmations int                  `json:"moneroConfirmations,omitempty"`
	MoneroReceived      string               `json:"moneroReceived,omitempty"`
	MoneroTxIDs         []string             `json:"moneroTxIds,omitempty"`
	MoneroAccount       uint32               `json:"-"`
	MoneroIndex         uint32               `json:"-"`
	MoneroSeen          map[string]time.Time `json:"-"`

	BitcoinNetwork       string    `json:"bitcoinNetwork,omitempty"`
	BitcoinRate          string    `json:"bitcoinRate,omitempty"`
	BitcoinRequired      int       `json:"bitcoinRequired,omitempty"`
	BitcoinConfirmations int       `json:"bitcoinConfirmations,omitempty"`
	BitcoinReceived      string    `json:"bitcoinReceived,omitempty"`
	BitcoinTxIDs         []string  `json:"bitcoinTxIds,omitempty"`
	ID                   string    `json:"id"`
	Wallet               string    `json:"wallet"`
	Email                string    `json:"email"`
	Planet               string    `json:"planet"`
	Tier                 string    `json:"tier"`
	Random               bool      `json:"random"`
	ChainID              int       `json:"chainId"`
	Asset                string    `json:"asset"`
	Amount               string    `json:"amount"`
	Recipient            string    `json:"recipient"`
	Created              time.Time `json:"created"`
	Expires              time.Time `json:"expires"`
	Status               string    `json:"status"`
	TxHash               string    `json:"txHash"`
	NextCheck            time.Time `json:"-"`
	Checks               int       `json:"-"`
	Error                string    `json:"error,omitempty"`
	Secret               string    `json:"-"`
	StageAt              time.Time `json:"-"`
	EmailSent            bool      `json:"emailSent"`
	EmailAttempts        int       `json:"-"`
	EmailNext            time.Time `json:"-"`
	PaidAt               time.Time `json:"paidAt"`
	ReceiptSent          bool      `json:"receiptSent"`
	ReceiptError         string    `json:"receiptError,omitempty"`
	ReceiptAttempts      int       `json:"-"`
	ReceiptNext          time.Time `json:"-"`
}

// Persisted fields are separate from the public order representation.
type storedOrder struct {
	NockLockRoot  string
	NockKeyID     string
	NockIndex     uint32
	MoneroAccount uint32
	MoneroIndex   uint32
	MoneroSeen    map[string]time.Time
	Order
	Secret          string
	NextCheck       time.Time
	Checks          int
	StageAt         time.Time
	EmailAttempts   int
	EmailNext       time.Time
	ReceiptAttempts int
	ReceiptNext     time.Time
}

func putOrder(tx *bolt.Tx, o Order) error {
	b, e := json.Marshal(storedOrder{Order: o, NockLockRoot: o.NockLockRoot, NockKeyID: o.NockKeyID, NockIndex: o.NockIndex, MoneroAccount: o.MoneroAccount, MoneroIndex: o.MoneroIndex, MoneroSeen: o.MoneroSeen, Secret: o.Secret, NextCheck: o.NextCheck, Checks: o.Checks, StageAt: o.StageAt, EmailAttempts: o.EmailAttempts, EmailNext: o.EmailNext, ReceiptAttempts: o.ReceiptAttempts, ReceiptNext: o.ReceiptNext})
	if e != nil {
		return e
	}
	bucket := tx.Bucket([]byte("orders"))
	if bytes.Equal(bucket.Get([]byte(o.ID)), b) {
		return nil
	}
	if e := bucket.Put([]byte(o.ID), b); e != nil {
		return e
	}
	if o.Status == "spawn_submitted" || o.Status == "accept_submitted" || o.Status == "delivered" {
		if e := recordSpawnIntent(tx, o); e != nil {
			return e
		}
	}
	return appendEvent(tx, Event{Kind: "order.updated", OrderID: o.ID, Planet: o.Planet, Wallet: o.Wallet,
		NockConfirmations: o.NockConfirmations, NockReceived: o.NockReceived, NockTxIDs: o.NockTxIDs,
		Status: o.Status, Asset: o.Asset, Amount: o.Amount, ChainID: o.ChainID, TxHash: o.TxHash,
		Tier: o.Tier, NameAndSigil: o.NameAndSigil, Random: o.Random, PaymentChecks: o.Checks, EmailAttempts: o.EmailAttempts,
		ReceiptAttempts: o.ReceiptAttempts, EmailSent: o.EmailSent, ReceiptSent: o.ReceiptSent,
		Error: o.Error, ReceiptError: o.ReceiptError, BitcoinNetwork: o.BitcoinNetwork, BitcoinConfirmations: o.BitcoinConfirmations, BitcoinReceived: o.BitcoinReceived, BitcoinTxIDs: o.BitcoinTxIDs, MoneroNetwork: o.MoneroNetwork, MoneroConfirmations: o.MoneroConfirmations, MoneroReceived: o.MoneroReceived, MoneroTxIDs: o.MoneroTxIDs})
}
func decodeOrder(b []byte) (Order, error) {
	var s storedOrder
	e := json.Unmarshal(b, &s)
	s.Order.NockLockRoot = s.NockLockRoot
	s.Order.NockKeyID = s.NockKeyID
	s.Order.NockIndex = s.NockIndex
	s.Order.MoneroAccount = s.MoneroAccount
	s.Order.MoneroIndex = s.MoneroIndex
	s.Order.MoneroSeen = s.MoneroSeen
	s.Order.Secret = s.Secret
	s.Order.NextCheck = s.NextCheck
	s.Order.Checks = s.Checks
	s.Order.StageAt = s.StageAt
	s.Order.EmailAttempts = s.EmailAttempts
	s.Order.EmailNext = s.EmailNext
	s.Order.ReceiptAttempts = s.ReceiptAttempts
	s.Order.ReceiptNext = s.ReceiptNext
	return s.Order, e
}
func getOrder(tx *bolt.Tx, id string) (Order, error) {
	b := tx.Bucket([]byte("orders")).Get([]byte(id))
	if b == nil {
		return Order{}, errors.New("order not found")
	}
	return decodeOrder(b)
}
func allOrders(tx *bolt.Tx) ([]Order, error) {
	out := []Order{}
	err := tx.Bucket([]byte("orders")).ForEach(func(_, v []byte) error {
		o, e := decodeOrder(v)
		if e == nil {
			out = append(out, o)
		}
		return e
	})
	return out, err
}
func settings(tx *bolt.Tx) Settings {
	s := Settings{Prices: Prices{ETH: "0.003", USDC: "10", RareETH: "0.0045", RareUSDC: "15", PremiumETH: "0.009", PremiumUSDC: "30", UniqueETH: "0.0075", UniqueUSDC: "25", PremiumSigilETH: "0.0105", PremiumSigilUSDC: "35"}, RareStrategies: []string{"any_english", "any_approx", "alliteration"}, PremiumStrategies: []string{"only_english", "doubles", "only_approx"}, UniqueStrategies: []string{"sigils"}}
	b := tx.Bucket([]byte("settings")).Get([]byte("store"))
	if b != nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}
func validSettings(s Settings) error {
	if !address(s.Recipient) {
		return errors.New("valid payment recipient required")
	}
	for _, p := range []struct {
		v string
		d int
	}{{s.Prices.ETH, 18}, {s.Prices.USDC, 6}, {s.Prices.RareETH, 18}, {s.Prices.RareUSDC, 6}, {s.Prices.PremiumETH, 18}, {s.Prices.PremiumUSDC, 6}, {s.Prices.UniqueETH, 18}, {s.Prices.UniqueUSDC, 6}, {s.Prices.PremiumSigilETH, 18}, {s.Prices.PremiumSigilUSDC, 6}} {
		if _, e := units(p.v, p.d); e != nil {
			return e
		}
	}
	for _, v := range append(append(append([]string{}, s.RareStrategies...), s.UniqueStrategies...), s.PremiumStrategies...) {
		if !strategy(v) {
			return errors.New("unknown rarity strategy")
		}
	}
	return nil
}
func units(s string, decimals int) (*big.Int, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(s) > 40 {
		return nil, errors.New("invalid price")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > decimals {
		return nil, errors.New("price has too many decimals")
	}
	digits := parts[0] + fraction + strings.Repeat("0", decimals-len(fraction))
	for _, r := range digits {
		if r < '0' || r > '9' {
			return nil, errors.New("invalid price")
		}
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 256 {
		return nil, errors.New("price must be positive")
	}
	return n, nil
}
func openDB(path string) (*bolt.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, n := range []string{"orders", "settings", "payments", "inventory", "meta", "events", "spawns"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(n)); e != nil {
				return e
			}
		}
		if err := migrateStore(tx); err != nil {
			return err
		}
		return migrateUniqueTier(tx)
	})
	if err != nil {
		db.Close()
	}
	return db, err
}
func encrypt(key []byte, id string, c any) (string, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, b, []byte(id))), nil
}
func decryptPayload(key []byte, id, s string) ([]byte, error) {
	b, e := base64.StdEncoding.DecodeString(s)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	if len(b) < g.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	return g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte(id))
}
func decrypt(key []byte, id, secret string) (types.PlanetCode, error) {
	var c types.PlanetCode
	plain, err := decryptPayload(key, id, secret)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(plain, &c)
	return c, err
}
