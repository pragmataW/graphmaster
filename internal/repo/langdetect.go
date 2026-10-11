package repo

import (
	"bytes"
	"path"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// shebangLangs maps interpreter names found in a "#!" line to a language.
var shebangLangs = map[string]model.Lang{
	"node": model.LangJavaScript, "nodejs": model.LangJavaScript,
	"php": model.LangPHP,
}

// DetectLang returns the language of a file. The extension decides when
// present (see model.LangFromPath); extensionless files fall back to the
// interpreter in a shebang line taken from head (the first bytes of the
// file, may be nil).
func DetectLang(p string, head []byte) model.Lang {
	if l := model.LangFromPath(p); l != model.LangUnknown {
		return l
	}
	base := path.Base(p)
	if strings.LastIndexByte(base, '.') > 0 || !bytes.HasPrefix(head, []byte("#!")) {
		return model.LangUnknown
	}
	line := head[2:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return model.LangUnknown
	}
	interp := path.Base(fields[0])
	if interp == "env" {
		// Skip env options such as -S.
		interp = ""
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") {
				interp = path.Base(f)
				break
			}
		}
	}
	if strings.HasPrefix(interp, "python") { // python3.12 and friends
		return model.LangPython
	}
	return shebangLangs[interp]
}
