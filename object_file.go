package repomofo

import (
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// fileObjectStore stores git objects on the filesystem as loose objects
// and pack files, implementing the standard .git/objects layout.
type fileObjectStore struct {
	repoPath string
	opts     RepoOpts
}

func newFileObjectStore(repoPath string, opts RepoOpts) *fileObjectStore {
	return &fileObjectStore{repoPath: repoPath, opts: opts}
}

func (s *fileObjectStore) WriteObject(header ObjectHeader, reader io.Reader) ([]byte, error) {
	headerStr := fmt.Sprintf("%s %d\x00", header.Kind.Name(), header.Size)

	tempFile, err := os.CreateTemp(s.repoPath, "object.temp.*")
	if err != nil {
		return nil, err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	defer tempFile.Close()

	hasher := s.opts.Hash.NewHasher()
	hasher.Write([]byte(headerStr))
	if _, err := tempFile.Write([]byte(headerStr)); err != nil {
		return nil, err
	}

	w := io.MultiWriter(tempFile, hasher)
	if _, err := io.Copy(w, reader); err != nil {
		return nil, err
	}

	oidBytes := hasher.Sum(nil)
	oidHex := hex.EncodeToString(oidBytes)

	objDir := filepath.Join(s.repoPath, "objects", oidHex[:2])
	objPath := filepath.Join(objDir, oidHex[2:])
	if _, err := os.Stat(objPath); err == nil {
		return oidBytes, nil
	}

	if err := os.MkdirAll(objDir, 0755); err != nil {
		return nil, err
	}

	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	lock, err := newLockFile(objDir, oidHex[2:])
	if err != nil {
		return nil, err
	}
	defer lock.Close()

	zlibW := zlib.NewWriter(lock.File)
	if _, err := io.Copy(zlibW, tempFile); err != nil {
		return nil, err
	}
	if err := zlibW.Close(); err != nil {
		return nil, err
	}

	lock.Success = true
	return oidBytes, nil
}

func (s *fileObjectStore) ReadObject(oidHex string) (ObjectReader, error) {
	loose, err := s.openLooseObject(oidHex)
	if err == nil {
		return loose, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	pack, err := newPackObjectReaderFromIndex(s, s.repoPath, s.opts.Hash, s.opts.bufferSize(), oidHex)
	if err != nil {
		return nil, err
	}
	return pack, nil
}

func (s *fileObjectStore) openLooseObject(oidHex string) (*looseObjectReader, error) {
	objPath := filepath.Join(s.repoPath, "objects", oidHex[:2], oidHex[2:])
	f, err := os.Open(objPath)
	if err != nil {
		return nil, err
	}
	zlibR, err := zlib.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	header, err := parseObjectHeaderFromReader(zlibR)
	if err != nil {
		zlibR.Close()
		f.Close()
		return nil, err
	}
	return &looseObjectReader{file: f, zlibReader: zlibR, header: header}, nil
}

// CopyFromPackIterator writes pack objects as loose objects.
func (s *fileObjectStore) CopyFromPackIterator(iter *PackIterator) error {
	offsetToOID := make(map[uint64][]byte)

	for {
		por, err := iter.Next(s, offsetToOID)
		if err != nil {
			return err
		}
		if por == nil {
			break
		}

		startPos := iter.StartPosition()
		header := por.Header()

		oidBytes, err := s.WriteObject(header, por)
		por.Close()
		if err != nil {
			return err
		}

		offsetToOID[startPos] = oidBytes
	}
	return nil
}

// ---------------------------------------------------------------------------
// looseObjectReader
// ---------------------------------------------------------------------------

type looseObjectReader struct {
	file       *os.File
	zlibReader io.ReadCloser
	header     ObjectHeader
}

func (r *looseObjectReader) Close() {
	if r.zlibReader != nil {
		r.zlibReader.Close()
	}
	if r.file != nil {
		r.file.Close()
	}
}

func (r *looseObjectReader) Reset() error {
	if r.zlibReader != nil {
		r.zlibReader.Close()
	}
	if _, err := r.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	zlibR, err := zlib.NewReader(r.file)
	if err != nil {
		return err
	}
	r.zlibReader = zlibR
	// skip header bytes (up to and including null)
	for {
		var buf [1]byte
		if _, err := io.ReadFull(r.zlibReader, buf[:]); err != nil {
			return err
		}
		if buf[0] == 0 {
			break
		}
	}
	return nil
}

func (r *looseObjectReader) Read(p []byte) (int, error) {
	return r.zlibReader.Read(p)
}

func (r *looseObjectReader) Header() ObjectHeader {
	return r.header
}

func (r *looseObjectReader) SkipBytes(n uint64) error {
	var buf [512]byte
	rem := n
	for rem > 0 {
		toRead := rem
		if toRead > uint64(len(buf)) {
			toRead = uint64(len(buf))
		}
		nr, err := r.Read(buf[:toRead])
		if nr == 0 && err != nil {
			return err
		}
		rem -= uint64(nr)
	}
	return nil
}

func (r *looseObjectReader) Position() uint64 {
	return 0
}
