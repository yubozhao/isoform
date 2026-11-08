// Stage the Windows speech runtime without colliding with the OS's
// onnxruntime.dll. PreferSystem32Images can override app-local DLL lookup.
package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const runtimeDLL = "isoform-ort.dll"

func renameRuntimeImport(data []byte, required bool) error {
	image, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer image.Close()
	var imports, signature pe.DataDirectory
	switch header := image.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		imports, signature = header.DataDirectory[1], header.DataDirectory[4]
	case *pe.OptionalHeader32:
		imports, signature = header.DataDirectory[1], header.DataDirectory[4]
	default:
		return fmt.Errorf("missing PE optional header")
	}
	if signature.Size != 0 {
		return fmt.Errorf("refusing to modify an Authenticode-signed DLL")
	}
	// Convert an import-directory RVA to a checked offset in the raw file.
	offset := func(rva uint32, size int) (int, error) {
		for _, section := range image.Sections {
			if rva >= section.VirtualAddress {
				delta := uint64(rva - section.VirtualAddress)
				start := uint64(section.Offset) + delta
				if delta+uint64(size) <= uint64(section.Size) && start+uint64(size) <= uint64(len(data)) {
					return int(start), nil
				}
			}
		}
		return 0, fmt.Errorf("invalid PE import RVA %#x", rva)
	}
	patched := false
	for n := uint64(0); n+20 <= uint64(imports.Size); n += 20 {
		rva := uint64(imports.VirtualAddress) + n
		if rva > uint64(^uint32(0)) {
			return fmt.Errorf("PE import RVA overflow")
		}
		descriptor, err := offset(uint32(rva), 20)
		if err != nil {
			return err
		}
		nameRVA := binary.LittleEndian.Uint32(data[descriptor+12 : descriptor+16])
		if nameRVA == 0 {
			break
		}
		name, err := offset(nameRVA, len("onnxruntime.dll")+1)
		if err != nil {
			return err
		}
		if strings.EqualFold(string(data[name:name+15]), "onnxruntime.dll") && data[name+15] == 0 {
			// Both names have the same length: no PE addresses or sections move.
			copy(data[name:name+15], runtimeDLL)
			patched = true
		}
	}
	if !patched && required {
		return fmt.Errorf("expected onnxruntime.dll import was not found")
	}
	return nil
}

func stage(source, destination string) error {
	files := make(map[string][]byte)
	for _, name := range []string{"onnxruntime.dll", "sherpa-onnx-c-api.dll", "sherpa-onnx-cxx-api.dll"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		if name == "onnxruntime.dll" {
			name = runtimeDLL
		} else if err := renameRuntimeImport(data, name == "sherpa-onnx-c-api.dll"); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files[name] = data
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	for name, data := range files {
		target := filepath.Join(destination, name)
		// Previous module-cache copies can carry the Windows read-only bit.
		if err := os.Chmod(target, 0o644); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	source := flag.String("source", "", "sherpa module's Windows lib directory")
	destination := flag.String("destination", "", "directory containing the Windows executable")
	flag.Parse()
	if *source == "" || *destination == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := stage(*source, *destination); err != nil {
		fmt.Fprintln(os.Stderr, "[windows-runtime-dlls]", err)
		os.Exit(1)
	}
}
