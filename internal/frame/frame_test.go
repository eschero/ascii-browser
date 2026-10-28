package frame

import (
	"bytes"
	"testing"
)

func TestDumpFormat(t *testing.T) {
	f := New(5, 3)
	f.Set(0, 0, 'a')
	f.Set(4, 2, 'z')
	f.Set(2, 1, 0x07) // not printable: must become '?'
	f.Set(9, 9, 'x')  // outside: ignored
	var buf bytes.Buffer
	n, err := f.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := "a    \n  ?  \n    z\n"
	if buf.String() != want {
		t.Fatalf("dump = %q, want %q", buf.String(), want)
	}
	if n != int64(len(want)) {
		t.Fatalf("n = %d, want %d", n, len(want))
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(buf.Bytes(), []byte("\n")), []byte("\n")) {
		if len(line) != 5 {
			t.Fatalf("line %q has %d bytes, want 5", line, len(line))
		}
	}
}

func TestDiff(t *testing.T) {
	a := New(3, 2)
	b := a.Clone()
	if len(a.Diff(b)) != 0 || !a.Equal(b) {
		t.Fatal("identical frames must not differ")
	}
	b.Set(1, 1, '#')
	d := b.Diff(a)
	if len(d) != 1 || d[0] != (Change{Col: 1, Row: 1, C: '#'}) {
		t.Fatalf("diff = %+v", d)
	}
	if len(b.Diff(nil)) != 6 {
		t.Fatal("diff against nil must list every cell")
	}
}
