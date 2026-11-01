//go:build windows

package nativetest_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // certificate thumbprints are SHA-1
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"software.sslmate.com/src/go-pkcs12"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/pkg/sign"
)

var (
	wintrustDLL     = syscall.NewLazyDLL("wintrust.dll")
	pWinVerifyTrust = wintrustDLL.NewProc("WinVerifyTrust")
)

// HRESULTs of WinVerifyTrust.
const (
	trustOK            = 0
	trustBadDigest     = 0x80096010
	trustNoSignature   = 0x800B0100
	trustUntrustedRoot = 0x800B0109
)

type wintrustFileInfo struct {
	cbStruct uint32
	filePath *uint16
	_        uintptr // hFile
	_        uintptr // pgKnownSubject
}

type wintrustData struct {
	cbStruct           uint32
	policyCallbackData uintptr
	sipClientData      uintptr
	uiChoice           uint32
	revocationChecks   uint32
	unionChoice        uint32
	file               *wintrustFileInfo
	stateAction        uint32
	stateData          uintptr
	urlReference       *uint16
	provFlags          uint32
	uiContext          uint32
	pSignatureSettings uintptr
}

// genericVerifyV2 is WINTRUST_ACTION_GENERIC_VERIFY_V2,
// {00AAC56B-CD44-11d0-8CC2-00C04FC295EE}.
var genericVerifyV2 = syscall.GUID{Data1: 0x00AAC56B, Data2: 0xCD44, Data3: 0x11D0, Data4: [8]byte{0x8C, 0xC2, 0x00, 0xC0, 0x4F, 0xC2, 0x95, 0xEE}}

// winVerifyTrust returns the Authenticode verdict of Windows for path,
// without UI and without revocation checks.
func winVerifyTrust(t *testing.T, path string) uint32 {
	t.Helper()

	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	fi := &wintrustFileInfo{filePath: p}
	fi.cbStruct = uint32(unsafe.Sizeof(*fi))

	data := &wintrustData{
		uiChoice:    2, // WTD_UI_NONE
		unionChoice: 1, // WTD_CHOICE_FILE
		file:        fi,
		stateAction: 1,      // WTD_STATEACTION_VERIFY
		provFlags:   0x1000, // WTD_CACHE_ONLY_URL_RETRIEVAL
	}
	data.cbStruct = uint32(unsafe.Sizeof(*data))

	invalidHandle := ^uintptr(0)
	r, _, _ := pWinVerifyTrust.Call(invalidHandle, uintptr(unsafe.Pointer(&genericVerifyV2)), uintptr(unsafe.Pointer(data)))

	data.stateAction = 2 // WTD_STATEACTION_CLOSE
	_, _, _ = pWinVerifyTrust.Call(invalidHandle, uintptr(unsafe.Pointer(&genericVerifyV2)), uintptr(unsafe.Pointer(data)))

	return uint32(r)
}

// signingPKI is a test CA and a code-signing leaf issued by it.
type signingPKI struct {
	ca      *x509.Certificate
	leaf    *x509.Certificate
	leafKey crypto.Signer
	trusted bool
}

func newCertificate(t *testing.T, tmpl, parent *x509.Certificate, pub crypto.PublicKey, parentKey crypto.Signer) *x509.Certificate {
	t.Helper()

	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return cert
}

// newPKI creates a CA and installs it in LocalMachine\Root for the test.
// Without elevation the CA stays untrusted, and a valid signature shows
// as CERT_E_UNTRUSTEDROOT instead of success.
func newPKI(t *testing.T, leafKey crypto.Signer) *signingPKI {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "msi-builder test CA " + now.Format(time.RFC3339Nano)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	ca := newCertificate(t, caTmpl, caTmpl, caKey.Public(), caKey)

	leaf := newCertificate(t, &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano() + 1),
		Subject:      pkix.Name{CommonName: "msi-builder test signer"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}, ca, leafKey.Public(), caKey)

	pki := &signingPKI{ca: ca, leaf: leaf, leafKey: leafKey}

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.cer")

	if err := os.WriteFile(caFile, ca.Raw, 0o600); err != nil {
		t.Fatal(err)
	}

	thumb := sha1.Sum(ca.Raw) //nolint:gosec // thumbprint
	out, err := powershell("Import-Certificate -FilePath '" + caFile + "' -CertStoreLocation Cert:\\LocalMachine\\Root | Out-Null")
	if err != nil {
		t.Logf("CA not trusted (needs elevation): %v %s", err, out)

		return pki
	}

	pki.trusted = true

	t.Cleanup(func() {
		_, _ = powershell("Remove-Item -LiteralPath 'Cert:\\LocalMachine\\Root\\" + strings.ToUpper(hex.EncodeToString(thumb[:])) + "'")
	})

	return pki
}

func powershell(command string) (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()

	return strings.TrimSpace(string(out)), err
}

// wantValid is the WinVerifyTrust result of a valid signature.
func (p *signingPKI) wantValid() uint32 {
	if p.trusted {
		return trustOK
	}

	return trustUntrustedRoot
}

func (p *signingPKI) options(h crypto.Hash) sign.Options {
	return sign.Options{Signer: p.leafKey, Certificates: []*x509.Certificate{p.leaf}, Hash: h, Description: "msi-builder test"}
}

func rsaLeafKey(t *testing.T) crypto.Signer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	return key
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// authenticodeStatus is Get-AuthenticodeSignature's Status.
func authenticodeStatus(t *testing.T, path string) string {
	t.Helper()

	out, err := powershell("(Get-AuthenticodeSignature -LiteralPath '" + path + "').Status")
	if err != nil {
		t.Fatalf("Get-AuthenticodeSignature: %v %s", err, out)
	}

	return out
}

// findSigntool returns the newest signtool.exe of the Windows SDK, or "".
func findSigntool() string {
	if p, err := exec.LookPath("signtool.exe"); err == nil {
		return p
	}

	matches, _ := filepath.Glob(filepath.Join(os.Getenv("ProgramFiles(x86)"), "Windows Kits", "10", "bin", "*", "x64", "signtool.exe"))
	slices.Sort(matches)

	if len(matches) == 0 {
		return ""
	}

	return matches[len(matches)-1]
}

// signtoolSign signs path in place with signtool and the leaf key.
func signtoolSign(t *testing.T, signtool string, pki *signingPKI, path, digest string) {
	t.Helper()

	pfx, err := pkcs12.Modern.Encode(pki.leafKey, pki.leaf, []*x509.Certificate{pki.ca}, "test")
	if err != nil {
		t.Fatal(err)
	}

	pfxFile := writeTemp(t, "leaf.pfx", pfx)

	out, err := exec.Command(signtool, "sign", "/fd", digest, "/f", pfxFile, "/p", "test", path).CombinedOutput()
	if err != nil {
		t.Fatalf("signtool sign: %v\n%s", err, out)
	}
}

func signtoolVerify(t *testing.T, signtool, path string) (string, error) {
	t.Helper()

	out, err := exec.Command(signtool, "verify", "/pa", "/v", path).CombinedOutput()

	return string(out), err
}

// rewrite re-lays out pkg with change applied to its root streams.
func rewrite(t *testing.T, pkg []byte, opts cfb.Options, change func([]cfb.Stream) []cfb.Stream) []byte {
	t.Helper()

	file, err := cfb.Read(pkg)
	if err != nil {
		t.Fatal(err)
	}

	streams := make([]cfb.Stream, 0, len(file.Streams()))
	for _, s := range file.Streams() {
		streams = append(streams, cfb.Stream{Name: s.Name, Data: bytes.Clone(s.Data)})
	}

	if change != nil {
		streams = change(streams)
	}

	if opts.CLSID == ([16]byte{}) {
		opts.CLSID = file.CLSID
	}

	out, err := cfb.Write(streams, opts)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func unsignedMinimal(t *testing.T) []byte {
	t.Helper()

	path, _ := buildExample(t, "minimal", nil, minimalInputs())

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func mustSign(t *testing.T, pkg []byte, opts sign.Options) []byte {
	t.Helper()

	signed, err := sign.Sign(context.Background(), pkg, opts)
	if err != nil {
		t.Fatal(err)
	}

	return signed
}

// TestSignatureSIG01 signs the minimal example with msi-builder and with
// signtool and checks each against the other verifier (MSISPEC-RESULTS
// SIG-01).
func TestSignatureSIG01(t *testing.T) {
	if testing.Short() {
		t.Skip("native signature tests")
	}

	pki := newPKI(t, rsaLeafKey(t))
	unsigned := unsignedMinimal(t)

	if got := winVerifyTrust(t, writeTemp(t, "unsigned.msi", unsigned)); got != trustNoSignature {
		t.Errorf("unsigned: WinVerifyTrust = %#x, want TRUST_E_NOSIGNATURE", got)
	}

	signed := mustSign(t, unsigned, pki.options(crypto.SHA256))
	signedPath := writeTemp(t, "signed.msi", signed)

	if got := winVerifyTrust(t, signedPath); got != pki.wantValid() {
		t.Errorf("msi-builder signed: WinVerifyTrust = %#x, want %#x", got, pki.wantValid())
	}

	if pki.trusted {
		if got := authenticodeStatus(t, signedPath); got != "Valid" {
			t.Errorf("msi-builder signed: Get-AuthenticodeSignature = %s", got)
		}
	}

	db := openDB(t, signedPath)
	if len(query(t, db, "SELECT `File` FROM `File`")) != 1 {
		t.Error("msi.dll does not read the signed package")
	}

	// One flipped content byte.
	tampered := rewrite(t, signed, cfb.Options{Version: 4}, func(s []cfb.Stream) []cfb.Stream {
		for i := range s {
			if s[i].Name != sign.StreamSignature && len(s[i].Data) > 0 {
				s[i].Data[0] ^= 1

				break
			}
		}

		return s
	})
	tamperedPath := writeTemp(t, "tampered.msi", tampered)

	if got := winVerifyTrust(t, tamperedPath); got != trustBadDigest {
		t.Errorf("tampered: WinVerifyTrust = %#x, want TRUST_E_BAD_DIGEST", got)
	}

	if got := authenticodeStatus(t, tamperedPath); got != "HashMismatch" {
		t.Errorf("tampered: Get-AuthenticodeSignature = %s", got)
	}

	signtool := findSigntool()
	if signtool == "" {
		t.Log("SIG-01: signtool.exe not found; skipping the signtool cross-checks")

		return
	}

	if pki.trusted {
		if out, err := signtoolVerify(t, signtool, signedPath); err != nil {
			t.Errorf("signtool verify of the msi-builder signature: %v\n%s", err, out)
		}
	}

	viaSigntool := writeTemp(t, "signtool.msi", unsigned)
	signtoolSign(t, signtool, pki, viaSigntool, "SHA256")

	data, err := os.ReadFile(viaSigntool)
	if err != nil {
		t.Fatal(err)
	}

	sig, err := sign.Verify(data)
	if err != nil {
		t.Fatalf("sign.Verify of the signtool signature: %v", err)
	}

	d, err := sign.Digest(unsigned, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}

	if !sig.Extended && !bytes.Equal(sig.Digest, d) {
		t.Errorf("signtool signed D %x, msi-builder computes %x", sig.Digest, d)
	}

	t.Logf("SIG-01: signtool %s: D %x, extended %v; msi-builder signature verified by WinVerifyTrust (trusted CA: %v)", signtool, sig.Digest, sig.Extended, pki.trusted)
}

// TestSignatureSIG02 records whether signtool writes MsiDigitalSignatureEx
// and checks the pre-hash msi-builder computes against it.
func TestSignatureSIG02(t *testing.T) {
	if testing.Short() {
		t.Skip("native signature tests")
	}

	signtool := findSigntool()
	if signtool == "" {
		t.Skip("signtool.exe not found")
	}

	pki := newPKI(t, rsaLeafKey(t))
	unsigned := unsignedMinimal(t)

	for _, digest := range []string{"SHA1", "SHA256"} {
		path := writeTemp(t, "signtool-"+digest+".msi", unsigned)
		signtoolSign(t, signtool, pki, path, digest)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		file, err := cfb.Read(data)
		if err != nil {
			t.Fatal(err)
		}

		ex, hasEx := file.Stream(sign.StreamSignatureEx)

		sig, err := sign.Verify(data)
		if err != nil {
			t.Errorf("%s: sign.Verify: %v", digest, err)

			continue
		}

		t.Logf("SIG-02: signtool /fd %s: MsiDigitalSignatureEx present %v (%d bytes), msi-builder verified %v, extended %v", digest, hasEx, len(ex), sig.Hash, sig.Extended)
	}
}

func reverse(streams []cfb.Stream) []cfb.Stream {
	slices.Reverse(streams)

	return streams
}

// TestSignatureSIG03 re-lays out signed packages: a basic-mode signature
// must survive another container version and stream order.
func TestSignatureSIG03(t *testing.T) {
	if testing.Short() {
		t.Skip("native signature tests")
	}

	pki := newPKI(t, rsaLeafKey(t))
	signed := mustSign(t, unsignedMinimal(t), pki.options(crypto.SHA256))

	for name, out := range map[string][]byte{
		"v3":       rewrite(t, signed, cfb.Options{Version: 3}, nil),
		"reversed": rewrite(t, signed, cfb.Options{Version: 4, AllBlack: true}, reverse),
	} {
		if got := winVerifyTrust(t, writeTemp(t, name+".msi", out)); got != pki.wantValid() {
			t.Errorf("SIG-03 %s: WinVerifyTrust = %#x, want %#x", name, got, pki.wantValid())
		}
	}

	signtool := findSigntool()
	if signtool == "" {
		return
	}

	// An extended signature from signtool, re-laid out by our writer with
	// zero times: log what Windows says.
	path := writeTemp(t, "signtool.msi", unsignedMinimal(t))
	signtoolSign(t, signtool, pki, path, "SHA256")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	relaid := writeTemp(t, "signtool-v3.msi", rewrite(t, data, cfb.Options{Version: 3}, nil))
	t.Logf("SIG-03: signtool signature after a v4 to v3 re-layout: WinVerifyTrust %#x", winVerifyTrust(t, relaid))
}

// TestSignatureSIG04 signs packages with prefix-related names and names
// that sort before \u0005MsiDigitalSignatureEx.
func TestSignatureSIG04(t *testing.T) {
	if testing.Short() {
		t.Skip("native signature tests")
	}

	pki := newPKI(t, rsaLeafKey(t))
	pkg := rewrite(t, unsignedMinimal(t), cfb.Options{Version: 4}, func(s []cfb.Stream) []cfb.Stream {
		return append(s,
			cfb.Stream{Name: "A", Data: []byte("short")},
			cfb.Stream{Name: "AB", Data: []byte("longer")},
			cfb.Stream{Name: "ABC", Data: []byte("longest")},
			cfb.Stream{Name: "\u3801X", Data: []byte("sorts before U+0005")},
		)
	})

	signed := mustSign(t, pkg, pki.options(crypto.SHA256))
	if got := winVerifyTrust(t, writeTemp(t, "prefix.msi", signed)); got != pki.wantValid() {
		t.Errorf("SIG-04 msi-builder: WinVerifyTrust = %#x, want %#x", got, pki.wantValid())
	}

	signtool := findSigntool()
	if signtool == "" {
		return
	}

	path := writeTemp(t, "prefix-signtool.msi", pkg)
	signtoolSign(t, signtool, pki, path, "SHA256")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	sig, err := sign.Verify(data)
	if err != nil {
		t.Errorf("SIG-04 signtool signature: sign.Verify: %v", err)
	} else {
		t.Logf("SIG-04: signtool signature verified by msi-builder, extended %v", sig.Extended)
	}
}

// withStorage rebuilds pkg's root streams with one root-level storage named
// 1031 that holds an Inner stream with data and, with decoy, a stream named
// like the signature stream.
func withStorage(t *testing.T, pkg []byte, data string, clsid [16]byte, decoy bool) []byte {
	t.Helper()

	file, err := cfb.Read(pkg)
	if err != nil {
		t.Fatal(err)
	}

	sources := make([]cfb.Source, 0, len(file.Streams()))
	for _, s := range file.Streams() {
		sources = append(sources, s.Source())
	}

	storages := []cfb.Storage{{Name: "1031", CLSID: clsid, Streams: []cfb.Source{
		cfb.Stream{Name: "Inner", Data: []byte(data)}.Source(),
	}}}
	if decoy {
		storages[0].Streams = append(storages[0].Streams, cfb.Stream{Name: sign.StreamSignature, Data: []byte("not a signature")}.Source())
	}

	var buf bytes.Buffer

	if _, err := cfb.WriteStoragesTo(&buf, sources, storages, cfb.Options{Version: file.Version, CLSID: file.CLSID}); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// TestSignatureSIG05 changes the root CLSID of a signed package, and signs
// a package with a root-level storage, as embedded transforms are stored,
// then changes the storage's stream data and CLSID. A signtool signature
// over a storage that holds a stream named like the signature tells
// whether Windows excludes that name below the root.
func TestSignatureSIG05(t *testing.T) {
	if testing.Short() {
		t.Skip("native signature tests")
	}

	pki := newPKI(t, rsaLeafKey(t))
	signed := mustSign(t, unsignedMinimal(t), pki.options(crypto.SHA256))

	clsid := cfb.CLSIDMSI
	clsid[15] ^= 1

	changed := writeTemp(t, "clsid.msi", rewrite(t, signed, cfb.Options{Version: 4, CLSID: clsid}, nil))
	got := winVerifyTrust(t, changed)
	t.Logf("SIG-05: changed root CLSID: WinVerifyTrust %#x", got)

	// A different CLSID may also make the MSI SIP not claim the file.
	if got == trustOK || got == trustUntrustedRoot {
		t.Errorf("SIG-05: a changed root CLSID still verifies: %#x", got)
	}

	unsigned := withStorage(t, unsignedMinimal(t), "transform", cfb.CLSIDTransform, true)
	stored := mustSign(t, unsigned, pki.options(crypto.SHA256))

	got = winVerifyTrust(t, writeTemp(t, "storage.msi", stored))
	t.Logf("SIG-05: msi-builder signed, storage 1031: WinVerifyTrust %#x", got)

	if got != pki.wantValid() {
		t.Errorf("SIG-05: signed package with a storage: WinVerifyTrust = %#x, want %#x", got, pki.wantValid())
	}

	clsid = cfb.CLSIDTransform
	clsid[15] ^= 1

	for name, pkg := range map[string][]byte{
		"storage stream data": withStorage(t, stored, "tampered", cfb.CLSIDTransform, false),
		"storage CLSID":       withStorage(t, stored, "transform", clsid, false),
	} {
		got := winVerifyTrust(t, writeTemp(t, "storage-changed.msi", pkg))
		t.Logf("SIG-05: changed %s: WinVerifyTrust %#x", name, got)

		if got != trustBadDigest {
			t.Errorf("SIG-05: changed %s: WinVerifyTrust = %#x, want TRUST_E_BAD_DIGEST", name, got)
		}
	}

	signtool := findSigntool()
	if signtool == "" {
		return
	}

	path := writeTemp(t, "storage-signtool.msi", unsigned)
	signtoolSign(t, signtool, pki, path, "SHA256")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// sign.Verify hashes the decoy below the root. If Windows excluded it,
	// the digests differ and this fails.
	_, err = sign.Verify(data)
	t.Logf("SIG-05: signtool-signed, storage with a signature-named stream: WinVerifyTrust %#x, sign.Verify %v", winVerifyTrust(t, path), err)

	if err != nil {
		t.Errorf("SIG-05: signtool-signed package with a storage: sign.Verify: %v", err)
	}
}

// TestSignatureSIG06 signs with every digest and an ECDSA key, and with an
// RFC 3161 timestamp.
func TestSignatureSIG06(t *testing.T) {
	if testing.Short() {
		t.Skip("native signature tests")
	}

	unsigned := unsignedMinimal(t)
	rsaPKI := newPKI(t, rsaLeafKey(t))

	for _, h := range []crypto.Hash{crypto.SHA1, crypto.SHA256, crypto.SHA384, crypto.SHA512} {
		got := winVerifyTrust(t, writeTemp(t, "rsa.msi", mustSign(t, unsigned, rsaPKI.options(h))))
		t.Logf("SIG-06: RSA %v: WinVerifyTrust %#x", h, got)

		if got != rsaPKI.wantValid() {
			t.Errorf("SIG-06: RSA %v: WinVerifyTrust = %#x, want %#x", h, got, rsaPKI.wantValid())
		}
	}

	for _, tc := range []struct {
		curve elliptic.Curve
		hash  crypto.Hash
	}{{elliptic.P256(), crypto.SHA256}, {elliptic.P384(), crypto.SHA384}} {
		key, err := ecdsa.GenerateKey(tc.curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}

		ecPKI := newPKI(t, key)
		got := winVerifyTrust(t, writeTemp(t, "ec.msi", mustSign(t, unsigned, ecPKI.options(tc.hash))))
		// ECDSA passes on Server 2025 but is untested on older Windows: record, don't assert.
		t.Logf("SIG-06: ECDSA %s %v: WinVerifyTrust %#x (valid would be %#x)", tc.curve.Params().Name, tc.hash, got, ecPKI.wantValid())
	}

	opts := rsaPKI.options(crypto.SHA256)
	opts.TimestampURL = "http://timestamp.digicert.com"

	stamped, err := sign.Sign(context.Background(), unsigned, opts)
	if err != nil {
		if errors.Is(err, sign.ErrTimestampUnavailable) {
			t.Skipf("timestamp authority unavailable: %v", err)
		}

		t.Fatal(err)
	}

	sig, err := sign.Verify(stamped)
	if err != nil {
		t.Fatal(err)
	}

	got := winVerifyTrust(t, writeTemp(t, "stamped.msi", stamped))
	t.Logf("SIG-06: RFC 3161 timestamp %v: WinVerifyTrust %#x", sig.Timestamp, got)

	if got != rsaPKI.wantValid() {
		t.Errorf("SIG-06: timestamped: WinVerifyTrust = %#x, want %#x", got, rsaPKI.wantValid())
	}
}

// TestSignatureNameTerminatorCFB11 overwrites only the NUL terminator of
// the signature stream's name in a signed package. Windows then finds no
// signature (TRUST_E_NOSIGNATURE), and sign.Verify must not report one
// either: the reader rejects a name without its terminator (CFB-11).
func TestSignatureNameTerminatorCFB11(t *testing.T) {
	if testing.Short() {
		t.Skip("native signature tests")
	}

	pki := newPKI(t, rsaLeafKey(t))
	signed := mustSign(t, unsignedMinimal(t), pki.options(crypto.SHA256))

	// The stored name, UTF-16LE, followed by its terminator.
	name := []byte(strings.Join(strings.Split(sign.StreamSignature, ""), "\x00") + "\x00\x00\x00")
	if bytes.Count(signed, name) != 1 {
		t.Fatal("cannot find the signature stream's directory entry")
	}

	bad := bytes.Clone(signed)
	at := bytes.Index(bad, name) + len(name) - 2
	bad[at] = 'X'

	if got := winVerifyTrust(t, writeTemp(t, "unterminated.msi", bad)); got != trustNoSignature {
		t.Errorf("CFB-11: WinVerifyTrust = %#x, want TRUST_E_NOSIGNATURE", got)
	}

	if _, err := sign.Verify(bad); err == nil || !strings.Contains(err.Error(), "terminator") {
		t.Errorf("CFB-11: sign.Verify = %v, want an error about the name terminator", err)
	}
}
