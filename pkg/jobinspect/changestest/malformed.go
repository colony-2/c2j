package changestest

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
)

// BreakTree derives a missing-tree fixture from an actual writer bundle. It
// changes one packed tree to a blob and recomputes the pack checksum, preserving
// the advertised commit. Readers must detect closure/type failure, not merely a
// bad transport checksum. The caller persists these bytes only during test setup.
func BreakTree(bundle []byte) ([]byte, error) {
	boundary := bytes.Index(bundle, []byte("\n\n"))
	if boundary < 0 {
		return nil, fmt.Errorf("bundle header missing")
	}
	start := boundary + 2
	format256 := bytes.Contains(bundle[:start], []byte("@object-format=sha256"))
	checksum := 20
	if format256 {
		checksum = 32
	}
	if len(bundle) < start+12+checksum || string(bundle[start:start+4]) != "PACK" {
		return nil, fmt.Errorf("pack missing")
	}
	pack := append([]byte(nil), bundle[start:]...)
	reader := bytes.NewReader(pack)
	var header [12]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	count := binary.BigEndian.Uint32(header[8:])
	found := false
	for i := uint32(0); i < count; i++ {
		offset := len(pack) - reader.Len()
		first, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		kind := (first >> 4) & 7
		b := first
		for b&128 != 0 {
			b, err = reader.ReadByte()
			if err != nil {
				return nil, err
			}
		}
		if kind == 6 {
			for {
				b, err = reader.ReadByte()
				if err != nil {
					return nil, err
				}
				if b&128 == 0 {
					break
				}
			}
		} else if kind == 7 {
			if _, err = reader.Seek(int64(checksum), io.SeekCurrent); err != nil {
				return nil, err
			}
		}
		z, err := zlib.NewReader(reader)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(io.Discard, z)
		ce := z.Close()
		if err != nil {
			return nil, err
		}
		if ce != nil {
			return nil, ce
		}
		if kind == 2 && !found {
			pack[offset] = first&^0x70 | 3<<4
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("writer bundle has no plain tree")
	}
	body := pack[:len(pack)-checksum]
	if format256 {
		hash := sha256.Sum256(body)
		copy(pack[len(body):], hash[:])
	} else {
		hash := sha1.Sum(body)
		copy(pack[len(body):], hash[:])
	}
	return append(append([]byte(nil), bundle[:start]...), pack...), nil
}
