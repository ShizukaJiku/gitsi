package desktop

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// The executable's own icon (Explorer, Alt+Tab, the taskbar) is a Windows resource linked into the binary. Go links any .syso
// object next to main.go, so cmd/gitsi keeps one per architecture, made by ResourceObject from the same drawing as the tray icon
// (without the state dot): no .ico file, no resource compiler, no third-party tool. `go generate ./cmd/gitsi` writes them and a
// test checks that the committed ones are current.

// iconMark is the mark alone, without a state dot (the executable's icon).
const iconMark IconState = -1

// exeIconSizes are the sizes the executable's icon carries: Windows picks the closest one for each place and DPI.
var exeIconSizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

// Resource and COFF constants.
const (
	rtIcon        = 3
	rtGroupIcon   = 14
	langNeutral   = 0x0409 // en-US, what resource compilers write by default
	machineAMD64  = 0x8664
	machineARM64  = 0xAA64
	relAMD64Addr  = 3          // IMAGE_REL_AMD64_ADDR32NB
	relARM64Addr  = 2          // IMAGE_REL_ARM64_ADDR32NB
	scnRsrcFlags  = 0x40000040 // IMAGE_SCN_CNT_INITIALIZED_DATA | IMAGE_SCN_MEM_READ
	symClassStat  = 3
	coffHeaderLen = 20
	sectionLen    = 40
)

// ResourceObject returns a COFF object with the executable's icon for an architecture ("amd64" or "arm64").
func ResourceObject(arch string) ([]byte, error) {
	var machine, rel uint16
	switch arch {
	case "amd64":
		machine, rel = machineAMD64, relAMD64Addr
	case "arm64":
		machine, rel = machineARM64, relARM64Addr
	default:
		return nil, fmt.Errorf("no icon resource for %s", arch)
	}
	var images [][]byte
	for _, s := range exeIconSizes {
		img, err := iconImage(s)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	data, relocs := resourceSection(images)

	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	relOff := coffHeaderLen + sectionLen + len(data)
	symOff := relOff + 10*len(relocs)
	// COFF header
	w(machine)
	w(uint16(1)) // sections
	w(uint32(0)) // time stamp: 0 keeps the file the same on every run
	w(uint32(symOff))
	w(uint32(1)) // symbols
	w(uint16(0)) // optional header
	w(uint16(0)) // characteristics
	// section header
	b.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0)) // virtual size
	w(uint32(0)) // virtual address
	w(uint32(len(data)))
	w(uint32(coffHeaderLen + sectionLen))
	w(uint32(relOff))
	w(uint32(0)) // line numbers
	w(uint16(len(relocs)))
	w(uint16(0))
	w(uint32(scnRsrcFlags))
	b.Write(data)
	// relocations: each data entry's address becomes an RVA once linked
	for _, off := range relocs {
		w(uint32(off))
		w(uint32(0)) // symbol 0: the section
		w(rel)
	}
	// symbol table: the section's symbol, then an empty string table
	b.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0))
	w(int16(1))
	w(uint16(0))
	w(uint8(symClassStat))
	w(uint8(0))
	w(uint32(4))
	return b.Bytes(), nil
}

// iconImage is one image of the icon: a PNG for 256 pixels, a 32-bit DIB with its mask for the rest (what Windows expects in an
// RT_ICON resource).
func iconImage(size int) ([]byte, error) {
	px := IconPixels(size, iconMark)
	if size >= 256 {
		img := image.NewNRGBA(image.Rect(0, 0, size, size))
		for i := 0; i < size*size; i++ {
			img.Set(i%size, i/size, color.NRGBA{px[i*4+2], px[i*4+1], px[i*4], px[i*4+3]})
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	maskRow := ((size + 31) / 32) * 4
	w(bitmapHeader{size: 40, width: int32(size), height: int32(2 * size), planes: 1, bitCount: 32,
		sizeImage: uint32(size*size*4 + maskRow*size)})
	for y := size - 1; y >= 0; y-- { // DIB rows go bottom-up
		b.Write(px[y*size*4 : (y+1)*size*4])
	}
	for y := size - 1; y >= 0; y-- {
		row := make([]byte, maskRow)
		for x := 0; x < size; x++ {
			if px[(y*size+x)*4+3] == 0 {
				row[x/8] |= 0x80 >> (x % 8) // transparent for renderers that ignore alpha
			}
		}
		b.Write(row)
	}
	return b.Bytes(), nil
}

// bitmapHeader is BITMAPINFOHEADER, for writing (the Windows-only file has its own for the API).
type bitmapHeader struct {
	size          uint32
	width, height int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPPM, yPPM    int32
	clrUsed       uint32
	clrImportant  uint32
}

// resourceSection lays out the .rsrc section: the three-level directory (type, id, language), the data entries and the data.
// relocs are the offsets of the data entries' address fields.
func resourceSection(images [][]byte) (data []byte, relocs []int) {
	group := groupIcon(images)
	type leaf struct {
		typ, id int
		blob    []byte
	}
	var leaves []leaf
	for i, img := range images {
		leaves = append(leaves, leaf{rtIcon, i + 1, img})
	}
	leaves = append(leaves, leaf{rtGroupIcon, 1, group})

	// Layout: root (2 types) | per type: id directory | per id: language directory | data entries | blobs.
	const dirLen, entryLen, dataEntryLen = 16, 8, 16
	root := dirLen + 2*entryLen
	iconIDs := dirLen + len(images)*entryLen
	groupIDs := dirLen + entryLen
	langs := len(leaves) * (dirLen + entryLen)
	dataEntries := len(leaves) * dataEntryLen
	pos := root + iconIDs + groupIDs + langs + dataEntries
	blobAt := make([]int, len(leaves))
	for i, l := range leaves {
		pos = (pos + 7) &^ 7
		blobAt[i] = pos
		pos += len(l.blob)
	}
	out := make([]byte, (pos+7)&^7)
	le := binary.LittleEndian
	dir := func(at, ids int) { le.PutUint16(out[at+14:], uint16(ids)) } // only id entries; the rest of the header stays zero
	entry := func(at, id, off int, sub bool) {
		le.PutUint32(out[at:], uint32(id))
		v := uint32(off)
		if sub {
			v |= 0x80000000
		}
		le.PutUint32(out[at+4:], v)
	}
	iconDir := root
	groupDir := iconDir + iconIDs
	langDir := groupDir + groupIDs
	dataDir := langDir + langs

	dir(0, 2)
	entry(dirLen, rtIcon, iconDir, true)
	entry(dirLen+entryLen, rtGroupIcon, groupDir, true)
	dir(iconDir, len(images))
	dir(groupDir, 1)
	for i, l := range leaves {
		ld := langDir + i*(dirLen+entryLen)
		if l.typ == rtIcon {
			entry(iconDir+dirLen+i*entryLen, l.id, ld, true)
		} else {
			entry(groupDir+dirLen, l.id, ld, true)
		}
		dir(ld, 1)
		de := dataDir + i*dataEntryLen
		entry(ld+dirLen, langNeutral, de, false)
		le.PutUint32(out[de:], uint32(blobAt[i])) // an offset in the section: the relocation makes it an RVA
		le.PutUint32(out[de+4:], uint32(len(l.blob)))
		relocs = append(relocs, de)
		copy(out[blobAt[i]:], l.blob)
	}
	return out, relocs
}

// groupIcon is the RT_GROUP_ICON resource: the list of images, each pointing to its RT_ICON by id.
func groupIcon(images [][]byte) []byte {
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	w(uint16(0))
	w(uint16(1)) // icon
	w(uint16(len(images)))
	for i, img := range images {
		s := exeIconSizes[i]
		dim := uint8(s)
		if s >= 256 {
			dim = 0 // 0 means 256
		}
		w(dim)
		w(dim)
		w(uint8(0)) // colors
		w(uint8(0))
		w(uint16(1))  // planes
		w(uint16(32)) // bits per pixel
		w(uint32(len(img)))
		w(uint16(i + 1))
	}
	return b.Bytes()
}
