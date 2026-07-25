package browserprofile

import (
	"bytes"
	"errors"
	"testing"
)

func TestPayloadRoundTripAcrossAuthenticatedChunks(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	identity := testIdentity()
	metadata, encryptor, err := protector.Create(identity, root)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := bytes.Repeat([]byte("browser-profile-data"), (2*payloadChunkSize/20)+100)
	var encrypted bytes.Buffer
	if err := encryptor.Encrypt(&encrypted, bytes.NewReader(plaintext)); err != nil {
		t.Fatal(err)
	}
	encryptor.Close()

	decryptor, err := protector.Open(metadata, identity, root)
	if err != nil {
		t.Fatal(err)
	}
	defer decryptor.Close()
	var decrypted bytes.Buffer
	if err := decryptor.Decrypt(&decrypted, bytes.NewReader(encrypted.Bytes())); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted.Bytes(), plaintext) {
		t.Fatalf("decrypted payload length = %d, want %d", decrypted.Len(), len(plaintext))
	}
}

func TestPayloadTamperingTruncationAndTrailingDataFailClosed(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	identity := testIdentity()
	metadata, encryptor, err := protector.Create(identity, root)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := encryptor.Encrypt(&encrypted, bytes.NewReader([]byte("profile"))); err != nil {
		t.Fatal(err)
	}
	encryptor.Close()

	cases := map[string][]byte{}
	tampered := append([]byte(nil), encrypted.Bytes()...)
	tampered[len(payloadMagic)+noncePrefixSize+4] ^= 0xff
	cases["tampered"] = tampered
	cases["truncated"] = append([]byte(nil), encrypted.Bytes()[:encrypted.Len()-1]...)
	cases["trailing"] = append(append([]byte(nil), encrypted.Bytes()...), 0x01)
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			decryptor, err := protector.Open(metadata, identity, root)
			if err != nil {
				t.Fatal(err)
			}
			defer decryptor.Close()
			if err := decryptor.Decrypt(&bytes.Buffer{}, bytes.NewReader(payload)); !errors.Is(err, ErrProfileCorrupt) {
				t.Fatalf("Decrypt() error = %v, want %v", err, ErrProfileCorrupt)
			}
		})
	}
}

func TestPayloadFinalRecordPreventsAuthenticatedTruncation(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	identity := testIdentity()
	metadata, encryptor, err := protector.Create(identity, root)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := bytes.Repeat([]byte{'x'}, payloadChunkSize+100)
	var encrypted bytes.Buffer
	if err := encryptor.Encrypt(&encrypted, bytes.NewReader(plaintext)); err != nil {
		t.Fatal(err)
	}
	encryptor.Close()

	// Drop the second encrypted chunk but retain the original final record.
	firstRecordSize := 4 + payloadChunkSize + 16
	headerSize := len(payloadMagic) + noncePrefixSize
	finalRecordSize := 4 + 16
	truncated := append([]byte(nil), encrypted.Bytes()[:headerSize+firstRecordSize]...)
	truncated = append(truncated, encrypted.Bytes()[encrypted.Len()-finalRecordSize:]...)

	decryptor, err := protector.Open(metadata, identity, root)
	if err != nil {
		t.Fatal(err)
	}
	defer decryptor.Close()
	if err := decryptor.Decrypt(&bytes.Buffer{}, bytes.NewReader(truncated)); !errors.Is(err, ErrProfileCorrupt) {
		t.Fatalf("Decrypt() error = %v, want %v", err, ErrProfileCorrupt)
	}
}
