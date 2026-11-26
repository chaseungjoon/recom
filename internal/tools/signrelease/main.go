// Command signrelease signs a release's checksums.txt (writing
// checksums.txt.sig next to it) with the Ed25519 seed in RECOM_SIGNING_KEY,
// or with -verify checks it against the public key built into recom.
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/chaseungjoon/recom/internal/selfupdate"
)

func main() {
	verify := flag.Bool("verify", false, "verify FILE.sig against the embedded release key")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: signrelease [-verify] checksums.txt")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *verify); err != nil {
		fmt.Fprintln(os.Stderr, "signrelease:", err)
		os.Exit(1)
	}
}

func run(file string, verify bool) error {
	sums, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if verify {
		sig, err := os.ReadFile(file + ".sig")
		if err != nil {
			return err
		}
		if err := selfupdate.Verify(nil, sums, sig); err != nil {
			return err
		}
		fmt.Println("signature OK:", file)
		return nil
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("RECOM_SIGNING_KEY")))
	if err != nil {
		return fmt.Errorf("RECOM_SIGNING_KEY: %w", err)
	}
	sig, err := selfupdate.Sign(seed, sums)
	if err != nil {
		return err
	}
	if err := selfupdate.Verify(nil, sums, sig); err != nil {
		return fmt.Errorf("RECOM_SIGNING_KEY does not match the embedded public key: %w", err)
	}
	return os.WriteFile(file+".sig", sig, 0o644)
}
