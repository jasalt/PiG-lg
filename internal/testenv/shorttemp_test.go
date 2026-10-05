package testenv

import (
	"strings"
	"testing"
)

func TestShortTempBase(t *testing.T) {
	const localAppData = `C:\Users\runner\AppData\Local`
	longTemp := `D:\a\_temp\` + strings.Repeat("d", maxShortTempDirBytes)
	for _, test := range []struct {
		name, goos, temp, localAppData, want string
	}{
		{"darwin falls back from a long temp directory", "darwin", "/var/folders/zz/" + strings.Repeat("d", 80), localAppData, "/tmp"},
		{"linux keeps a short disk-backed temp directory", "linux", "/var/tmp/p.123456", "", "/var/tmp/p.123456"},
		{"darwin keeps a short temp directory", "darwin", "/var/tmp/p.123456", "", "/var/tmp/p.123456"},
		{"linux keeps a temp directory that just fits", "linux", "/" + strings.Repeat("d", maxShortTempDirBytes-len("/")-len("/pe")-10), "", "/" + strings.Repeat("d", maxShortTempDirBytes-len("/")-len("/pe")-10)},
		{"linux falls back when the temp directory is one byte too long", "linux", "/" + strings.Repeat("d", maxShortTempDirBytes-len("/")-len("/pe")-10+1), "", "/tmp"},
		{"darwin falls back before exceeding its smaller socket path", "darwin", "/" + strings.Repeat("d", maxShortTempDirBytes-len("/")-len("/pe")-10), "", "/tmp"},
		{"windows keeps a short temp directory", "windows", `C:\T`, localAppData, `C:\T`},
		{"windows keeps a temp directory that just fits", "windows", `D:\` + strings.Repeat("d", maxShortTempDirBytes-len(`D:\`)-len(`\pe`)-10), localAppData, `D:\` + strings.Repeat("d", maxShortTempDirBytes-len(`D:\`)-len(`\pe`)-10)},
		{"windows falls back when the temp directory is one byte too long", "windows", `D:\` + strings.Repeat("d", maxShortTempDirBytes-len(`D:\`)-len(`\pe`)-10+1), localAppData, localAppData + `\pig\s`},
		{"windows falls back from a long temp directory", "windows", longTemp, localAppData, localAppData + `\pig\s`},
		{"windows without local app data keeps the long temp directory", "windows", longTemp, "", longTemp},
	} {
		if got := shortTempBase(test.goos, test.temp, test.localAppData, "pe"); strings.ReplaceAll(got, "/", `\`) != strings.ReplaceAll(test.want, "/", `\`) {
			t.Errorf("%s: shortTempBase = %q, want %q", test.name, got, test.want)
		}
	}
}
