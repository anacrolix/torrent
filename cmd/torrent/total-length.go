package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func totalLength(path string) (totalLength int64, err error) {
	err = filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		totalLength += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walking path, %w", err)
	}
	return totalLength, nil
}

func mutiFileTotalLength(filePaths ...string) (totalLength int64, err error) {
	for _, path := range filePaths {
		err = filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}
			totalLength += info.Size()
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("walking path, %w", err)
		}
	}
	return totalLength, nil
}
