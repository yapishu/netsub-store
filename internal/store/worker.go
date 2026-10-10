package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Native-Planet/perigee/libprg"
	"github.com/Native-Planet/perigee/roller"
	bolt "go.etcd.io/bbolt"
)

func (s *Server) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	inventoryTicker := time.NewTicker(time.Minute)
	defer inventoryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-inventoryTicker.C:
			if !s.cfg.Demo {
				refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := s.refreshInventory(refreshCtx); err != nil {
					s.LogError("inventory refresh failed", err)
				}
				cancel()
			}
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}
func (s *Server) save(o Order) error {
	return s.db.Update(func(tx *bolt.Tx) error { return putOrder(tx, o) })
}
func (s *Server) tick(ctx context.Context) {
	if s.cfg.Demo {
		return
	}
	var orders []Order
	if e := s.db.View(func(tx *bolt.Tx) error { var e error; orders, e = allOrders(tx); return e }); e != nil {
		s.LogError("worker read failed", e)
		return
	}
	for _, o := range orders {
		if ctx.Err() != nil {
			return
		}
		s.processOrder(ctx, o.ID)
	}
}
func (s *Server) processOrder(ctx context.Context, id string) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	var o Order
	if e := s.db.View(func(tx *bolt.Tx) error { var e error; o, e = getOrder(tx, id); return e }); e != nil {
		s.LogError("worker order read failed", e)
		return
	}
	if !addressPaymentAsset(o.Asset) && o.Status == "awaiting_payment" && time.Now().After(o.Expires.Add(24*time.Hour)) {
		o.Status = "expired"
		if e := s.save(o); e != nil {
			s.LogError("expiration save failed", e)
		}
		return
	}
	if !o.PaidAt.IsZero() && !o.ReceiptSent && o.ReceiptAttempts < 6 && time.Now().After(o.ReceiptNext) {
		s.receipt(ctx, o)
		return
	}
	if o.Status == "delivered" {
		if !o.EmailSent && o.EmailAttempts < 6 && time.Now().After(o.EmailNext) {
			s.email(ctx, o)
		}
		return
	}
	if time.Now().Before(o.NextCheck) {
		return
	}
	if o.Asset == "NOCK" && (o.Status == "awaiting_payment" || o.Status == "confirming") {
		paymentCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		s.checkNockPayment(paymentCtx, o)
		return
	}
	if o.Asset == "XMR" && (o.Status == "awaiting_address" || o.Status == "awaiting_payment" || o.Status == "confirming") {
		paymentCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if o.Status == "awaiting_address" && time.Now().After(o.Expires) {
			o.Status = "expired"
			o.Error = "Monero address was not ready before the quote expired"
			_ = s.save(o)
			return
		}
		if o.Status == "awaiting_address" {
			s.prepareMoneroAddress(paymentCtx, o)
		} else {
			s.checkMoneroPayment(paymentCtx, o)
		}
		return
	}
	if o.Asset == "BTC" && (o.Status == "awaiting_address" || o.Status == "awaiting_payment" || o.Status == "confirming") {
		paymentCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if o.Status == "awaiting_address" && time.Now().After(o.Expires) {
			o.Status = "expired"
			o.Error = "Bitcoin address was not ready before the quote expired"
			_ = s.save(o)
			return
		}
		if o.Status == "awaiting_address" {
			s.prepareBitcoinAddress(paymentCtx, o)
		} else {
			s.checkBitcoinPayment(paymentCtx, o)
		}
		return
	}
	if o.Status == "confirming" {
		s.checkPayment(ctx, o)
		return
	}
	if o.Status == "paid" || o.Status == "prepared" || strings.HasSuffix(o.Status, "_submitted") {
		if o.Checks < 1440 {
			s.fulfill(ctx, o)
		} else {
			message := "Automatic fulfillment checks paused; contact the store. Your planet code and reservation are retained."
			if o.Error != message {
				o.Error = message
				if e := s.save(o); e != nil {
					s.LogError("fulfillment pause save failed", e)
				}
			}
		}
	}
}
func (s *Server) checkPayment(ctx context.Context, o Order) {
	checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	e := s.chain.Verify(checkCtx, s.cfg.Chains[o.ChainID], o)
	o.Checks++
	if e == nil {
		o.Status = "paid"
		o.Error = ""
		o.Checks = 0
		o.NextCheck = time.Now()
	} else if errors.Is(e, ErrPayment) {
		o.Status = "review_payment"
		o.Error = "Transaction does not match the quoted payment. Contact the store or submit the correct transaction."
	} else if o.Checks >= 120 {
		o.Status = "review_payment"
		o.Error = "Automatic payment checks paused. The store can resume verification."
	} else {
		delay := time.Duration(o.Checks) * 15 * time.Second
		if delay > 10*time.Minute {
			delay = 10 * time.Minute
		}
		o.NextCheck = time.Now().Add(delay)
		if !errors.Is(e, ErrPending) {
			o.Error = "Payment provider is temporarily unavailable; verification will retry."
		} else {
			o.Error = ""
		}
	}
	if e = s.db.Update(func(tx *bolt.Tx) error {
		if o.Status == "paid" {
			key := fmt.Sprintf("%d:%s", o.ChainID, o.TxHash)
			b := tx.Bucket([]byte("payments"))
			if id := b.Get([]byte(key)); id != nil && string(id) != o.ID {
				o.Status = "review_payment"
				o.Error = "Payment is already used by another order"
			} else if e := b.Put([]byte(key), []byte(o.ID)); e != nil {
				return e
			}
		}
		if o.Status == "paid" && o.PaidAt.IsZero() {
			o.PaidAt = time.Now().UTC()
			o.ReceiptNext = o.PaidAt
		}
		return putOrder(tx, o)
	}); e != nil {
		s.LogError("payment save failed", e)
	}
}
func (s *Server) fulfill(ctx context.Context, o Order) {
	opCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	o.Checks++
	o.NextCheck = time.Now().Add(time.Minute)
	if o.Status == "paid" {
		c, e := libprg.GeneratePlanetCode(o.Planet)
		if e != nil {
			o.Error = "Planet code generation failed; will retry"
			_ = s.save(o)
			return
		}
		secret, e := encrypt(s.cfg.EncryptionKey, o.ID, c)
		if e != nil {
			s.LogError("planet code encryption failed", e)
			return
		}
		o.Secret = secret
		o.Status = "prepared"
		o.Error = ""
		if e = s.save(o); e != nil {
			s.LogError("planet code persistence failed", e)
			return
		}
	}
	c, e := decrypt(s.cfg.EncryptionKey, o.ID, o.Secret)
	if e != nil {
		s.LogError("planet code unlock failed", e)
		return
	}
	if o.Status == "prepared" {
		if err := s.verifyUnspawned(opCtx, o.Planet); err != nil {
			if errors.Is(err, errSpawned) {
				o.Status = "review_fulfillment"
				o.Error = "Planet is already spawned. Contact the store; your payment and planet code are retained."
			} else {
				o.Error = "Cannot verify planet availability; will retry before spawning"
			}
			if err := s.save(o); err != nil {
				s.LogError("availability result save failed", err)
			}
			return
		}
		o.Status = "spawn_submitted"
		o.StageAt = time.Now()
		if e = s.save(o); e != nil {
			s.LogError("spawn intent save failed", e)
			return
		}
		_, e = libprg.SpawnPlanetCode(opCtx, s.cfg.Star, s.cfg.SpawnKey, "", c.Code)
		if e != nil {
			o.Error = "Spawn submission needs reconciliation; it will not be resent automatically."
			_ = s.save(o)
		}
		return
	}
	p, e := roller.Client.GetPoint(opCtx, o.Planet)
	if e != nil {
		o.Error = "Waiting for the roller to report planet state"
		_ = s.save(o)
		return
	}
	owns := strings.EqualFold(p.Ownership.Owner.Address, c.Owner)
	transfer := strings.EqualFold(p.Ownership.TransferProxy.Address, c.Owner)
	if p.Dominion != "l2" {
		o.Error = "Waiting for the roller to report the L2 planet"
		_ = s.save(o)
		return
	}
	if owns {
		o.Status = "delivered"
		o.Error = ""
		o.EmailNext = time.Now()
		if e = s.save(o); e != nil {
			s.LogError("planet code delivery persistence failed", e)
		}
		return
	}
	if o.Status == "spawn_submitted" && transfer {
		o.Status = "accept_submitted"
		o.StageAt = time.Now()
		o.Error = ""
		if e = s.save(o); e != nil {
			return
		}
		if _, e = libprg.AcceptPlanetCode(opCtx, c.Code); e != nil {
			o.Error = "Invite ownership handoff needs reconciliation"
			_ = s.save(o)
		}
		return
	}
	o.Error = "Waiting for the roller batch to make your planet code redeemable"
	_ = s.save(o)
}

func (s *Server) email(ctx context.Context, o Order) {
	c, e := decrypt(s.cfg.EncryptionKey, o.ID, o.Secret)
	if e != nil {
		return
	}
	o.EmailAttempts++
	o.EmailNext = time.Now().Add(time.Duration(1<<o.EmailAttempts) * time.Minute)
	if err := s.save(o); err != nil {
		s.LogError("email attempt save failed", err)
		return
	}
	text := deliveryText(c.Planet, c.Code, c.URL, s.cfg.Origin)
	payload := map[string]any{"personalizations": []any{map[string]any{"to": []any{map[string]string{"email": o.Email}}}}, "from": map[string]string{"email": s.cfg.EmailFrom, "name": "Networked Subject"}, "subject": "Your planet code for " + c.Planet, "content": []any{map[string]string{"type": "text/plain", "value": text}}, "tracking_settings": map[string]any{"click_tracking": map[string]bool{"enable": false, "enable_text": false}, "open_tracking": map[string]bool{"enable": false}}}
	if err := s.sendMail(ctx, payload); err == nil {
		o.EmailSent = true
		o.Error = ""
	} else {
		o.Error = err.Error() + "; your planet code remains available in the store"
	}
	if err := s.save(o); err != nil {
		s.LogError("email status save failed", err)
	}
}

func (s *Server) sendMail(ctx context.Context, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("Cannot prepare email")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.sendgrid.com/v3/mail/send", bytes.NewReader(body))
	if err != nil {
		return errors.New("Cannot prepare email")
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SendGridKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return errors.New("Email provider unavailable")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 202 {
		return fmt.Errorf("Email delivery failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func deliveryText(planet, code, url, origin string) string {
	return fmt.Sprintf("Your L2 planet %s\n\nPlanet code: %s\n\nRedeem on Bridge: %s\n\nPlease wait up to 1 hour for the batch to process and the planet code to become redeemable.\n\nBridge creates a fresh master ticket in your browser and transfers the planet to it. Keep your planet code private until redeemed; anyone with the code can claim the planet.\n\nYou can also sign in with your purchasing wallet at %s to reveal and download your planet code.\n", planet, code, url, origin)
}
