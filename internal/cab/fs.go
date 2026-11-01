package cab

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// FS is a read-only fs.FS over the files of a cabinet. Cabinet names use
// backslashes between directories; FS maps them to slash-separated paths and
// synthesises the directories. MSZIP and LZX are block compressions, not
// containers, so there is no fs.FS for them: FS already serves decompressed
// bytes.
type FS struct {
	files map[string]*File
	dirs  map[string][]string // directory -> sorted child names
	time  time.Time
}

const opReadDir = "readdir"

// Compile-time checks of the optional fs interfaces.
var (
	_ fs.FS         = (*FS)(nil)
	_ fs.ReadFileFS = (*FS)(nil)
	_ fs.ReadDirFS  = (*FS)(nil)
	_ fs.StatFS     = (*FS)(nil)
)

// Open parses a cabinet and returns it as a file system.
func Open(raw []byte) (*FS, error) {
	files, err := Read(raw)
	if err != nil {
		return nil, err
	}

	return NewFS(files)
}

// NewFS wraps already extracted files. Names are cleaned; two entries that
// clean to the same path are an error, and so is a file whose path is the
// directory of another ("a" and "a/b"), whatever their order.
func NewFS(files []File) (*FS, error) {
	fsys := &FS{files: make(map[string]*File, len(files)), dirs: make(map[string][]string)}
	fsys.dirs["."] = nil

	for i := range files {
		file := &files[i]

		name := path.Clean(strings.ReplaceAll(file.Name, `\`, "/"))
		if name == "." || !fs.ValidPath(name) {
			return nil, fmt.Errorf("cab: %q is not a valid file system path", file.Name)
		}

		if _, dup := fsys.files[name]; dup {
			return nil, fmt.Errorf("cab: %q appears twice", name)
		}

		if err := fsys.checkCollision(name); err != nil {
			return nil, err
		}

		fsys.files[name] = file
		fsys.link(name)

		if file.Time.After(fsys.time) {
			fsys.time = file.Time
		}
	}

	for dir := range fsys.dirs {
		slices.Sort(fsys.dirs[dir])
	}

	return fsys, nil
}

// Open implements fs.FS.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	if file, ok := f.files[name]; ok {
		return &openFile{info: fileInfo{name: path.Base(name), file: file}, Reader: bytes.NewReader(file.Data)}, nil
	}

	if _, ok := f.dirs[name]; ok {
		entries, _ := f.ReadDir(name)

		return &openDir{info: dirInfo{name: path.Base(name), time: f.time}, entries: entries}, nil
	}

	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadFile implements fs.ReadFileFS.
func (f *FS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrInvalid}
	}

	file, ok := f.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
	}

	return append([]byte(nil), file.Data...), nil
}

// ReadDir implements fs.ReadDirFS.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: opReadDir, Path: name, Err: fs.ErrInvalid}
	}

	children, ok := f.dirs[name]
	if !ok {
		if _, isFile := f.files[name]; isFile {
			return nil, &fs.PathError{Op: opReadDir, Path: name, Err: errors.New("not a directory")}
		}

		return nil, &fs.PathError{Op: opReadDir, Path: name, Err: fs.ErrNotExist}
	}

	entries := make([]fs.DirEntry, 0, len(children))

	for _, child := range children {
		info, _ := f.Stat(path.Join(name, child))
		entries = append(entries, fs.FileInfoToDirEntry(info))
	}

	return entries, nil
}

// Stat implements fs.StatFS.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}

	if file, ok := f.files[name]; ok {
		return fileInfo{name: path.Base(name), file: file}, nil
	}

	if _, ok := f.dirs[name]; ok {
		return dirInfo{name: path.Base(name), time: f.time}, nil
	}

	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// checkCollision reports a file at name that would clash with a directory:
// one that other files already lie in, or a file among its parents.
func (f *FS) checkCollision(name string) error {
	if _, dir := f.dirs[name]; dir {
		return fmt.Errorf("cab: %q is both a file and a directory", name)
	}

	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if _, file := f.files[parent]; file {
			return fmt.Errorf("cab: %q is both a file and a directory", parent)
		}
	}

	return nil
}

// link records name under its parent directories.
func (f *FS) link(name string) {
	for name != "." {
		parent := path.Dir(name)
		base := path.Base(name)

		if _, dir := f.dirs[name]; !dir {
			if _, file := f.files[name]; !file {
				f.dirs[name] = nil
			}
		}

		if !slices.Contains(f.dirs[parent], base) {
			f.dirs[parent] = append(f.dirs[parent], base)
		}

		name = parent
	}
}

// Files collects the regular files of a file system as cabinet entries, in
// fs.WalkDir order, with "/" turned into the cabinet's "\" separator. It is
// the writer-side counterpart of FS: cab.Write(cab.Files(os.DirFS(dir))).
func Files(fsys fs.FS) ([]File, error) {
	var files []File

	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("cab: %s is not a regular file", name)
		}

		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		files = append(files, File{
			Name:       strings.ReplaceAll(name, "/", `\`),
			Data:       data,
			Time:       info.ModTime().UTC(),
			Attributes: AttrArchive,
		})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cab: %w", err)
	}

	return files, nil
}

// Entries lists the regular files of fsys as streaming cabinet entries in
// WalkDir order, with slashes turned into backslashes and the archive
// attribute set. Each entry opens its file from fsys when the cabinet is
// written, so nothing is read here.
func Entries(fsys fs.FS) ([]Entry, error) {
	var entries []Entry

	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("cab: %s is not a regular file", name)
		}

		entries = append(entries, Entry{
			Name:       strings.ReplaceAll(name, "/", `\`),
			Size:       info.Size(),
			Time:       info.ModTime().UTC(),
			Attributes: AttrArchive,
			Open:       func() (io.ReadCloser, error) { return fsys.Open(name) },
		})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cab: %w", err)
	}

	return entries, nil
}

type fileInfo struct {
	file *File
	name string
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return int64(len(i.file.Data)) }
func (i fileInfo) ModTime() time.Time { return i.file.Time }
func (i fileInfo) IsDir() bool        { return false }
func (i fileInfo) Sys() any           { return i.file }

func (i fileInfo) Mode() fs.FileMode {
	if i.file.Attributes&AttrReadOnly != 0 {
		return 0o444
	}

	return 0o644
}

type dirInfo struct {
	time time.Time
	name string
}

func (i dirInfo) Name() string       { return i.name }
func (i dirInfo) Size() int64        { return 0 }
func (i dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (i dirInfo) ModTime() time.Time { return i.time }
func (i dirInfo) IsDir() bool        { return true }
func (i dirInfo) Sys() any           { return nil }

type openFile struct {
	*bytes.Reader

	info fileInfo
}

func (f *openFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *openFile) Close() error               { return nil }

type openDir struct {
	info    dirInfo
	entries []fs.DirEntry
	offset  int
}

func (d *openDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *openDir) Close() error               { return nil }

func (d *openDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: errors.New("is a directory")}
}

// ReadDir implements fs.ReadDirFile.
func (d *openDir) ReadDir(count int) ([]fs.DirEntry, error) {
	rest := d.entries[d.offset:]

	if count <= 0 {
		d.offset = len(d.entries)

		return rest, nil
	}

	if len(rest) == 0 {
		return nil, io.EOF
	}

	if len(rest) > count {
		rest = rest[:count]
	}

	d.offset += len(rest)

	return rest, nil
}
