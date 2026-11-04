// Command gofuncnames prints every function name, inlined ones included, in the name table a Go ELF binary keeps for its stack traces and so keeps when stripped, or with -symtab whether the binary keeps its ELF symbol table.
package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	symtab := flag.Bool("symtab", false, "print present or absent for the binary's ELF symbol table instead of its function names")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: gofuncnames [-symtab] <binary>")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *symtab); err != nil {
		fmt.Fprintf(os.Stderr, "gofuncnames: %s: %v\n", flag.Arg(0), err)
		os.Exit(1)
	}
}

func run(path string, symtab bool) error {
	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if symtab {
		fmt.Println(map[bool]string{true: "present", false: "absent"}[f.Section(".symtab") != nil])
		return nil
	}
	names, err := funcNames(f)
	if err != nil {
		return err
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

func funcNames(f *elf.File) ([]string, error) {
	sec := f.Section(".gopclntab")
	if sec == nil {
		return nil, errors.New("no .gopclntab section")
	}
	tab, err := sec.Data()
	if err != nil {
		return nil, err
	}
	return namesIn(tab)
}

// namesIn reads the function name table out of a Go 1.18 or later pclntab, whose header gives its bounds as the funcnameOffset and cuOffset words.
func namesIn(tab []byte) ([]string, error) {
	if len(tab) < 8 {
		return nil, errors.New("pclntab shorter than its header")
	}
	if magic := binary.LittleEndian.Uint32(tab); magic != 0xfffffff0 && magic != 0xfffffff1 {
		return nil, fmt.Errorf("pclntab magic %#x is not Go 1.18 or later", magic)
	}
	ptr := int(tab[7])
	if ptr != 4 && ptr != 8 {
		return nil, fmt.Errorf("pclntab pointer size %d", ptr)
	}
	if len(tab) < 8+5*ptr {
		return nil, errors.New("pclntab shorter than its header")
	}
	word := func(i int) uint64 {
		if ptr == 4 {
			return uint64(binary.LittleEndian.Uint32(tab[8+i*ptr:]))
		}
		return binary.LittleEndian.Uint64(tab[8+i*ptr:])
	}
	start, end := word(3), word(4)
	if start == 0 || end <= start || end > uint64(len(tab)) {
		return nil, fmt.Errorf("function name table bounds %d..%d outside a pclntab of %d bytes", start, end, len(tab))
	}
	var names []string
	for _, n := range bytes.Split(tab[start:end], []byte{0}) {
		if len(n) > 0 {
			names = append(names, string(n))
		}
	}
	return names, nil
}
