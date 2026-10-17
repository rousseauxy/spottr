package spotnet

import (
	"crypto/rsa"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"math/big"
	"strings"
)

// Spotnet signs with RSA PKCS#1 v1.5 over SHA-1. That is weak by modern
// standards and we cannot change it — it is the wire format every Spotnet
// client implements. Verification still has real value: it binds a spot to the
// key that produced it, which is what makes spotter blacklisting meaningful and
// stops trivial forgery of another spotter's identity. It is emphatically not a
// strong authenticity guarantee, so a verified spot is "attributable", not
// "trusted".

// MasterKeys are the well-known Spotnet header-signing public keys, indexed by
// the keyid embedded in the From header. Modulus and exponent are base64 as
// distributed with Spotnet clients (mirrors Spotweb's `rsa_keys` setting).
var MasterKeys = map[int]PublicKey{
	2: {Modulus: "ys8WSlqonQMWT8ubG0tAA2Q07P36E+CJmb875wSR1XH7IFhEi0CCwlUzNqBFhC+P", Exponent: "AQAB"},
	3: {Modulus: "uiyChPV23eguLAJNttC/o0nAsxXgdjtvUvidV2JL+hjNzc4Tc/PPo2JdYvsqUsat", Exponent: "AQAB"},
	4: {Modulus: "1k6RNDVD6yBYWR6kHmwzmSud7JkNV4SMigBrs+jFgOK5Ldzwl17mKXJhl+su/GR9", Exponent: "AQAB"},
}

// PublicKey is a base64-encoded RSA public key as it appears on the wire.
type PublicKey struct {
	Modulus  string
	Exponent string
}

// userKeyXML mirrors the X-User-Key header payload.
type userKeyXML struct {
	Modulus  string `xml:"Modulus"`
	Exponent string `xml:"Exponent"`
}

// rsaPub converts the wire form into a usable key.
func (k PublicKey) rsaPub() (*rsa.PublicKey, error) {
	n, err := base64.StdEncoding.DecodeString(k.Modulus)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	e, err := base64.StdEncoding.DecodeString(k.Exponent)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	if len(n) == 0 || len(e) == 0 {
		return nil, fmt.Errorf("empty key material")
	}
	exp := new(big.Int).SetBytes(e)
	if !exp.IsInt64() || exp.Int64() > 1<<31-1 {
		return nil, fmt.Errorf("exponent out of range")
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(n),
		E: int(exp.Int64()),
	}, nil
}

// UnprepareBase64 reverses Spotnet's header-safe base64 variant: '/' and '+'
// are transported as "-s" and "-p", and the padding is stripped.
func UnprepareBase64(s string) string {
	s = strings.ReplaceAll(s, "-s", "/")
	s = strings.ReplaceAll(s, "-p", "+")
	if pad := len(s) % 4; pad > 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return s
}

// SpotterID derives the stable spotter identifier from a base64 RSA modulus.
//
// Mirrors Spotweb's calculateSpotterId: CRC32 (IEEE) of the raw modulus bytes,
// serialized little-endian, base64-encoded, then '/', '+' and '=' removed. This
// is an identifier, not a security primitive — it is what public blacklists key
// on, so it must match Spotweb byte for byte.
func SpotterID(modulusB64 string) string {
	raw, err := base64.StdEncoding.DecodeString(modulusB64)
	if err != nil || len(raw) == 0 {
		return ""
	}
	sum := crc32.ChecksumIEEE(raw)

	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], sum)

	enc := base64.StdEncoding.EncodeToString(b[:])
	return strings.NewReplacer("/", "", "+", "", "=", "").Replace(enc)
}

// verify checks an RSA PKCS#1 v1.5 SHA-1 signature.
func verify(key PublicKey, data string, signatureB64 string) bool {
	pub, err := key.rsaPub()
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil || len(sig) == 0 {
		return false
	}
	digest := sha1.Sum([]byte(data))
	return verifyPKCS1v15SHA1(pub, digest[:], sig)
}

// sha1DigestInfo is the DER prefix PKCS#1 v1.5 puts before a SHA-1 digest.
var sha1DigestInfo = []byte{0x30, 0x21, 0x30, 0x09, 0x06, 0x05, 0x2b, 0x0e, 0x03, 0x02, 0x1a, 0x05, 0x00, 0x04, 0x14}

// verifyPKCS1v15SHA1 is rsa.VerifyPKCS1v15 without its key-size floor. Since Go 1.24 crypto/rsa
// rejects keys under 1024 bits, and Spotnet's self-signed keys (withdrawals, poster keys) are 384
// bits: after the move to Go 1.27 (0.3.0) not one of 2,700 withdrawals verified any more
// (measured 2026-10-17). Verifying with a public key is plain modular arithmetic; nothing secret
// is involved, so the textbook check is as good as the library's.
func verifyPKCS1v15SHA1(pub *rsa.PublicKey, digest, sig []byte) bool {
	k := (pub.N.BitLen() + 7) / 8
	tLen := len(sha1DigestInfo) + len(digest)
	if len(sig) != k || k < tLen+11 || pub.E < 2 {
		return false
	}
	s := new(big.Int).SetBytes(sig)
	if s.Cmp(pub.N) >= 0 {
		return false
	}
	em := new(big.Int).Exp(s, big.NewInt(int64(pub.E)), pub.N).FillBytes(make([]byte, k))

	want := make([]byte, k)
	want[1] = 0x01
	for i := 2; i < k-tLen-1; i++ {
		want[i] = 0xff
	}
	copy(want[k-tLen:], sha1DigestInfo)
	copy(want[k-len(digest):], digest)
	return subtle.ConstantTimeCompare(em, want) == 1
}

// ParseUserKey extracts the spotter's public key from an X-User-Key header.
func ParseUserKey(header string) (PublicKey, error) {
	var k userKeyXML
	if err := xml.Unmarshal([]byte(strings.TrimSpace(header)), &k); err != nil {
		return PublicKey{}, fmt.Errorf("parse user key: %w", err)
	}
	if k.Modulus == "" {
		return PublicKey{}, fmt.Errorf("user key has no modulus")
	}
	return PublicKey{Modulus: k.Modulus, Exponent: k.Exponent}, nil
}

// headerSigningString reconstructs the bytes covered by the From-header
// signature: title + header-up-to-the-signature + poster.
func headerSigningString(title, header, poster string) (string, bool) {
	idx := strings.LastIndex(header, ".")
	if idx < 0 {
		return "", false
	}
	return title + header[:idx] + poster, true
}

// VerifyHeader checks the signature carried in the From header against the
// well-known master key named by keyID.
//
// keyID 1 identifies legacy unsigned spots, which cannot be verified at all.
// Self-signed articles (which carry their own public key in the From header)
// are handled by VerifySelfSigned instead — the master keys do not apply.
func VerifyHeader(title, header, poster, signatureB64 string, keyID int) bool {
	key, ok := MasterKeys[keyID]
	if !ok {
		return false
	}
	toCheck, ok := headerSigningString(title, header, poster)
	if !ok {
		return false
	}
	return verify(key, toCheck, UnprepareBase64(signatureB64))
}

// minSelfSignedKeyLen distinguishes a From header carrying the poster's public
// key from one carrying only a signature. Mirrors Spotweb's `strlen(...) > 50`.
const minSelfSignedKeyLen = 50

// hashcashPrefix is the proof-of-work an article must satisfy before its
// self-signed key is trusted. Deliberately cheap to check and costly to grind,
// which is what keeps self-signed articles from being free to mass-produce.
const hashcashPrefix = "0000"

// HasValidHashcash reports whether the message-id satisfies Spotnet's
// proof-of-work: SHA-1 of the bracketed message-id must begin with "0000".
func HasValidHashcash(messageID string) bool {
	id := messageID
	if !strings.HasPrefix(id, "<") {
		id = "<" + id + ">"
	}
	sum := sha1.Sum([]byte(id))
	return strings.HasPrefix(hex.EncodeToString(sum[:]), hashcashPrefix)
}

// VerifySelfSigned validates an article that carries its own public key in the
// From header, returning the spotter id it belongs to.
//
// This is how Spotnet withdrawal requests are authenticated, and the model is
// not "a moderator signed this" — it is "whoever holds this keypair signed
// this". Anyone can mint a keypair, so a valid signature alone authorises
// nothing. It only becomes meaningful once the caller confirms the resulting
// spotter id matches the spot being withdrawn; see ValidateDispose.
func VerifySelfSigned(title, header, poster, signatureB64, selfSignedPubKey, messageID string) (spotterID string, ok bool) {
	if len(selfSignedPubKey) <= minSelfSignedKeyLen {
		return "", false
	}
	if !HasValidHashcash(messageID) {
		return "", false
	}
	toCheck, ok := headerSigningString(title, header, poster)
	if !ok {
		return "", false
	}
	key := PublicKey{Modulus: selfSignedPubKey, Exponent: "AQAB"}
	if !verify(key, toCheck, UnprepareBase64(signatureB64)) {
		return "", false
	}
	return SpotterID(key.Modulus), true
}

// VerifyFullSpot checks the spotter's own signature over the article.
//
// Spotweb signs the literal "<message-id>" string, falling back to signing the
// XML signature blob for older clients. Both are accepted here for the same
// compatibility reason.
func VerifyFullSpot(messageID string, userKey PublicKey, userSignatureB64, xmlSignatureB64 string) bool {
	if userSignatureB64 == "" || userKey.Modulus == "" {
		return false
	}
	sig := UnprepareBase64(userSignatureB64)

	id := messageID
	if !strings.HasPrefix(id, "<") {
		id = "<" + id + ">"
	}
	if verify(userKey, id, sig) {
		return true
	}
	if xmlSignatureB64 != "" {
		return verify(userKey, xmlSignatureB64, sig)
	}
	return false
}
