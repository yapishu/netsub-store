package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestPlanetCodeMigrationPreservesEncryptedRecoveryData(t *testing.T) {
	s := testServer(t)
	o := create(t, s, "~sampel-palnet")
	original := map[string]string{"planet": o.Planet, "ticket": "~doznec-binwes-samper-siglet", "owner": "0x5EfED9f04129342e83c92dd8eB26332b05afb989", "keyfile": "saved-boot-key", "code": "saved-login-code"}
	secret, err := encrypt(s.cfg.EncryptionKey, o.ID, original)
	if err != nil {
		t.Fatal(err)
	}
	o.Secret = secret
	o.Status = "keys_submitted"
	o.EmailSent = true
	if err = s.save(o); err != nil {
		t.Fatal(err)
	}
	s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Delete([]byte("bridge-planet-codes")) })
	if err = s.migratePlanetCodes(); err != nil {
		t.Fatal(err)
	}
	saved := readOrder(t, s, o.ID)
	if saved.Status != "accept_submitted" || saved.Amount != o.Amount || !saved.EmailSent {
		t.Fatal("order changed outside migration")
	}
	code, err := decrypt(s.cfg.EncryptionKey, o.ID, saved.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if code.Code != "~doznec-binwes-samper-siglet-sampel-palnet" || code.Owner != original["owner"] {
		t.Fatal("invite changed")
	}
	if err = s.migratePlanetCodes(); err != nil {
		t.Fatal(err)
	}
	if readOrder(t, s, o.ID).Secret != saved.Secret {
		t.Fatal("migration repeats")
	}
	s.db.Close()
	path := filepath.Join(t.TempDir(), "recovered.json")
	if err = Recover(s.cfg, o.ID, path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"saved-boot-key", "saved-login-code", code.Code} {
		if !bytes.Contains(b, []byte(value)) {
			t.Fatal("recovery loses saved data")
		}
	}
	var data map[string]json.RawMessage
	if err = json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if len(data["saved"]) == 0 || len(data["planetCode"]) == 0 {
		t.Fatal("missing recovery sections")
	}
}
