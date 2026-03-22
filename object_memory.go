package repomofo

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
)

// MemoryObjectStore is an in-memory implementation of ObjectStore.
// It stores git objects in a map keyed by hex OID.
type MemoryObjectStore struct {
	mu      sync.RWMutex
	objects map[string][]byte // oidHex → raw object data (header + content)
	hash    HashKind
}

func NewMemoryObjectStore(hash HashKind) *MemoryObjectStore {
	return &MemoryObjectStore{
		objects: make(map[string][]byte),
		hash:    hash,
	}
}

func (s *MemoryObjectStore) WriteObject(header ObjectHeader, reader io.Reader) ([]byte, error) {
	headerStr := fmt.Sprintf("%s %d\x00", header.Kind.Name(), header.Size)

	var buf bytes.Buffer
	buf.WriteString(headerStr)
	if _, err := io.Copy(&buf, reader); err != nil {
		return nil, err
	}
	data := buf.Bytes()

	hasher := s.hash.NewHasher()
	hasher.Write(data)
	oidBytes := hasher.Sum(nil)
	oidHex := hex.EncodeToString(oidBytes)

	s.mu.Lock()
	s.objects[oidHex] = data
	s.mu.Unlock()

	return oidBytes, nil
}

func (s *MemoryObjectStore) ReadObject(oidHex string) (ObjectReader, error) {
	s.mu.RLock()
	data, ok := s.objects[oidHex]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrObjectNotFound
	}

	header, contentStart, err := parseObjectHeader(data)
	if err != nil {
		return nil, err
	}

	return &memoryObjectReader{
		header:  header,
		content: data[contentStart:],
		pos:     0,
	}, nil
}

// memoryObjectReader implements ObjectReader for in-memory objects.
type memoryObjectReader struct {
	header  ObjectHeader
	content []byte
	pos     uint64
}

func (r *memoryObjectReader) Close() {}

func (r *memoryObjectReader) Header() ObjectHeader {
	return r.header
}

func (r *memoryObjectReader) Reset() error {
	r.pos = 0
	return nil
}

func (r *memoryObjectReader) Read(p []byte) (int, error) {
	if r.pos >= uint64(len(r.content)) {
		return 0, io.EOF
	}
	n := copy(p, r.content[r.pos:])
	r.pos += uint64(n)
	return n, nil
}

func (r *memoryObjectReader) SkipBytes(n uint64) error {
	r.pos += n
	if r.pos > uint64(len(r.content)) {
		r.pos = uint64(len(r.content))
	}
	return nil
}

func (r *memoryObjectReader) Position() uint64 {
	return r.pos
}
