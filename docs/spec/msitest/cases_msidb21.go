package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

func init() { cases["MSIDB-21"] = caseMSIDB21 }

var pMsiDatabaseGetPrimaryKeysW = msiDLL.NewProc("MsiDatabaseGetPrimaryKeysW")

// caseMSIDB21 patches the _Columns rows of a native table into catalogs that
// msidb.Table.Validate refuses (a column name twice, no primary key, a key
// column after a non-key column) and records what msi.dll does with each.
// The string pool refcounts are corrected, so only the catalog is malformed.
func caseMSIDB21() {
	c := newCase("MSIDB-21", "malformed-catalog")
	defer c.close()

	base, _ := filepath.Abs(c.path("control.msi"))
	os.Remove(base)
	db, err := openDB(base, dbCreate)
	if err != nil {
		c.logf("create: %v", err)
		return
	}
	for _, sql := range []string{
		"CREATE TABLE `T` (`K` CHAR(16) NOT NULL, `V` CHAR(16), `N` SHORT PRIMARY KEY `K`)",
		"INSERT INTO `T` (`K`, `V`, `N`) VALUES ('a', 'x', 1)",
		"INSERT INTO `T` (`K`, `V`, `N`) VALUES ('b', 'y', 2)",
	} {
		if _, err := query(db, sql); err != nil {
			c.logf("%s: %v", sql, err)
		}
	}
	c.logf("control commit: %v", commit(db))
	closeHandle(db)

	variants := []struct {
		name  string
		edits map[int]catalogEdit
	}{
		{"control", nil},
		{"dup-key-name", map[int]catalogEdit{2: {name: "K"}}},    // K, K, N with the first K the key
		{"dup-nonkey-name", map[int]catalogEdit{3: {name: "V"}}}, // K, V, V
		{"no-key", map[int]catalogEdit{1: {key: -1}}},            // K, V, N, none a key
		{"key-after-nonkey", map[int]catalogEdit{3: {key: 1}}},   // keys K and N, V between them
		{"only-key-last", map[int]catalogEdit{1: {key: -1}, 3: {key: 1}}},
	}
	for _, v := range variants {
		path, _ := filepath.Abs(c.path(v.name + ".msi"))
		raw, err := patchCatalog(mustRead(base), "T", v.edits)
		if err != nil {
			c.logf("RESULT %s: patch failed: %v", v.name, err)
			continue
		}
		os.WriteFile(path, raw, 0o644)
		probeCatalog(c, v.name, path)
	}
}

// catalogEdit changes one _Columns row: a new name (a string already in the
// pool) and the key bit.
type catalogEdit struct {
	name string // "" keeps the name
	key  int    // 1 sets the key bit, -1 clears it, 0 keeps it
}

// patchCatalog rewrites the _Columns rows of table, keeping every stream's
// length, and moves the refcounts of renamed columns to the new name.
func patchCatalog(raw []byte, table string, edits map[int]catalogEdit) ([]byte, error) {
	rdb, err := openRawMSI(raw)
	if err != nil {
		return nil, err
	}
	if rdb.LongRefs {
		return nil, fmt.Errorf("long references not handled")
	}
	le := binary.LittleEndian
	cols := append([]byte(nil), rdb.TableStr["_Columns"].Data...)
	pool := append([]byte(nil), rdb.TableStr["_StringPool"].Data...)
	n := len(cols) / 8
	tid := rdb.idOf(table)
	for r := 0; r < n; r++ {
		if uint32(le.Uint16(cols[r*2:])) != tid {
			continue
		}
		num := int(le.Uint16(cols[n*2+r*2:])) - 0x8000
		e, ok := edits[num]
		if !ok {
			continue
		}
		name, key := e.name, e.key
		nameOff := n*4 + r*2
		typeOff := n*6 + r*2
		if name != "" {
			oldID := uint32(le.Uint16(cols[nameOff:]))
			newID := rdb.idOf(name)
			if newID == 0 {
				return nil, fmt.Errorf("string %q is not in the pool", name)
			}
			le.PutUint16(cols[nameOff:], uint16(newID))
			for _, adj := range []struct {
				id    uint32
				delta int
			}{{oldID, -1}, {newID, 1}} {
				off := 4 + int(adj.id-1)*4 + 2 // no long entries in this pool
				le.PutUint16(pool[off:], uint16(int(le.Uint16(pool[off:]))+adj.delta))
			}
		}
		typ := le.Uint16(cols[typeOff:]) - 0x8000
		switch key {
		case 1:
			typ |= 0x2000
		case -1:
			typ &^= 0x2000
		}
		le.PutUint16(cols[typeOff:], typ+0x8000)
	}
	out := rdb.f.patchStream(rdb.TableStr["_Columns"], cols)
	f, err := readCFB(out)
	if err != nil {
		return nil, err
	}
	for _, e := range f.Entries {
		if e.Type != 2 || e.Parent != 0 {
			continue
		}
		if n, table := decodeStreamName(e.Name); table && n == "_StringPool" {
			return f.patchStream(e, pool), nil
		}
	}
	return nil, fmt.Errorf("no _StringPool")
}

// probeCatalog records how msi.dll opens, reads, exports and changes a table T.
func probeCatalog(c *caseLog, name, path string) {
	db, err := openDB(path, dbReadOnly)
	if err != nil {
		c.logf("RESULT %s: open read-only: %v", name, err)
		return
	}
	rows, err := query(db, "SELECT `Name`, `Number` FROM `_Columns` WHERE `Table`='T'")
	c.logf("%s: _Columns of T: %q err=%v", name, rows, err)
	rows, err = query(db, "SELECT * FROM `T`")
	c.logf("RESULT %s: SELECT * -> %q err=%v", name, rows, err)
	rows, err = query(db, "SELECT `K`, `V`, `N` FROM `T`")
	c.logf("RESULT %s: SELECT K, V, N -> %q err=%v", name, rows, err)
	rows, err = query(db, "SELECT `V` FROM `T` WHERE `K`='b'")
	c.logf("RESULT %s: SELECT V WHERE K='b' -> %q err=%v", name, rows, err)
	var rec uint32
	err = call("MsiDatabaseGetPrimaryKeysW", pMsiDatabaseGetPrimaryKeysW, uintptr(db), uintptr(unsafe.Pointer(wstr("T"))), uintptr(unsafe.Pointer(&rec)))
	keys := []string(nil)
	if err == nil {
		keys = recordStrings(rec)
		closeHandle(rec)
	}
	c.logf("RESULT %s: MsiDatabaseGetPrimaryKeys -> %q err=%v", name, keys, err)
	dir, _ := filepath.Abs(c.dir)
	err = exportIDT(db, "T", dir, name+".idt")
	idt, _ := os.ReadFile(filepath.Join(dir, name+".idt"))
	c.logf("RESULT %s: export err=%v idt=%q", name, err, idt)
	closeHandle(db)

	db, err = openDB(path, dbTransact)
	if err != nil {
		c.logf("RESULT %s: open transact: %v", name, err)
		return
	}
	_, ins := query(db, "INSERT INTO `T` (`K`, `V`, `N`) VALUES ('c', 'z', 3)")
	_, dup := query(db, "INSERT INTO `T` (`K`, `V`, `N`) VALUES ('a', 'w', 4)")
	com := commit(db)
	closeHandle(db)
	c.logf("RESULT %s: insert new key err=%v, insert existing K err=%v, commit err=%v", name, ins, dup, com)
	if db, err = openDB(path, dbReadOnly); err == nil {
		rows, err = query(db, "SELECT * FROM `T`")
		c.logf("RESULT %s: after commit SELECT * -> %q err=%v", name, rows, err)
		closeHandle(db)
	}
}
