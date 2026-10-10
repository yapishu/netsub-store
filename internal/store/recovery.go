package store

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Recover writes the persisted planet code and recovery snapshots without changing an order or
// contacting any chain. The database must be closed by the serving process.
func Recover(c Config, id, path string) error {
	db, e := bolt.Open(c.DBPath, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if e != nil {
		return errors.New("cannot lock database for recovery; stop the store first")
	}
	defer db.Close()
	var o Order
	if e = db.View(func(tx *bolt.Tx) error { var e error; o, e = getOrder(tx, id); return e }); e != nil {
		return e
	}
	if o.Secret == "" {
		return errors.New("this order has no generated planet code")
	}
	code, e := decryptPayload(c.EncryptionKey, o.ID, o.Secret)
	if e != nil {
		return errors.New("cannot decrypt planet code; check DATA_ENCRYPTION_KEY")
	}
	var saved json.RawMessage
	if e = db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket([]byte("recovery")); b != nil {
			if snapshot := b.Get([]byte(id)); snapshot != nil {
				var err error
				saved, err = decryptPayload(c.EncryptionKey, id, string(snapshot))
				return err
			}
		}
		return nil
	}); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	e = encoder.Encode(struct {
		Order      Order           `json:"order"`
		PlanetCode json.RawMessage `json:"planetCode"`
		Saved      json.RawMessage `json:"saved,omitempty"`
	}{o, code, saved})
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
