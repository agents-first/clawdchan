package identity

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestGenerateProducesDistinctKeys(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if a.SigningPublic == b.SigningPublic {
		t.Fatal("two generated identities share a signing public key")
	}
	if a.KexPublic == b.KexPublic {
		t.Fatal("two generated identities share a kex public key")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id.json")
	if err := Save(id, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SigningPublic != id.SigningPublic {
		t.Fatalf("signing public mismatch after reload")
	}
	if !bytes.Equal(loaded.SigningPrivate, id.SigningPrivate) {
		t.Fatalf("signing private mismatch after reload")
	}
	if loaded.KexPublic != id.KexPublic {
		t.Fatalf("kex public mismatch after reload")
	}
	if loaded.KexPrivate != id.KexPrivate {
		t.Fatalf("kex private mismatch after reload")
	}
}
