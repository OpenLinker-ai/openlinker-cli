package browserprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestMetadataCreateMarshalOpenAndIdentityBinding(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	identity := testIdentity()

	metadata, payloadCipher, err := protector.Create(identity, root)
	if err != nil {
		t.Fatal(err)
	}
	defer payloadCipher.Close()
	raw, err := MarshalMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := protector.Open(decoded, identity, root)
	if err != nil {
		t.Fatal(err)
	}
	opened.Close()

	other := identity
	other.PrincipalScopeID = "scope_other"
	if _, err := protector.Open(decoded, other, root); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("cross-principal Open() error = %v, want %v", err, ErrIdentityMismatch)
	}
}

func TestMetadataParserRejectsUnknownTrailingAndOversizedInput(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	metadata, payloadCipher, err := protector.Create(testIdentity(), root)
	if err != nil {
		t.Fatal(err)
	}
	payloadCipher.Close()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["unexpected"] = true
	unknown, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"unknown":   unknown,
		"trailing":  append(append([]byte(nil), raw...), []byte("{}")...),
		"oversized": bytes.Repeat([]byte{'x'}, maxMetadataBytes+1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMetadata(payload); !errors.Is(err, ErrProfileCorrupt) {
				t.Fatalf("ParseMetadata() error = %v, want %v", err, ErrProfileCorrupt)
			}
		})
	}
}

func TestMetadataRejectsWrongOrTamperedRootMaterial(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	identity := testIdentity()
	metadata, payloadCipher, err := protector.Create(identity, root)
	if err != nil {
		t.Fatal(err)
	}
	payloadCipher.Close()

	wrongGeneration := testRootKey(t, 2, 0x11)
	defer wrongGeneration.Close()
	if _, err := protector.Open(metadata, identity, wrongGeneration); !errors.Is(err, ErrKeyGeneration) {
		t.Fatalf("wrong generation error = %v, want %v", err, ErrKeyGeneration)
	}
	wrongKey := testRootKey(t, 1, 0x22)
	defer wrongKey.Close()
	if _, err := protector.Open(metadata, identity, wrongKey); !errors.Is(err, ErrProfileCorrupt) {
		t.Fatalf("wrong key error = %v, want %v", err, ErrProfileCorrupt)
	}
	tampered := cloneMetadata(metadata)
	tampered.WrappedDEK[0] ^= 0xff
	if _, err := protector.Open(tampered, identity, root); !errors.Is(err, ErrProfileCorrupt) {
		t.Fatalf("tampered metadata error = %v, want %v", err, ErrProfileCorrupt)
	}
}

func TestMetadataRewrapChangesOnlyWrappedKeyMetadata(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	oldRoot := testRootKey(t, 1, 0x11)
	defer oldRoot.Close()
	newRoot := testRootKey(t, 2, 0x22)
	defer newRoot.Close()
	identity := testIdentity()
	metadata, payloadCipher, err := protector.Create(identity, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	plaintext := bytes.Repeat([]byte("profile-data-"), 1000)
	if err := payloadCipher.Encrypt(&encrypted, bytes.NewReader(plaintext)); err != nil {
		t.Fatal(err)
	}
	payloadCipher.Close()
	payloadBefore := append([]byte(nil), encrypted.Bytes()...)

	rewrapped, err := protector.Rewrap(metadata, identity, oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if rewrapped.RootKeyGeneration != newRoot.Generation() ||
		bytes.Equal(rewrapped.WrappedDEK, metadata.WrappedDEK) {
		t.Fatalf("rewrapped metadata = %#v", rewrapped)
	}
	if !bytes.Equal(payloadBefore, encrypted.Bytes()) {
		t.Fatal("rewrap changed the encrypted profile payload")
	}
	if _, err := protector.Open(rewrapped, identity, oldRoot); !errors.Is(err, ErrKeyGeneration) {
		t.Fatalf("old root Open() error = %v, want %v", err, ErrKeyGeneration)
	}
	opened, err := protector.Open(rewrapped, identity, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	var decrypted bytes.Buffer
	if err := opened.Decrypt(&decrypted, bytes.NewReader(encrypted.Bytes())); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted.Bytes(), plaintext) {
		t.Fatal("rewrapped profile plaintext mismatch")
	}
}

func TestClosedRootAndPayloadKeysFailClosed(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	identity := testIdentity()
	metadata, payloadCipher, err := protector.Create(identity, root)
	if err != nil {
		t.Fatal(err)
	}
	payloadCipher.Close()
	if err := payloadCipher.Encrypt(&bytes.Buffer{}, bytes.NewReader(nil)); !errors.Is(err, ErrKeyClosed) {
		t.Fatalf("closed payload key error = %v, want %v", err, ErrKeyClosed)
	}
	root.Close()
	if _, err := protector.Open(metadata, identity, root); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("closed root error = %v, want %v", err, ErrInvalidConfiguration)
	}
}

func cloneMetadata(metadata Metadata) Metadata {
	cloned := metadata
	cloned.WrapNonce = append([]byte(nil), metadata.WrapNonce...)
	cloned.WrappedDEK = append([]byte(nil), metadata.WrappedDEK...)
	return cloned
}

func testRootKey(t *testing.T, generation uint64, fill byte) *RootKey {
	t.Helper()
	root, err := NewRootKey(generation, bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testIdentity() Identity {
	return Identity{
		AgentID:           "11111111-1111-4111-8111-111111111111",
		PrincipalScopeID:  "scope_222222222222",
		ProfileSlot:       "default",
		ProfileGeneration: 1,
	}
}
