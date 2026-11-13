// Command sk5base checks a TECHO5 release's signed manifest and says what the fork builds on in it:
// the Show's root filesystem's address, SHA-256 and size (sk5/base.sh). A manifest the key given did
// not sign, or one without that root filesystem, is refused.
//
//	sk5base -manifest manifest.json -sig manifest.json.sig -key <base64 public key> [-rootfs arm]
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/HuskerMinion/techo5/echod/internal/update"
)

func main() {
	manifest := flag.String("manifest", "", "the release's manifest.json")
	sig := flag.String("sig", "", "its manifest.json.sig")
	key := flag.String("key", "", "the release key, base64")
	rootfs := flag.String("rootfs", "arm", "the key of the root filesystem wanted")
	flag.Parse()
	if err := run(*manifest, *sig, *key, *rootfs); err != nil {
		fmt.Fprintln(os.Stderr, "sk5base:", err)
		os.Exit(1)
	}
}

func run(manifest, sig, key, rootfs string) error {
	m, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	s, err := os.ReadFile(sig)
	if err != nil {
		return err
	}
	pub, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("not an ed25519 public key")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(s)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("the signature is malformed")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), m, raw) {
		return errors.New("the manifest is not signed by the key given")
	}
	var mf update.Manifest
	if err := json.Unmarshal(m, &mf); err != nil {
		return err
	}
	b, ok := mf.Rootfs[rootfs]
	if !ok || b.URL == "" || len(b.SHA256) != 64 || b.Size <= 0 {
		return fmt.Errorf("%s names no usable %q root filesystem", mf.Version, rootfs)
	}
	fmt.Printf("%s %s %d %s\n", mf.Version, b.SHA256, b.Size, b.URL)
	return nil
}
