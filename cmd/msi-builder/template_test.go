package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

const templateYAML = `vars:
  ARCH: x64
---
schema: msi-builder/v1
product:
  name: Hello
  manufacturer: Example
  version: "{{ .VERSION }}"
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: "{{ .ARCH }}"
components:
  - files:
      - source: hello.exe
{{- if eq .ARCH "arm64" }}
      - source: hello-arm64.dll
{{- end }}
`

// TestTemplates checks build, validate and render on a template.
func TestTemplates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	yaml := filepath.Join(dir, "msi.yaml")
	if err := os.WriteFile(yaml, []byte(templateYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	if code := run([]string{"validate", yaml}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "variable VERSION is not set; pass --var VERSION=VALUE") {
		t.Errorf("validate without VERSION: exit %d, stderr %q", code, stderr.String())
	}

	stderr.Reset()

	if code := run([]string{"validate", "--var", "VERSION=1.0.0", yaml}, &stdout, &stderr); code != 0 {
		t.Errorf("validate: exit %d, stderr %q", code, stderr.String())
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.msi")
	if code := run([]string{"build", "--var", "VERSION=1.0.0", "-o", out, yaml}, &stdout, &stderr); code != 0 {
		t.Errorf("build: exit %d, stderr %q", code, stderr.String())
	}

	stdout.Reset()

	if code := run([]string{"render", "--var", "ARCH=arm64", "--var", "VERSION=2.0.0", yaml}, &stdout, &stderr); code != 0 {
		t.Fatalf("render: exit %d, stderr %q", code, stderr.String())
	}

	for _, want := range []string{"vars:\n  ARCH: x64\n---\n", `platform: "arm64"`, "source: hello-arm64.dll\n", `version: "2.0.0"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("render output %q lacks %q", stdout.String(), want)
		}
	}

	stderr.Reset()

	if code := run([]string{"render", filepath.Join(dir, "missing.yaml")}, &stdout, &stderr); code != 1 {
		t.Errorf("render of a missing file: exit %d, stderr %q", code, stderr.String())
	}
}
