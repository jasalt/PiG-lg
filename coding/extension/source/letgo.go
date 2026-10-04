package source

import (
	"path/filepath"
	"strings"
)

// LetGoEntryNames are the conventional let-go directory entries. A directory
// holding both is ambiguous.
var LetGoEntryNames = []string{"extension.lg", "extension.cljc"}

// IsLetGoFile reports whether name is an exact let-go source form.
// pig additive (D89): .lg and portable .cljc are let-go entries; .clj is not.
func IsLetGoFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".lg", ".cljc":
		return true
	}
	return false
}

// HasLetGoEntry reports whether dir holds a conventional let-go entry.
func HasLetGoEntry(dir string) bool {
	for _, name := range LetGoEntryNames {
		if exists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}
