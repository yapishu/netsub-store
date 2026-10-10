package store

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tyler-smith/go-bip32"
	"github.com/tyler-smith/go-bip39"
)

// signingKey accepts a proxy's BIP39 mnemonic or a raw Ethereum private key.
// Urbit HD wallet proxy mnemonics use m/44'/60'/0'/0/0, as in Perigee's keygen.
// The server checks the derived address against the star's spawning authority.
func signingKey(privateKey, mnemonic, passphrase string) (string, error) {
	privateKey = strings.TrimPrefix(strings.TrimSpace(privateKey), "0x")
	mnemonic = strings.Join(strings.Fields(mnemonic), " ")
	if (privateKey == "") == (mnemonic == "") {
		return "", errors.New("set exactly one of SPAWN_PRIVATE_KEY or SPAWN_MNEMONIC")
	}
	if privateKey != "" {
		if passphrase != "" {
			return "", errors.New("SPAWN_MNEMONIC_PASSPHRASE requires SPAWN_MNEMONIC")
		}
		if _, err := crypto.HexToECDSA(privateKey); err != nil {
			return "", errors.New("SPAWN_PRIVATE_KEY must be a valid 32-byte hex private key")
		}
		return privateKey, nil
	}
	if !bip39.IsMnemonicValid(mnemonic) {
		return "", errors.New("SPAWN_MNEMONIC must be a valid BIP39 word phrase, not an Urbit master ticket")
	}
	master, err := bip32.NewMasterKey(bip39.NewSeed(mnemonic, passphrase))
	if err != nil {
		return "", errors.New("cannot derive proxy mnemonic")
	}
	key := master
	for _, index := range []uint32{44 + bip32.FirstHardenedChild, 60 + bip32.FirstHardenedChild, bip32.FirstHardenedChild, 0, 0} {
		key, err = key.NewChildKey(index)
		if err != nil {
			return "", errors.New("cannot derive proxy key at m/44'/60'/0'/0/0")
		}
	}
	return hex.EncodeToString(key.Key), nil
}
