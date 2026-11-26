package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ReleasePublicKey is the Ed25519 key that signs checksums.txt of every
// release from FirstSigned on (base64, raw 32 bytes). The private key lives
// only in the repository's RECOM_SIGNING_KEY Actions secret.
const ReleasePublicKey = "fYKb92UP3iQvFLxRU6GTnbwDVwcqW/WjWPPqcULr9ss="

// FirstSigned is the first release published with checksums.txt.sig.
var FirstSigned = Version{0, 2, 4}

// SignatureAsset is the release asset holding the checksums.txt signature.
const SignatureAsset = "checksums.txt.sig"

func releaseKey() ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(ReleasePublicKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("selfupdate: invalid ReleasePublicKey")
	}
	return ed25519.PublicKey(b)
}

// Sign returns the checksums.txt.sig contents for sums.
func Sign(seed, sums []byte) ([]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key must be a %d-byte Ed25519 seed", ed25519.SeedSize)
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), sums)
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n"), nil
}

// Verify checks a checksums.txt.sig produced by Sign. A nil key means the
// embedded ReleasePublicKey.
func Verify(pub ed25519.PublicKey, sums, sigFile []byte) error {
	if pub == nil {
		pub = releaseKey()
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, sums, sig) {
		return errors.New("checksums.txt signature is invalid")
	}
	return nil
}

// needsSignature reports whether tag was published after signing started.
// Unparseable tags are treated as new.
func needsSignature(tag string) bool {
	v, ok := Parse(tag)
	return !ok || !v.Less(FirstSigned)
}
