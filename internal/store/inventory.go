package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Native-Planet/perigee/types"
	bolt "go.etcd.io/bbolt"
)

var strategies = []string{"any_approx", "only_approx", "any_english", "only_english", "doubles", "alliteration", "sigils"}

func strategy(s string) bool {
	for _, v := range strategies {
		if v == s {
			return true
		}
	}
	return false
}
func loadRarity(dir string) (map[string][]string, error) {
	out := map[string][]string{}
	if dir == "" {
		return out, nil
	}
	for _, kind := range strategies {
		f, e := os.Open(filepath.Join(dir, kind+"_planets.txt"))
		if e != nil {
			return nil, e
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			p, n, e := types.ValidateAndNormalizePatp(strings.TrimSpace(scanner.Text()))
			if e != nil || n < 65536 {
				f.Close()
				return nil, errors.New("invalid planet in gonetia output")
			}
			out[p] = append(out[p], kind)
		}
		e = scanner.Err()
		f.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Server) refreshInventory(ctx context.Context) error {
	s.inventoryMu.Lock()
	defer s.inventoryMu.Unlock()
	if s.cfg.Demo {
		return nil
	}
	tags, e := loadRarity(s.cfg.GonetiaDir)
	if e != nil {
		return e
	}
	info, e := s.roller.GetPoint(ctx, s.cfg.Star)
	if e != nil {
		return e
	}
	if info.Dominion != "l2" && info.Dominion != "spawn" {
		return errors.New("planet-code vending requires an L2 or L2-spawning star")
	}
	spawned, e := s.readSpawned(ctx)
	if e != nil {
		return e
	}
	ships, e := s.roller.GetUnspawned(ctx, s.cfg.Star)
	if e != nil {
		return e
	}
	_, star, e := types.ValidateAndNormalizePatp(s.cfg.Star)
	if e != nil {
		return e
	}
	for name := range tags {
		_, n, e := types.ValidateAndNormalizePatp(name)
		if e != nil || n&65535 != star {
			return errors.New("gonetia output belongs to a different star")
		}
	}
	list := []Planet{}
	for _, p := range ships {
		name, n, e := types.ValidateAndNormalizePatp(p.Name)
		if e != nil || n < 65536 || n&65535 != star {
			return errors.New("roller returned a planet outside this star")
		}
		list = append(list, Planet{Name: name, Tags: tags[name]})
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if e := rememberSpawned(tx, spawned); e != nil {
			return e
		}
		b := tx.Bucket([]byte("inventory"))
		data, e := json.Marshal(list)
		if e != nil {
			return e
		}
		if e = b.Put([]byte("list"), data); e != nil {
			return e
		}
		return b.Put([]byte("updated"), []byte(time.Now().UTC().Format(time.RFC3339)))
	})
}
func (s *Server) available(tx *bolt.Tx) ([]Planet, error) {
	var list []Planet
	if b := tx.Bucket([]byte("inventory")).Get([]byte("list")); b != nil {
		if e := json.Unmarshal(b, &list); e != nil {
			return nil, e
		}
	}
	orders, e := allOrders(tx)
	if e != nil {
		return nil, e
	}
	held := map[string]bool{}
	for _, o := range orders {
		if o.Status != "expired" || o.Secret != "" {
			held[o.Planet] = true
		}
	}
	cfg := settings(tx)
	out := []Planet{}
	for _, p := range list {
		if held[p.Name] || tx.Bucket([]byte("spawns")).Get([]byte(p.Name)) != nil {
			continue
		}
		p.Tier = planetTier(p.Tags, cfg)
		p.NameAndSigil = p.Tier == "premium" && slices.Contains(p.Tags, "sigils")
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func planetTier(tags []string, cfg Settings) string {
	for _, group := range []struct {
		tier       string
		strategies []string
	}{{"premium", cfg.PremiumStrategies}, {"unique", cfg.UniqueStrategies}, {"rare", cfg.RareStrategies}} {
		for _, tag := range tags {
			for _, strategy := range group.strategies {
				if tag == strategy {
					return group.tier
				}
			}
		}
	}
	return "standard"
}

type SpawnRecord struct {
	Planet      string    `json:"planet"`
	OrderID     string    `json:"orderId,omitempty"`
	SubmittedAt time.Time `json:"submittedAt,omitempty"`
	ObservedAt  time.Time `json:"observedAt,omitempty"`
}

func recordSpawnIntent(tx *bolt.Tx, order Order) error {
	b := tx.Bucket([]byte("spawns"))
	var record SpawnRecord
	if old := b.Get([]byte(order.Planet)); old != nil {
		if err := json.Unmarshal(old, &record); err != nil {
			return err
		}
		if record.OrderID != "" {
			return nil
		}
	}
	record.Planet, record.OrderID, record.SubmittedAt = order.Planet, order.ID, order.StageAt
	if record.SubmittedAt.IsZero() {
		record.SubmittedAt = time.Now().UTC()
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return b.Put([]byte(order.Planet), data)
}

func rememberSpawned(tx *bolt.Tx, names []string) error {
	bucket := tx.Bucket([]byte("spawns"))
	for _, name := range names {
		record := SpawnRecord{Planet: name}
		if old := bucket.Get([]byte(name)); old != nil {
			if err := json.Unmarshal(old, &record); err != nil {
				return err
			}
			if !record.ObservedAt.IsZero() {
				continue
			}
		}
		record.ObservedAt = time.Now().UTC()
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(name), data); err != nil {
			return err
		}
		if record.OrderID != "" {
			if err := appendEvent(tx, Event{Kind: "spawn.observed", OrderID: record.OrderID, Planet: name}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) readSpawned(ctx context.Context) ([]string, error) {
	ships, err := s.roller.GetSpawned(ctx, s.cfg.Star)
	if err != nil {
		return nil, err
	}
	_, star, err := types.ValidateAndNormalizePatp(s.cfg.Star)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ships))
	for _, ship := range ships {
		name, n, err := types.ValidateAndNormalizePatp(ship.Name)
		if err != nil || n < 65536 || n&65535 != star {
			return nil, fmt.Errorf("roller returned spawned planet outside %s", s.cfg.Star)
		}
		names = append(names, name)
	}
	return names, nil
}

func (s *Server) refreshSpawned(ctx context.Context) error {
	names, err := s.readSpawned(ctx)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error { return rememberSpawned(tx, names) })
}

func (s *Server) verifyUnspawned(ctx context.Context, planet string) error {
	if err := s.refreshSpawned(ctx); err != nil {
		return err
	}
	return s.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("spawns")).Get([]byte(planet)) != nil {
			return errSpawned
		}
		return nil
	})
}

var errSpawned = errors.New("Planet is already spawned")

type CategoryCount struct {
	Name      string `json:"name"`
	Generated int    `json:"generated"`
	Available int    `json:"available"`
	Tier      string `json:"tier"`
}

func (s *Server) categoryCounts(cfg Settings, available []Planet) ([]CategoryCount, error) {
	tags, err := loadRarity(s.cfg.GonetiaDir)
	if err != nil {
		return nil, err
	}
	counts := make([]CategoryCount, 0, len(strategies))
	for _, kind := range strategies {
		count := CategoryCount{Name: kind, Tier: planetTier([]string{kind}, cfg)}
		for _, kinds := range tags {
			for _, tag := range kinds {
				if tag == kind {
					count.Generated++
					break
				}
			}
		}
		for _, p := range available {
			for _, tag := range p.Tags {
				if tag == kind {
					count.Available++
					break
				}
			}
		}
		counts = append(counts, count)
	}
	return counts, nil
}
