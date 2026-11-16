package jobinspect

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChangesJ28TreeEncoding(t *testing.T) {
	for _, size := range []int{20, 32} {
		entry := func(mode, name string) []byte {
			return append([]byte(mode+" "+name+"\x00"), bytes.Repeat([]byte{1}, size)...)
		}
		cases := map[string][]byte{
			"duplicate":                append(entry("100644", "a"), entry("100755", "a")...),
			"separated duplicate":      append(append(entry("100644", "a"), entry("100644", "a.c")...), entry("40000", "a")...),
			"wrong directory ordering": append(entry("40000", "a"), entry("100644", "a.c")...),
			"empty name":               entry("100644", ""),
			"slash":                    entry("100644", "a/b"),
			"dot":                      entry("100644", "."),
			"dotdot":                   entry("40000", ".."),
			"dotgit":                   entry("100644", ".git"),
			"dotgit case":              entry("160000", ".GiT"),
			"null hash":                append([]byte("100644 a\x00"), make([]byte, size)...),
			"null gitlink":             append([]byte("160000 a\x00"), make([]byte, size)...),
			"missing mode separator":   []byte("100644"),
			"missing name terminator":  []byte("100644 a"),
			"truncated object":         append([]byte("100644 a\x00"), bytes.Repeat([]byte{1}, size-1)...),
			"zero padded mode":         entry("040000", "a"),
			"invalid file mode":        entry("100664", "a"),
			"invalid object mode":      entry("170000", "a"),
			"trailing bytes":           append(entry("100644", "a"), 'x'),
		}
		for name, b := range cases {
			t.Run(map[int]string{20: "sha1", 32: "sha256"}[size]+"/"+name, func(t *testing.T) {
				objects, children, e := decodeChangesTree(b, size)
				require.Error(t, e)
				require.Equal(t, "invalid_objects", ChangesDiagnostic(e).Code)
				require.Nil(t, objects)
				require.Nil(t, children)
			})
		}
		valid := append(entry("100644", "a.c"), entry("40000", "a")...)
		valid = append(valid, entry("100755", "line\n\t")...)
		valid = append(valid, entry("160000", "opaque")...)
		valid = append(valid, entry("120000", "unicode-μ")...)
		objects, children, e := decodeChangesTree(valid, size)
		require.NoError(t, e)
		require.Len(t, objects, 4)
		require.Len(t, children, 1)
		require.Equal(t, "tree", objects[1].kind)
		require.Len(t, objects[0].hash, size*2)
		objects, children, e = decodeChangesTree(nil, size)
		require.NoError(t, e)
		require.Empty(t, objects)
		require.Empty(t, children)
	}
	_, _, e := decodeChangesTree(nil, 21)
	require.Equal(t, "invalid_objects", ChangesDiagnostic(e).Code)
}
