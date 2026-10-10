package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Native-Planet/perigee/roller"
	"github.com/Native-Planet/perigee/types"
	"github.com/deelawn/urbit-gob/co"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	bolt "go.etcd.io/bbolt"
)

func TestCatalogMigrationPreservesPurchases(t *testing.T) {
	s := testServer(t)
	err := s.db.Update(func(tx *bolt.Tx) error {
		tx.Bucket([]byte("meta")).Delete([]byte("catalog-three-tiers"))
		tx.Bucket([]byte("settings")).Put([]byte("store"), []byte(`{"open":true,"recipient":"`+recipient+`","prices":{"eth":"0.003","usdc":"10","rareEth":"0.009","rareUsdc":"30"},"rareStrategies":["doubles","only_english","only_approx"]}`))
		tx.Bucket([]byte("orders")).Put([]byte("purchase"), []byte(`{"id":"purchase","rare":true,"amount":"30000000","status":"spawn_submitted","Secret":"ciphertext","Checks":7,"EmailAttempts":2}`))
		if err := migrateStore(tx); err != nil {
			return err
		}
		cfg := settings(tx)
		if !cfg.Open || cfg.Recipient != recipient || cfg.Prices.PremiumUSDC != "30" || cfg.Prices.RareUSDC != "15" || planetTier([]string{"sigils"}, cfg) != "premium" {
			t.Fatal(cfg)
		}
		o, err := getOrder(tx, "purchase")
		if err != nil {
			return err
		}
		if o.Tier != "premium" || o.Amount != "30000000" || o.Secret != "ciphertext" || o.Checks != 7 || o.EmailAttempts != 2 {
			t.Fatal("purchase state changed", o)
		}
		before := string(tx.Bucket([]byte("settings")).Get([]byte("store")))
		if err := migrateStore(tx); err != nil {
			return err
		}
		if string(tx.Bucket([]byte("settings")).Get([]byte("store"))) != before {
			t.Fatal("migration repeats")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRandomCheckoutUsesOnlyStandardAndReservesOnce(t *testing.T) {
	s := testServer(t)
	list := []Planet{{Name: "~sampel-palnet"}, {Name: "~sampel-sampel", Tags: []string{"doubles"}}, {Name: "~ravmel-ropdyl", Tags: []string{"any_english"}}}
	s.db.Update(func(tx *bolt.Tx) error {
		b, _ := json.Marshal(list)
		return tx.Bucket([]byte("inventory")).Put([]byte("list"), b)
	})
	for _, body := range []string{`{"random":true,"planet":"~sampel-sampel","email":"buyer@example.com","chainId":8453,"asset":"USDC"}`} {
		if w := request(s, "POST", "/api/orders", body, "buyer"); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(s, "POST", "/api/orders", `{"random":true,"email":"buyer@example.com","chainId":8453,"asset":"USDC"}`, "buyer")
		}()
	}
	wg.Wait()
	close(results)
	successful := 0
	for w := range results {
		if w.Code == 200 {
			successful++
			var o Order
			json.Unmarshal(w.Body.Bytes(), &o)
			if o.Planet != "~sampel-palnet" || o.Tier != "standard" || !o.Random || o.Amount != "5000000" {
				t.Fatal(o)
			}
		} else if w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if successful != 1 {
		t.Fatal("duplicate reservation", successful)
	}
	if planetTier([]string{"any_english", "sigils"}, Settings{RareStrategies: []string{"any_english"}, PremiumStrategies: []string{"sigils"}}) != "premium" {
		t.Fatal("tier precedence")
	}
}

func TestSpawnAvailabilityPersistsAcrossRestart(t *testing.T) {
	s := testServer(t)
	s.cfg.Star = "~matwet"
	// A persisted submission excludes the name even when the roller returns no spawned points.
	const planet = "~sampel-palnet"
	o := Order{ID: "pending", Planet: planet, Status: "spawn_submitted", StageAt: time.Now()}
	if err := s.save(o); err != nil {
		t.Fatal(err)
	}
	if err := s.verifyUnspawned(context.Background(), planet); err != errSpawned {
		t.Fatal(err)
	}
	path := s.cfg.DBPath
	s.db.Close()
	var err error
	s.db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Update(func(tx *bolt.Tx) error { o.Status = "expired"; return putOrder(tx, o) })
	s.db.View(func(tx *bolt.Tx) error {
		list, err := s.available(tx)
		if err != nil {
			return err
		}
		for _, p := range list {
			if p.Name == planet {
				t.Fatal("pending spawn sold again")
			}
		}
		return nil
	})
	s.db.Update(func(tx *bolt.Tx) error { return rememberSpawned(tx, []string{planet}) })
	s.db.View(func(tx *bolt.Tx) error {
		var record SpawnRecord
		json.Unmarshal(tx.Bucket([]byte("spawns")).Get([]byte(planet)), &record)
		if record.OrderID != "pending" || record.ObservedAt.IsZero() || record.SubmittedAt.IsZero() {
			t.Fatal(record)
		}
		return nil
	})
}

func TestCheckoutRejectsRollerFailure(t *testing.T) {
	s := testServer(t)
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer rpc.Close()
	s.roller = roller.New(roller.Config{Endpoint: rpc.URL})
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":8453,"asset":"USDC"}`, "buyer")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.db.View(func(tx *bolt.Tx) error {
		orders, _ := allOrders(tx)
		if len(orders) != 0 {
			t.Fatal("unverified checkout reserved")
		}
		return nil
	})
}

func TestReceiptRecipientsRetryAndAuditRedaction(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	o.PaidAt = time.Now().UTC()
	o.Status = "paid"
	o.TxHash = hash
	o.Secret = "private-ciphertext"
	s.save(o)
	attempt := 0
	s.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		attempt++
		saved := readOrder(t, s, o.ID)
		if saved.ReceiptAttempts != attempt || saved.ReceiptNext.IsZero() {
			t.Fatal("attempt not durable before send")
		}
		b, _ := io.ReadAll(r.Body)
		var payload struct {
			Personalizations []struct{ To []struct{ Email string } }
			Attachments      any
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Personalizations) != 2 || payload.Personalizations[0].To[0].Email != o.Email || payload.Personalizations[1].To[0].Email != "matwet@subject.network" || payload.Attachments != nil {
			t.Fatal(string(b))
		}
		if strings.Contains(string(b), o.Secret) || !strings.Contains(string(b), "10 USDC") || !strings.Contains(string(b), hash) {
			t.Fatal("incorrect receipt")
		}
		status := 202
		if attempt == 1 {
			status = 503
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("provider-sensitive-response"))}, nil
	})}
	s.receipt(context.Background(), o)
	failed := readOrder(t, s, o.ID)
	if failed.ReceiptSent || failed.ReceiptError == "" || failed.ReceiptAttempts != 1 {
		t.Fatal(failed)
	}
	s.receipt(context.Background(), failed)
	done := readOrder(t, s, o.ID)
	if !done.ReceiptSent || done.ReceiptError != "" || done.ReceiptAttempts != 2 {
		t.Fatal(done)
	}
	request(s, "POST", "/api/orders", `{"email":"body-secret"}`, "buyer")
	s.db.View(func(tx *bolt.Tx) error {
		started, finished := false, false
		tx.Bucket([]byte("events")).ForEach(func(_, b []byte) error {
			for _, secret := range []string{o.Secret, "body-secret", "provider-sensitive-response"} {
				if strings.Contains(string(b), secret) {
					t.Fatal("audit leaked secret")
				}
			}
			var e Event
			json.Unmarshal(b, &e)
			started = started || e.Kind == "request.started"
			finished = finished || (e.Kind == "request.finished" && e.HTTPStatus == 400)
			return nil
		})
		if !started || !finished {
			t.Fatal("rejected attempt missing")
		}
		return nil
	})
}

func TestWalletLoginOnLocalAndPublicOrigins(t *testing.T) {
	for _, origin := range []string{"http://localhost:8811", "http://127.0.0.1:8811", "https://store.test"} {
		t.Run(origin, func(t *testing.T) {
			s := testServer(t)
			key, _ := crypto.GenerateKey()
			wallet := crypto.PubkeyToAddress(key.PublicKey).Hex()
			call := func(method, path, body, requestOrigin string, cookie *http.Cookie) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
				r.Header.Set("Origin", requestOrigin)
				r.Header.Set("Content-Type", "application/json")
				if cookie != nil {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				return w
			}
			w := call("POST", "/api/auth/challenge", fmt.Sprintf(`{"address":%q}`, wallet), origin, nil)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var c map[string]string
			json.Unmarshal(w.Body.Bytes(), &c)
			if !strings.Contains(c["message"], "URI: "+origin) {
				t.Fatal(c)
			}
			sig, _ := crypto.Sign(accounts.TextHash([]byte(c["message"])), key)
			body := fmt.Sprintf(`{"challenge":%q,"signature":"0x%x"}`, c["id"], sig)
			w = call("POST", "/api/auth/login", body, origin, nil)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			cookie := w.Result().Cookies()[0]
			if cookie.Secure != strings.HasPrefix(origin, "https:") {
				t.Fatal("cookie origin mismatch")
			}
			w = call("GET", "/api/auth/session", "", "", cookie)
			if !strings.Contains(w.Body.String(), strings.ToLower(wallet)) {
				t.Fatal("session lost without Origin", w.Body.String())
			}
			if call("POST", "/api/auth/login", body, origin, nil).Code != 401 {
				t.Fatal("replay accepted")
			}
			if call("POST", "/api/auth/challenge", `{}`, "http://evil.test", nil).Code != 403 {
				t.Fatal("foreign origin accepted")
			}
			if origin != "https://store.test" {
				w = call("GET", "/api/auth/session", "", s.cfg.Origin, cookie)
				if strings.Contains(w.Body.String(), strings.ToLower(wallet)) {
					t.Fatal("session crosses origins")
				}
			}
		})
	}
	s := testServer(t)
	r := httptest.NewRequest("POST", "https://store.test/api/auth/challenge", nil)
	if s.allowedOrigin("http://localhost", r) {
		t.Fatal("localhost origin accepted on public host")
	}
}

func TestSpawnedPlanetRejectsCheckoutAndFulfillment(t *testing.T) {
	s := testServer(t)
	const planet = "~sampel-palnet"
	_, point, err := types.ValidateAndNormalizePatp(planet)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Star, err = co.Point2Patp(new(big.Int).SetUint64(uint64(point & 65535)))
	if err != nil {
		t.Fatal(err)
	}
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string
			Method string
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "getSpawned" {
			t.Error("unexpected method", req.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": []map[string]string{{"name": planet}}})
	}))
	defer rpc.Close()
	s.roller = roller.New(roller.Config{Endpoint: rpc.URL})
	w := request(s, "POST", "/api/orders", `{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":8453,"asset":"USDC"}`, "buyer")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	o := Order{ID: "prepared", Planet: planet, Status: "prepared"}
	o.Secret, err = encrypt(s.cfg.EncryptionKey, o.ID, types.PlanetCode{Planet: planet, Owner: recipient, Code: "private-planet-code"})
	if err != nil {
		t.Fatal(err)
	}
	s.save(o)
	s.fulfill(context.Background(), o)
	if saved := readOrder(t, s, o.ID); saved.Status != "review_fulfillment" || saved.Secret != o.Secret {
		t.Fatal("spawned name submitted or credentials lost", saved.Status)
	}
}

func TestUniqueTierMigrationPreservesExistingOrders(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	o.Tier = "premium"
	o.Amount = "30000000"
	o.Secret = "retained-encrypted-code"
	s.save(o)
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		meta.Delete([]byte("catalog-unique-tier"))
		cfg := settings(tx)
		cfg.PremiumStrategies = append(cfg.PremiumStrategies, "sigils")
		cfg.UniqueStrategies = nil
		cfg.Prices.UniqueUSDC = ""
		cfg.Prices.PremiumSigilUSDC = ""
		data, _ := json.Marshal(cfg)
		tx.Bucket([]byte("settings")).Put([]byte("store"), data)
		original := string(tx.Bucket([]byte("orders")).Get([]byte(o.ID)))
		if err := migrateUniqueTier(tx); err != nil {
			return err
		}
		cfg = settings(tx)
		if cfg.Prices.UniqueUSDC != "25" || cfg.Prices.PremiumSigilUSDC != "35" || cfg.Prices.PremiumUSDC != "30" || planetTier([]string{"sigils"}, cfg) != "unique" || planetTier([]string{"only_english", "sigils"}, cfg) != "premium" {
			t.Fatal(cfg)
		}
		if string(tx.Bucket([]byte("orders")).Get([]byte(o.ID))) != original {
			t.Fatal("migration changes a saved purchase")
		}
		before := string(tx.Bucket([]byte("settings")).Get([]byte("store")))
		if err := migrateUniqueTier(tx); err != nil {
			return err
		}
		if string(tx.Bucket([]byte("settings")).Get([]byte("store"))) != before {
			t.Fatal("migration repeats")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestNameAndSigilPricesAcrossCurrencies(t *testing.T) {
	for _, asset := range []string{"USDC", "ETH", "BTC", "XMR"} {
		for _, shape := range []struct {
			name string
			tags []string
			tier string
			usd  int64
			both bool
		}{
			{"sigil", []string{"sigils"}, "unique", 25, false},
			{"name", []string{"only_english"}, "premium", 30, false},
			{"name-and-sigil", []string{"only_english", "sigils"}, "premium", 35, true},
			{"rare", []string{"any_english"}, "rare", 15, false},
			{"rare-and-sigil", []string{"any_english", "sigils"}, "unique", 25, false},
		} {
			t.Run(asset+"/"+shape.name, func(t *testing.T) {
				var s *Server
				switch asset {
				case "BTC":
					s, _ = bitcoinTestServer(t)
				case "XMR":
					s, _ = moneroTestServer(t)
				default:
					s = testServer(t)
				}
				s.db.Update(func(tx *bolt.Tx) error {
					data, _ := json.Marshal([]Planet{{Name: "~sampel-palnet", Tags: shape.tags}})
					return tx.Bucket([]byte("inventory")).Put([]byte("list"), data)
				})
				chain := 8453
				if asset == "BTC" || asset == "XMR" {
					chain = 0
				}
				w := request(s, "POST", "/api/orders", fmt.Sprintf(`{"planet":"~sampel-palnet","email":"buyer@example.com","chainId":%d,"asset":%q}`, chain, asset), "buyer")
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				var o Order
				json.Unmarshal(w.Body.Bytes(), &o)
				want := fmt.Sprint(shape.usd * 1_000_000)
				switch asset {
				case "ETH":
					want = fmt.Sprint(shape.usd * 400_000_000_000_000)
				case "BTC":
					want = fmt.Sprint(shape.usd * 1000)
				case "XMR":
					want = fmt.Sprint(shape.usd * 5_000_000_000)
				}
				if o.Tier != shape.tier || o.Amount != want || o.NameAndSigil != shape.both {
					t.Fatal(o.Tier, o.Amount, o.NameAndSigil, "want", shape.tier, want, shape.both)
				}
				saved := readOrder(t, s, o.ID)
				if saved.Amount != want || saved.NameAndSigil != shape.both {
					t.Fatal("quote snapshot changed")
				}
			})
		}
	}
}

func TestRandomETHCheckoutUsesLiveUSDRate(t *testing.T) {
	for _, chain := range []int{1, 8453} {
		t.Run(fmt.Sprint(chain), func(t *testing.T) {
			s := testServer(t)
			w := request(s, "POST", "/api/orders", fmt.Sprintf(`{"random":true,"email":"buyer@example.com","chainId":%d,"asset":"ETH"}`, chain), "buyer")
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var o Order
			if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
				t.Fatal(err)
			}
			if !o.Random || o.Tier != "standard" || o.Asset != "ETH" || o.ChainID != chain || o.Amount != "2000000000000000" || o.ETHRate != "2500" {
				t.Fatal(o.Amount, o.ETHRate, o.Tier, o.Random)
			}
			if saved := readOrder(t, s, o.ID); saved.Amount != o.Amount {
				t.Fatal("quote not persisted")
			}
		})
	}
}
