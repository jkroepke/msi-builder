package builder_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jkroepke/msi-builder/internal/artwork"
	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/summary"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

var buildTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) //nolint:gochecknoglobals // lookup table

// example copies examples/<name>/msi.yaml into a temporary directory next to
// the given input files and loads it.
func example(t *testing.T, name string, vars map[string]string, inputs map[string][]byte) *config.Config {
	t.Helper()

	dir := t.TempDir()

	src, err := os.ReadFile(filepath.Join("..", "..", "examples", name, "msi.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), src, 0o600); err != nil {
		t.Fatal(err)
	}

	for n, data := range inputs {
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{Vars: vars})
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

func mustBuild(t *testing.T, cfg *config.Config) *builder.Result {
	t.Helper()

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	for _, d := range res.Diagnostics {
		t.Logf("diagnostic: %s: %s", d.Path, d.Message)
	}

	return res
}

func rows(t *testing.T, db *msidb.Database, table string) [][]any {
	t.Helper()

	tb := db.Table(table)
	if tb == nil {
		t.Fatalf("table %s is missing", table)
	}

	return tb.Rows
}

func cell(t *testing.T, db *msidb.Database, table, column string, match func([]any) bool) any {
	t.Helper()

	tbl := db.Table(table)
	if tbl == nil {
		t.Fatalf("table %s is missing", table)
	}

	col := tbl.Column(column)
	if col < 0 {
		t.Fatalf("%s has no column %s", table, column)
	}

	for _, r := range tbl.Rows {
		if match(r) {
			return r[col]
		}
	}

	t.Fatalf("%s: no matching row", table)

	return nil
}

func TestMinimal(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	cfg := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})
	res := mustBuild(t, cfg)

	if len(res.Package) == 0 || res.Package[0] != 0xD0 {
		t.Fatal("no package written")
	}

	wantFile := builder.File{Key: "hello.exe", Name: "hello.exe", Source: "hello.exe", Component: "hello.exe", Version: "1.0.0.0", Language: "1033", Size: int64(len(exe)), Sequence: 1}
	if len(res.Files) != 1 || res.Files[0] != wantFile {
		t.Errorf("file %+v, want %+v", res.Files[0], wantFile)
	}

	file, err := cfb.Read(res.Package)
	if err != nil {
		t.Fatal(err)
	}

	if file.CLSID != cfb.CLSIDMSI {
		t.Errorf("CLSID %x", file.CLSID)
	}

	database, err := msidb.Unmarshal(res.Package)
	if err != nil {
		t.Fatal(err)
	}

	if len(database.Problems) > 0 {
		t.Errorf("problems: %v", database.Problems)
	}

	// File row: versioned x64 executable, its 8.3 name as is, sequence 1.
	fileRows := rows(t, database, "File")
	if len(fileRows) != 1 {
		t.Fatalf("File rows: %d", len(fileRows))
	}

	if fileRows[0][0] != "hello.exe" || fileRows[0][4] != "1.0.0.0" || fileRows[0][5] != "1033" || fileRows[0][7] != 1 {
		t.Errorf("File row %v", fileRows[0])
	}

	if _, ok := fileRows[0][2].(string); !ok || fileRows[0][2] != "hello.exe" {
		t.Errorf("FileName %v, want the valid short name as is", fileRows[0][2])
	}

	if cell(t, database, "Media", "LastSequence", func([]any) bool { return true }) != 1 {
		t.Error("Media.LastSequence")
	}

	cabName := cell(t, database, "Media", "Cabinet", func([]any) bool { return true })
	if cabName != "#Hello.cab" {
		t.Errorf("Media.Cabinet %v", cabName)
	}

	data, ok := database.Stream("Hello.cab")
	if !ok {
		t.Fatal("cabinet stream missing")
	}

	entries, err := cab.Read(data)
	if err != nil || len(entries) != 1 || !bytes.Equal(entries[0].Data, exe) {
		t.Fatalf("cabinet: %v, %d entries", err, len(entries))
	}

	// Component: id from the key file, 64-bit attribute, key path is the file.
	attrs := cell(t, database, "Component", "Attributes", func(r []any) bool { return r[0] == "hello.exe" })
	if attrs != 256 {
		t.Errorf("Component.Attributes %v", attrs)
	}

	// Directory tree: TARGETDIR/SourceDir, ProgramFiles64Folder, INSTALLDIR.
	if cell(t, database, "Directory", "DefaultDir", func(r []any) bool { return r[0] == "TARGETDIR" }) != "SourceDir" {
		t.Error("TARGETDIR")
	}

	if cell(t, database, "Directory", "DefaultDir", func(r []any) bool { return r[0] == "ProgramFiles64Folder" }) != "PFiles64" {
		t.Error("ProgramFiles64Folder")
	}

	// Configurable directory → secure property.
	secure := cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "SecureCustomProperties" })
	if s := fmt.Sprint(secure); !strings.Contains(s, "INSTALLDIR") || !strings.Contains(s, "UPGRADE") {
		t.Errorf("SecureCustomProperties %v", secure)
	}

	if cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "ALLUSERS" }) != "1" {
		t.Error("ALLUSERS")
	}

	if cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "ProductCode" }) != res.ProductCode {
		t.Error("ProductCode")
	}

	// Sequences: the always set plus the file actions and the upgrade.
	ies := rows(t, database, "InstallExecuteSequence")
	want := map[string]int{
		"CostInitialize": 800, "InstallFiles": 4000, "RemoveFiles": 3500, "CreateFolders": 3700, "RemoveFolders": 3600,
		"InstallFinalize": 6600, "FindRelatedProducts": 25, "RemoveExistingProducts": 1501, "MigrateFeatureStates": 1200,
		"LaunchConditions": 100, "PublishProduct": 6400,
	}

	for _, row := range ies {
		if n, ok := want[fmt.Sprint(row[0])]; ok {
			if row[2] != n {
				t.Errorf("%s at %v, want %d", row[0], row[2], n)
			}

			delete(want, fmt.Sprint(row[0]))
		}
	}

	for name := range want {
		t.Errorf("%s is not scheduled", name)
	}

	for _, tbl := range []string{
		"InstallUISequence", "AdminExecuteSequence", "AdminUISequence", "AdvtExecuteSequence",
		"_Validation", "Upgrade", "LaunchCondition", "Feature", "FeatureComponents",
	} {
		if len(rows(t, database, tbl)) == 0 {
			t.Errorf("%s is empty", tbl)
		}
	}

	// Summary information.
	if summary.Get(database.Summary, summary.PIDTemplate) != "x64;1033" {
		t.Errorf("template %v", summary.Get(database.Summary, summary.PIDTemplate))
	}

	if summary.Get(database.Summary, summary.PIDPageCount) != 500 {
		t.Errorf("page count %v", summary.Get(database.Summary, summary.PIDPageCount))
	}

	if summary.Get(database.Summary, summary.PIDCodepage) != 1252 {
		t.Errorf("summary codepage %v", summary.Get(database.Summary, summary.PIDCodepage))
	}

	if summary.Get(database.Summary, summary.PIDRevision) != res.PackageCode || res.PackageCode == "" || res.PackageCode[0] != '{' {
		t.Errorf("package code %q / %v", res.PackageCode, summary.Get(database.Summary, summary.PIDRevision))
	}

	if ts, ok := summary.Get(database.Summary, summary.PIDCreated).(time.Time); !ok || !ts.Equal(buildTime) {
		t.Errorf("created %v", ts)
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	first := mustBuild(t, example(t, "minimal", nil, map[string][]byte{"hello.exe": exe}))
	b := mustBuild(t, example(t, "minimal", nil, map[string][]byte{"hello.exe": exe}))

	if !bytes.Equal(first.Package, b.Package) {
		t.Error("two builds of the same inputs differ")
	}

	// Changed content gives a new product code and package code.
	exe2 := testpe.Build(testpe.Options{FileVersion: "1.0.0.1", Languages: []int{1033}})
	changed := mustBuild(t, example(t, "minimal", nil, map[string][]byte{"hello.exe": exe2}))

	if changed.ProductCode == first.ProductCode {
		t.Error("product code did not change with the input")
	}

	if changed.PackageCode == first.PackageCode {
		t.Error("package code did not change with the input")
	}

	// Turning the upgrade off changes the tables, so the product code too.
	noUpgrade := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})
	noUpgrade.Upgrade.Disabled = true

	if mustBuild(t, noUpgrade).ProductCode == first.ProductCode {
		t.Error("product code did not change with upgrade: false")
	}
}

// TestDeterministicAcrossProcesses builds the windows_exporter example in 20
// fresh processes, each with its own map iteration order and allocation
// pattern, and requires one package hash (BUILD-02).
func TestDeterministicAcrossProcesses(t *testing.T) {
	t.Parallel()

	if os.Getenv("MSI_BUILDER_BUILD02_CHILD") == "1" {
		exe := testpe.Build(testpe.Options{FileVersion: "0.32.0.5", Languages: []int{1033}})

		icon := append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xAB}, 64)...)

		res := mustBuild(t, example(t, "windows_exporter", map[string]string{"VERSION": "0.32.0.5"}, map[string][]byte{
			"windows_exporter.exe": exe,
			"icon.ico":             icon,
		}))

		fmt.Printf("BUILD-02 %x\n", sha256.Sum256(res.Package)) //nolint:forbidigo // the parent process reads it

		return
	}

	hashes := map[string]int{}

	for range 20 {
		//nolint:gosec // the test binary runs itself
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDeterministicAcrossProcesses$", "-test.count=1")

		cmd.Env = append(os.Environ(), "MSI_BUILDER_BUILD02_CHILD=1")

		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("child build: %v\n%s", err, out)
		}

		_, hash, ok := strings.Cut(string(out), "BUILD-02 ")
		if !ok {
			t.Fatalf("child printed no hash:\n%s", out)
		}

		hash, _, _ = strings.Cut(hash, "\n")
		hashes[hash]++
	}

	if len(hashes) != 1 {
		t.Errorf("20 processes built %d different packages: %v", len(hashes), hashes)
	}

	t.Logf("BUILD-02: %v", hashes)
}

// TestProductCodeFollowsStreams checks that productCode: auto changes with
// the content of icons, binaries and binary cells of custom tables, not only
// with the payload files (a changed custom action DLL under the old
// ProductCode is refused with error 1638).
func TestProductCodeFollowsStreams(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "0.32.0.5", Languages: []int{1033}})

	icon := append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xAB}, 64)...)

	build := func(inputs map[string][]byte) *builder.Result {
		files := map[string][]byte{"windows_exporter.exe": exe, "icon.ico": icon, "ca.dll": []byte("dll v1"), "blob.bin": []byte("blob v1")}
		maps.Copy(files, inputs)

		cfg := example(t, "windows_exporter", map[string]string{"VERSION": "0.32.0.5"}, files)
		cfg.Binaries = map[string]string{"ca.dll": "ca.dll"}
		cfg.Tables = append(cfg.Tables, &config.Table{
			Name:    "Blobs",
			Columns: []config.Column{{Name: "Name", Type: "s72", Key: true}, {Name: "Data", Type: "V0"}},
			Rows:    [][]any{{"blob", "blob.bin"}},
		})

		return mustBuild(t, cfg)
	}

	base := build(nil)

	if again := build(nil); again.ProductCode != base.ProductCode {
		t.Error("the product code is not stable")
	}

	for name, data := range map[string][]byte{
		"icon.ico": append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xCD}, 64)...),
		"ca.dll":   []byte("dll v2"),
		"blob.bin": []byte("blob v2"),
	} {
		if changed := build(map[string][]byte{name: data}); changed.ProductCode == base.ProductCode {
			t.Errorf("the product code did not change with %s", name)
		}
	}
}

func TestCFBVersion3(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	cfg := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime, CFBVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	if res.Package[0x1A] != 3 || len(res.Package)%512 != 0 {
		t.Errorf("not a v3 file: version %d, size %d", res.Package[0x1A], len(res.Package))
	}
}

func TestMissingInput(t *testing.T) {
	t.Parallel()

	cfg := example(t, "minimal", nil, nil)

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err == nil {
		t.Fatal("expected an error for the missing hello.exe")
	}

	if !strings.Contains(fmt.Sprint(res.Diagnostics), "hello.exe") {
		t.Errorf("diagnostics do not name the file: %s", res.Diagnostics)
	}

	if res != nil && len(res.Package) != 0 {
		t.Errorf("a failed build returned a %d-byte package", len(res.Package))
	}
}

func TestWindowsExporter(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "0.32.0.5", Languages: []int{1033}})

	icon := append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xAB}, 64)...)

	cfg := example(t, "windows_exporter", map[string]string{"VERSION": "0.32.0.5"}, map[string][]byte{
		"windows_exporter.exe": exe,
		"icon.ico":             icon,
	})

	res := mustBuild(t, cfg)

	database, err := msidb.Unmarshal(res.Package)
	if err != nil {
		t.Fatal(err)
	}

	if len(database.Problems) > 0 {
		t.Errorf("problems: %v", database.Problems)
	}

	// Services.
	if len(rows(t, database, "ServiceInstall")) != 1 || len(rows(t, database, "ServiceControl")) != 2 {
		t.Error("service rows")
	}

	if len(rows(t, database, "MsiServiceConfig")) != 2 {
		t.Errorf("MsiServiceConfig rows: %d", len(rows(t, database, "MsiServiceConfig")))
	}

	hidden := cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "MsiHiddenProperties" })
	if !strings.Contains(fmt.Sprint(hidden), "SERVICE_PASSWORD") {
		t.Errorf("MsiHiddenProperties %v", hidden)
	}

	// Event log registry rows.
	v := cell(t, database, "Registry", "Value", func(r []any) bool { return r[3] == "TypesSupported" })
	if v != "#7" {
		t.Errorf("TypesSupported %v", v)
	}

	// Icon table and ARP icon.
	if len(rows(t, database, "Icon")) != 1 {
		t.Error("Icon rows")
	}

	// Firewall and recovery actions exist, are deferred and sit inside the
	// InstallInitialize … InstallFinalize window.
	ies := rows(t, database, "InstallExecuteSequence")
	seqOf := map[string]int{}

	for _, row := range ies {
		n, ok := row[2].(int)
		if !ok {
			t.Fatalf("sequence %v is not an int", row[2])
		}

		seqOf[fmt.Sprint(row[0])] = n
	}

	deferredActions := 0

	for _, row := range rows(t, database, "CustomAction") {
		name := fmt.Sprint(row[0])
		if !strings.HasPrefix(name, "Fw") && !strings.HasPrefix(name, "ServiceRecovery") {
			continue
		}

		deferredActions++

		if typ, ok := row[1].(int); !ok || typ&1024 == 0 {
			t.Errorf("%s is not deferred: type %v", name, row[1])
		}

		n, ok := seqOf[name]
		if !ok || n <= seqOf["InstallInitialize"] || n >= seqOf["InstallFinalize"] {
			t.Errorf("%s at %d is outside the script window", name, n)
		}
	}

	// Every EXE action's working directory has a Directory row (error 2727
	// otherwise), including standard folders added after the tree walk.
	dirs := map[any]bool{}
	for _, row := range rows(t, database, "Directory") {
		dirs[row[0]] = true
	}

	for _, row := range rows(t, database, "CustomAction") {
		if typ, ok := row[1].(int); ok && typ&0x3F == 34 && !dirs[row[2]] {
			t.Errorf("%s runs in %v, which has no Directory row", row[0], row[2])
		}
	}

	if deferredActions != 6 {
		t.Errorf("%d firewall/recovery actions, want 6", deferredActions)
	}

	// REP after InstallInitialize (window 2) unless the example says otherwise.
	if rep := seqOf["RemoveExistingProducts"]; rep == 0 {
		t.Error("RemoveExistingProducts missing")
	}

	// Custom actions from the config are scheduled.
	for _, name := range []string{"SetMaintenance", "SetReinstallAll", "SetReinstallMode"} {
		if _, ok := seqOf[name]; !ok {
			t.Errorf("%s not scheduled", name)
		}
	}

	// Conditional service arguments are set right before InstallServices.
	args := fmt.Sprint(cell(t, database, "ServiceInstall", "Arguments", func([]any) bool { return true }))
	if args != "[Arg_windows_exporter_1] [Arg_windows_exporter_2] [Arg_windows_exporter_3] [Arg_windows_exporter_4] "+
		"[Arg_windows_exporter_5] [Arg_windows_exporter_6] [EXTRA_FLAGS]" {
		t.Errorf("ServiceInstall.Arguments %q", args)
	}

	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("SetArg_windows_exporter_%d", i)
		if n, ok := seqOf[name]; !ok || n <= seqOf["InstallFiles"] || n >= seqOf["InstallServices"] {
			t.Errorf("%s at %d, want between InstallFiles and InstallServices", name, n)
		}
	}

	if summary.Get(database.Summary, summary.PIDTemplate) != "x64;1033" {
		t.Errorf("template %v", summary.Get(database.Summary, summary.PIDTemplate))
	}

	if strings.Contains(fmt.Sprint(res.Diagnostics), "wizard") {
		t.Errorf("unexpected wizard diagnostic: %s", res.Diagnostics)
	}
}

func TestInlineComponentPaths(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: A, manufacturer: B, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
features:
  - id: Main
    components:
      - files: [{ source: missing.exe }]
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = t.TempDir()

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err == nil {
		t.Fatal("built without the input file")
	}

	if msg := fmt.Sprint(err, res.Diagnostics); !strings.Contains(msg, "features[0].components[0].files[0]") {
		t.Errorf("error does not name the feature path: %s", msg)
	}
}

func TestTargetDirPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: A, manufacturer: B, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
installDir: { parent: TARGETDIR, name: Acme }
components:
  - files: [{ source: a.txt }]
  - { directory: TARGETDIR/sub, files: [{ source: b.txt }] }
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = dir

	res := mustBuild(t, cfg)

	database, err := msidb.Unmarshal(res.Package)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"INSTALLDIR", "TARGETDIR_sub"} {
		if cell(t, database, "Directory", "Directory_Parent", func(r []any) bool { return r[0] == id }) != "TARGETDIR" {
			t.Errorf("%s is not below TARGETDIR", id)
		}
	}
}

func TestWithoutUpgrade(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
upgrade: false
components:
  - files: [{ source: hello.exe }]
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = dir

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	if tbl := database.Table("Upgrade"); tbl != nil && len(tbl.Rows) > 0 {
		t.Errorf("Upgrade rows: %v", tbl.Rows)
	}

	for _, row := range rows(t, database, "InstallExecuteSequence") {
		if row[0] == "RemoveExistingProducts" || row[0] == "FindRelatedProducts" {
			t.Errorf("%s is scheduled without an upgrade", row[0])
		}
	}
}

// TestIgnoreRemoveFailure checks that upgrade.ignoreRemoveFailure sets
// bit 4 on every Upgrade row that removes products and leaves the
// detect-only rows alone.
func TestIgnoreRemoveFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
upgrade:
  ignoreRemoveFailure: true
  related:
    - { upgradeCode: 11111111-2222-3333-4444-555555555555, maxVersion: 1.0.0, property: OLDNAME }
    - { upgradeCode: 66666666-2222-3333-4444-555555555555, maxVersion: 1.0.0, onlyDetect: true, property: OTHERFOUND }
components:
  - files: [{ source: hello.exe }]
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = dir

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	// Attributes are column 4, ActionProperty column 6.
	want := map[string]int{
		"OLDERVERSIONBEINGUPGRADED": 1 | 4 | 512,
		"NEWERVERSIONDETECTED":      2,
		"OLDNAME":                   1 | 4 | 256,
		"OTHERFOUND":                2 | 256,
	}

	for _, row := range rows(t, database, "Upgrade") {
		property := fmt.Sprint(row[6])

		attrs, ok := want[property]
		if !ok {
			t.Errorf("unexpected Upgrade row %v", row)

			continue
		}

		delete(want, property)

		if got := fmt.Sprint(row[4]); got != strconv.Itoa(attrs) {
			t.Errorf("Upgrade row %s: attributes %s, want %d", property, got, attrs)
		}
	}

	if len(want) > 0 {
		t.Errorf("missing Upgrade rows: %v", want)
	}
}

// TestUpgradeMinVersion checks that upgrade.minVersion is the VersionMin
// of the row that removes older versions, inclusive unless includeMin is
// false, and leaves the detection of newer versions alone (UPG-13).
func TestUpgradeMinVersion(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, tc := range []struct {
		upgrade string
		min     any
		attrs   int
	}{
		{"{}", nil, 1 | 512},
		{"{minVersion: 0.18.0}", "0.18.0", 1 | 256 | 512},
		{"{minVersion: 0.18.0, includeMin: false, allowSameVersion: false}", "0.18.0", 1},
	} {
		t.Run(tc.upgrade, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.2.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
upgrade: `+tc.upgrade+`
components:
  - files: [{ source: hello.exe }]
`), config.Options{})
			if err != nil {
				t.Fatal(err)
			}

			cfg.BaseDir = dir

			database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
			if err != nil {
				t.Fatal(err)
			}

			seen := map[any]int{}

			for _, row := range rows(t, database, "Upgrade") {
				seen[row[6]]++

				switch row[6] {
				case "OLDERVERSIONBEINGUPGRADED":
					if row[1] != tc.min || row[2] != "1.2.0" || fmt.Sprint(row[4]) != strconv.Itoa(tc.attrs) {
						t.Errorf("removing row %v, want VersionMin %v, VersionMax 1.2.0, attributes %d", row, tc.min, tc.attrs)
					}
				case "NEWERVERSIONDETECTED":
					if row[1] != "1.2.0" || row[2] != nil || fmt.Sprint(row[4]) != "2" {
						t.Errorf("detection row %v", row)
					}
				default:
					t.Errorf("unexpected Upgrade row %v", row)
				}
			}

			if seen["OLDERVERSIONBEINGUPGRADED"] != 1 || seen["NEWERVERSIONDETECTED"] != 1 {
				t.Errorf("Upgrade rows by property: %v, want one removing and one detection row", seen)
			}
		})
	}
}

// TestFeatureDisplay checks the Feature.Display parity the selection tree
// relies on: even values show a feature collapsed, odd values expanded and 0
// hides it, whatever the display state of the features before it.
func TestFeatureDisplay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src := `schema: msi-builder/v1
product:
  name: Hello
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: x64
upgrade: {}
directories:
  - id: ProgramFiles64Folder
    children:
      - id: INSTALLDIR
        name: Hello
features:
  - id: Collapsed
    title: Collapsed
    components: [Hello]
    features:
      - id: Expanded
        title: Expanded
        display: expanded
        components: [Hello]
      - id: Hidden
        title: Hidden
        display: hidden
        components: [Hello]
  - id: Expanded2
    title: Expanded2
    display: expanded
    components: [Hello]
  - id: Collapsed2
    title: Collapsed2
    components: [Hello]
components:
  - id: Hello
    directory: INSTALLDIR
    files:
      - source: hello.exe
`

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]int{
		"Collapsed":  2,
		"Expanded":   5,
		"Hidden":     0,
		"Expanded2":  9,
		"Collapsed2": 10,
	}

	seen := map[int]bool{}

	for _, row := range rows(t, database, "Feature") {
		featureID := fmt.Sprint(row[0])

		display, ok := row[4].(int)
		if !ok {
			t.Fatalf("%s: Display %v is not an int", featureID, row[4])
		}

		expected, ok := want[featureID]
		if !ok {
			t.Fatalf("unexpected feature %s", featureID)
		}

		delete(want, featureID)

		if display != expected {
			t.Errorf("%s: Display %d, want %d", featureID, display, expected)
		}

		if display != 0 && seen[display] {
			t.Errorf("%s: Display %d is used twice", featureID, display)
		}

		seen[display] = true
	}

	for featureID := range want {
		t.Errorf("feature %s is missing", featureID)
	}
}

func TestFeatureFirewall(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	rule := "[{ name: %s, program: '[#hello.exe]', protocol: tcp, port: '80' }]"

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
features:
  - id: Main
    components:
      - files: [{ source: hello.exe }]
      - createFolder: true
  - id: Web
    default: off
    firewall: `+fmt.Sprintf(rule, "web")+`
  - id: Api
    firewall: `+fmt.Sprintf(rule, "api")+`
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = dir

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	// The carriers share INSTALLDIR with a directory-keyed component, yet
	// each has its own component code.
	codes := map[any]string{}

	for _, row := range rows(t, database, "Component") {
		if other, dup := codes[row[1]]; dup {
			t.Errorf("%s and %s share the component code %v", row[0], other, row[1])
		}

		codes[row[1]] = fmt.Sprint(row[0])
	}

	if level := cell(t, database, "Feature", "Level", func(r []any) bool { return r[0] == "Web" }); level != 2 {
		t.Errorf("Web level %v, want 2", level)
	}

	for feature, component := range map[string]string{"Web": "Web_Firewall", "Api": "Api_Firewall"} {
		if cell(t, database, "FeatureComponents", "Component_", func(r []any) bool { return r[0] == feature }) != component {
			t.Errorf("%s does not carry %s", feature, component)
		}
	}

	var conditions strings.Builder
	for _, row := range rows(t, database, "InstallExecuteSequence") {
		fmt.Fprintln(&conditions, row[1])
	}

	for _, want := range []string{"$Web_Firewall=3", "$Api_Firewall=3"} {
		if !strings.Contains(conditions.String(), want) {
			t.Errorf("no action conditioned on %s", want)
		}
	}
}

// TestDualScopeFirewall checks that a dual package's firewall carrier
// installs only per machine and that its netsh actions test the context
// too (INST-08).
func TestDualScopeFirewall(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64, scope: perMachineOrUser }
features:
  - id: Main
    components:
      - files: [{ source: hello.exe }]
    firewall: [{ name: web, program: '[#hello.exe]', protocol: tcp, port: '80' }]
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = dir

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	if got := cell(t, database, "Component", "Condition", func(r []any) bool { return r[0] == "Main_Firewall" }); got != "ALLUSERS=1" {
		t.Errorf("carrier condition %v, want ALLUSERS=1", got)
	}

	actions := 0

	for _, row := range rows(t, database, "InstallExecuteSequence") {
		name := fmt.Sprint(row[0])
		if !strings.HasPrefix(name, "Fw") {
			continue
		}

		actions++

		if cond := fmt.Sprint(row[1]); !strings.HasSuffix(cond, " AND ALLUSERS=1") {
			t.Errorf("%s: condition %q does not test ALLUSERS=1", name, cond)
		}
	}

	if actions != 5 {
		t.Errorf("%d netsh actions, want 5", actions)
	}
}

// TestEnvironmentParts checks that several parts of one variable in a
// component get Environment rows of their own (ENVVAR-04), and that the
// first keeps the key it had before.
func TestEnvironmentParts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	build := func(environment string) map[string]string {
		t.Helper()

		cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64, scope: perUserOrMachine }
components:
  - files: [{ source: hello.exe }]
    environment: [`+environment+`]
`), config.Options{})
		if err != nil {
			t.Fatal(err)
		}

		cfg.BaseDir = dir

		database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
		if err != nil {
			t.Fatal(err)
		}

		values := map[string]string{}
		for _, row := range rows(t, database, "Environment") {
			values[fmt.Sprint(row[0])] = fmt.Sprint(row[1], " ", row[2])
		}

		return values
	}

	one := build("{ name: PATH, value: '[INSTALLDIR]', action: append }")
	both := build("{ name: PATH, value: '[INSTALLDIR]', action: append }, { name: PATH, value: '[INSTALLDIR]bin', action: append }")

	// scope auto: one row per context and part.
	if len(one) != 2 || len(both) != 4 {
		t.Fatalf("rows %v and %v, want 2 and 4", one, both)
	}

	for key, value := range one {
		if both[key] != value {
			t.Errorf("row %s changed from %q to %q", key, value, both[key])
		}
	}

	parts := map[string]int{}
	for _, value := range both {
		parts[value]++
	}

	for _, want := range []string{`=-*PATH [~];[INSTALLDIR]bin`, `=-PATH [~];[INSTALLDIR]bin`} {
		if parts[want] != 1 {
			t.Errorf("no row %q in %v", want, both)
		}
	}
}

func TestServiceRecoverySteps(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "0.32.0.5", Languages: []int{1033}})
	cfg := example(t, "windows_exporter", map[string]string{"VERSION": "0.32.0.5"}, map[string][]byte{
		"windows_exporter.exe": exe,
		"icon.ico":             append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xAB}, 64)...),
	})

	minute := config.Duration(time.Minute)
	reset := config.Duration(24 * time.Hour)

	var svc *config.Service

	for _, comp := range cfg.Components {
		for _, s := range comp.Services {
			svc = s
		}
	}

	// A zero restartDelay is a delay of its own, not the default.
	for delay, want := range map[time.Duration]string{
		5 * time.Minute: `failure "windows_exporter" reset= 86400 actions= restart/60000/restart/300000/none/0`,
		0:               `failure "windows_exporter" reset= 86400 actions= restart/60000/restart/0/none/0`,
	} {
		svc.Recovery = &config.ServiceRecovery{
			ResetAfter:   &reset,
			RestartDelay: new(config.Duration(delay)),
			Actions:      []config.RecoveryAction{{Action: "restart", Restart: &minute}, {Action: "restart"}},
		}

		database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
		if err != nil {
			t.Fatal(err)
		}

		target := cell(t, database, "CustomAction", "Target", func(r []any) bool { return strings.HasPrefix(fmt.Sprint(r[0]), "ServiceRecovery") })
		if !strings.HasSuffix(fmt.Sprint(target), want) {
			t.Errorf("restartDelay %v: recovery command %q, want suffix %q", delay, target, want)
		}
	}
}

// Wait is a column of the ServiceControl row, so a start that waits
// differently from the stop and delete events gets a row of its own.
func TestServiceControlWait(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "0.32.0.5", Languages: []int{1033}})
	inputs := map[string][]byte{
		"windows_exporter.exe": exe,
		"icon.ico":             append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xAB}, 64)...),
	}

	for _, tc := range []struct {
		name                       string
		start, stop, wait, onStart bool
		want                       string
	}{
		{name: "same wait", start: true, stop: true, wait: true, onStart: true, want: "sc_windows_exporter/163/1"},
		{name: "start without wait", start: true, stop: true, wait: true, onStart: false, want: "sc_windows_exporter/162/1 scs_windows_exporter/1/0"},
		{name: "only start waits", start: true, stop: true, wait: false, onStart: true, want: "sc_windows_exporter/162/0 scs_windows_exporter/1/1"},
		{name: "start only", start: true, stop: false, wait: true, onStart: false, want: "sc_windows_exporter/1/0"},
		{name: "no start", start: false, stop: true, wait: true, onStart: false, want: "sc_windows_exporter/162/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := example(t, "windows_exporter", map[string]string{"VERSION": "0.32.0.5"}, inputs)

			for _, comp := range cfg.Components {
				for _, svc := range comp.Services {
					svc.Control = config.ServiceControl{
						StartOnInstall: new(tc.start), StopOnInstall: new(tc.stop), StopOnUninstall: new(tc.stop),
						DeleteOnUninstall: new(tc.stop), Wait: new(tc.wait), WaitForStart: new(tc.onStart),
					}
				}
			}

			database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
			if err != nil {
				t.Fatal(err)
			}

			controls := rows(t, database, "ServiceControl")
			got := make([]string, 0, len(controls))

			for _, row := range controls {
				got = append(got, fmt.Sprintf("%v/%v/%v", row[0], row[2], row[4]))
			}

			slices.Sort(got)

			if strings.Join(got, " ") != tc.want {
				t.Errorf("ServiceControl rows %q, want %q", strings.Join(got, " "), tc.want)
			}
		})
	}
}

// deleteOnInstall adds event 8 to the service's row, a start condition moves
// the start to a generated component with that condition, and
// serviceControl entries become rows of their component (SVC-06 to SVC-08).
func TestServiceControlEvents(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "0.32.0.5", Languages: []int{1033}})
	inputs := map[string][]byte{
		"windows_exporter.exe": exe,
		"icon.ico":             append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xAB}, 64)...),
	}

	cfg := example(t, "windows_exporter", map[string]string{"VERSION": "0.32.0.5"}, inputs)

	var owner *config.Component

	for _, comp := range cfg.Components {
		for _, svc := range comp.Services {
			owner = comp
			svc.Control.DeleteOnInstall = new(true)
			svc.Control.StartCondition = `START_AFTER_INSTALL="1"`
			comp.ServiceControl = []*config.ControlledService{
				{Name: "OldExporter", StopOnInstall: true, DeleteOnInstall: true, StopOnUninstall: true, DeleteOnUninstall: true, Wait: new(true)},
				{Name: "Spooler", StopOnInstall: true, StartOnUninstall: true, Wait: new(false)},
			}
		}
	}

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, 4)
	carrier := ""

	for _, row := range rows(t, database, "ServiceControl") {
		comp := fmt.Sprint(row[5])
		if comp != owner.ID {
			carrier, comp = comp, "carrier"
		}

		got = append(got, fmt.Sprintf("%v/%v/%v/%v", row[1], row[2], row[4], comp))
	}

	slices.Sort(got)

	want := "OldExporter/170/1/" + owner.ID + " Spooler/18/0/" + owner.ID +
		" windows_exporter/1/0/carrier windows_exporter/170/1/" + owner.ID
	if strings.Join(got, " ") != want {
		t.Fatalf("ServiceControl rows %q, want %q", strings.Join(got, " "), want)
	}

	cond := cell(t, database, "Component", "Condition", func(r []any) bool { return r[0] == carrier })
	if cond != `(START_AFTER_INSTALL="1")` {
		t.Errorf("carrier condition %v", cond)
	}

	keyPath := cell(t, database, "Component", "KeyPath", func(r []any) bool { return r[0] == carrier })
	if root := cell(t, database, "Registry", "Root", func(r []any) bool { return r[0] == keyPath }); root != 2 {
		t.Errorf("carrier key path %v in root %v, want HKLM", keyPath, root)
	}

	features := map[string]bool{}

	for _, row := range rows(t, database, "FeatureComponents") {
		features[fmt.Sprint(row[0], "/", row[1])] = true
	}

	for _, row := range rows(t, database, "FeatureComponents") {
		if row[1] == owner.ID && !features[fmt.Sprint(row[0], "/", carrier)] {
			t.Errorf("carrier is not in feature %v", row[0])
		}
	}

	secure := fmt.Sprint(cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "SecureCustomProperties" }))
	if !slices.Contains(strings.Split(secure, ";"), "START_AFTER_INSTALL") {
		t.Errorf("SecureCustomProperties %q lacks START_AFTER_INSTALL", secure)
	}
}

// Text the database code page cannot hold must fail with a diagnostic, not
// with a marshal error (FuzzBuild finding).
func TestCodepageDiagnostic(t *testing.T) {
	t.Parallel()

	input := fuzzInput{vars: defaultVars(), data: []byte("x")}
	input.vars["DIRNAME"] = "Папка"

	res, err := buildFuzzInput(t, input)
	if err == nil {
		t.Fatal("expected an error for Cyrillic text in code page 1252")
	}

	if res == nil || !strings.Contains(fmt.Sprint(res.Diagnostics), "Directory.DefaultDir") {
		t.Fatalf("no diagnostic names the column: %v", err)
	}
}

// The summary information is ANSI even for a UTF-8 database (SUM-04), so
// text it cannot hold is replaced and reported as a warning.
func TestSummaryCodepage(t *testing.T) {
	t.Parallel()

	input := fuzzInput{
		vars: map[string]string{
			"NAME": "Привет", "MANUFACTURER": "Пример", "VERSION": "1.0.0", "DIRNAME": "Папка",
			"FILENAME": "файл.txt", "PROPNAME": "P", "REGKEY": "K", "TEXT": "Ж",
			"CONDITION": "1", "CODEPAGE": "65001",
		},
		data: []byte("x"),
	}

	res, err := buildFuzzInput(t, input)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	checkBuilt(t, res, input)

	if got := summary.Get(res.Database.Summary, summary.PIDSubject); got != "?" {
		t.Errorf("summary subject %q, want %q", got, "?")
	}

	// One warning per replaced property, at the field the text comes from.
	// The author defaults to the manufacturer.
	for _, path := range []string{"product.description", "package.summary.title", "product.manufacturer"} {
		found := false

		for _, d := range res.Diagnostics {
			if d.Path == path && strings.Contains(d.Message, "summary information") {
				found = true
			}
		}

		if !found {
			t.Errorf("no summary warning at %s: %v", path, res.Diagnostics)
		}
	}
}

// A custom binary cell is stored in the stream Table.Key, which must be a
// valid stream name (review finding on the FuzzBuild fixes).
func TestCustomBinaryStreamName(t *testing.T) {
	t.Parallel()

	for key, valid := range map[string]bool{
		"Ok":                    true,
		"Grüße":                 false,
		strings.Repeat("a", 60): false,
	} {
		inputs := map[string][]byte{"hello.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0"}), "blob.bin": []byte("blob")}
		cfg := example(t, "minimal", nil, inputs)
		cfg.Tables = append(cfg.Tables, &config.Table{
			Name:    "MyTbl",
			Columns: []config.Column{{Name: "Key", Type: "s72", Key: true}, {Name: "Data", Type: "V0"}},
			Rows:    [][]any{{key, "blob.bin"}},
		})

		res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
		if valid {
			if err != nil {
				t.Errorf("key %q: %v", key, err)
			}

			continue
		}

		if err == nil || res.Diagnostics.Err() == nil || !strings.Contains(fmt.Sprint(res.Diagnostics), "tables[0].rows[0]") {
			t.Errorf("key %q: want a tables[0].rows[0] diagnostic, got %v (%v)", key, err, res.Diagnostics)
		}
	}
}

// Two streams with one name fail the build with a config path instead of a
// marshal error (review finding on the FuzzBuild fixes).
func TestStreamCollision(t *testing.T) {
	t.Parallel()

	oneKey := []config.Column{{Name: "Key", Type: "s72", Key: true}, {Name: "Data", Type: "V0"}}
	twoKeys := []config.Column{{Name: "A", Type: "s72", Key: true}, {Name: "B", Type: "s72", Key: true}, {Name: "Data", Type: "V0"}}
	table := func(name string, columns []config.Column, rows ...[]any) *config.Table {
		return &config.Table{Name: name, Columns: columns, Rows: rows}
	}

	for name, tc := range map[string]struct {
		tables   []*config.Table
		cabinet  string
		binaries map[string]string
		icons    map[string]string
		wizard   bool
		path     string
		other    string
	}{
		"cabinet product.cab": {
			tables: []*config.Table{table("product", oneKey, []any{"cab", "blob.bin"})},
			path:   "package.cabinet", other: "tables[0].rows[0]",
		},
		"explicit cabinet": {
			tables: []*config.Table{table("Foo", oneKey, []any{"Bar", "blob.bin"})}, cabinet: "Foo.Bar",
			path: "package.cabinet", other: "tables[0].rows[0]",
		},
		"keys that join to one name": {
			tables: []*config.Table{table("T", twoKeys, []any{"x.y", "z", "blob.bin"}, []any{"x", "y.z", "blob.bin"})},
			path:   "tables[0].rows[1]", other: "tables[0].rows[0]",
		},
		"tables in config order": {
			tables: []*config.Table{table("A.B", oneKey, []any{"c", "blob.bin"}), table("A", twoKeys, []any{"B", "c", "blob.bin"})},
			path:   "tables[1].rows[0]", other: "tables[0].rows[0]",
		},
		"binaries entry": {
			tables:   []*config.Table{table("Binary.x", oneKey, []any{"y", "blob.bin"})},
			binaries: map[string]string{"x.y": "blob.bin"},
			path:     "tables[0].rows[0]", other: "binaries.x.y",
		},
		"icons entry": {
			tables: []*config.Table{table("Icon.x", oneKey, []any{"y", "blob.bin"})},
			icons:  map[string]string{"x.y": "blob.bin"},
			path:   "tables[0].rows[0]", other: "icons.x.y",
		},
		"cabinet and binaries entry": {
			binaries: map[string]string{"x": "blob.bin"}, cabinet: "Binary.x",
			path: "package.cabinet", other: "binaries.x",
		},
		"wizard bitmap": {
			binaries: map[string]string{"BannerBmp": "blob.bin"}, wizard: true,
			path: "ui", other: "binaries.BannerBmp",
		},
	} {
		inputs := map[string][]byte{"hello.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0"}), "blob.bin": []byte("blob")}
		cfg := example(t, "minimal", nil, inputs)
		cfg.Package.Cabinet = "product.cab"
		cfg.Binaries = tc.binaries
		cfg.Icons = tc.icons
		cfg.Tables = append(cfg.Tables, tc.tables...)

		if tc.cabinet != "" {
			cfg.Package.Cabinet = tc.cabinet
		}

		if tc.wizard {
			cfg.UI.Enabled = true
		}

		res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
		if err == nil || res.Diagnostics.Err() == nil {
			t.Errorf("%s: want a diagnostic, got %v (%v)", name, err, res.Diagnostics)

			continue
		}

		found := false

		for _, d := range res.Diagnostics {
			if d.Path == tc.path && strings.Contains(d.Message, "also used by "+tc.other) {
				found = true
			}
		}

		if !found {
			t.Errorf("%s: no collision diagnostic at %s naming %s: %v", name, tc.path, tc.other, res.Diagnostics)
		}
	}
}

// The subject and comments both default to the product name; a name the
// summary code page cannot hold is reported once at product.name, also
// with an automatic package code, which builds the summary twice.
func TestSummaryWarningOnce(t *testing.T) {
	t.Parallel()

	inputs := map[string][]byte{"hello.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0"})}
	cfg := example(t, "minimal", nil, inputs)
	cfg.Product.Name = "Привет"
	cfg.Product.Description = ""
	cfg.Product.Comments = ""
	cfg.Package.Codepage = 65001
	cfg.Product.PackageCode = config.Auto

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	count := 0

	for _, d := range res.Diagnostics {
		if strings.Contains(d.Message, "summary information") {
			if d.Path != "product.name" {
				t.Errorf("summary warning at %s, want product.name: %s", d.Path, d.Message)
			}

			count++
		}
	}

	if count != 1 {
		t.Errorf("%d summary warnings, want 1: %v", count, res.Diagnostics)
	}
}

// Text the user wrote is reported at its own field.
func TestSummaryExplicitFields(t *testing.T) {
	t.Parallel()

	inputs := map[string][]byte{"hello.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0"})}
	cfg := example(t, "minimal", nil, inputs)
	cfg.Product.Comments = "Ж comments"
	cfg.Package.Summary.Author = "Ж author"
	cfg.Package.Summary.Keywords = "Ж keywords"

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	for _, path := range []string{"product.comments", "package.summary.author", "package.summary.keywords"} {
		found := false

		for _, d := range res.Diagnostics {
			if d.Path == path && strings.Contains(d.Message, "summary information") {
				found = true
			}
		}

		if !found {
			t.Errorf("no summary warning at %s: %v", path, res.Diagnostics)
		}
	}
}

func TestPurge(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src := `schema: msi-builder/v1
product:
  name: Hello
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: x64
components:
  - files: [{ source: hello.exe }]
  - id: Data
    directory: CommonAppDataFolder/Example/Hello
    createFolder: true
    purge:
      condition: DELETE_DATA="1"
`

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	var comp *config.Component

	for _, c := range cfg.Components {
		if c.ID == "Data" {
			comp = c
		}
	}

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	name := "Purge_" + comp.ID
	target := cell(t, database, "CustomAction", "Target", func(r []any) bool { return r[0] == name })

	if want := `"[System64Folder]cmd.exe" /c rmdir /s /q "[` + comp.Directory + `]."`; target != want {
		t.Errorf("purge command %q, want %q", target, want)
	}

	typ := cell(t, database, "CustomAction", "Type", func(r []any) bool { return r[0] == name })
	if typ != 34|64|1024|2048 {
		t.Errorf("purge type %v, want 3170", typ)
	}

	cond := cell(t, database, "InstallExecuteSequence", "Condition", func(r []any) bool { return r[0] == name })
	if want := `REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE AND ` + comp.Directory + ` AND (DELETE_DATA="1")`; cond != want {
		t.Errorf("purge condition %q, want %q", cond, want)
	}

	secure := fmt.Sprint(cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "SecureCustomProperties" }))
	if !strings.Contains(";"+secure+";", ";DELETE_DATA;") {
		t.Errorf("SecureCustomProperties %q lacks DELETE_DATA (FILE-03 opt-in)", secure)
	}
}

// TestRememberInstallDir checks the SEARCH-03 and SEARCH-07 recipe for
// installDir.remember: a registry value with the folder, a 64-bit directory
// RegLocator search into a separate property, an empty Signature table, and
// a guarded type-51 action after AppSearch in both sequences.
func TestRememberInstallDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src := `schema: msi-builder/v1
product:
  name: Hello
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: x64
installDir:
  remember: true
components:
  - files: [{ source: hello.exe }]
`

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	isRemember := func(r []any) bool { return r[0] == config.RememberComponentID }

	if got := cell(t, database, "Registry", "Value", func(r []any) bool { return r[5] == config.RememberComponentID }); got != "[INSTALLDIR]" {
		t.Errorf("Registry value %v, want [INSTALLDIR]", got)
	}

	if got := cell(t, database, "Registry", "Key", func(r []any) bool { return r[5] == config.RememberComponentID }); got != `Software\Example\Hello` {
		t.Errorf("Registry key %v", got)
	}

	if got := cell(t, database, "RegLocator", "Type", isRemember); got != 16 {
		t.Errorf("RegLocator type %v, want 16 (directory, 64-bit)", got)
	}

	if got := cell(t, database, "RegLocator", "Root", isRemember); got != 2 {
		t.Errorf("RegLocator root %v, want 2 (HKLM)", got)
	}

	if got := cell(t, database, "AppSearch", "Property", func(r []any) bool { return r[1] == config.RememberComponentID }); got != "INSTALLDIR_REMEMBERED" {
		t.Errorf("AppSearch property %v", got)
	}

	if database.Table("Signature") == nil {
		t.Error("Signature table missing (SEARCH-03: AppSearch fails with 2228 without it)")
	}

	if got := cell(t, database, "CustomAction", "Type", func(r []any) bool { return r[0] == "RestoreInstallDir" }); got != 51 {
		t.Errorf("RestoreInstallDir type %v, want 51", got)
	}

	for _, seq := range []string{"InstallExecuteSequence", "InstallUISequence"} {
		match := func(name string) func([]any) bool { return func(r []any) bool { return r[0] == name } }

		cond := cell(t, database, seq, "Condition", match("RestoreInstallDir"))
		if cond != "INSTALLDIR_REMEMBERED AND NOT INSTALLDIR" {
			t.Errorf("%s: condition %v", seq, cond)
		}

		search := fmt.Sprint(cell(t, database, seq, "Sequence", match("AppSearch")))
		restore := fmt.Sprint(cell(t, database, seq, "Sequence", match("RestoreInstallDir")))
		costFinalize := fmt.Sprint(cell(t, database, seq, "Sequence", match("CostFinalize")))

		if seqNumber(t, search) >= seqNumber(t, restore) || seqNumber(t, restore) >= seqNumber(t, costFinalize) {
			t.Errorf("%s: AppSearch %s, RestoreInstallDir %s, CostFinalize %s", seq, search, restore, costFinalize)
		}
	}

	secure := fmt.Sprint(cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "SecureCustomProperties" }))
	if !strings.Contains(";"+secure+";", ";INSTALLDIR_REMEMBERED;") {
		t.Errorf("SecureCustomProperties %q lacks INSTALLDIR_REMEMBERED", secure)
	}
}

func seqNumber(t *testing.T, value string) int {
	t.Helper()

	n, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("sequence %q: %v", value, err)
	}

	return n
}

// TestRememberInstallDir32BitFolder checks that installDir.remember in a
// 32-bit folder of an x64 package writes and searches the 32-bit registry
// view (ICE80).
func TestRememberInstallDir32BitFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src := `schema: msi-builder/v1
product:
  name: Hello
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: x64
installDir:
  parent: ProgramFilesFolder
  remember: true
components:
  - bitness: "32"
    files: [{ source: hello.exe }]
`

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	res := mustBuild(t, cfg)
	for _, d := range res.Diagnostics {
		if strings.Contains(d.Message, "ICE80") {
			t.Errorf("unexpected diagnostic: %v", d)
		}
	}

	database, err := msidb.Unmarshal(res.Package)
	if err != nil {
		t.Fatal(err)
	}

	isRemember := func(r []any) bool { return r[0] == config.RememberComponentID }

	if got := cell(t, database, "RegLocator", "Type", isRemember); got != 0 {
		t.Errorf("RegLocator type %v, want 0 (directory, 32-bit)", got)
	}

	if got, ok := cell(t, database, "Component", "Attributes", isRemember).(int); !ok || got&256 != 0 {
		t.Errorf("Component attributes %v, want a 32-bit component", got)
	}
}

// TestProductCodeFollowsWizardInputs checks that productCode: auto changes
// with the content of the license, the translated license and the theme
// pictures at an unchanged path: they change the installer as much as a
// payload does.
func TestProductCodeFollowsWizardInputs(t *testing.T) {
	t.Parallel()

	picture := func(img *image.RGBA, marked bool) []byte {
		if marked {
			img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
		}

		return artwork.EncodeBMP(img)
	}

	build := func(inputs map[string][]byte) string {
		files := map[string][]byte{
			"viewer.exe":  testpe.Build(testpe.Options{FileVersion: "2.0.0.0", Languages: []int{1033}}),
			"LICENSE.txt": []byte("License version one\n"),
			"LIZENZ.txt":  []byte("Lizenz Version eins\n"),
			"banner.bmp":  picture(artwork.Banner(artwork.Default()), false),
			"dialog.bmp":  picture(artwork.Dialog(artwork.Default()), false),
		}
		maps.Copy(files, inputs)

		cfg := example(t, "desktop", map[string]string{"VERSION": "2.0.0"}, files)
		cfg.UI.Theme.Banner = "banner.bmp"
		cfg.UI.Theme.Dialog = "dialog.bmp"
		cfg.UI.Languages = map[string]*config.Language{"de": {License: "LIZENZ.txt"}}

		return mustBuild(t, cfg).ProductCode
	}

	base := build(nil)

	if again := build(nil); again != base {
		t.Error("the product code is not stable")
	}

	for name, data := range map[string][]byte{
		"LICENSE.txt": []byte("License version two\n"),
		"LIZENZ.txt":  []byte("Lizenz Version zwei\n"),
		"banner.bmp":  picture(artwork.Banner(artwork.Default()), true),
		"dialog.bmp":  picture(artwork.Dialog(artwork.Default()), true),
	} {
		if build(map[string][]byte{name: data}) == base {
			t.Errorf("the product code did not change with %s", name)
		}
	}
}
