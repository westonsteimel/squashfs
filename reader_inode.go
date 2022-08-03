package squashfs

import (
	"errors"
	"io"

	"github.com/sylabs/squashfs/internal/data"
	"github.com/sylabs/squashfs/internal/directory"
	"github.com/sylabs/squashfs/internal/inode"
	"github.com/sylabs/squashfs/internal/metadata"
	"github.com/sylabs/squashfs/internal/toreader"
)

func (r Reader) inodeFromRef(ref uint64) (i inode.Inode, err error) {
	offset, meta := (ref>>16)+r.s.InodeTableStart, ref&0xFFFF
	rdr, err := metadata.NewReader(toreader.NewReader(r.r, int64(offset)), r.d)
	if err != nil {
		return
	}
	_, err = rdr.Read(make([]byte, meta))
	if err != nil {
		return
	}
	return inode.Read(rdr, r.s.BlockSize)
}

func (r Reader) inodeFromDir(e directory.Entry) (i inode.Inode, err error) {
	rdr, err := metadata.NewReader(toreader.NewReader(r.r, int64(uint64(e.BlockStart)+r.s.InodeTableStart)), r.d)
	if err != nil {
		return
	}
	_, err = rdr.Read(make([]byte, e.Offset))
	if err != nil {
		return
	}
	return inode.Read(rdr, r.s.BlockSize)
}

func (r Reader) getData(i inode.Inode) (io.Reader, error) {
	var fragOffset uint64
	var blockOffset uint32
	var blockSizes []uint32
	var fragInd uint32
	if i.Type == inode.Fil {
		fragOffset = uint64(i.Data.(inode.File).Offset)
		blockOffset = i.Data.(inode.File).BlockStart
		blockSizes = i.Data.(inode.File).BlockSizes
		fragInd = i.Data.(inode.File).FragInd
	} else if i.Type == inode.EFil {
		fragOffset = uint64(i.Data.(inode.EFile).Offset)
		blockOffset = i.Data.(inode.EFile).BlockStart
		blockSizes = i.Data.(inode.EFile).BlockSizes
		fragInd = i.Data.(inode.EFile).FragInd
	} else {
		return nil, errors.New("getData called on non-file type")
	}
	rdr, err := data.NewReader(toreader.NewReader(r.r, int64(blockOffset)), r.d, blockSizes, r.s.BlockSize)
	if err != nil {
		return nil, err
	}
	if fragInd != 0xFFFFFFFF {
		var fragRdr io.Reader
		fragRdr, err = r.fragReader(fragInd)
		if err != nil {
			return nil, err
		}
		_, err = fragRdr.Read(make([]byte, fragOffset))
		if err != nil {
			return nil, err
		}
		rdr.AddFragment(fragRdr)
	}
	return rdr, nil
}

func (r Reader) getFullReader(i inode.Inode) (rdr *data.FullReader, err error) {
	var fragOffset uint64
	var blockOffset uint32
	var blockSizes []uint32
	var fragInd uint32
	if i.Type == inode.Fil {
		fragOffset = uint64(i.Data.(inode.File).Offset)
		blockOffset = i.Data.(inode.File).BlockStart
		blockSizes = i.Data.(inode.File).BlockSizes
		fragInd = i.Data.(inode.File).FragInd
	} else if i.Type == inode.EFil {
		fragOffset = uint64(i.Data.(inode.EFile).Offset)
		blockOffset = i.Data.(inode.EFile).BlockStart
		blockSizes = i.Data.(inode.EFile).BlockSizes
		fragInd = i.Data.(inode.EFile).FragInd
	} else {
		return nil, errors.New("getData called on non-file type")
	}
	rdr = data.NewFullReader(r.r, uint64(blockOffset), r.d, blockSizes, r.s.BlockSize)
	if fragInd != 0xFFFFFFFF {
		var fragRdr io.Reader
		fragRdr, err = r.fragReader(fragInd)
		if err != nil {
			return nil, err
		}
		_, err = fragRdr.Read(make([]byte, fragOffset))
		if err != nil {
			return nil, err
		}
		rdr.AddFragment(fragRdr)
	}
	return rdr, nil
}

func (r Reader) readDirectory(i inode.Inode) ([]directory.Entry, error) {
	var offset uint64
	var blockOffset uint16
	var size uint32
	if i.Type == inode.Dir {
		offset = uint64(i.Data.(inode.Directory).BlockStart)
		blockOffset = i.Data.(inode.Directory).Offset
		size = uint32(i.Data.(inode.Directory).Size)
	} else if i.Type == inode.EDir {
		offset = uint64(i.Data.(inode.EDirectory).BlockStart)
		blockOffset = i.Data.(inode.EDirectory).Offset
		size = i.Data.(inode.EDirectory).Size
	} else {
		return nil, errors.New("readDirectory called on non-directory type")
	}
	rdr, err := metadata.NewReader(toreader.NewReader(r.r, int64(offset+r.s.DirTableStart)), r.d)
	if err != nil {
		return nil, err
	}
	_, err = rdr.Read(make([]byte, blockOffset))
	if err != nil {
		return nil, err
	}
	return directory.ReadEntries(rdr, size)
}
