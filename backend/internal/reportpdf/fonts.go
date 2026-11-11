package reportpdf

import (
	_ "embed"
	"sync"

	"github.com/tdewolff/canvas"
)

// Source Han Sans CN (SIL OFL 1.1, see fonts/OFL-SourceHanSans.txt) is embedded so the export looks the same on every server.

//go:embed fonts/SourceHanSansCN-Regular.otf
var regularOTF []byte

//go:embed fonts/SourceHanSansCN-Bold.otf
var boldOTF []byte

var (
	famOnce sync.Once
	fam     *canvas.FontFamily
	famErr  error
)

func family() (*canvas.FontFamily, error) {
	famOnce.Do(func() {
		f := canvas.NewFontFamily("han")
		if err := f.LoadFont(regularOTF, 0, canvas.FontRegular); err != nil {
			famErr = err
			return
		}
		if err := f.LoadFont(boldOTF, 0, canvas.FontBold); err != nil {
			famErr = err
			return
		}
		fam = f
	})
	return fam, famErr
}
