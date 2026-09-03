package run

import (
	"fmt"
	"os"
)

type EvidenceFile struct {
	Name string
	Size int64
}

func EvidenceFiles(directory string) ([]EvidenceFile, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("list evidence: %w", err)
	}
	var files []EvidenceFile
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect evidence %q: %w", entry.Name(), err)
		}
		if info.Mode().IsRegular() {
			files = append(files, EvidenceFile{Name: entry.Name(), Size: info.Size()})
		}
	}
	return files, nil
}

func EvidenceSize(size int64) string {
	if size == 0 {
		return "empty"
	}
	return fmt.Sprintf("%d bytes", size)
}
