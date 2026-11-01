package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"

	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/sign"
)

func TestBuildValidateInspect(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "minimal", "msi.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	yaml := filepath.Join(dir, "msi.yaml")
	if err := os.WriteFile(yaml, src, 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	if code := run([]string{"validate", yaml}, &stdout, &stderr); code != 0 {
		t.Fatalf("validate: exit %d: %s", code, stderr.String())
	}

	out := filepath.Join(dir, "out.msi")

	if code := run([]string{"build", "-o", out, yaml}, &stdout, &stderr); code != 0 {
		t.Fatalf("build: exit %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "1 files") {
		t.Errorf("build output %q", stdout.String())
	}

	stdout.Reset()

	if code := run([]string{"inspect", "--table", "File", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("inspect: exit %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "hello.exe") {
		t.Errorf("inspect output %q", stdout.String())
	}

	stdout.Reset()

	if code := run([]string{"inspect", out}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "summary  9:") {
		t.Errorf("inspect: exit %d, output %q", code, stdout.String())
	}

	stderr.Reset()

	// An error without a diagnostic is printed with its cause.
	if code := run([]string{"build", "-o", filepath.Join(dir, "missing", "x.msi"), yaml}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "build failed: ") || !strings.Contains(stderr.String(), "missing") {
		t.Errorf("missing output directory: exit %d, stderr %q", code, stderr.String())
	}

	if code := run([]string{"build", filepath.Join(dir, "missing.yaml")}, &stdout, &stderr); code != 1 {
		t.Errorf("missing file: exit %d", code)
	}

	if code := run(nil, &stdout, &stderr); code != exitUsage {
		t.Errorf("no command: exit %d", code)
	}

	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Errorf("version: exit %d", code)
	}
}

// testKeyFiles are the paths writeTestKey wrote.
type testKeyFiles struct{ cert, key, pfx string }

func writeTestKey(t *testing.T, dir string) testKeyFiles {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "msi-builder CLI test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	pfx, err := pkcs12.Modern.Encode(key, cert, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	files := testKeyFiles{cert: filepath.Join(dir, "cert.pem"), key: filepath.Join(dir, "key.pem"), pfx: filepath.Join(dir, "key.pfx")}

	for name, data := range map[string][]byte{
		files.cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		files.key:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		files.pfx:  pfx,
	} {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return files
}

func TestSignVerify(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "minimal", "msi.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	yaml := filepath.Join(dir, "msi.yaml")
	if err := os.WriteFile(yaml, src, 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	keys := writeTestKey(t, dir)
	certFile, keyFile, pfxFile := keys.cert, keys.key, keys.pfx

	var stdout, stderr bytes.Buffer

	unsigned := filepath.Join(dir, "unsigned.msi")
	if code := run([]string{"build", "-q", "-o", unsigned, yaml}, &stdout, &stderr); code != 0 {
		t.Fatalf("build: exit %d: %s", code, stderr.String())
	}

	if code := run([]string{"verify", unsigned}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "not signed") {
		t.Errorf("verify unsigned: exit %d: %s", code, stderr.String())
	}

	signed := filepath.Join(dir, "signed.msi")
	if code := run([]string{"build", "-q", "-o", signed, "--cert", certFile, "--key", keyFile, yaml}, &stdout, &stderr); code != 0 {
		t.Fatalf("build --cert: exit %d: %s", code, stderr.String())
	}

	stdout.Reset()

	if code := run([]string{"verify", signed}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "signature valid (basic, SHA-256, no timestamp)") {
		t.Errorf("verify: exit %d: %s %s", code, stdout.String(), stderr.String())
	}

	// A build whose signing fails leaves no unsigned package behind and
	// keeps a package that was already there.
	badKey := filepath.Join(dir, "bad.pfx")
	if err := os.WriteFile(badKey, []byte("not a pfx"), 0o600); err != nil {
		t.Fatal(err)
	}

	failed := filepath.Join(dir, "failed.msi")
	if code := run([]string{"build", "-q", "-o", failed, "--pfx", badKey, yaml}, &stdout, &stderr); code == 0 {
		t.Error("build with a bad key succeeded")
	}

	if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the failed signing build left %s behind: %v", failed, err)
	}

	before, err := os.ReadFile(unsigned)
	if err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"build", "-q", "-o", unsigned, "--pfx", badKey, yaml}, &stdout, &stderr); code == 0 {
		t.Error("build with a bad key succeeded")
	}

	if kept, err := os.ReadFile(unsigned); err != nil || !bytes.Equal(kept, before) {
		t.Errorf("the failed signing build touched the existing package: %v", err)
	}

	left, _ := os.ReadDir(dir)
	for _, ent := range left {
		if strings.HasPrefix(ent.Name(), ".msi-builder-") {
			t.Errorf("the failed signing build left %s behind", ent.Name())
		}
	}

	resigned := filepath.Join(dir, "resigned.msi")
	if code := run([]string{"sign", "--pfx", pfxFile, "--hash", "sha384", "-o", resigned, unsigned}, &stdout, &stderr); code != 0 {
		t.Fatalf("sign: exit %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "SHA-384") {
		t.Errorf("sign output %q", stdout.String())
	}

	for name, args := range map[string][]string{
		"no key":       {"sign", unsigned},
		"both":         {"sign", "--pfx", pfxFile, "--cert", certFile, "--key", keyFile, unsigned},
		"bad hash":     {"sign", "--pfx", pfxFile, "--hash", "md5", unsigned},
		"missing env":  {"sign", "--pfx", pfxFile, "--password-env", "MSI_BUILDER_TEST_UNSET_VARIABLE", unsigned},
		"cert only":    {"sign", "--cert", certFile, unsigned},
		"no key build": {"build", "--timestamp-url", "http://example.com", yaml},
		"env with pem": {"sign", "--cert", certFile, "--key", keyFile, "--password-env", "HOME", unsigned},
		"no package":   {"verify"},
		"missing file": {"verify", filepath.Join(dir, "missing.msi")},
	} {
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Errorf("%s: exit 0", name)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"unknown flag":       {"build", "--no-such-flag", "msi.yaml"},
		"single-dash long":   {"sign", "-pfx", "key.pfx", "package.msi"},
		"no argument":        {"validate"},
		"two arguments":      {"inspect", "a.msi", "b.msi"},
		"unknown command":    {"bild"},
		"bad flag value":     {"build", "--cfb", "x", "msi.yaml"},
		"bad --var":          {"build", "--var", "NOVALUE", "msi.yaml"},
		"version argument":   {"version", "extra"},
		"ignored sign flags": {"build", "--timestamp-url", "http://example.com", "msi.yaml"},
		"cfb out of range":   {"build", "--cfb", "5", "missing.yaml"},
		"bad hash":           {"sign", "--pfx", "key.pfx", "--hash", "md5", "missing.msi"},
		"cert without key":   {"build", "--cert", "cert.pem", "missing.yaml"},
		"key without cert":   {"sign", "--key", "key.pem", "missing.msi"},
		"pfx and cert":       {"sign", "--pfx", "key.pfx", "--cert", "c.pem", "--key", "k.pem", "missing.msi"},
		"no shell":           {"completion"},
		"unknown shell":      {"completion", "fsh"},
		"unknown help topic": {"help", "nosuch"},
		"version shorthand":  {"-v"},
		"double dash only":   {"--"},
		"command after --":   {"--", "build"},
	} {
		var stdout, stderr bytes.Buffer

		if code := run(args, &stdout, &stderr); code != exitUsage {
			t.Errorf("%s: exit %d, want %d: %s", name, code, exitUsage, stderr.String())
		}

		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "msi-builder: ") || !strings.Contains(stderr.String(), "--help' for usage") {
			t.Errorf("%s: stdout %q, stderr %q", name, stdout.String(), stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer

	if code := run(nil, &stdout, &stderr); code != exitUsage || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Available Commands:") {
		t.Errorf("no command: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}

	stderr.Reset()
	run([]string{"bild"}, &stdout, &stderr)

	if !strings.Contains(stderr.String(), "Did you mean this?\n\tbuild") {
		t.Errorf("no suggestion for a misspelled command: %q", stderr.String())
	}
}

func TestFlagErrorsGoToStderr(t *testing.T) {
	t.Parallel()

	for _, cmd := range []string{"build", "validate", "inspect", "sign", "verify", "version", "completion bash"} {
		var stdout, stderr bytes.Buffer

		if code := run(append(strings.Fields(cmd), "--no-such-flag"), &stdout, &stderr); code != exitUsage {
			t.Errorf("%s: exit %d for an unknown flag", cmd, code)
		}

		want := "msi-builder: unknown flag: --no-such-flag\nRun 'msi-builder " + cmd + " --help' for usage.\n"
		if stdout.Len() != 0 || stderr.String() != want {
			t.Errorf("%s: stdout %q, stderr %q", cmd, stdout.String(), stderr.String())
		}
	}
}

func TestHelpAndCompletion(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"help":            {[]string{"--help"}, "Available Commands:"},
		"command help":    {[]string{"build", "-h"}, "--password-env"},
		"help command":    {[]string{"help", "sign"}, "--timestamp-url"},
		"version flag":    {[]string{"--version"}, "msi-builder dev (commit none"},
		"version":         {[]string{"version"}, "msi-builder dev (commit none"},
		"bash completion": {[]string{"completion", "bash"}, "__start_msi-builder"},
		"fish completion": {[]string{"completion", "fish"}, "complete -c msi-builder"},
		"zsh completion":  {[]string{"completion", "zsh", "--no-descriptions"}, "#compdef msi-builder"},
		"ps completion":   {[]string{"completion", "powershell"}, "Register-ArgumentCompleter"},
		"complete help":   {[]string{"__complete", "help", ""}, "inspect"},
		"complete help 2": {[]string{"__complete", "help", "completion", "z"}, "zsh\tPrint the completion script for Zsh"},
		"complete hash":   {[]string{"__complete", "sign", "--hash", ""}, "sha384"},
		"complete cfb":    {[]string{"__complete", "build", "--cfb", ""}, "3\n4\n"},
		"complete yaml":   {[]string{"__complete", "build", ""}, "yaml\nyml\n"},
	} {
		var stdout, stderr bytes.Buffer

		if code := run(tc.args, &stdout, &stderr); code != exitOK || !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout.String(), stderr.String())
		}
	}
}

func TestFlagsAfterArguments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "minimal", "msi.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	yaml := filepath.Join(dir, "msi.yaml")
	if err := os.WriteFile(yaml, src, 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.msi")

	var stdout, stderr bytes.Buffer

	if code := run([]string{"build", yaml, "--output", out, "--quiet"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("build: exit %d: %s", code, stderr.String())
	}

	if stdout.Len() != 0 {
		t.Errorf("--quiet printed %q", stdout.String())
	}

	stdout.Reset()

	if code := run([]string{"__complete", "inspect", out, "--table", ""}, &stdout, &stderr); code != exitOK || !strings.Contains(stdout.String(), "\nFile\n") {
		t.Errorf("table completion: exit %d, stdout %q", code, stdout.String())
	}
}

// TestSignExternalCabinets checks a signed build whose cabinets go beside
// the package: they arrive with the signed package, and a signing that
// fails touches neither the existing package nor its cabinets.
func TestSignExternalCabinets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "minimal", "msi.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// A Windows checkout may have CRLF line endings, so keep the line end.
	src = bytes.Replace(src, []byte("  platform: x64"), []byte("  platform: x64\n  embedCabinets: false"), 1)
	if !bytes.Contains(src, []byte("embedCabinets")) {
		t.Fatal("the minimal example has no platform line")
	}

	yaml := filepath.Join(dir, "msi.yaml")
	if err := os.WriteFile(yaml, src, 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	keys := writeTestKey(t, dir)
	out := filepath.Join(dir, "out")

	if err := os.Mkdir(out, 0o750); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	signed := filepath.Join(out, "signed.msi")
	cabinet := filepath.Join(out, "Hello.cab")

	if code := run([]string{"build", "-o", signed, "--pfx", keys.pfx, yaml}, &stdout, &stderr); code != 0 {
		t.Fatalf("build --pfx: exit %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), cabinet+": ") {
		t.Errorf("build output %q does not name %s", stdout.String(), cabinet)
	}

	if code := run([]string{"verify", signed}, &stdout, &stderr); code != 0 {
		t.Errorf("verify: exit %d: %s", code, stderr.String())
	}

	// The cabinet carries a signature of its own that the package lists.
	cabFile, err := os.Open(cabinet)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sign.VerifyCabinet(cabFile)
	_ = cabFile.Close()

	if err != nil {
		t.Errorf("cabinet signature: %v", err)
	}

	before := map[string][]byte{}

	for _, path := range []string{signed, cabinet} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		before[path] = data
	}

	// A changed payload with a signing that fails at the timestamp server.
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), append(exe, 0), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"build", "-q", "-o", signed, "--pfx", keys.pfx, "--timestamp-url", "http://127.0.0.1:1", yaml}, &stdout, &stderr); code == 0 {
		t.Fatal("build with an unreachable timestamp server succeeded")
	}

	for path, data := range before {
		if kept, err := os.ReadFile(path); err != nil || !bytes.Equal(kept, data) {
			t.Errorf("the failed signing build changed %s: %v", path, err)
		}
	}

	left, _ := os.ReadDir(out)
	if len(left) != 2 {
		t.Errorf("the output folder holds %d entries, want the package and its cabinet", len(left))
	}
}

// TestDefaultOutputStaysBesideTheConfig checks that the default output name
// built from a display name with path separators stays in the YAML file's
// folder (REVIEW1 #9).
func TestDefaultOutputStaysBesideTheConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "config")

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	yaml := filepath.Join(dir, "msi.yaml")
	src := `schema: msi-builder/v1
product: { name: ../Client/Server, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
installDir: { name: Application }
components:
  - files: [{ source: readme.txt }]
`

	for name, data := range map[string]string{yaml: src, filepath.Join(dir, "readme.txt"): "hello"} {
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer

	if code := run([]string{"build", "-q", yaml}, &stdout, &stderr); code != 0 {
		t.Fatalf("build: exit %d: %s", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(dir, "_Client_Server-1.0.0.msi")); err != nil {
		t.Errorf("package not beside the YAML file: %v", err)
	}

	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("the build wrote outside the YAML file's folder: %v", entries)
	}
}

func TestDefaultFileName(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"Hello-1.0.0":          "Hello-1.0.0",
		"Client/Server-1.0.0":  "Client_Server-1.0.0",
		`a<b>c:d"e\f|g?h*-1.0`: "a_b_c_d_e_f_g_h_-1.0",
		"..-1.0.0":             "1.0.0",
		"-":                    "package",
		". .hidden-1.0":        "hidden-1.0",
		"tab\there-1.0":        "tab_here-1.0",
	} {
		if got := defaultFileName(name); got != want {
			t.Errorf("defaultFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

// Two custom actions anchored to each other fail validate, not only build,
// at the placement on the cycle.
func TestValidateScheduleCycle(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "minimal", "msi.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	src = append(src, []byte(`
customActions:
  - id: A
    setProperty: {name: PA, value: x}
    schedule: [{sequence: execute, before: B}]
  - id: B
    setProperty: {name: PB, value: x}
    schedule: [{sequence: execute, before: A}]
`)...)

	yaml := filepath.Join(t.TempDir(), "msi.yaml")
	if err := os.WriteFile(yaml, src, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	if code := run([]string{"validate", yaml}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "customActions[0].schedule[0]") ||
		!strings.Contains(stderr.String(), "A → B → A") {
		t.Errorf("validate: exit %d: %s", code, stderr.String())
	}
}
