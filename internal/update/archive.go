package update

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// archiveFiles is everything a release archive may hold; the binary is required.
var archiveFiles = map[string]bool{"todobem": true, "LICENSE": true, "LICENSING.md": true, "OFL.txt": true, "README.md": true}

const maxBinary = 256 << 20

// ErrChecksum is an archive whose sha256 is not the one its release lists.
var ErrChecksum = errors.New("checksum mismatch")

// CheckSum verifies the file at path against the sha256 listed for name.
func CheckSum(path, name string, sums map[string]string) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%s does not list %s", SumsName, name)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, MaxArchive+1)); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: %w (got %s, %s lists %s)", name, ErrChecksum, got[:12], SumsName, want[:12])
	}
	return nil
}

// ExtractBinary writes the archive's todobem to dst (mode 0755). The archive must hold only
// the known files, each a regular file at the top level, none twice.
func ExtractBinary(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(io.LimitReader(f, MaxArchive))
	if err != nil {
		return fmt.Errorf("%s: not a gzip archive", archive)
	}
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}
		if !archiveFiles[h.Name] || seen[h.Name] {
			return fmt.Errorf("%s: unexpected entry %q", archive, h.Name)
		}
		seen[h.Name] = true
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s: %q is not a regular file", archive, h.Name)
		}
		if h.Name != "todobem" {
			continue
		}
		if h.Size > maxBinary {
			return fmt.Errorf("%s: the binary is larger than %d bytes", archive, maxBinary)
		}
		if err := writeExecutable(dst, io.LimitReader(tr, maxBinary)); err != nil {
			return err
		}
		found = true
	}
	if !found {
		os.Remove(dst)
		return fmt.Errorf("%s: no todobem binary inside", archive)
	}
	return nil
}

func writeExecutable(dst string, r io.Reader) error {
	os.Remove(dst)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, 0755)
}
