package browserprofile

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type oneByteWriter struct {
	buffer bytes.Buffer
}

func (writer *oneByteWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return writer.buffer.Write(value[:1])
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

func TestPayloadHandlesPartialWritesAndRejectsZeroProgress(t *testing.T) {
	t.Parallel()
	protector := NewProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	metadata, encryptor, err := protector.Create(testIdentity(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer encryptor.Close()

	encrypted := &oneByteWriter{}
	if err := encryptor.Encrypt(encrypted, bytes.NewReader([]byte("profile"))); err != nil {
		t.Fatal(err)
	}
	decryptor, err := protector.Open(metadata, testIdentity(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer decryptor.Close()
	decrypted := &oneByteWriter{}
	if err := decryptor.Decrypt(decrypted, bytes.NewReader(encrypted.buffer.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got := decrypted.buffer.String(); got != "profile" {
		t.Fatalf("decrypted payload = %q, want profile", got)
	}
	if err := encryptor.Encrypt(zeroWriter{}, bytes.NewReader(nil)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress writer error = %v, want %v", err, io.ErrShortWrite)
	}
}
