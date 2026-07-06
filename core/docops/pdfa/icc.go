package pdfa

import (
	"encoding/binary"
	"math"
)

// icc.go generates a minimal but structurally valid ICC v2 RGB display profile
// (an approximation of sRGB) for use as a PDF/A OutputIntent DestOutputProfile.
// It contains the tags a v2 RGB monitor profile requires: desc, wtpt, rXYZ,
// gXYZ, bXYZ, rTRC, gTRC, bTRC, cprt. It is generated rather than shipped as a
// binary blob so the package stays dependency-free.
//
// NOTE: this is a self-contained profile, not the canonical sRGB IEC 61966-2.1
// data; it is sufficient to give a PDF/A file a valid embedded output profile.

// s15Fixed16 encodes a float as ICC s15Fixed16Number.
func s15Fixed16(v float64) uint32 {
	return uint32(int32(math.Round(v * 65536)))
}

// xyzType builds an 'XYZ ' tag (X, Y, Z as s15Fixed16).
func xyzType(x, y, z float64) []byte {
	b := make([]byte, 20)
	copy(b, "XYZ ")
	binary.BigEndian.PutUint32(b[8:], s15Fixed16(x))
	binary.BigEndian.PutUint32(b[12:], s15Fixed16(y))
	binary.BigEndian.PutUint32(b[16:], s15Fixed16(z))
	return b
}

// curveType builds a 'curv' tag with a single gamma value (u8Fixed8Number).
func curveType(gamma float64) []byte {
	b := make([]byte, 14)
	copy(b, "curv")
	binary.BigEndian.PutUint32(b[8:], 1) // one entry = gamma
	binary.BigEndian.PutUint16(b[12:], uint16(math.Round(gamma*256)))
	return b
}

// textType builds a 'text' tag (ASCII + null).
func textType(s string) []byte {
	b := make([]byte, 8+len(s)+1)
	copy(b, "text")
	copy(b[8:], s)
	return b
}

// descType builds a v2 'desc' textDescriptionType tag.
func descType(s string) []byte {
	ascii := append([]byte(s), 0)
	// 8 (sig+reserved) + 4 (count) + ascii + 8 (unicode lang+count) +
	// 3 (scriptcode code+count) + 67 (scriptcode desc).
	b := make([]byte, 8+4+len(ascii)+8+3+67)
	copy(b, "desc")
	binary.BigEndian.PutUint32(b[8:], uint32(len(ascii)))
	copy(b[12:], ascii)
	// The remaining unicode/scriptcode fields stay zero (empty), which is valid.
	return b
}

// tag is one ICC tag: its 4-byte signature and data.
type tag struct {
	sig  string
	data []byte
}

// GenerateSRGBICC returns a minimal valid ICC v2 RGB profile.
func GenerateSRGBICC() []byte {
	tags := []tag{
		{"desc", descType("FluxDocs sRGB")},
		{"wtpt", xyzType(0.9642, 1.0, 0.8249)}, // D50 white point
		{"rXYZ", xyzType(0.43607, 0.22249, 0.01392)},
		{"gXYZ", xyzType(0.38515, 0.71687, 0.09708)},
		{"bXYZ", xyzType(0.14307, 0.06061, 0.71410)},
		{"rTRC", curveType(2.2)},
		{"gTRC", curveType(2.2)},
		{"bTRC", curveType(2.2)},
		{"cprt", textType("Public Domain")},
	}

	const headerSize = 128
	tagCount := len(tags)
	tableSize := 4 + tagCount*12 // count + entries
	dataStart := headerSize + tableSize

	// Lay out tag data (4-byte aligned) and record offsets. rTRC/gTRC/bTRC are
	// identical, so they can share one data block — but keep it simple: each tag
	// gets its own block.
	var data []byte
	offsets := make([]int, tagCount)
	sizes := make([]int, tagCount)
	for i, t := range tags {
		offsets[i] = dataStart + len(data)
		sizes[i] = len(t.data)
		data = append(data, t.data...)
		for len(data)%4 != 0 { // pad to 4-byte boundary
			data = append(data, 0)
		}
	}
	profileSize := dataStart + len(data)

	out := make([]byte, profileSize)

	// Header (128 bytes).
	binary.BigEndian.PutUint32(out[0:], uint32(profileSize))
	binary.BigEndian.PutUint32(out[8:], 0x02100000) // version 2.1
	copy(out[12:], "mntr")                          // device class: display
	copy(out[16:], "RGB ")                          // colour space
	copy(out[20:], "XYZ ")                          // PCS
	copy(out[36:], "acsp")                          // required signature
	// PCS illuminant = D50.
	binary.BigEndian.PutUint32(out[68:], s15Fixed16(0.9642))
	binary.BigEndian.PutUint32(out[72:], s15Fixed16(1.0))
	binary.BigEndian.PutUint32(out[76:], s15Fixed16(0.8249))

	// Tag table.
	binary.BigEndian.PutUint32(out[headerSize:], uint32(tagCount))
	for i, t := range tags {
		e := headerSize + 4 + i*12
		copy(out[e:], t.sig)
		binary.BigEndian.PutUint32(out[e+4:], uint32(offsets[i]))
		binary.BigEndian.PutUint32(out[e+8:], uint32(sizes[i]))
	}

	// Tag data.
	copy(out[dataStart:], data)
	return out
}
