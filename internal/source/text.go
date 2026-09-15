package source

import (
	"os"
	"strings"

	"github.com/extractumio/todobem/internal/model"
)

// Clip cuts s to n bytes on a rune boundary, marking the cut with an ellipsis.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// FirstLine returns s up to its first newline.
func FirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// OrDefault returns s, or d when s is empty.
func OrDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ReadSpan returns the exact source line an op or marker points at.
func ReadSpan(src model.Src) ([]byte, error) {
	f, err := os.Open(src.File)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, src.Len)
	n, err := f.ReadAt(buf, src.Off)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}
