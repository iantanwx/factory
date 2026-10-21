// Command loop-release builds and signs a loop release (KTD26). The
// Makefile runs it: `make release-dist` in CI, `make release-sign` on the
// operator's offline machine, and `make release-dry` with a throwaway key.
//
//	loop-release keygen -pub FILE -sec FILE
//	loop-release dist   -dir DIR -version V [-min-from V] [-no-rollback]
//	loop-release sign   -dir DIR -key FILE
//	loop-release verify -dir DIR -pub FILE
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"

	"github.com/iantanwx/loop/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "loop-release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: loop-release keygen|dist|sign|verify [flags]")
	}
	fl := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := fl.String("dir", "", "the release directory")
	version := fl.String("version", "", "the release version, vMAJOR.MINOR.PATCH")
	minFrom := fl.String("min-from", "", "the lowest version this release upgrades from")
	noRollback := fl.Bool("no-rollback", false, "mark the release no-rollback")
	key := fl.String("key", "", "the secret key file")
	pub := fl.String("pub", "", "the public key file")
	sec := fl.String("sec", "", "the secret key file to create")
	if err := fl.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "keygen":
		return keygen(*pub, *sec)
	case "dist":
		return release.WriteDist(context.Background(), *dir, release.DistOptions{Version: *version, MinFrom: *minFrom, NoRollback: *noRollback})
	case "sign":
		data, err := os.ReadFile(*key)
		if err != nil {
			return err
		}
		k, err := release.ParseSecretKey(string(data))
		if err != nil {
			return err
		}
		if err := release.SignDist(*dir, k); err != nil {
			return err
		}
		fmt.Printf("signed %s/%s with key %s\n", *dir, release.SignatureFile, k.Public().KeyID())
		return nil
	case "verify":
		data, err := os.ReadFile(*pub)
		if err != nil {
			return err
		}
		keys, err := release.ParsePublicKeys(string(data))
		if err != nil {
			return err
		}
		v, err := release.VerifyDist(*dir, keys)
		if err != nil {
			return err
		}
		fmt.Printf("verified %s: the signature, the trusted comment and every checksum\n", v)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// keygen writes a new key pair. It never replaces a file.
func keygen(pubPath, secPath string) error {
	if pubPath == "" || secPath == "" {
		return errors.New("keygen needs -pub and -sec")
	}
	for _, p := range []string{pubPath, secPath} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s exists; keygen never replaces a key", p)
		}
	}
	pub, sec, err := release.GenerateKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(secPath, []byte(sec.String()), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, []byte(pub.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("key %s: public key %s, secret key %s\n", pub.KeyID(), pubPath, secPath)
	return nil
}
