package jobinspect

import (
	"bufio"
	"compress/zlib"
	"context"
	"encoding/binary"
	"io"
	"os"
)

// inspectPack bounds expansion before invoking index-pack. Delta headers carry
// the final object size as well as the delta program size. No object body is
// retained in memory; zlib input/output and declared counts are checked here.
func inspectChangesPack(ctx context.Context, path string, format string, l ChangesLimits) error {
	f, err := os.Open(path)
	if err != nil {
		return changesErr("missing_source", "cannot reopen bundle")
	}
	defer f.Close()
	r := bufio.NewReader(changesContextReader{ctx, f})
	var headerBytes int64
	for {
		line, e := r.ReadString('\n')
		headerBytes += int64(len(line))
		if headerBytes > l.HeaderBytes {
			return changesLimit("header_bytes")
		}
		if e != nil {
			return changesErr("invalid_objects", "missing pack body")
		}
		if line == "\n" {
			break
		}
	}
	header := make([]byte, 12)
	if _, err = io.ReadFull(r, header); err != nil || string(header[:4]) != "PACK" {
		return changesErr("invalid_objects", "invalid pack header")
	}
	version := binary.BigEndian.Uint32(header[4:8])
	if version != 2 && version != 3 {
		return changesErr("invalid_objects", "unsupported pack version")
	}
	count := int64(binary.BigEndian.Uint32(header[8:]))
	if count > int64(l.Objects) {
		return changesLimit("objects")
	}
	var total int64
	for i := int64(0); i < count; i++ {
		b, e := r.ReadByte()
		if e != nil {
			return changesErr("invalid_objects", "truncated pack entry")
		}
		kind := (b >> 4) & 7
		size := int64(b & 15)
		shift := uint(4)
		for b&128 != 0 {
			if shift > 60 {
				return changesErr("invalid_objects", "invalid packed size")
			}
			b, e = r.ReadByte()
			if e != nil {
				return changesErr("invalid_objects", "truncated packed size")
			}
			size |= int64(b&127) << shift
			shift += 7
		}
		if size < 0 || size > l.ObjectBytes {
			return changesLimit("object_bytes")
		}
		switch kind {
		case 1, 2, 3, 4:
		case 6:
			for n := 0; ; n++ {
				b, e = r.ReadByte()
				if e != nil || n > 9 {
					return changesErr("invalid_objects", "invalid delta offset")
				}
				if b&128 == 0 {
					break
				}
			}
		case 7:
			n := 40
			if format == "sha256" {
				n = 64
			}
			if _, e = io.CopyN(io.Discard, r, int64(n/2)); e != nil {
				return changesErr("invalid_objects", "truncated delta base")
			}
		default:
			return changesErr("invalid_objects", "invalid packed object type")
		}
		z, e := zlib.NewReader(r)
		if e != nil {
			return changesErr("invalid_objects", "invalid packed compression")
		}
		inflated := bufio.NewReader(z)
		resultSize := size
		var consumed int64
		if kind == 6 || kind == 7 {
			_, n, e := changesDeltaSize(inflated)
			consumed += n
			if e != nil {
				z.Close()
				return e
			}
			var n2 int64
			resultSize, n2, e = changesDeltaSize(inflated)
			consumed += n2
			if e != nil {
				z.Close()
				return e
			}
			if resultSize > l.ObjectBytes {
				z.Close()
				return changesLimit("object_bytes")
			}
		}
		if resultSize > l.InflatedBytes-total {
			z.Close()
			return changesLimit("inflated_bytes")
		}
		total += resultSize
		n, e := io.Copy(io.Discard, io.LimitReader(inflated, l.ObjectBytes+1))
		consumed += n
		ce := z.Close()
		if consumed > l.ObjectBytes {
			return changesLimit("object_bytes")
		}
		if e != nil || ce != nil || consumed != size {
			return changesErr("invalid_objects", "packed object size or compression mismatch")
		}
	}
	checksum := 20
	if format == "sha256" {
		checksum = 32
	}
	if _, err = io.CopyN(io.Discard, r, int64(checksum)); err != nil {
		return changesErr("invalid_objects", "truncated pack checksum")
	}
	if _, err = r.ReadByte(); err != io.EOF {
		return changesErr("invalid_objects", "unexpected pack suffix")
	}
	return nil
}

func changesDeltaSize(r io.ByteReader) (int64, int64, error) {
	var value int64
	for shift := uint(0); shift < 63; shift += 7 {
		b, e := r.ReadByte()
		if e != nil {
			return 0, 0, changesErr("invalid_objects", "truncated delta size")
		}
		value |= int64(b&127) << shift
		if b&128 == 0 {
			if value < 0 {
				return 0, 0, changesErr("invalid_objects", "invalid delta size")
			}
			return value, int64(shift/7 + 1), nil
		}
	}
	return 0, 0, changesErr("invalid_objects", "oversized delta size")
}
