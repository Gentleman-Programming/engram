// Command windows-version-resource prepares metadata in a new disposable directory.
// It is deliberately not wired into Engram builds or release workflows.
package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

var stableTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type version struct {
	text  string
	parts [3]uint16
}

func parseTag(tag string) (version, error) {
	match := stableTag.FindStringSubmatch(tag)
	if match == nil {
		return version{}, fmt.Errorf("expected stable vMAJOR.MINOR.PATCH tag")
	}
	v := version{text: strings.TrimPrefix(tag, "v")}
	for i := range v.parts {
		n, err := strconv.ParseUint(match[i+1], 10, 16)
		if err != nil {
			return version{}, fmt.Errorf("version component exceeds uint16: %s", match[i+1])
		}
		v.parts[i] = uint16(n)
	}
	return v, nil
}

var little = binary.LittleEndian

func wide(s string) []byte {
	var b bytes.Buffer
	for _, c := range append(utf16.Encode([]rune(s)), 0) {
		_ = binary.Write(&b, little, c)
	}
	return b.Bytes()
}

// VERSIONINFO nodes and their values/children are DWORD aligned. Text value
// lengths count UTF-16 code units; binary value lengths count bytes.
func node(key string, typ uint16, value []byte, children ...[]byte) []byte {
	b := make([]byte, 6)
	b = append(b, wide(key)...)
	pad := func() {
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}
	pad()
	b = append(b, value...)
	for _, child := range children {
		pad()
		b = append(b, child...)
	}
	little.PutUint16(b, uint16(len(b)))
	size := len(value)
	if typ == 1 {
		size /= 2
	}
	little.PutUint16(b[2:], uint16(size))
	little.PutUint16(b[4:], typ)
	return b
}

func versionInfo(v version) []byte {
	pair := uint32(v.parts[0])<<16 | uint32(v.parts[1])
	tail := uint32(v.parts[2]) << 16 // fourth fixed component is always zero
	var fixed bytes.Buffer
	for _, n := range []uint32{0xfeef04bd, 0x10000, pair, tail, pair, tail, 0x3f, 0, 0x40004, 1, 0, 0, 0} {
		_ = binary.Write(&fixed, little, n)
	}
	var fields [][]byte
	for _, field := range [][2]string{{"ProductName", "engram"}, {"ProductVersion", v.text}, {"FileVersion", v.text}, {"OriginalFilename", "engram.exe"}} {
		fields = append(fields, node(field[0], 1, wide(field[1])))
	}
	strings := node("StringFileInfo", 1, nil, node("040904B0", 1, nil, fields...))
	// en-US (0409), UTF-16 (04B0); resource language itself is neutral.
	vars := node("VarFileInfo", 1, nil, node("Translation", 0, []byte{9, 4, 0xb0, 4}))
	return node("VS_VERSION_INFO", 0, fixed.Bytes(), strings, vars)
}

func machine(arch string) (uint16, error) {
	switch arch {
	case "amd64":
		return pe.IMAGE_FILE_MACHINE_AMD64, nil
	case "arm64":
		return pe.IMAGE_FILE_MACHINE_ARM64, nil
	}
	return 0, fmt.Errorf("unsupported architecture %q", arch)
}

// The only supported resource tree is RT_VERSION(16)/#1/neutral(0), with
// three single-entry directories and one data entry. Offsets are section-relative.
func resource(v version, rva uint32) []byte {
	info := versionInfo(v)
	b := make([]byte, 88)
	for depth, id := range []uint32{16, 1, 0} {
		off := depth * 24
		little.PutUint16(b[off+14:], 1)
		little.PutUint32(b[off+16:], id)
		next := uint32(off + 24)
		if depth < 2 {
			next |= 0x80000000
		}
		little.PutUint32(b[off+20:], next)
	}
	little.PutUint32(b[72:], rva+88)
	little.PutUint32(b[76:], uint32(len(info)))
	return append(b, info...)
}

// Emit one COFF section, one section symbol and one ADDR32NB relocation for
// IMAGE_RESOURCE_DATA_ENTRY.OffsetToData. No executable code or external tool.
func object(v version, arch string) ([]byte, error) {
	m, err := machine(arch)
	if err != nil {
		return nil, err
	}
	raw := resource(v, 0)
	reloc := uint16(3) // IMAGE_REL_AMD64_ADDR32NB
	if arch == "arm64" {
		reloc = 2
	} // IMAGE_REL_ARM64_ADDR32NB
	var b bytes.Buffer
	header := pe.FileHeader{Machine: m, NumberOfSections: 1, PointerToSymbolTable: uint32(60 + len(raw) + 10), NumberOfSymbols: 1}
	_ = binary.Write(&b, little, header)
	section := pe.SectionHeader32{SizeOfRawData: uint32(len(raw)), PointerToRawData: 60, PointerToRelocations: uint32(60 + len(raw)), NumberOfRelocations: 1, Characteristics: 0x40000040}
	copy(section.Name[:], ".rsrc")
	_ = binary.Write(&b, little, section)
	b.Write(raw)
	_ = binary.Write(&b, little, pe.Reloc{VirtualAddress: 72, SymbolTableIndex: 0, Type: reloc})
	symbol := pe.COFFSymbol{SectionNumber: 1, StorageClass: 3}
	copy(symbol.Name[:], ".rsrc")
	_ = binary.Write(&b, little, symbol)
	_ = binary.Write(&b, little, uint32(4)) // empty COFF string table, including length
	return b.Bytes(), nil
}

func generate(v version, out string) error {
	if out == "" {
		return fmt.Errorf("-out must name a new disposable directory")
	}
	// Refuse existing directories (including symlinks) rather than overwrite stale
	// resources or accidentally modify cmd/engram. Parent must already exist.
	if err := os.Mkdir(out, 0o700); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		data, err := object(v, arch)
		if err != nil {
			return err
		}
		path := filepath.Join(out, "rsrc_windows_"+arch+".syso")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// verify accepts only this helper's canonical metadata shape in a linked PE32+
// executable. It is not general PE validation, signature or trust verification.
func verify(v version, arch, path string) error {
	m, err := machine(arch)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only file; read errors are returned below
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 128<<20 {
		return fmt.Errorf("expected regular PE file no larger than 128 MiB")
	}
	// Cap input size before parsing; debug/pe is not a hardened untrusted-file parser.
	data, err := io.ReadAll(io.LimitReader(f, (128<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 128<<20 {
		return fmt.Errorf("PE file grew beyond 128 MiB")
	}
	if len(data) < 64 || !bytes.Equal(data[:2], []byte("MZ")) {
		return fmt.Errorf("missing DOS header")
	}
	image, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("read PE: %w", err)
	}
	// NewFile wraps a bytes.Reader; there is no underlying file to close.
	if image.Machine != m || image.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 {
		return fmt.Errorf("wrong machine or not an executable image")
	}
	optional, ok := image.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || optional.NumberOfRvaAndSizes < 3 {
		return fmt.Errorf("expected PE32+ resource directory")
	}
	directory := optional.DataDirectory[2]
	var section *pe.Section
	for _, s := range image.Sections {
		if s.Name == ".rsrc" {
			if section != nil {
				return fmt.Errorf("duplicate resource section")
			}
			section = s
		}
	}
	if section == nil || section.VirtualAddress != directory.VirtualAddress {
		return fmt.Errorf("missing or noncanonical resource directory")
	}
	expected := resource(v, section.VirtualAddress)
	endRVA := uint64(section.VirtualAddress) + uint64(len(expected))
	if directory.Size > section.VirtualSize || endRVA > uint64(optional.SizeOfImage) {
		return fmt.Errorf("resource exceeds virtual section or image extent")
	}
	if section.VirtualAddress > ^uint32(0)-uint32(len(expected)) || directory.Size != uint32(len(expected)) || section.Size < directory.Size || uint64(section.Offset)+uint64(section.Size) > uint64(len(data)) {
		return fmt.Errorf("resource extent mismatch")
	}
	actual := data[section.Offset : uint64(section.Offset)+uint64(section.Size)]
	if !bytes.Equal(actual[:len(expected)], expected) {
		return fmt.Errorf("metadata differs from canonical product/version/resource shape")
	}
	for _, b := range actual[len(expected):] {
		if b != 0 {
			return fmt.Errorf("unexpected trailing resource data")
		}
	}
	return nil
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "generate" && args[0] != "verify") {
		_, _ = fmt.Fprintln(stderr, "usage: windows-version-resource generate|verify -tag vMAJOR.MINOR.PATCH [options]")
		return 2
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	tag := flags.String("tag", "", "explicit stable vMAJOR.MINOR.PATCH tag")
	var out, arch, file string
	if args[0] == "generate" {
		flags.StringVar(&out, "out", "", "new disposable directory (must not exist)")
	} else {
		flags.StringVar(&arch, "arch", "", "amd64 or arm64")
		flags.StringVar(&file, "file", "", "linked PE file to read without executing")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	v, err := parseTag(*tag)
	if err == nil {
		if args[0] == "generate" {
			err = generate(v, out)
		} else {
			err = verify(v, arch, file)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "metadata", args[0], "OK:", v.text)
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
