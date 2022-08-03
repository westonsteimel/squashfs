package squashfs

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"time"

	"github.com/sylabs/squashfs/internal/decompress"
	"github.com/sylabs/squashfs/internal/directory"
	"github.com/sylabs/squashfs/internal/inode"
	"github.com/sylabs/squashfs/internal/metadata"
	"github.com/sylabs/squashfs/internal/toreader"
)

type Reader struct {
	*FS
	d           decompress.Decompressor
	r           io.ReaderAt
	fragEntries []fragEntry
	ids         []uint32
	exportTable []uint64
	s           superblock
}

var (
	ErrorMagic = errors.New("magic incorrect. probably not reading squashfs archive")
	ErrorLog   = errors.New("block log is incorrect. possible corrupted archive")
)

const (
	GZipCompression = uint16(iota + 1)
	LZMACompression
	LZOCompression
	XZCompression
	LZ4Compression
	ZSTDCompression
)

func NewReaderFromReader(r io.Reader) (*Reader, error) {
	rdr, err := toreader.NewReaderAt(r)
	if err != nil {
		return nil, err
	}
	return NewReader(rdr)
}

func NewReader(r io.ReaderAt) (*Reader, error) {
	var squash Reader
	squash.r = r
	err := binary.Read(toreader.NewReader(r, 0), binary.LittleEndian, &squash.s)
	if err != nil {
		return nil, err
	}
	if !squash.s.hasMagic() {
		return nil, ErrorMagic
	}
	if !squash.s.checkBlockLog() {
		return nil, ErrorLog
	}
	switch squash.s.CompType {
	case GZipCompression:
		squash.d = decompress.GZip{}
	case LZMACompression:
		squash.d = decompress.Lzma{}
	case LZOCompression:
		squash.d = decompress.Lzo{}
	case XZCompression:
		squash.d = decompress.Xz{}
	case LZ4Compression:
		squash.d = decompress.Lz4{}
	case ZSTDCompression:
		squash.d = decompress.Zstd{}
	default:
		return nil, errors.New("uh, I need to do this, OR something if very wrong")
	}
	if !squash.s.noFragments() && squash.s.FragCount > 0 {
		fragOffsets := make([]uint64, int(math.Ceil(float64(squash.s.FragCount)/512)))
		err = binary.Read(toreader.NewReader(r, int64(squash.s.FragTableStart)), binary.LittleEndian, &fragOffsets)
		if err != nil {
			return nil, err
		}
		squash.fragEntries = make([]fragEntry, squash.s.FragCount)
		if len(fragOffsets) == 1 {
			var rdr *metadata.Reader
			rdr, err = metadata.NewReader(toreader.NewReader(r, int64(fragOffsets[0])), squash.d)
			if err != nil {
				return nil, err
			}
			err = binary.Read(rdr, binary.LittleEndian, &squash.fragEntries)
			if err != nil {
				return nil, err
			}
		} else {
			toRead := squash.s.IdCount
			var curRead uint16
			var tmp []fragEntry
			var rdr *metadata.Reader
			var offset int
			for i := range fragOffsets {
				curRead = uint16(math.Min(512, float64(toRead)))
				tmp = make([]fragEntry, curRead)
				rdr, err = metadata.NewReader(toreader.NewReader(r, int64(fragOffsets[i])), squash.d)
				if err != nil {
					return nil, err
				}
				err = binary.Read(rdr, binary.LittleEndian, &tmp)
				if err != nil {
					return nil, err
				}
				offset = int(squash.s.IdCount - toRead)
				for i := range tmp {
					squash.fragEntries[offset+i] = tmp[i]
				}
				toRead -= curRead
			}
		}
	}
	if squash.s.IdCount > 0 {
		idOffsets := make([]uint64, int(math.Ceil(float64(squash.s.IdCount)/2048)))
		err = binary.Read(toreader.NewReader(r, int64(squash.s.IdTableStart)), binary.LittleEndian, &idOffsets)
		if err != nil {
			return nil, err
		}
		squash.ids = make([]uint32, squash.s.IdCount)
		if len(idOffsets) == 1 {
			var rdr *metadata.Reader
			rdr, err = metadata.NewReader(toreader.NewReader(r, int64(idOffsets[0])), squash.d)
			if err != nil {
				return nil, err
			}
			err = binary.Read(rdr, binary.LittleEndian, &squash.ids)
			if err != nil {
				return nil, err
			}
		} else {
			toRead := squash.s.IdCount
			var curRead uint16
			var tmp []uint32
			var rdr *metadata.Reader
			var offset int
			for i := range idOffsets {
				curRead = uint16(math.Min(2048, float64(toRead)))
				tmp = make([]uint32, curRead)
				rdr, err = metadata.NewReader(toreader.NewReader(r, int64(idOffsets[i])), squash.d)
				if err != nil {
					return nil, err
				}
				err = binary.Read(rdr, binary.LittleEndian, &tmp)
				if err != nil {
					return nil, err
				}
				offset = int(squash.s.IdCount - toRead)
				for i := range tmp {
					squash.ids[offset+i] = tmp[i]
				}
				toRead -= curRead
			}
		}
	}
	root, err := squash.inodeFromRef(squash.s.RootInodeRef)
	if err != nil {
		return nil, err
	}
	rootEnts, err := squash.readDirectory(root)
	if err != nil {
		return nil, err
	}
	enType := root.Type
	if enType == inode.EDir {
		enType = inode.Dir
	}
	squash.FS = &FS{
		e: rootEnts,
		File: &File{
			rdr: &squash,
			i:   root,
			e: directory.Entry{
				Name: "root",
				Type: enType,
			},
		},
	}
	return &squash, nil
}

func (r *Reader) initExport() (err error) {
	num := int(math.Ceil(float64(r.s.InodeCount) / 1024))
	offsets := make([]uint64, num)
	err = binary.Read(toreader.NewReader(r.r, int64(r.s.ExportTableStart)), binary.LittleEndian, &offsets)
	if err != nil {
		return
	}
	left := r.s.InodeCount
	var toRead uint32
	var new []uint64
	var rdr *metadata.Reader
	for i := range offsets {
		rdr, err = metadata.NewReader(toreader.NewReader(r.r, int64(offsets[i])), r.d)
		if err != nil {
			return
		}
		toRead = uint32(math.Min(1024, float64(left)))
		new = make([]uint64, toRead)
		err = binary.Read(rdr, binary.LittleEndian, &new)
		if err != nil {
			return
		}
		left -= toRead
		r.exportTable = append(r.exportTable, new...)
	}
	return nil
}

func (r *Reader) inode(index uint32) (i inode.Inode, err error) {
	if r.s.exportable() {
		if r.exportTable == nil {
			err = r.initExport()
			if err != nil {
				return
			}
		}
		return r.inodeFromRef(r.exportTable[index-1])
	}
	err = errors.New("archive is not exportable")
	return
}

func (r Reader) ModTime() time.Time {
	return time.Unix(int64(r.s.ModTime), 0)
}
