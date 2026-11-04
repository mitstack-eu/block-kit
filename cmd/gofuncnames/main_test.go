package main

// gofuncnames lists every function a Go binary's name table holds, one inlined at every call included, and refuses a table it cannot read.

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"slices"
	"strings"
	"testing"
)

// pkg is the path a test binary names this package's functions under.
const pkg = "github.com/mitstack-eu/block-kit/cmd/gofuncnames"

func double(n int) int { return n * 2 }

func TestFuncNames_ListsThisBinarysFunctionsAndAnInlinedOne(t *testing.T) {
	if n := len(t.Name()); double(n) != n+n {
		t.Fatal("double is wrong")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	names, err := funcNames(f)

	if err != nil {
		t.Fatalf("funcNames(%s): %v", exe, err)
	}
	for _, want := range []string{"testing.tRunner", pkg + ".namesIn", pkg + ".double"} {
		if !slices.Contains(names, want) {
			t.Errorf("the name table of %s lacks %q among its %d names", exe, want, len(names))
		}
	}
}

func TestNamesIn_RefusesATableItCannotRead(t *testing.T) {
	header := func(magic uint32, start, end uint64) []byte {
		tab := make([]byte, 8+8*8+16)
		binary.LittleEndian.PutUint32(tab, magic)
		tab[7] = 8
		binary.LittleEndian.PutUint64(tab[8+3*8:], start)
		binary.LittleEndian.PutUint64(tab[8+4*8:], end)
		return tab
	}
	cases := []struct {
		name, want string
		tab        []byte
	}{
		{"a table shorter than its header", "shorter than its header", []byte{0xf1, 0xff}},
		{"a table from before Go 1.18", "is not Go 1.18 or later", header(0xfffffffa, 72, 80)},
		{"a name table running past the end", "outside a pclntab", header(0xfffffff1, 72, 4096)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := namesIn(c.tab)

			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("namesIn error = %v; want one naming %q", err, c.want)
			}
		})
	}
}
