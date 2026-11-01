//go:build windows

package msidb //nolint:testpackage // the tests patch the encoded catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var (
	msiDLL                      = syscall.NewLazyDLL("msi.dll")                //nolint:gochecknoglobals // one DLL table for the file
	pMsiOpenDatabaseW           = msiDLL.NewProc("MsiOpenDatabaseW")           //nolint:gochecknoglobals // one DLL table for the file
	pMsiDatabaseOpenViewW       = msiDLL.NewProc("MsiDatabaseOpenViewW")       //nolint:gochecknoglobals // one DLL table for the file
	pMsiDatabaseGetPrimaryKeysW = msiDLL.NewProc("MsiDatabaseGetPrimaryKeysW") //nolint:gochecknoglobals // one DLL table for the file
	pMsiViewExecute             = msiDLL.NewProc("MsiViewExecute")             //nolint:gochecknoglobals // one DLL table for the file
	pMsiViewFetch               = msiDLL.NewProc("MsiViewFetch")               //nolint:gochecknoglobals // one DLL table for the file
	pMsiViewClose               = msiDLL.NewProc("MsiViewClose")               //nolint:gochecknoglobals // one DLL table for the file
	pMsiRecordGetFieldCount     = msiDLL.NewProc("MsiRecordGetFieldCount")     //nolint:gochecknoglobals // one DLL table for the file
	pMsiRecordGetStringW        = msiDLL.NewProc("MsiRecordGetStringW")        //nolint:gochecknoglobals // one DLL table for the file
	pMsiCloseHandle             = msiDLL.NewProc("MsiCloseHandle")             //nolint:gochecknoglobals // one DLL table for the file
)

const (
	errorNoMoreItems = 259
	errorBadQuerySQL = 1615
)

var errMsi = errors.New("msi.dll")

func msiClose(handle uint32) { _, _, _ = pMsiCloseHandle.Call(uintptr(handle)) }

func msiRecord(rec uint32) []string {
	count, _, _ := pMsiRecordGetFieldCount.Call(uintptr(rec))
	out := make([]string, count)

	for i := range out {
		const bufSize = 256

		buf := make([]uint16, bufSize)
		size := uint32(bufSize)
		_, _, _ = pMsiRecordGetStringW.Call(uintptr(rec), uintptr(i+1), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
		// On ERROR_MORE_DATA size is the length needed; the buffer holds
		// a cut, NUL-terminated value, which the test values never need.
		out[i] = syscall.UTF16ToString(buf[:min(int(size), bufSize-1)])
	}

	return out
}

// msiQuery runs a SELECT through msi.dll and returns its rows, or an error
// that names the call that failed and its code.
func msiQuery(handle uint32, sql string) ([][]string, uintptr, error) {
	query, err := syscall.UTF16PtrFromString(sql)
	if err != nil {
		return nil, 0, err
	}

	var view uint32
	if code, _, _ := pMsiDatabaseOpenViewW.Call(uintptr(handle), uintptr(unsafe.Pointer(query)), uintptr(unsafe.Pointer(&view))); code != 0 {
		return nil, code, fmt.Errorf("%w: MsiDatabaseOpenView %d", errMsi, code)
	}

	defer msiClose(view)

	if code, _, _ := pMsiViewExecute.Call(uintptr(view), 0); code != 0 {
		return nil, code, fmt.Errorf("%w: MsiViewExecute %d", errMsi, code)
	}

	defer func() { _, _, _ = pMsiViewClose.Call(uintptr(view)) }()

	var rows [][]string

	for {
		var rec uint32

		code, _, _ := pMsiViewFetch.Call(uintptr(view), uintptr(unsafe.Pointer(&rec)))
		if code == errorNoMoreItems {
			return rows, 0, nil
		}

		if code != 0 {
			return rows, code, fmt.Errorf("%w: MsiViewFetch %d", errMsi, code)
		}

		rows = append(rows, msiRecord(rec))
		msiClose(rec)
	}
}

func msiPrimaryKeys(t *testing.T, handle uint32, table string) []string {
	t.Helper()

	name, err := syscall.UTF16PtrFromString(table)
	if err != nil {
		t.Fatal(err)
	}

	var rec uint32
	if code, _, _ := pMsiDatabaseGetPrimaryKeysW.Call(uintptr(handle), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&rec))); code != 0 {
		t.Fatalf("MsiDatabaseGetPrimaryKeys: %d", code)
	}

	defer msiClose(rec)

	return msiRecord(rec)
}

func msiOpen(t *testing.T, path string) uint32 {
	t.Helper()

	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	var handle uint32
	if code, _, _ := pMsiOpenDatabaseW.Call(uintptr(unsafe.Pointer(name)), 0 /* MSIDBOPEN_READONLY */, uintptr(unsafe.Pointer(&handle))); code != 0 {
		t.Fatalf("MsiOpenDatabase: %d", code)
	}

	t.Cleanup(func() { msiClose(handle) })

	return handle
}

// TestNativeMalformedCatalog opens the catalogs that the reader reports as
// problems with msi.dll (MSIDB-21). msi.dll reads all of them: SELECT *
// returns every row, a duplicated column name means its first column and
// leaves the other one unreachable by name, and only the key columns before
// the first non-key column are primary keys. The reader decodes the same
// rows and the same number of keys.
func TestNativeMalformedCatalog(t *testing.T) { //nolint:paralleltest // msi.dll calls one at a time
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	dir := t.TempDir()
	allRows := [][]string{{"a", "x", "1"}, {"b", "y", "2"}}

	for _, tc := range []struct { //nolint:paralleltest // msi.dll calls one at a time
		edits map[int]catalogEdit
		named map[string][][]string // a query by column names and its rows; nil rows: error 1615
		name  string
		want  string // the reader's one problem
		keys  []string
	}{
		{name: "control", keys: []string{"K"}, named: map[string][][]string{
			"SELECT `K`, `V`, `N` FROM `T`":     allRows,
			"SELECT `V` FROM `T` WHERE `K`='b'": {{"y"}},
		}},
		// K, K, N: V is gone.
		{name: "duplicate key name", want: "table T: column K defined twice", edits: map[int]catalogEdit{2: {name: "K"}}, keys: []string{"K"}, named: map[string][][]string{
			"SELECT `K`, `V`, `N` FROM `T`":     nil,
			"SELECT `N` FROM `T` WHERE `K`='b'": {{"2"}},
			"SELECT `K` FROM `T` WHERE `N`=2":   {{"b"}},
		}},
		// K, V, V: `V` is column 2, N is gone.
		{name: "duplicate non-key name", want: "table T: column V defined twice", edits: map[int]catalogEdit{3: {name: "V"}}, keys: []string{"K"}, named: map[string][][]string{
			"SELECT `K`, `V`, `N` FROM `T`":     nil,
			"SELECT `V` FROM `T` WHERE `K`='b'": {{"y"}},
		}},
		{name: "no primary key", want: "table T has no primary key", edits: map[int]catalogEdit{1: {key: -1}}, keys: nil, named: map[string][][]string{
			"SELECT `V` FROM `T` WHERE `K`='b'": {{"y"}},
		}},
		{name: "key after a non-key column", want: "table T: key column N must come before the other columns", edits: map[int]catalogEdit{3: {key: 1}}, keys: []string{"K"}, named: map[string][][]string{
			"SELECT `N` FROM `T` WHERE `V`='y'": {{"2"}},
		}},
		{name: "only key last", want: "table T: key column N must come before the other columns", edits: map[int]catalogEdit{1: {key: -1}, 3: {key: 1}}, keys: nil, named: map[string][][]string{
			"SELECT `N` FROM `T` WHERE `V`='y'": {{"2"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := patchedCatalog(t, catalogBase(), tc.edits)

			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".msi")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			handle := msiOpen(t, path)

			rows, _, err := msiQuery(handle, "SELECT * FROM `T`") //nolint:unqueryvet // every column, whatever their names
			if err != nil || !slices.EqualFunc(rows, allRows, slices.Equal) {
				t.Errorf("SELECT *: %q, %v; want %q", rows, err, allRows)
			}

			for sql, want := range tc.named {
				rows, code, err := msiQuery(handle, sql)

				switch {
				case want == nil && code != errorBadQuerySQL:
					t.Errorf("%s: %q, %v; want error %d", sql, rows, err, errorBadQuerySQL)
				case want != nil && (err != nil || !slices.EqualFunc(rows, want, slices.Equal)):
					t.Errorf("%s: %q, %v; want %q", sql, rows, err, want)
				}
			}

			keys := msiPrimaryKeys(t, handle, "T")
			if !slices.Equal(keys, tc.keys) && len(keys)+len(tc.keys) > 0 {
				t.Errorf("primary keys %q, want %q", keys, tc.keys)
			}

			database, err := Unmarshal(raw)
			if err != nil {
				t.Fatal(err)
			}

			tbl := database.Table("T")
			if tbl.Keys() != len(keys) {
				t.Errorf("reader keys %d, msi.dll %q", tbl.Keys(), keys)
			}

			got := make([][]string, len(tbl.Rows))
			for i, row := range tbl.Rows {
				for _, cell := range row {
					got[i] = append(got[i], fmt.Sprint(cell))
				}
			}

			if !slices.EqualFunc(got, allRows, slices.Equal) {
				t.Errorf("reader rows %q, msi.dll %q", got, allRows)
			}

			if want := []string{tc.want}; !slices.Equal(database.Problems, want) && (tc.want != "" || len(database.Problems) > 0) {
				t.Errorf("reader problems %q", database.Problems)
			}

			t.Logf("msi.dll keys %q; reader problems %q", keys, database.Problems)
		})
	}
}
