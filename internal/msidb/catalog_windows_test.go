//go:build windows

package msidb //nolint:testpackage // the tests patch the encoded catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var (
	msiDLL                      = syscall.NewLazyDLL("msi.dll")
	pMsiOpenDatabaseW           = msiDLL.NewProc("MsiOpenDatabaseW")
	pMsiDatabaseOpenViewW       = msiDLL.NewProc("MsiDatabaseOpenViewW")
	pMsiDatabaseGetPrimaryKeysW = msiDLL.NewProc("MsiDatabaseGetPrimaryKeysW")
	pMsiViewExecute             = msiDLL.NewProc("MsiViewExecute")
	pMsiViewFetch               = msiDLL.NewProc("MsiViewFetch")
	pMsiViewClose               = msiDLL.NewProc("MsiViewClose")
	pMsiRecordGetFieldCount     = msiDLL.NewProc("MsiRecordGetFieldCount")
	pMsiRecordGetStringW        = msiDLL.NewProc("MsiRecordGetStringW")
	pMsiCloseHandle             = msiDLL.NewProc("MsiCloseHandle")
)

const errorNoMoreItems = 259

func msiClose(handle uint32) { _, _, _ = pMsiCloseHandle.Call(uintptr(handle)) }

func msiRecord(rec uint32) []string {
	count, _, _ := pMsiRecordGetFieldCount.Call(uintptr(rec))
	out := make([]string, count)

	for i := range out {
		buf := make([]uint16, 256)
		size := uint32(len(buf))
		_, _, _ = pMsiRecordGetStringW.Call(uintptr(rec), uintptr(i+1), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
		out[i] = syscall.UTF16ToString(buf[:size])
	}

	return out
}

// msiQuery runs a SELECT through msi.dll and returns its rows, or the
// error code of the call that failed.
func msiQuery(db uint32, sql string) ([][]string, error) {
	query, err := syscall.UTF16PtrFromString(sql)
	if err != nil {
		return nil, err //nolint:wrapcheck // test helper
	}

	var view uint32
	if code, _, _ := pMsiDatabaseOpenViewW.Call(uintptr(db), uintptr(unsafe.Pointer(query)), uintptr(unsafe.Pointer(&view))); code != 0 {
		return nil, fmt.Errorf("MsiDatabaseOpenView: %d", code)
	}

	defer msiClose(view)

	if code, _, _ := pMsiViewExecute.Call(uintptr(view), 0); code != 0 {
		return nil, fmt.Errorf("MsiViewExecute: %d", code)
	}

	defer func() { _, _, _ = pMsiViewClose.Call(uintptr(view)) }()

	var rows [][]string

	for {
		var rec uint32

		code, _, _ := pMsiViewFetch.Call(uintptr(view), uintptr(unsafe.Pointer(&rec)))
		if code == errorNoMoreItems {
			return rows, nil
		}

		if code != 0 {
			return rows, fmt.Errorf("MsiViewFetch: %d", code)
		}

		rows = append(rows, msiRecord(rec))
		msiClose(rec)
	}
}

// TestNativeMalformedCatalog records what msi.dll does with the catalogs
// that the reader reports as problems (MSIDB-21).
func TestNativeMalformedCatalog(t *testing.T) { //nolint:paralleltest // msi.dll calls one at a time
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	dir := t.TempDir()

	var report []string

	for _, tc := range []struct {
		edits map[int]catalogEdit
		name  string
	}{
		{name: "control"},
		{name: "duplicate key name", edits: map[int]catalogEdit{2: {name: "K"}}},
		{name: "duplicate non-key name", edits: map[int]catalogEdit{3: {name: "V"}}},
		{name: "no primary key", edits: map[int]catalogEdit{1: {key: -1}}},
		{name: "key after a non-key column", edits: map[int]catalogEdit{3: {key: 1}}},
		{name: "only key last", edits: map[int]catalogEdit{1: {key: -1}, 3: {key: 1}}},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".msi")
		if err := os.WriteFile(path, patchedCatalog(t, catalogBase(), tc.edits), 0o600); err != nil {
			t.Fatal(err)
		}

		name, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}

		var db uint32
		if code, _, _ := pMsiOpenDatabaseW.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&db))); code != 0 {
			report = append(report, fmt.Sprintf("%s: MsiOpenDatabase %d", tc.name, code))

			continue
		}

		line := tc.name + ":"

		for _, sql := range []string{
			"SELECT * FROM `T`",
			"SELECT `K`, `V`, `N` FROM `T`",
			"SELECT `V` FROM `T` WHERE `K`='b'",
			"SELECT `N` FROM `T` WHERE `V`='y'",
		} {
			rows, err := msiQuery(db, sql)
			line += fmt.Sprintf(" [%s] %q %v;", sql, rows, err)
		}

		table, _ := syscall.UTF16PtrFromString("T")

		var rec uint32
		if code, _, _ := pMsiDatabaseGetPrimaryKeysW.Call(uintptr(db), uintptr(unsafe.Pointer(table)), uintptr(unsafe.Pointer(&rec))); code != 0 {
			line += fmt.Sprintf(" primary keys: %d", code)
		} else {
			line += fmt.Sprintf(" primary keys: %q", msiRecord(rec))
			msiClose(rec)
		}

		msiClose(db)

		report = append(report, line)
	}

	// Discovery run: print what msi.dll did.
	t.Errorf("MSIDB-21:\n%s", strings.Join(report, "\n"))
}
