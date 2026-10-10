package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/mail"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Native-Planet/perigee/roller"
	"github.com/ethereum/go-ethereum/crypto"
	bolt "go.etcd.io/bbolt"
)

type Server struct {
	nock        *Nock
	monero      *Monero
	bitcoin     *Bitcoin
	cfg         Config
	db          *bolt.DB
	auth        *auth
	chain       *ChainReader
	inventoryMu sync.Mutex
	workerMu    sync.Mutex
	http        *http.Client
	roller      *roller.Roller
	checkoutMu  sync.Mutex
}

func New(c Config) (*Server, error) {
	db, e := openDB(c.DBPath)
	if e != nil {
		return nil, e
	}
	s := &Server{cfg: c, db: db, auth: newAuth(), roller: roller.Client, http: &http.Client{Timeout: 25 * time.Second}}
	s.chain = &ChainReader{HTTP: s.http}
	if c.Bitcoin.RPC != "" {
		s.bitcoin = &Bitcoin{cfg: c.Bitcoin, http: s.http}
	}
	if c.Monero.WalletRPC != "" {
		s.monero = newMonero(c.Monero, s.http)
	}
	if c.Nock.BaseEnabled || c.Nock.Zpub != "" {
		s.nock, e = newNock(c.Nock)
		if e != nil {
			db.Close()
			return nil, e
		}
	}
	if e = s.bindDatabase(); e != nil {
		db.Close()
		return nil, e
	}
	if e = s.migratePlanetCodes(); e != nil {
		db.Close()
		return nil, e
	}
	if c.Demo {
		e = s.seedDemo()
	} else {
		key, e := crypto.HexToECDSA(c.SpawnKey)
		if e != nil {
			db.Close()
			return nil, errors.New("invalid spawn key")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p, e := roller.Client.GetPoint(ctx, c.Star)
		if e != nil {
			db.Close()
			return nil, fmt.Errorf("read star: %w", e)
		}
		a := crypto.PubkeyToAddress(key.PublicKey).Hex()
		if !strings.EqualFold(a, p.Ownership.Owner.Address) && !strings.EqualFold(a, p.Ownership.SpawnProxy.Address) {
			db.Close()
			return nil, errors.New("key is not the star owner or spawn proxy; management alone cannot spawn")
		}
		e = s.refreshInventory(ctx)
		if e != nil {
			db.Close()
			return nil, e
		}
	}
	if e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Server) Close() error { return s.db.Close() }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := s.db.View(func(tx *bolt.Tx) error { return nil }); e != nil {
			fail(w, 503, "Database unavailable")
			return
		}
		respond(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/config", s.publicConfig)
	mux.HandleFunc("GET /api/planets", s.planets)
	mux.HandleFunc("POST /api/auth/challenge", s.challenge)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/session", func(w http.ResponseWriter, r *http.Request) {
		v, ok := s.current(r)
		if !ok {
			fail(w, 401, "Sign in to continue")
			return
		}
		respond(w, vPublic(v))
	})
	mux.HandleFunc("POST /api/auth/logout", s.protect(false, func(w http.ResponseWriter, r *http.Request) {
		s.auth.mu.Lock()
		if c, e := r.Cookie("netsub_session"); e == nil {
			delete(s.auth.sessions, c.Value)
		}
		s.auth.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "netsub_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(s.requestOrigin(r), "https:"), SameSite: http.SameSiteStrictMode})
		respond(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/orders", s.protect(false, s.createOrder))
	mux.HandleFunc("GET /api/orders", s.protect(false, s.orders))
	mux.HandleFunc("POST /api/orders/{id}/payment", s.protect(false, s.submitPayment))
	mux.HandleFunc("GET /api/orders/{id}/planet-code", s.protect(false, s.planetCode))
	mux.HandleFunc("GET /api/admin/events", s.protect(true, s.events))
	mux.HandleFunc("POST /api/admin/orders/{id}/receipt", s.protect(true, s.retryReceipt))
	mux.HandleFunc("GET /api/admin", s.protect(true, s.admin))
	mux.HandleFunc("PUT /api/admin/settings", s.protect(true, s.saveSettings))
	mux.HandleFunc("POST /api/admin/inventory", s.protect(true, func(w http.ResponseWriter, r *http.Request) {
		if e := s.refreshInventory(r.Context()); e != nil {
			fail(w, 502, "Inventory refresh failed; check star and gonetia configuration")
			return
		}
		respond(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/admin/orders/{id}/recheck", s.protect(true, s.recheck))
	mux.HandleFunc("POST /api/admin/orders/{id}/email", s.protect(true, s.retryEmail))
	mux.HandleFunc("POST /api/admin/orders/{id}/release", s.protect(true, s.releaseOrder))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "Not found") })
	files := http.FileServer(http.Dir(s.cfg.WebDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/admin" {
			http.ServeFile(w, r, filepath.Join(s.cfg.WebDir, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/JetBrainsMonoReg.woff2" {
			files.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return s.auditRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://*.walletconnect.com https://*.reown.com; connect-src 'self' https://*.walletconnect.com wss://*.walletconnect.com https://*.walletconnect.org wss://*.walletconnect.org https://*.reown.com wss://*.reown.com; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(s.requestOrigin(r), "https:") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if !s.allowedOrigin(r.Header.Get("Origin"), r) {
				fail(w, 403, "Origin rejected")
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, 415, "JSON required")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.auth.allow("api:"+ip(r), 240, time.Minute) {
			fail(w, 429, "Too many requests")
			return
		}
		mux.ServeHTTP(w, r)
	}))
}
func (s *Server) protect(admin bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := s.current(r)
		if !ok {
			fail(w, 401, "Sign in to continue")
			return
		}
		if admin && !v.Admin || !admin && v.Wallet == "" && r.URL.Path != "/api/auth/logout" {
			fail(w, 403, "Access denied")
			return
		}
		if r.Method != "GET" && r.Header.Get("X-CSRF-Token") != v.CSRF {
			fail(w, 403, "Invalid session token")
			return
		}
		h(w, r)
	}
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, n int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(n)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		fail(w, 400, "Invalid request")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "Invalid request")
		return false
	}
	return true
}
func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) {
	var cfg Settings
	_ = s.db.View(func(tx *bolt.Tx) error { cfg = settings(tx); return nil })
	chains := []Chain{s.cfg.Chains[1], s.cfg.Chains[8453]}
	respond(w, map[string]any{"adminLogin": map[string]bool{"wallet": s.cfg.AdminWallet != "", "password": s.cfg.AdminHash != ""}, "star": s.cfg.Star, "demo": s.cfg.Demo, "open": cfg.Open && !s.cfg.Demo, "prices": cfg.Prices, "chains": chains, "walletConnectId": s.cfg.WalletConnectID, "randomUsdc": "5", "nockchain": map[string]any{"enabled": s.nock != nil && s.cfg.Nock.Zpub != "", "confirmations": s.cfg.Nock.Confirmations}, "baseNock": map[string]any{"enabled": s.nock != nil && s.cfg.Nock.BaseEnabled, "contract": baseNockToken, "decimals": 16}, "monero": map[string]any{"enabled": s.monero != nil, "network": s.cfg.Monero.Network, "confirmations": s.cfg.Monero.Confirmations}, "bitcoin": map[string]any{"enabled": s.bitcoin != nil, "network": s.cfg.Bitcoin.Network, "confirmations": s.cfg.Bitcoin.Confirmations}})
}
func (s *Server) planets(w http.ResponseWriter, r *http.Request) {
	var planets []Planet
	e := s.db.View(func(tx *bolt.Tx) error { var e error; planets, e = s.available(tx); return e })
	if e != nil {
		fail(w, 500, "Cannot read inventory")
		return
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	filter := r.URL.Query().Get("filter")
	filtered := []Planet{}
	for _, p := range planets {
		if !strings.Contains(p.Name, q) || filter != "" && filter != "all" && p.Tier != filter {
			continue
		}
		filtered = append(filtered, p)
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + 48
	if end > len(filtered) {
		end = len(filtered)
	}
	respond(w, map[string]any{"planets": filtered[offset:end], "total": len(filtered)})
}
func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	v, _ := s.current(r)
	if s.cfg.Demo {
		fail(w, 403, "Preview mode does not accept payments")
		return
	}
	if !s.auth.allow("order:"+v.Wallet, 6, time.Hour) || !s.auth.allow("order-ip:"+ip(r), 20, time.Hour) {
		fail(w, 429, "Too many checkouts; use your existing order")
		return
	}
	var in struct {
		Planet  string `json:"planet"`
		Random  bool   `json:"random"`
		Email   string `json:"email"`
		ChainID int    `json:"chainId"`
		Asset   string `json:"asset"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	email, e := mail.ParseAddress(in.Email)
	if e != nil || email.Address != in.Email || len(in.Email) > 254 {
		fail(w, 400, "Enter a valid email address")
		return
	}
	_, evmNetwork := s.cfg.Chains[in.ChainID]
	supported := evmNetwork && (in.Asset == "ETH" || in.Asset == "USDC")
	if in.Asset == "BTC" {
		supported = s.bitcoin != nil && in.ChainID == 0
	}
	if in.Asset == "XMR" {
		supported = s.monero != nil && in.ChainID == 0
	}
	if in.Asset == "NOCK" {
		supported = s.nock != nil && s.cfg.Nock.Zpub != "" && in.ChainID == 0
	}
	if in.Asset == "NOCK_BASE" {
		supported = s.nock != nil && s.cfg.Nock.BaseEnabled && in.ChainID == 8453 && evmNetwork
	}
	if !supported {
		fail(w, 400, "Unsupported payment option")
		return
	}
	if in.Random && (in.Planet != "" || in.Asset == "ETH") {
		fail(w, 400, "Random planets cost $5; select an available currency other than ETH without a name")
		return
	}
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	checkCtx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	// Refresh the catalog periodically and consult spawned state at every checkout.
	var updated time.Time
	_ = s.db.View(func(tx *bolt.Tx) error {
		updated, _ = time.Parse(time.RFC3339, string(tx.Bucket([]byte("inventory")).Get([]byte("updated"))))
		return nil
	})
	if time.Since(updated) > time.Minute {
		if e = s.refreshInventory(checkCtx); e != nil {
			fail(w, 503, "Cannot verify planet availability")
			return
		}
	}
	if e := s.refreshSpawned(checkCtx); e != nil {
		fail(w, 503, "Cannot verify planet availability")
		return
	}
	var rate string
	if in.Asset == "BTC" {
		if err := s.bitcoin.ready(checkCtx); err != nil {
			fail(w, 503, "Bitcoin node is unavailable or synchronizing")
			return
		}
		rate, e = s.bitcoin.usdRate(checkCtx)
		if e != nil {
			fail(w, 503, "Cannot quote a Bitcoin price; try again later")
			return
		}
	}
	if in.Asset == "XMR" {
		if err := s.monero.ready(checkCtx); err != nil {
			fail(w, 503, "Monero wallet or node is unavailable or synchronizing")
			return
		}
		rate, e = s.monero.usdRate(checkCtx)
		if e != nil {
			fail(w, 503, "Cannot quote a Monero price; try again later")
			return
		}
	}
	var order Order
	if in.Asset == "NOCK" || in.Asset == "NOCK_BASE" {
		if in.Asset == "NOCK" {
			if _, err := s.nock.tip(checkCtx); err != nil {
				fail(w, 503, "Nockchain index is unavailable or synchronizing")
				return
			}
		}
		rate, e = s.nock.usdRate(checkCtx)
		if e != nil {
			fail(w, 503, "Cannot quote a NOCK price; try again later")
			return
		}
	}
	e = s.db.Update(func(tx *bolt.Tx) error {
		cfg := settings(tx)
		if !cfg.Open {
			return errors.New("Store is paused")
		}
		if e := validSettings(cfg); e != nil {
			return errors.New("Store configuration incomplete")
		}
		orders, e := allOrders(tx)
		if e != nil {
			return e
		}
		active := 0
		for _, o := range orders {
			if o.Wallet == v.Wallet && (o.Status == "awaiting_address" || o.Status == "awaiting_payment" || o.Status == "confirming") {
				active++
			}
		}
		if active >= 3 {
			return errors.New("Finish an existing checkout first")
		}
		available, e := s.available(tx)
		if e != nil {
			return e
		}
		var chosen *Planet
		candidates := []Planet{}
		for _, p := range available {
			if in.Random && p.Tier == "standard" {
				candidates = append(candidates, p)
			}
			if !in.Random && p.Name == in.Planet {
				copy := p
				chosen = &copy
				break
			}
		}
		if in.Random && len(candidates) > 0 {
			index, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
			if err != nil {
				return err
			}
			chosen = &candidates[index.Int64()]
		}
		if chosen == nil {
			return errors.New("Planet is no longer available")
		}
		p := cfg.Prices.ETH
		d := 18
		if in.Asset != "ETH" {
			p = cfg.Prices.USDC
			d = 6
		}
		if chosen.Tier == "rare" {
			if in.Asset != "ETH" {
				p = cfg.Prices.RareUSDC
			} else {
				p = cfg.Prices.RareETH
			}
		}
		if chosen.Tier == "unique" {
			if in.Asset != "ETH" {
				p = cfg.Prices.UniqueUSDC
			} else {
				p = cfg.Prices.UniqueETH
			}
		}
		if chosen.Tier == "premium" {
			if in.Asset != "ETH" {
				p = cfg.Prices.PremiumUSDC
			} else {
				p = cfg.Prices.PremiumETH
			}
		}
		if chosen.NameAndSigil {
			if in.Asset != "ETH" {
				p = cfg.Prices.PremiumSigilUSDC
			} else {
				p = cfg.Prices.PremiumSigilETH
			}
		}
		if in.Random {
			p = "5"
			d = 6
		}
		amount, e := units(p, d)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		order = Order{ID: token(), Wallet: v.Wallet, Email: in.Email, Planet: chosen.Name, Tier: chosen.Tier, NameAndSigil: chosen.NameAndSigil, Random: in.Random, ChainID: in.ChainID, Asset: in.Asset, Amount: amount.String(), Recipient: cfg.Recipient, Created: now, Expires: now.Add(20 * time.Minute), Status: "awaiting_payment"}
		if in.Asset == "BTC" {
			quote, err := bitcoinQuote(p, rate)
			if err != nil {
				return err
			}
			order.Amount = quote
			order.Status = "awaiting_address"
			order.Recipient = ""
			order.BitcoinRate = rate
			order.BitcoinNetwork = s.cfg.Bitcoin.Network
			order.BitcoinRequired = s.cfg.Bitcoin.Confirmations
		}
		if in.Asset == "XMR" {
			quote, err := moneroQuote(p, rate)
			if err != nil {
				return err
			}
			order.Amount = quote
			order.Status = "awaiting_address"
			order.Recipient = ""
			order.MoneroRate = rate
			order.MoneroNetwork = s.cfg.Monero.Network
			order.MoneroRequired = s.cfg.Monero.Confirmations
			order.MoneroAccount = s.cfg.Monero.Account
		}
		if in.Asset == "NOCK" || in.Asset == "NOCK_BASE" {
			quote, err := nockQuote(p, rate, in.Asset == "NOCK_BASE")
			if err != nil {
				return err
			}
			order.Amount = quote
			order.NockRate = rate
			if in.Asset == "NOCK" {
				order.NockRequired = s.cfg.Nock.Confirmations
				if err = s.nock.allocate(tx, &order); err != nil {
					return err
				}
			}
		}
		return putOrder(tx, order)
	})
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	if order.Asset == "BTC" || order.Asset == "XMR" {
		s.workerMu.Lock()
		// The worker may already prepare this persisted reservation while the handler waits.
		_ = s.db.View(func(tx *bolt.Tx) error {
			current, err := getOrder(tx, order.ID)
			if err == nil {
				order = current
			}
			return err
		})
		if order.Status == "awaiting_address" {
			if order.Asset == "XMR" {
				order = s.prepareMoneroAddress(checkCtx, order)
			} else {
				order = s.prepareBitcoinAddress(checkCtx, order)
			}
		}
		s.workerMu.Unlock()
	}
	respond(w, order)
}
func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	v, _ := s.current(r)
	out := []Order{}
	e := s.db.View(func(tx *bolt.Tx) error {
		all, e := allOrders(tx)
		for _, o := range all {
			if o.Wallet == v.Wallet {
				out = append(out, o)
			}
		}
		return e
	})
	if e != nil {
		fail(w, 500, "Cannot read orders")
		return
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	respond(w, out)
}

var txPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

func (s *Server) submitPayment(w http.ResponseWriter, r *http.Request) {
	v, _ := s.current(r)
	if !s.auth.allow("payment-submit:"+v.Wallet, 12, time.Hour) {
		fail(w, 429, "Too many transaction submissions; contact the store")
		return
	}
	var in struct {
		Hash string `json:"hash"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !txPattern.MatchString(in.Hash) {
		fail(w, 400, "Invalid transaction hash")
		return
	}
	in.Hash = strings.ToLower(in.Hash)
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	var o Order
	e := s.db.Update(func(tx *bolt.Tx) error {
		var e error
		o, e = getOrder(tx, r.PathValue("id"))
		if e != nil || o.Wallet != v.Wallet {
			return errors.New("Order not found")
		}
		if addressPaymentAsset(o.Asset) {
			return errors.New("Payments are detected automatically for this currency")
		}
		if o.TxHash == in.Hash {
			return nil
		}
		if o.Status != "awaiting_payment" && o.Status != "confirming" && o.Status != "review_payment" {
			return errors.New("Order cannot accept a transaction")
		}
		if time.Now().After(o.Expires.Add(24 * time.Hour)) {
			return errors.New("Payment submission window ended; contact the store")
		}
		key := fmt.Sprintf("%d:%s", o.ChainID, in.Hash)
		b := tx.Bucket([]byte("payments"))
		if id := b.Get([]byte(key)); id != nil && string(id) != o.ID {
			return errors.New("Transaction is assigned to another order")
		}
		o.TxHash = in.Hash
		o.Status = "confirming"
		o.Checks = 0
		o.Error = ""
		o.NextCheck = time.Now()
		return putOrder(tx, o)
	})
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	respond(w, o)
}
func (s *Server) planetCode(w http.ResponseWriter, r *http.Request) {
	v, _ := s.current(r)
	var o Order
	e := s.db.View(func(tx *bolt.Tx) error { var e error; o, e = getOrder(tx, r.PathValue("id")); return e })
	if e != nil || o.Wallet != v.Wallet {
		fail(w, 404, "Order not found")
		return
	}
	if o.Status != "delivered" {
		fail(w, 409, "Planet is still being prepared")
		return
	}
	c, e := decrypt(s.cfg.EncryptionKey, o.ID, o.Secret)
	if e != nil {
		fail(w, 500, "Cannot unlock planet code")
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, strings.TrimPrefix(o.Planet, "~")))
	}
	respond(w, c)
}
func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	var cfg Settings
	var orders []Order
	var count int
	var updated string
	var available []Planet
	pendingSpawns := 0
	e := s.db.View(func(tx *bolt.Tx) error {
		cfg = settings(tx)
		var e error
		orders, e = allOrders(tx)
		if e != nil {
			return e
		}
		a, e := s.available(tx)
		count = len(a)
		available = a
		if err := tx.Bucket([]byte("spawns")).ForEach(func(_, v []byte) error {
			var record SpawnRecord
			if err := json.Unmarshal(v, &record); err != nil {
				return err
			}
			if record.OrderID != "" && record.ObservedAt.IsZero() {
				pendingSpawns++
			}
			return nil
		}); err != nil {
			return err
		}
		updated = string(tx.Bucket([]byte("inventory")).Get([]byte("updated")))
		return e
	})
	if e != nil {
		fail(w, 500, "Cannot read store")
		return
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].Created.After(orders[j].Created) })
	counts, err := s.categoryCounts(cfg, available)
	if err != nil {
		fail(w, 500, "Cannot read name categories")
		return
	}
	respond(w, map[string]any{"categories": counts, "pendingSpawns": pendingSpawns, "receiptEmail": s.cfg.ReceiptTo, "settings": cfg, "orders": orders, "available": count, "inventoryUpdated": updated, "demo": s.cfg.Demo, "sendgridConfigured": s.cfg.SendGridKey != "", "rarityConfigured": s.cfg.GonetiaDir != ""})
}
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var cfg Settings
	if !readJSON(w, r, &cfg) {
		return
	}
	if e := validSettings(cfg); e != nil {
		fail(w, 400, e.Error())
		return
	}
	e := s.db.Update(func(tx *bolt.Tx) error {
		b, e := json.Marshal(cfg)
		if e != nil {
			return e
		}
		return tx.Bucket([]byte("settings")).Put([]byte("store"), b)
	})
	if e != nil {
		fail(w, 500, "Could not save settings")
		return
	}
	respond(w, cfg)
}
func (s *Server) recheck(w http.ResponseWriter, r *http.Request) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	e := s.db.Update(func(tx *bolt.Tx) error {
		o, e := getOrder(tx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if o.Status == "review_payment" {
			o.Status = "confirming"
		} else if o.Status == "review_fulfillment" {
			o.Status = "prepared"
		} else if !strings.HasSuffix(o.Status, "_submitted") {
			return errors.New("Only pending payment or fulfillment can be rechecked")
		}
		o.Checks = 0
		o.NextCheck = time.Now()
		o.Error = ""
		return putOrder(tx, o)
	})
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	respond(w, map[string]bool{"ok": true})
}
func (s *Server) retryEmail(w http.ResponseWriter, r *http.Request) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	e := s.db.Update(func(tx *bolt.Tx) error {
		o, e := getOrder(tx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if o.Status != "delivered" {
			return errors.New("Planet is not ready")
		}
		o.EmailSent = false
		o.EmailAttempts = 0
		o.EmailNext = time.Now()
		return putOrder(tx, o)
	})
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	respond(w, map[string]bool{"ok": true})
}
func (s *Server) seedDemo() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("meta")).Get([]byte("demo")) != nil {
			return nil
		}
		if tx.Bucket([]byte("orders")).Stats().KeyN > 0 {
			return errors.New("use a separate database for preview")
		}
		list := []Planet{{Name: "~sampel-palnet"}, {Name: "~sampel-sampel", Tags: []string{"doubles"}}, {Name: "~ravmel-ropdyl"}, {Name: "~mister-todteg", Tags: []string{"any_english"}}, {Name: "~sorreg-namtyv", Tags: []string{"sigils"}}, {Name: "~daplen-liswyd", Tags: []string{"only_approx", "sigils"}}}
		b, _ := json.Marshal(list)
		if e := tx.Bucket([]byte("inventory")).Put([]byte("list"), b); e != nil {
			return e
		}
		return tx.Bucket([]byte("meta")).Put([]byte("demo"), []byte("true"))
	})
}
func (s *Server) LogError(message string, err error) { slog.Error(message, "error", err) }

func (s *Server) releaseOrder(w http.ResponseWriter, r *http.Request) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	e := s.db.Update(func(tx *bolt.Tx) error {
		o, e := getOrder(tx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if o.Asset == "NOCK" && o.NockReceived != "" && o.NockReceived != "0" {
			return errors.New("Nockchain payment is recorded; resolve the payment before releasing the order")
		}
		if o.Asset == "XMR" && o.MoneroReceived != "" && o.MoneroReceived != "0" {
			return errors.New("Monero payment is recorded; resolve the payment before releasing the order")
		}
		if o.Asset == "BTC" && o.BitcoinReceived != "" && o.BitcoinReceived != "0" {
			return errors.New("Bitcoin payment is recorded; resolve the payment before releasing the order")
		}
		if (o.Status != "review_payment" && o.Status != "awaiting_payment") || o.Secret != "" || time.Now().Before(o.Expires.Add(24*time.Hour)) {
			return errors.New("Only unpaid orders past the 24-hour recovery window can be released")
		}
		o.Status = "expired"
		o.Error = "Unpaid order closed by the store"
		return putOrder(tx, o)
	})
	if e != nil {
		fail(w, 409, e.Error())
		return
	}
	respond(w, map[string]bool{"ok": true})
}
func (s *Server) bindDatabase() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("meta"))
		mode := "live"
		if s.cfg.Demo {
			mode = "demo"
		}
		for k, v := range map[string]string{"mode": mode, "star": s.cfg.Star} {
			if old := b.Get([]byte(k)); old != nil && string(old) != v {
				return fmt.Errorf("database belongs to a different %s; use a separate database", k)
			}
			if e := b.Put([]byte(k), []byte(v)); e != nil {
				return e
			}
		}
		if check := b.Get([]byte("key-check")); check != nil {
			if _, e := decryptPayload(s.cfg.EncryptionKey, "key-check", string(check)); e != nil {
				return errors.New("DATA_ENCRYPTION_KEY does not unlock this database")
			}
			return nil
		}
		check, e := encrypt(s.cfg.EncryptionKey, "key-check", map[string]string{"purpose": "key-check"})
		if e != nil {
			return e
		}
		return b.Put([]byte("key-check"), []byte(check))
	})
}
