package cab

import "encoding/binary"

// WithLZXBlock returns opts with every LZX block forced to one type:
// verbatim (1), aligned offset (2) or uncompressed (3).
func WithLZXBlock(opts Options, block int) Options {
	opts.lzxBlock = block

	return opts
}

// RawBlock is one CFDATA record of a hand-built cabinet.
type RawBlock struct {
	Payload []byte
	Uncomp  int
	BadSum  bool
}

// RawFolder is one CFFOLDER; Start overrides coffCabStart when Blocks is
// empty.
type RawFolder struct {
	Blocks []RawBlock
	Start  uint32
	Type   Compression
}

// RawFile is one CFFILE with its name bytes as stored.
type RawFile struct {
	Name   []byte
	Size   uint32
	Offset uint32
	Folder uint16
	Attr   uint16
}

// RawCabinet lays out a cabinet from records without the writer's checks,
// so that tests can build what other tools write or what is corrupt.
func RawCabinet(folders []RawFolder, files []RawFile) []byte {
	order := binary.LittleEndian
	coffFiles := headerSize + folderSize*len(folders)

	dataStart := coffFiles
	for _, file := range files {
		dataStart += fileEntrySize + len(file.Name) + 1
	}

	out := make([]byte, coffFiles, dataStart)
	copy(out, "MSCF")
	order.PutUint32(out[0x10:], uint32(coffFiles)) //nolint:gosec // small test cabinets
	out[0x18], out[0x19] = 3, 1
	order.PutUint16(out[0x1A:], uint16(len(folders))) //nolint:gosec // small test cabinets
	order.PutUint16(out[0x1C:], uint16(len(files)))   //nolint:gosec // small test cabinets

	for _, file := range files {
		var rec [fileEntrySize]byte

		order.PutUint32(rec[0:], file.Size)
		order.PutUint32(rec[4:], file.Offset)
		order.PutUint16(rec[8:], file.Folder)
		order.PutUint16(rec[10:], 0x0021)
		order.PutUint16(rec[14:], file.Attr)
		out = append(out, rec[:]...)
		out = append(out, file.Name...)
		out = append(out, 0)
	}

	for i, folder := range folders {
		rec := out[headerSize+folderSize*i:]
		order.PutUint32(rec, folder.Start)

		if len(folder.Blocks) > 0 {
			order.PutUint32(rec, uint32(len(out))) //nolint:gosec // small test cabinets
		}

		order.PutUint16(rec[4:], uint16(len(folder.Blocks))) //nolint:gosec // small test cabinets
		order.PutUint16(rec[6:], uint16(folder.Type))

		for _, block := range folder.Blocks {
			sum := blockChecksum(block.Payload, block.Uncomp)
			if block.BadSum {
				sum ^= 1
			}

			out = order.AppendUint32(out, sum)
			out = order.AppendUint16(out, uint16(len(block.Payload))) //nolint:gosec // small test cabinets
			out = order.AppendUint16(out, uint16(block.Uncomp))       //nolint:gosec // small test cabinets
			out = append(out, block.Payload...)
		}
	}

	order.PutUint32(out[cbCabinetOffset:], uint32(len(out))) //nolint:gosec // small test cabinets

	return out
}

// MSZIPBlock is compressBlock for the native tests.
func MSZIPBlock(chunk, dict []byte, level int) ([]byte, error) {
	return compressBlock(chunk, dict, level)
}

// AttrUTF8Name is the attribute of a UTF-8 name.
const AttrUTF8Name = attrUTF8Name
