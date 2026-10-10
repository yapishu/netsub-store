package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Native-Planet/perigee/libprg"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// migrateStore promotes the persisted two-tier catalog into three tiers.
func migrateStore(tx *bolt.Tx) error {
	meta := tx.Bucket([]byte("meta"))
	if meta.Get([]byte("catalog-three-tiers")) != nil {
		return nil
	}
	settingsBucket := tx.Bucket([]byte("settings"))
	if data := settingsBucket.Get([]byte("store")); data != nil {
		var old Settings
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("read settings migration: %w", err)
		}
		old.Prices.PremiumETH, old.Prices.PremiumUSDC = old.Prices.RareETH, old.Prices.RareUSDC
		old.Prices.RareETH, old.Prices.RareUSDC = "0.0045", "15"
		old.PremiumStrategies = append(old.RareStrategies, "sigils")
		old.RareStrategies = []string{"any_english", "any_approx", "alliteration"}
		data, err := json.Marshal(old)
		if err != nil {
			return err
		}
		if err := settingsBucket.Put([]byte("store"), data); err != nil {
			return err
		}
	}
	orders := tx.Bucket([]byte("orders"))
	cursor := orders.Cursor()
	for key, data := cursor.First(); key != nil; key, data = cursor.Next() {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return fmt.Errorf("read order migration: %w", err)
		}
		if _, exists := fields["tier"]; exists {
			continue
		}
		var rare bool
		if data := fields["rare"]; data != nil {
			if err := json.Unmarshal(data, &rare); err != nil {
				return err
			}
		}
		tier := "standard"
		if rare {
			tier = "premium"
		}
		encodedTier, err := json.Marshal(tier)
		if err != nil {
			return err
		}
		fields["tier"] = encodedTier
		delete(fields, "rare")
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if err := orders.Put(key, encoded); err != nil {
			return err
		}
	}
	return meta.Put([]byte("catalog-three-tiers"), []byte("1"))
}

// migratePlanetCodes preserves encrypted delivery data while presenting a
// Bridge activation code for each persisted invite wallet.
func (s *Server) migratePlanetCodes() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		if meta.Get([]byte("bridge-planet-codes")) != nil {
			return nil
		}
		archive, err := tx.CreateBucketIfNotExists([]byte("recovery"))
		if err != nil {
			return err
		}
		orders, err := allOrders(tx)
		if err != nil {
			return err
		}
		for _, o := range orders {
			changed := false
			if o.Secret != "" {
				plain, err := decryptPayload(s.cfg.EncryptionKey, o.ID, o.Secret)
				if err != nil {
					return fmt.Errorf("read planet-code migration: %w", err)
				}
				var saved struct{ Planet, Ticket, Owner string }
				if err = json.Unmarshal(plain, &saved); err != nil {
					return err
				}
				if saved.Ticket != "" {
					invite, err := libprg.PlanetCodeFromTicket(o.Planet, saved.Ticket)
					if err != nil {
						return fmt.Errorf("convert planet-code migration: %w", err)
					}
					if saved.Planet != o.Planet || !strings.EqualFold(saved.Owner, invite.Owner) {
						return errors.New("planet-code migration ownership mismatch")
					}
					if err = archive.Put([]byte(o.ID), []byte(o.Secret)); err != nil {
						return err
					}
					o.Secret, err = encrypt(s.cfg.EncryptionKey, o.ID, invite)
					if err != nil {
						return err
					}
					changed = true
				}
			}
			if o.Status == "keys_submitted" {
				o.Status = "accept_submitted"
				o.NextCheck = time.Now()
				changed = true
			}
			if changed {
				if err := putOrder(tx, o); err != nil {
					return err
				}
			}
		}
		return meta.Put([]byte("bridge-planet-codes"), []byte("1"))
	})
}

// migrateUniqueTier separates sigil-based pricing while retaining every order's quote.
func migrateUniqueTier(tx *bolt.Tx) error {
	meta := tx.Bucket([]byte("meta"))
	if meta.Get([]byte("catalog-unique-tier")) != nil {
		return nil
	}
	bucket := tx.Bucket([]byte("settings"))
	if data := bucket.Get([]byte("store")); data != nil {
		var cfg Settings
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Prices.UniqueETH = "0.0075"
		cfg.Prices.UniqueUSDC = "25"
		cfg.Prices.PremiumSigilETH = "0.0105"
		cfg.Prices.PremiumSigilUSDC = "35"
		withoutSigils := func(strategies []string) []string {
			out := []string{}
			for _, strategy := range strategies {
				if strategy != "sigils" {
					out = append(out, strategy)
				}
			}
			return out
		}
		cfg.RareStrategies = withoutSigils(cfg.RareStrategies)
		cfg.PremiumStrategies = withoutSigils(cfg.PremiumStrategies)
		cfg.UniqueStrategies = []string{"sigils"}
		data, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if err = bucket.Put([]byte("store"), data); err != nil {
			return err
		}
	}
	return meta.Put([]byte("catalog-unique-tier"), []byte("1"))
}
