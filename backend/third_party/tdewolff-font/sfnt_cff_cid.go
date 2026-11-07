package font

import (
	"fmt"

	"github.com/tdewolff/parse/v2"
)

// blankUnusedSubrs keeps every subroutine index in place but replaces subroutines that no glyph uses with a
// bare return, per Font DICT for local subroutines. Indices and biases stay valid, so charstrings need no rewrite.
func (cff *cffTable) blankUnusedSubrs() error {
	numGlyphs := uint16(cff.charStrings.Len())
	nFD := len(cff.fonts.localSubrs)
	localUsed := make([]map[int32]bool, nFD)
	for i := range localUsed {
		localUsed[i] = map[int32]bool{}
	}
	globalUsed := map[int32]bool{}
	for glyphID := uint16(0); glyphID < numGlyphs; glyphID++ {
		fd, ok := cff.fonts.Index(uint32(glyphID))
		if !ok || int(fd) >= nFD {
			return fmt.Errorf("CFF: bad FDSelect for glyph %v", glyphID)
		}
		err := cff.parseCharString(glyphID, func(_ *parse.BinaryReader, b0 int32, stack []int32) error {
			if b0 != cffCallsubr && b0 != cffCallgsubr {
				return nil
			} else if len(stack) == 0 {
				return ErrBadNumOperands
			}
			index, _, err := cff.getSubroutine(glyphID, b0, stack[len(stack)-1])
			if err != nil {
				return err
			}
			if b0 == cffCallsubr {
				localUsed[fd][index] = true
			} else {
				globalUsed[index] = true
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	blank := func(in *cffINDEX, used map[int32]bool) *cffINDEX {
		if in == nil {
			return nil
		}
		out := &cffINDEX{}
		for i := 0; i < in.Len(); i++ {
			if used[int32(i)] {
				out.Add(in.Get(uint16(i)))
			} else {
				out.Add([]byte{0x0B}) // return
			}
		}
		return out
	}
	fonts := *cff.fonts
	fonts.localSubrs = make([]*cffINDEX, nFD)
	for i := range fonts.localSubrs {
		fonts.localSubrs[i] = blank(cff.fonts.localSubrs[i], localUsed[i])
	}
	cff.fonts = &fonts
	cff.globalSubrs = blank(cff.globalSubrs, globalUsed)
	return nil
}

// writeCID writes a CID-keyed CFF with one Private DICT and Local Subrs INDEX per Font DICT. The charset is the
// identity mapping (CID equals glyph ID), which is what PDF writers expect after subsetting.
func (cff *cffTable) writeCID() ([]byte, error) {
	if cff.version != 1 {
		return nil, fmt.Errorf("unsupported version: %d", cff.version)
	}
	numGlyphs := cff.charStrings.Len()
	nFD := len(cff.fonts.private)
	if nFD == 0 || 255 < nFD || len(cff.fonts.localSubrs) != nFD {
		return nil, fmt.Errorf("CID font: bad number of Font DICTs")
	}
	// fixed-size integers keep every offset calculation single-pass
	int5 := func(w *parse.BinaryWriter, v int) {
		w.WriteUint8(29)
		w.WriteUint32(uint32(v))
	}

	nameIdx := &cffINDEX{}
	nameIdx.Add([]byte(cff.name))
	nameINDEX, err := nameIdx.Write()
	if err != nil {
		return nil, fmt.Errorf("Name INDEX: %v", err)
	}

	strs := &cffINDEX{}
	topBase, err := cff.top.Write(strs)
	if err != nil {
		return nil, fmt.Errorf("Top DICT: %v", err)
	}
	reg := strs.AddSID([]byte("Adobe"))
	ord := strs.AddSID([]byte("Identity"))
	stringINDEX, err := strs.Write()
	if err != nil {
		return nil, fmt.Errorf("String INDEX: %v", err)
	}

	globalSubrs := cff.globalSubrs
	if globalSubrs == nil {
		globalSubrs = &cffINDEX{}
	}
	globalSubrsINDEX, err := globalSubrs.Write()
	if err != nil {
		return nil, fmt.Errorf("Global Subrs INDEX: %v", err)
	}
	charStringsINDEX, err := cff.charStrings.Write()
	if err != nil {
		return nil, fmt.Errorf("CharStrings INDEX: %v", err)
	}

	var charset []byte
	if 2 <= numGlyphs {
		charset = []byte{2, 0, 1, byte((numGlyphs - 2) >> 8), byte(numGlyphs - 2)}
	} else {
		charset = []byte{0}
	}
	fdSelect := make([]byte, 1+numGlyphs) // format 0
	for g := 0; g < numGlyphs; g++ {
		fd, ok := cff.fonts.Index(uint32(g))
		if !ok || int(fd) >= nFD {
			return nil, fmt.Errorf("CID font: bad FDSelect for glyph %d", g)
		}
		fdSelect[1+g] = byte(fd)
	}

	privs := make([][]byte, nFD)
	subrs := make([][]byte, nFD)
	for i := 0; i < nFD; i++ {
		if cff.fonts.private[i] == nil {
			return nil, fmt.Errorf("CID font: missing Private DICT %d", i)
		}
		p, err := cff.fonts.private[i].Write()
		if err != nil {
			return nil, fmt.Errorf("Private DICT: %v", err)
		}
		if ls := cff.fonts.localSubrs[i]; ls != nil && 0 < ls.Len() {
			subrs[i], err = ls.Write()
			if err != nil {
				return nil, fmt.Errorf("Local Subrs INDEX: %v", err)
			}
			pw := parse.NewBinaryWriter(append([]byte{}, p...))
			int5(pw, len(p)+6) // Subrs offset is relative to the Private DICT and the INDEX follows it
			pw.WriteUint8(19)
			p = pw.Bytes()
		}
		privs[i] = p
	}

	buildTop := func(charsetOff, fdSelectOff, charStringsOff, fdArrayOff int) ([]byte, error) {
		w := parse.NewBinaryWriter([]byte{})
		writeDICTEntry(w, 256+30, reg, ord, 0) // ROS must come first
		w.WriteBytes(topBase)
		writeDICTEntry(w, 256+34, numGlyphs)
		int5(w, charsetOff)
		w.WriteUint8(15)
		int5(w, fdSelectOff)
		w.WriteUint8(12)
		w.WriteUint8(37)
		int5(w, charStringsOff)
		w.WriteUint8(17)
		int5(w, fdArrayOff)
		w.WriteUint8(12)
		w.WriteUint8(36)
		idx := &cffINDEX{}
		idx.Add(w.Bytes())
		return idx.Write()
	}
	buildFDs := func(offs []int) ([]byte, error) {
		idx := &cffINDEX{}
		for i := 0; i < nFD; i++ {
			w := parse.NewBinaryWriter([]byte{})
			int5(w, len(privs[i]))
			int5(w, offs[i])
			w.WriteUint8(18)
			idx.Add(w.Bytes())
		}
		return idx.Write()
	}

	header := []byte{1, 0, 4, 1}
	topProbe, err := buildTop(0, 0, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("Top DICT INDEX: %v", err)
	}
	fdProbe, err := buildFDs(make([]int, nFD))
	if err != nil {
		return nil, fmt.Errorf("Font DICT INDEX: %v", err)
	}
	charsetOff := len(header) + len(nameINDEX) + len(topProbe) + len(stringINDEX) + len(globalSubrsINDEX)
	fdSelectOff := charsetOff + len(charset)
	charStringsOff := fdSelectOff + len(fdSelect)
	fdArrayOff := charStringsOff + len(charStringsINDEX)
	offs := make([]int, nFD)
	next := fdArrayOff + len(fdProbe)
	for i := range offs {
		offs[i] = next
		next += len(privs[i]) + len(subrs[i])
	}
	topINDEX, err := buildTop(charsetOff, fdSelectOff, charStringsOff, fdArrayOff)
	if err != nil {
		return nil, fmt.Errorf("Top DICT INDEX: %v", err)
	}
	fdINDEX, err := buildFDs(offs)
	if err != nil {
		return nil, fmt.Errorf("Font DICT INDEX: %v", err)
	}
	if len(topINDEX) != len(topProbe) || len(fdINDEX) != len(fdProbe) {
		return nil, fmt.Errorf("CID font: unstable DICT sizes")
	}

	w := parse.NewBinaryWriter([]byte{})
	w.WriteBytes(header)
	w.WriteBytes(nameINDEX)
	w.WriteBytes(topINDEX)
	w.WriteBytes(stringINDEX)
	w.WriteBytes(globalSubrsINDEX)
	w.WriteBytes(charset)
	w.WriteBytes(fdSelect)
	w.WriteBytes(charStringsINDEX)
	w.WriteBytes(fdINDEX)
	for i := 0; i < nFD; i++ {
		w.WriteBytes(privs[i])
		w.WriteBytes(subrs[i])
	}
	return w.Bytes(), nil
}
