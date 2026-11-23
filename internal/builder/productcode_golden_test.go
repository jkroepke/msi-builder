package builder_test

import "testing"

// TestProductCodeGolden pins the automatic ProductCode of a configuration
// whose strings have blanks at line breaks, which yaml.v4 writes in another
// style than yaml.v3: the code must not change with a YAML module update.
func TestProductCodeGolden(t *testing.T) {
	t.Parallel()

	const src = `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
properties:
  TRAILING: { value: "first line \nsecond line" }
  LEADING: { value: "first line\n  second line" }
  BREAK: { value: "\n" }
  TAB: { value: "a\t\nb\t" }
components:
  - files: [{ source: hello.exe }]
`

	const want = "{16C4C164-6CA9-558D-9856-A79199A960D6}"

	got := cell(t, buildYAML(t, src), "Property", "Value", func(r []any) bool { return r[0] == "ProductCode" })
	if got != want {
		t.Errorf("ProductCode %v, want %v", got, want)
	}
}
