package kernels

import (
	"bytes"
	"debug/elf"
	"log"

	"github.com/sarchlab/mgpusim/v3/insts"
)

// LoadProgram loads program
func LoadProgram(filePath, kernelName string) *insts.HsaCo {
	executable, err := elf.Open(filePath)
	if err != nil {
		log.Fatal(err)
	}

	symbols, err := executable.Symbols()
	if err != nil {
		log.Fatal(err)
	}

	textSection := executable.Section(".text")
	if textSection == nil {
		log.Fatal(".text section is not found")
	}

	textSectionData, err := textSection.Data()
	if err != nil {
		log.Fatal(err)
	}

	// An empty kernel name is for the case where the symbol is not generated.
	// Use the whole text section in this case.
	if kernelName == "" {
		hsaco := insts.NewHsaCoFromData(textSectionData)
		return hsaco
	}

	for _, symbol := range symbols {
		if symbol.Name == kernelName {
			offset, ok := symbolOffsetInSection(symbol, textSection, len(textSectionData))
			if !ok {
				log.Fatalf(
					"symbol %s range is outside .text section", kernelName)
			}
			hsacoData := textSectionData[offset : offset+symbol.Size]
			hsaco := insts.NewHsaCoFromData(hsacoData)
			hsaco.Symbol = &symbol

			//fmt.Println(hsaco.Info())

			return hsaco
		}
	}

	return nil
}

// LoadProgramFromMemory loads program
func LoadProgramFromMemory(data []byte, kernelName string) *insts.HsaCo {
	reader := bytes.NewReader(data)
	executable, err := elf.NewFile(reader)
	if err != nil {
		log.Fatal(err)
	}

	symbols, err := executable.Symbols()
	if err != nil {
		log.Fatal(err)
	}

	textSection := executable.Section(".text")
	if textSection == nil {
		log.Fatal(".text section is not found")
	}

	textSectionData, err := textSection.Data()
	if err != nil {
		log.Fatal(err)
	}

	// An empty kernel name is for the case where the symbol is not generated.
	// Use the whole text section in this case.
	if kernelName == "" {
		hsaco := insts.NewHsaCoFromData(textSectionData)
		return hsaco
	}

	for _, symbol := range symbols {
		if symbol.Name == kernelName {
			offset, ok := symbolOffsetInSection(symbol, textSection, len(textSectionData))
			if !ok {
				log.Fatalf(
					"symbol %s range is outside .text section", kernelName)
			}
			hsacoData := textSectionData[offset : offset+symbol.Size]
			hsaco := insts.NewHsaCoFromData(hsacoData)
			symbolCopy := symbol
			hsaco.Symbol = &symbolCopy

			//fmt.Println(hsaco.Info())

			return hsaco
		}
	}

	return nil
}

func symbolOffsetInSection(
	symbol elf.Symbol,
	section *elf.Section,
	sectionDataLen int,
) (uint64, bool) {
	candidates := make([]uint64, 0, 2)
	if symbol.Value >= section.Addr {
		candidates = append(candidates, symbol.Value-section.Addr)
	}
	if symbol.Value >= section.Offset {
		candidates = append(candidates, symbol.Value-section.Offset)
	}

	sectionLen := uint64(sectionDataLen)
	for _, offset := range candidates {
		if offset <= sectionLen && symbol.Size <= sectionLen-offset {
			return offset, true
		}
	}
	return 0, false
}
