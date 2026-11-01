package builder

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/spool"
	"github.com/jkroepke/msi-builder/pkg/config"
	"github.com/jkroepke/msi-builder/pkg/sign"
)

// maxEmbedded is the most the embedded cabinets may add up to: a package
// above 2 GB is not installable (section 5), and the compound file writer
// stops below it.
const maxEmbedded = 0x7FFFFE00

// maxDiskID is the largest Media.DiskId, an i2 column.
const maxDiskID = 32767

// cabinetSigner is the MsiDigitalCertificate row of the certificate that
// signs the external cabinets.
const cabinetSigner = "CabinetSigner"

// cabinetInfo is one cabinet: a run of payload files in sequence order with
// a Media row of its own (section 5).
type cabinetInfo struct {
	// target holds the written cabinet until the package is committed.
	target spool.Target
	name   string
	files  []*fileInfo
	// copies holds, for each file, 1 + the index of the earlier file of the
	// cabinet with the same bytes, whose data it shares, or 0 (cab.Entry.Copy).
	copies []int
	// signature is the signer's signature value, MsiDigitalSignature.Hash,
	// once the cabinet is signed.
	signature []byte
	size      int64
}

// cabOptions are the cabinet writer options of package.compression.
func (b *build) cabOptions() cab.Options {
	opts := cab.Options{Compression: cab.MSZIP, SignatureReserve: b.signsCabinets()}

	if window, ok := config.LZXWindow(b.cfg.Package.Compression); ok {
		opts.Compression, opts.Window = cab.LZX, window
	} else if b.cfg.Package.Compression == "none" {
		opts.Compression = cab.None
	}

	return opts
}

// signsCabinets reports whether the cabinets get signatures of their own:
// only external ones do, since Windows Installer checks no other
// (section 12).
func (b *build) signsCabinets() bool {
	return b.opts.SignCabinets != nil && !b.cfg.Package.CabinetsEmbedded()
}

// splitCabinets puts the payload files, in sequence order, into as few
// cabinets as hold them: a cabinet ends before the file that would take it
// over 65535 files, over 65535 blocks or, with the data compressing as
// badly as it can, over Options.CabinetSize or 2 GiB. Files are never split
// across cabinets (section 16, "Decisions not adopted"). A file that alone
// is above the size limit gets a cabinet of its own and fails to write only
// when its data really does not fit. A file with the same bytes as an
// earlier file of its cabinet shares that file's data (section 5).
func (b *build) splitCabinets() {
	limit := int64(cab.MaxSize)
	if b.opts.CabinetSize > 0 {
		limit = min(limit, b.opts.CabinetSize)
	}

	comp := b.cabOptions().Compression
	shared := sharedContents(b.files)

	var (
		current *cabinetInfo
		load    cabinetLoad
	)

	for _, file := range b.files {
		next, copyOf := load.with(file, shared)
		if current != nil && !next.fits(comp, limit) {
			current = nil
		}

		if current == nil {
			current = &cabinetInfo{name: cabinetName(b.cfg.Package.Cabinet, len(b.cabinets)+1)}
			b.cabinets = append(b.cabinets, current)
			load = cabinetLoad{}
			next, copyOf = load.with(file, shared)
		}

		load = next.commit(file, copyOf, shared)
		current.files = append(current.files, file)
		current.copies = append(current.copies, copyOf)
	}

	if len(b.cabinets) > maxDiskID {
		b.errorf("package.cabinet", "the payload needs %d cabinets, but Media.DiskId holds at most %d", len(b.cabinets), maxDiskID)
	}

	if !b.cfg.Package.CabinetsEmbedded() {
		return // an external cabinet is a file, not a stream
	}

	// package.cabinet itself is validated with the config.
	for i, cabinet := range b.cabinets[min(1, len(b.cabinets)):] {
		if err := msidb.CheckStreamName(cabinet.name); err != nil {
			b.errorf("package.cabinet", "the name of cabinet %d, %q, is not a valid stream name (MSIDB-11): %v; use a shorter name", i+2, cabinet.name, err)
		}
	}
}

// contentKey identifies a file's bytes: its size and SHA-256.
type contentKey struct {
	digest [sha256.Size]byte
	size   int64
}

// minShared is the smallest file whose copies share its bytes: one CFDATA
// block. A smaller copy stored again costs at most one block, about what
// the folder of its own that sharing needs costs in records and lost
// compression history (section 5).
const minShared = 32768

// sharedContents returns the contents that more than one payload file of at
// least minShared bytes has.
func sharedContents(files []*fileInfo) map[contentKey]bool {
	seen := make(map[contentKey]bool)
	shared := make(map[contentKey]bool)

	for _, file := range files {
		if file.Size < minShared {
			continue
		}

		key := contentKey{file.digest, file.Size}
		if seen[key] {
			shared[key] = true
		}

		seen[key] = true
	}

	return shared
}

// cabinetLoad is what the files of one cabinet take so far.
type cabinetLoad struct {
	// stored maps a shared content to 1 + the index of the file of the
	// cabinet that stores it.
	stored                 map[contentKey]int
	files, folders         int
	nameBytes, folderBytes int64
	// open tells whether the last folder takes the next stored file.
	open bool
}

// with returns the load with file added and, when an earlier file of the
// cabinet stores the same bytes, 1 + its index. It counts folders as
// cab.Folders does, except that every file whose content is shared gets a
// folder of its own even when no later file of the cabinet copies it, which
// can only overestimate.
func (l cabinetLoad) with(file *fileInfo, shared map[contentKey]bool) (cabinetLoad, int) {
	l.files++
	l.nameBytes += int64(len(file.Key))

	key := contentKey{file.digest, file.Size}

	if shared[key] {
		if index, ok := l.stored[key]; ok {
			l.open = false

			return l, index
		}

		l.folders++
		l.open = false
	} else if !l.open {
		l.folders++
		l.open = true
	}

	l.folderBytes += file.Size

	return l, 0
}

// commit records that file, the last one with added, is in the cabinet.
func (l cabinetLoad) commit(file *fileInfo, copyOf int, shared map[contentKey]bool) cabinetLoad {
	key := contentKey{file.digest, file.Size}

	if copyOf == 0 && shared[key] {
		if l.stored == nil {
			l.stored = make(map[contentKey]int)
		}

		l.stored[key] = l.files
	}

	return l
}

// fits reports whether the load fits one cabinet of at most limit bytes,
// counting the signature reserve whether or not the cabinet is signed. A
// signature follows cbCabinet and is not counted.
func (l cabinetLoad) fits(comp cab.Compression, limit int64) bool {
	return l.files <= cab.MaxFiles && l.folderBytes <= cab.MaxFolder &&
		cab.Bound(l.files, l.folders, l.nameBytes, l.folderBytes, comp)+cab.SignatureReserve <= limit
}

// cabinetName is the name of the number-th cabinet: package.cabinet for the
// first, then the number before its extension, product2.cab for the second.
func cabinetName(first string, number int) string {
	if number == 1 {
		return first
	}

	ext := filepath.Ext(first)

	return strings.TrimSuffix(first, ext) + strconv.Itoa(number) + ext
}

// writeMedia adds a Media row per cabinet: DiskId from 1 and the last
// sequence of its files (section 11.6). A package without files has one row
// without a cabinet (ICE71).
func (b *build) writeMedia() {
	media := b.table("Media")

	if len(b.cabinets) == 0 {
		media.Add(1, 0, nil, nil, nil, nil)

		return
	}

	prefix := ""
	if b.cfg.Package.CabinetsEmbedded() {
		prefix = "#"
	}

	for i, cabinet := range b.cabinets {
		media.Add(i+1, cabinet.files[len(cabinet.files)-1].Sequence, nil, prefix+cabinet.name, nil, nil)
	}
}

// writeSignatures adds the MsiDigitalCertificate row of the signing
// certificate and an MsiDigitalSignature row per external cabinet when the
// cabinets are signed. Each Hash is filled in once its cabinet is signed.
func (b *build) writeSignatures() {
	if !b.signsCabinets() || len(b.cabinets) == 0 {
		return
	}

	b.table("MsiDigitalCertificate").Add(cabinetSigner, b.opts.SignCabinets.Certificates[0].Raw)

	signatures := b.table("MsiDigitalSignature")

	for i := range b.cabinets {
		signatures.Add("Media", strconv.Itoa(i+1), cabinetSigner, nil)
	}
}

// signCabinet signs the external cabinet in its spool and records the
// signature value as the Hash of its MsiDigitalSignature row, the index-th.
func (b *build) signCabinet(cabinet *cabinetInfo, index int) error {
	// The build has no context; a timestamp request is bounded by the
	// HTTP client's timeout.
	signature, err := sign.SignCabinet(context.Background(), cabinet.target, *b.opts.SignCabinets)
	if err != nil {
		return fmt.Errorf("cabinet %s: %w", cabinet.name, err)
	}

	size, err := spool.Size(cabinet.target)
	if err != nil {
		return fmt.Errorf("cabinet %s: %w", cabinet.name, err)
	}

	cabinet.signature, cabinet.size = signature, size
	b.tables["MsiDigitalSignature"].Rows[index][3] = signature

	return nil
}

// writeCabinet packs the files of cabinet into target, streaming each file
// from disk.
func (b *build) writeCabinet(target spool.Target, cabinet *cabinetInfo) error {
	entries := make([]cab.Entry, 0, len(cabinet.files))

	for i, entry := range cabinet.files {
		entries = append(entries, cab.Entry{
			Name:       entry.Key,
			Size:       entry.Size,
			Time:       b.opts.BuildTime,
			Attributes: cab.AttrArchive,
			Open:       entry.open,
			Copy:       cabinet.copies[i],
		})
	}

	size, err := cab.WriteTo(target, entries, b.cabOptions())
	if err != nil {
		return fmt.Errorf("cabinet %s: %w", cabinet.name, err)
	}

	cabinet.target = target
	cabinet.size = size

	return nil
}

// source is the cabinet as a stream of the package.
func (c *cabinetInfo) source() cfb.Source {
	return cfb.Source{
		Name: c.name,
		Size: c.size,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(io.NewSectionReader(c.target, 0, c.size)), nil },
	}
}
