//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCredentialFileAcceptsOwnerOnlyAbsoluteFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "channel-token")
	token := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readCredentialFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != token {
		t.Fatalf("credential = %q, want token", value)
	}
}

func TestReadCredentialFileRejectsUnsafeSources(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid")
	if err := os.WriteFile(valid, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	worldReadable := filepath.Join(dir, "world-readable")
	if err := os.WriteFile(worldReadable, []byte(strings.Repeat("a", 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	whitespace := filepath.Join(dir, "whitespace")
	if err := os.WriteFile(whitespace, []byte(strings.Repeat("a", 32)+" "+strings.Repeat("b", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	leadingWhitespace := filepath.Join(dir, "leading-whitespace")
	if err := os.WriteFile(leadingWhitespace, []byte(" "+strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing":        filepath.Join(dir, "missing"),
		"relative":       "relative-token",
		"symlink":        symlink,
		"world-readable": worldReadable,
		"whitespace":     whitespace,
		"leading-space":  leadingWhitespace,
		"short":          short,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readCredentialFile(path); err == nil {
				t.Fatal("readCredentialFile() succeeded, want error")
			}
		})
	}
}
