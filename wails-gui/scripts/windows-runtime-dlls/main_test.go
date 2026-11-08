package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"testing"
)

// A minimal PE with one import plus an unrelated copy of the same string.
// Staging must change only the import, preserving code/data and PE layout.
func importFixture(t *testing.T) []byte {
	t.Helper()
	buffer := bytes.NewBuffer(make([]byte, 128))
	buffer.Bytes()[0], buffer.Bytes()[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(buffer.Bytes()[60:], 128)
	buffer.WriteString("PE\x00\x00")
	header := pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, NumberOfSections: 1, SizeOfOptionalHeader: 240}
	optional := pe.OptionalHeader64{Magic: 0x20b, NumberOfRvaAndSizes: 16}
	optional.DataDirectory[1] = pe.DataDirectory{VirtualAddress: 0x1000, Size: 40}
	section := pe.SectionHeader32{VirtualAddress: 0x1000, VirtualSize: 512, SizeOfRawData: 512, PointerToRawData: 512}
	copy(section.Name[:], ".idata")
	for _, value := range []any{header, optional, section} {
		if err := binary.Write(buffer, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	data := make([]byte, 1024)
	copy(data, buffer.Bytes())
	binary.LittleEndian.PutUint32(data[512+12:], 0x1028)
	copy(data[552:], "onnxruntime.dll\x00")
	copy(data[600:], "onnxruntime.dll\x00")
	return data
}

func TestRenameRuntimeImportPreservesUnrelatedData(t *testing.T) {
	data := importFixture(t)
	expected := bytes.Clone(data)
	copy(expected[552:], runtimeDLL)
	if err := renameRuntimeImport(data, true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, expected) {
		t.Fatal("staging changed bytes outside the ONNX import name")
	}
}

func TestRenameRuntimeImportRejectsUnsafeInputs(t *testing.T) {
	for _, name := range []string{"not-pe", "signed", "invalid-rva", "missing-import"} {
		t.Run(name, func(t *testing.T) {
			data := importFixture(t)
			switch name {
			case "not-pe":
				data = []byte("not a PE image")
			case "signed":
				// OptionalHeader64's certificate-table data-directory size.
				binary.LittleEndian.PutUint32(data[128+24+112+4*8+4:], 1)
			case "invalid-rva":
				binary.LittleEndian.PutUint32(data[512+12:], 0xfffffff0)
			case "missing-import":
				copy(data[552:], "kernel32.dll\x00\x00\x00\x00")
			}
			original := bytes.Clone(data)
			if err := renameRuntimeImport(data, true); err == nil {
				t.Fatal("unsafe image was accepted")
			}
			if !bytes.Equal(data, original) {
				t.Fatal("rejected image was modified")
			}
		})
	}
	data := importFixture(t)
	copy(data[552:], "kernel32.dll\x00\x00\x00\x00")
	if err := renameRuntimeImport(data, false); err != nil {
		t.Fatalf("DLLs without a direct ONNX dependency must remain usable: %v", err)
	}
}
