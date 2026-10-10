package store

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/nathanlever/keygen"
)

// This is a public development mnemonic, not a funded account.
const testMnemonic = "test test test test test test test test test test test junk"

func TestProxyMnemonic(t *testing.T) {
	key, err := signingKey("", testMnemonic, "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80" {
		t.Fatal("incorrect BIP44 derivation")
	}
	private, _ := crypto.HexToECDSA(key)
	if crypto.PubkeyToAddress(private.PublicKey).Hex() != "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266" {
		t.Fatal("unexpected signing address")
	}
	raw, err := signingKey("0x"+key, "", "")
	if err != nil || raw != key {
		t.Fatal("raw key rejected", err)
	}
	normalized, err := signingKey("", "  "+strings.ReplaceAll(testMnemonic, " ", "\n ")+" ", "")
	if err != nil || normalized != key {
		t.Fatal("mnemonic whitespace changed key", err)
	}
	for _, input := range []struct{ private, mnemonic, passphrase string }{{}, {key, testMnemonic, ""}, {"", "not a seed", ""}, {"", "~sampel-palnet-sampel-palnet", ""}, {key, "", "passphrase"}, {strings.Repeat("0", 64), "", ""}} {
		if _, err := signingKey(input.private, input.mnemonic, input.passphrase); err == nil {
			t.Fatal("invalid signing configuration accepted")
		}
	}
}
func TestProxyMnemonicMatchesPerigee(t *testing.T) {
	const passphrase = "example-passphrase"
	wallet := keygen.GenerateWallet("sampel-palnet-sampel-palnet", 257, passphrase, 0, false)
	for _, node := range []keygen.Node{wallet.Management, *wallet.Spawn} {
		derived, err := signingKey("", node.Seed, passphrase)
		if err != nil || derived != node.Keys.Private {
			t.Fatalf("%s mnemonic differs from Perigee: %v", node.Type, err)
		}
	}
}

func TestMnemonicEnvironment(t *testing.T) {
	for k, v := range map[string]string{
		"DEMO": "false", "PUBLIC_ORIGIN": "https://store.example", "STAR": "~marzod",
		"DATA_ENCRYPTION_KEY": strings.Repeat("1", 64),
		"ADMIN_PASSWORD_HASH": "$2a$12$.....................................................",
		"SPAWN_PRIVATE_KEY":   "", "SPAWN_MNEMONIC": testMnemonic, "SPAWN_MNEMONIC_PASSPHRASE": "",
		"SENDGRID_API_KEY": "test-key", "EMAIL_FROM": "store@example.com",
		"ETH_RPC_URL": "https://eth.example", "BASE_RPC_URL": "https://base.example",
	} {
		t.Setenv(k, v)
	}
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := signingKey("", testMnemonic, "")
	if err != nil || c.SpawnKey != expected {
		t.Fatal("environment does not derive signing key")
	}
	t.Setenv("SPAWN_PRIVATE_KEY", expected)
	if _, err = FromEnv(); err == nil {
		t.Fatal("environment accepts competing signing inputs")
	}
}

func TestWalletOnlyAdminConfiguration(t *testing.T) {
	for k, v := range map[string]string{"DEMO": "true", "PUBLIC_ORIGIN": "https://store.example", "DATA_ENCRYPTION_KEY": strings.Repeat("1", 64), "ADMIN_PASSWORD_HASH": "", "ADMIN_WALLET_ADDRESS": "0x1111111111111111111111111111111111111111"} {
		t.Setenv(k, v)
	}
	cfg, err := FromEnv()
	if err != nil || cfg.AdminWallet == "" {
		t.Fatal("wallet-only admin rejected", err)
	}
	t.Setenv("ADMIN_WALLET_ADDRESS", "")
	if _, err = FromEnv(); err == nil {
		t.Fatal("missing admin authentication accepted")
	}
	t.Setenv("ADMIN_WALLET_ADDRESS", "0x0000000000000000000000000000000000000000")
	if _, err = FromEnv(); err == nil {
		t.Fatal("zero admin address accepted")
	}
}
