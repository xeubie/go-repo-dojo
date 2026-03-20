package repodojo

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrInvalidPackSig     = errors.New("invalid pack file signature")
	ErrInvalidPackVersion = errors.New("unsupported pack file version")
	ErrObjectNotFound     = errors.New("object not found")
	ErrInvalidDeltaCache  = errors.New("invalid delta cache")
)

// ---------------------------------------------------------------------------
// PackReader
// ---------------------------------------------------------------------------

// PackReader reads pack data from either a seekable file or a sequential stream.
type PackReader interface {
	io.Reader
	ReadByte() (byte, error)
	SeekTo(pos uint64) error
	LogicalPos() uint64
	Dupe() (PackReader, error)
	Close()
	IsFile() bool
}

func readPackUint32BE(pr PackReader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(pr, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

// FilePackReader reads pack data from a seekable file.
type FilePackReader struct {
	filePath string
	file     *os.File
	br       *bufio.Reader
}

func NewFilePackReader(path string, bufSize int) (*FilePackReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &FilePackReader{
		filePath: path,
		file:     f,
		br:       bufio.NewReaderSize(f, bufSize),
	}, nil
}

func (pr *FilePackReader) Close() {
	if pr.file != nil {
		pr.file.Close()
	}
}

func (pr *FilePackReader) Read(p []byte) (int, error) {
	return pr.br.Read(p)
}

func (pr *FilePackReader) ReadByte() (byte, error) {
	return pr.br.ReadByte()
}

func (pr *FilePackReader) SeekTo(pos uint64) error {
	_, err := pr.file.Seek(int64(pos), io.SeekStart)
	if err != nil {
		return err
	}
	pr.br.Reset(pr.file)
	return nil
}

func (pr *FilePackReader) LogicalPos() uint64 {
	filePos, _ := pr.file.Seek(0, io.SeekCurrent)
	return uint64(filePos) - uint64(pr.br.Buffered())
}

func (pr *FilePackReader) Dupe() (PackReader, error) {
	return NewFilePackReader(pr.filePath, pr.br.Size())
}

func (pr *FilePackReader) IsFile() bool { return true }

// StreamPackReader reads pack data from a sequential stream.
type StreamPackReader struct {
	counting *CountingReader
}

func NewStreamPackReader(r io.Reader, bufSize int) *StreamPackReader {
	br := bufio.NewReaderSize(r, bufSize)
	return &StreamPackReader{counting: NewCountingReader(br)}
}

func (pr *StreamPackReader) Close() {}

func (pr *StreamPackReader) Read(p []byte) (int, error) {
	return pr.counting.Read(p)
}

func (pr *StreamPackReader) ReadByte() (byte, error) {
	return pr.counting.ReadByte()
}

func (pr *StreamPackReader) SeekTo(pos uint64) error {
	if pos != pr.counting.LogicalPos() {
		return fmt.Errorf("stream PackReader cannot seek to %d (at %d)", pos, pr.counting.LogicalPos())
	}
	return nil
}

func (pr *StreamPackReader) LogicalPos() uint64 {
	return pr.counting.LogicalPos()
}

func (pr *StreamPackReader) Dupe() (PackReader, error) {
	return pr, nil
}

func (pr *StreamPackReader) IsFile() bool { return false }

// ---------------------------------------------------------------------------
// CountingReader
// ---------------------------------------------------------------------------

// CountingReader wraps a *bufio.Reader and tracks the number of logical bytes
// consumed. It implements io.ByteReader so that compress/flate will not wrap
// it in an extra bufio.Reader.
type CountingReader struct {
	br  *bufio.Reader
	pos uint64
}

func NewCountingReader(br *bufio.Reader) *CountingReader {
	return &CountingReader{br: br}
}

func (cr *CountingReader) Read(p []byte) (int, error) {
	n, err := cr.br.Read(p)
	cr.pos += uint64(n)
	return n, err
}

func (cr *CountingReader) ReadByte() (byte, error) {
	b, err := cr.br.ReadByte()
	if err != nil {
		return 0, err
	}
	cr.pos++
	return b, nil
}

func (cr *CountingReader) LogicalPos() uint64 {
	return cr.pos
}

// ---------------------------------------------------------------------------
// packObjectStream – zlib decompression for a single pack object
// ---------------------------------------------------------------------------

type packObjectStream struct {
	packReader PackReader
	ownsReader bool
	startPos   uint64
	endPos     uint64
	endPosSet  bool

	// zlib mode
	zlibReader io.ReadCloser

	// memory mode (small objects read into RAM)
	memBuf   []byte
	memPos   int
	isMem    bool
}

func newPackObjectStream(orig PackReader, startPos uint64) (*packObjectStream, error) {
	pr, err := orig.Dupe()
	if err != nil {
		return nil, err
	}
	owns := pr.IsFile()

	if err := pr.SeekTo(startPos); err != nil {
		if owns {
			pr.Close()
		}
		return nil, err
	}

	zlibR, err := zlib.NewReader(pr)
	if err != nil {
		if owns {
			pr.Close()
		}
		return nil, err
	}

	return &packObjectStream{
		packReader: pr,
		ownsReader: owns,
		startPos:   startPos,
		zlibReader: zlibR,
	}, nil
}

func (s *packObjectStream) close() {
	if !s.endPosSet {
		if ep, err := s.getEndPos(); err == nil {
			s.endPos = ep
			s.endPosSet = true
		}
	}
	if s.zlibReader != nil {
		s.zlibReader.Close()
	}
	if s.ownsReader {
		s.packReader.Close()
	}
}

func (s *packObjectStream) read(dest []byte) (int, error) {
	if s.isMem {
		if s.memPos >= len(s.memBuf) {
			return 0, io.EOF
		}
		n := copy(dest, s.memBuf[s.memPos:])
		s.memPos += n
		return n, nil
	}
	return s.zlibReader.Read(dest)
}

func (s *packObjectStream) readByte() (byte, error) {
	var buf [1]byte
	for {
		n, err := s.read(buf[:])
		if n > 0 {
			return buf[0], nil
		}
		if err != nil {
			return 0, err
		}
	}
}

func (s *packObjectStream) reset() error {
	if s.isMem {
		s.memPos = 0
		return nil
	}
	if err := s.packReader.SeekTo(s.startPos); err != nil {
		return err
	}
	if s.zlibReader != nil {
		s.zlibReader.Close()
	}
	zlibR, err := zlib.NewReader(s.packReader)
	if err != nil {
		return err
	}
	s.zlibReader = zlibR
	return nil
}

func (s *packObjectStream) getEndPos() (uint64, error) {
	if s.isMem {
		return s.endPos, nil
	}
	// drain remaining decompressed data; the zlib reader will consume
	// the checksum from the underlying reader on the last Read.
	if _, err := io.Copy(io.Discard, s.zlibReader); err != nil {
		return 0, err
	}
	return s.packReader.LogicalPos(), nil
}

func (s *packObjectStream) readIntoMemoryMaybe(objectSize uint64) error {
	if s.isMem || !s.packReader.IsFile() {
		return nil
	}
	const maxBuf = 50_000_000
	if objectSize > maxBuf {
		return nil
	}

	if err := s.reset(); err != nil {
		return err
	}

	buf := make([]byte, objectSize)
	if _, err := io.ReadFull(s, buf); err != nil {
		return err
	}

	endPos, err := s.getEndPos()
	if err != nil {
		return err
	}

	s.zlibReader.Close()
	s.zlibReader = nil
	s.memBuf = buf
	s.memPos = 0
	s.isMem = true
	s.endPos = endPos
	s.endPosSet = true
	return nil
}

// implement io.Reader so io.ReadFull can be used on the stream
func (s *packObjectStream) Read(p []byte) (int, error) { return s.read(p) }

func (s *packObjectStream) skipBytes(n uint64) error {
	var buf [512]byte
	rem := n
	for rem > 0 {
		toRead := rem
		if toRead > uint64(len(buf)) {
			toRead = uint64(len(buf))
		}
		nr, err := s.read(buf[:toRead])
		if nr == 0 && err != nil {
			return err
		}
		rem -= uint64(nr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// PackObjectReader
// ---------------------------------------------------------------------------

type packObjectKind uint8

const (
	packCommit   packObjectKind = 1
	packTree     packObjectKind = 2
	packBlob     packObjectKind = 3
	packTag      packObjectKind = 4
	packOfsDelta packObjectKind = 6
	packRefDelta packObjectKind = 7
)

type deltaChunkKind int

const (
	deltaAddNew       deltaChunkKind = iota
	deltaCopyFromBase
)

type deltaChunk struct {
	kind   deltaChunkKind
	offset uint64
	size   uint64
}

type deltaState struct {
	baseReader    *LooseOrPackObjectReader
	chunkIndex    int
	chunkPosition uint64
	realPosition  uint64
	chunks        []deltaChunk
	chunkData     [][]byte // parallel to chunks; nil = not cached
	reconSize     uint64
}

type packObjectMode int

const (
	packObjectBasic packObjectMode = iota
	packObjectDelta
)

type deltaRefKind int

const (
	deltaRefOfs deltaRefKind = iota
	deltaRefRef
)

type PackObjectReader struct {
	stream *packObjectStream
	relPos uint64 // position within decompressed data
	size   uint64 // decompressed size

	mode packObjectMode
	// basic
	basicHeader ObjectHeader
	// delta init
	deltaRefKind   deltaRefKind
	deltaOfsPos    uint64
	deltaRefOIDHex string
	// delta state (set after initDelta)
	deltaState *deltaState
}

func packObjectKindToObjectKind(k packObjectKind) ObjectKind {
	switch k {
	case packCommit:
		return ObjectKindCommit
	case packTree:
		return ObjectKindTree
	case packBlob:
		return ObjectKindBlob
	case packTag:
		return ObjectKindTag
	}
	return ObjectKindBlob
}

func objectKindToPackObjectKind(k ObjectKind) packObjectKind {
	switch k {
	case ObjectKindCommit:
		return packCommit
	case ObjectKindTree:
		return packTree
	case ObjectKindBlob:
		return packBlob
	case ObjectKindTag:
		return packTag
	}
	return packBlob
}

func initPackObjectReaderAtPosition(pr PackReader, position uint64) (*PackObjectReader, error) {
	if err := pr.SeekTo(position); err != nil {
		return nil, err
	}

	// first byte: [extra:1][kind:3][size_low:4]
	firstByte, err := pr.ReadByte()
	if err != nil {
		return nil, err
	}
	kind := packObjectKind((firstByte >> 4) & 0x07)
	size := uint64(firstByte & 0x0F)
	shift := uint(4)
	extra := firstByte&0x80 != 0
	for extra {
		b, err := pr.ReadByte()
		if err != nil {
			return nil, err
		}
		size |= uint64(b&0x7F) << shift
		shift += 7
		extra = b&0x80 != 0
	}

	switch kind {
	case packCommit, packTree, packBlob, packTag:
		startPos := pr.LogicalPos()
		stream, err := newPackObjectStream(pr, startPos)
		if err != nil {
			return nil, err
		}
		if err := stream.readIntoMemoryMaybe(size); err != nil {
			stream.close()
			return nil, err
		}
		return &PackObjectReader{
			stream: stream,
			relPos: 0,
			size:   size,
			mode:   packObjectBasic,
			basicHeader: ObjectHeader{
				Kind: packObjectKindToObjectKind(kind),
				Size: size,
			},
		}, nil

	case packOfsDelta:
		// big-endian variable length "offset encoding"
		var offset uint64
		for {
			b, err := pr.ReadByte()
			if err != nil {
				return nil, err
			}
			offset = (offset << 7) | uint64(b&0x7F)
			if b&0x80 == 0 {
				break
			}
			offset++
		}
		startPos := pr.LogicalPos()
		stream, err := newPackObjectStream(pr, startPos)
		if err != nil {
			return nil, err
		}
		return &PackObjectReader{
			stream:       stream,
			relPos:       0,
			size:         size,
			mode:         packObjectDelta,
			deltaRefKind: deltaRefOfs,
			deltaOfsPos:  position - offset,
		}, nil

	case packRefDelta:
		var oidBuf [32]byte // max hash size
		hashByteLen := 20 // SHA-1
		if _, err := io.ReadFull(pr, oidBuf[:hashByteLen]); err != nil {
			return nil, err
		}
		oidHex := hex.EncodeToString(oidBuf[:hashByteLen])
		startPos := pr.LogicalPos()
		stream, err := newPackObjectStream(pr, startPos)
		if err != nil {
			return nil, err
		}
		return &PackObjectReader{
			stream:         stream,
			relPos:         0,
			size:           size,
			mode:           packObjectDelta,
			deltaRefKind:   deltaRefRef,
			deltaRefOIDHex: oidHex,
		}, nil
	}

	return nil, fmt.Errorf("unknown pack object kind: %d", kind)
}

// initDelta reads delta instructions from the zlib stream and sets up the base reader.
func (por *PackObjectReader) initDelta(repo *Repo) error {
	var baseReader *LooseOrPackObjectReader

	if por.deltaRefKind == deltaRefOfs {
		dupPR, err := por.stream.packReader.Dupe()
		if err != nil {
			return err
		}
		defer dupPR.Close()
		basePack, err := initPackObjectReaderAtPosition(dupPR, por.deltaOfsPos)
		if err != nil {
			return err
		}
		baseReader = &LooseOrPackObjectReader{kind: objectReaderPack, pack: basePack}
	} else {
		lr, err := newLooseOrPackObjectReader(repo, por.deltaRefOIDHex)
		if err != nil {
			return err
		}
		baseReader = lr
	}

	// read variable-length sizes from zlib stream
	readVarSize := func() (uint64, uint64, error) {
		var val uint64
		var shift uint
		var consumed uint64
		for {
			b, err := por.stream.readByte()
			if err != nil {
				return 0, 0, err
			}
			consumed++
			val |= uint64(b&0x7F) << shift
			shift += 7
			if b&0x80 == 0 {
				break
			}
		}
		return val, consumed, nil
	}

	var bytesRead uint64

	// base size
	_, bc, err := readVarSize()
	if err != nil {
		baseReader.Close()
		return err
	}
	bytesRead += bc

	// reconstructed size
	reconSize, bc, err := readVarSize()
	if err != nil {
		baseReader.Close()
		return err
	}
	bytesRead += bc

	var chunks []deltaChunk
	var chunkData [][]byte

	isStream := !por.stream.packReader.IsFile()

	for bytesRead < por.size {
		instrByte, err := por.stream.readByte()
		if err != nil {
			baseReader.Close()
			return err
		}
		bytesRead++

		if instrByte&0x80 == 0 {
			// add new data
			addSize := uint64(instrByte & 0x7F)
			if addSize == 0 {
				continue // reserved
			}
			chunk := deltaChunk{kind: deltaAddNew, offset: bytesRead, size: addSize}
			chunks = append(chunks, chunk)

			if isStream {
				// cache for stream mode
				buf := make([]byte, addSize)
				if _, err := io.ReadFull(por.stream, buf); err != nil {
					baseReader.Close()
					return err
				}
				chunkData = append(chunkData, buf)
			} else {
				// skip in zlib stream (re-readable via seek for file mode)
				if err := por.stream.skipBytes(addSize); err != nil {
					baseReader.Close()
					return err
				}
				chunkData = append(chunkData, nil)
			}
			bytesRead += addSize
		} else {
			// copy from base
			var vals [7]byte
			for i := uint(0); i < 7; i++ {
				if instrByte&(1<<i) != 0 {
					b, err := por.stream.readByte()
					if err != nil {
						baseReader.Close()
						return err
					}
					vals[i] = b
					bytesRead++
				}
			}
			copyOffset := uint64(binary.LittleEndian.Uint32(vals[0:4]))
			copySize := uint64(vals[4]) | uint64(vals[5])<<8 | uint64(vals[6])<<16
			if copySize == 0 {
				copySize = 0x10000
			}
			chunk := deltaChunk{kind: deltaCopyFromBase, offset: copyOffset, size: copySize}
			chunks = append(chunks, chunk)
			chunkData = append(chunkData, nil) // filled by initCache
		}
	}

	if err := por.stream.readIntoMemoryMaybe(por.size); err != nil {
		baseReader.Close()
		return err
	}

	por.deltaState = &deltaState{
		baseReader:    baseReader,
		chunkIndex:    0,
		chunkPosition: 0,
		realPosition:  bytesRead,
		chunks:        chunks,
		chunkData:     chunkData,
		reconSize:     reconSize,
	}
	por.relPos = bytesRead
	return nil
}

// initDeltaAndCache initialises the full delta chain iteratively (not recursively)
// to avoid stack overflow on deep chains, then caches base data.
func (por *PackObjectReader) initDeltaAndCache(repo *Repo) error {
	var deltaObjects []*PackObjectReader
	last := por
	for last.mode != packObjectBasic {
		if err := last.initDelta(repo); err != nil {
			return err
		}
		deltaObjects = append(deltaObjects, last)
		if last.deltaState == nil {
			break
		}
		br := last.deltaState.baseReader
		if br.kind == objectReaderLoose {
			break
		}
		last = br.pack
	}

	// cache from innermost to outermost
	for i := len(deltaObjects) - 1; i >= 0; i-- {
		if err := deltaObjects[i].initCache(); err != nil {
			return err
		}
	}
	return nil
}

// initCache populates chunkData for copy_from_base chunks.
func (por *PackObjectReader) initCache() error {
	state := por.deltaState
	if state == nil {
		return nil
	}

	// collect copy_from_base indices, sorted by offset for sequential reading
	type cRef struct {
		idx   int
		chunk deltaChunk
	}
	var copyRefs []cRef
	for i, c := range state.chunks {
		if c.kind == deltaCopyFromBase {
			copyRefs = append(copyRefs, cRef{i, c})
		}
	}
	sort.Slice(copyRefs, func(a, b int) bool {
		if copyRefs[a].chunk.offset == copyRefs[b].chunk.offset {
			return copyRefs[a].chunk.size > copyRefs[b].chunk.size
		}
		return copyRefs[a].chunk.offset < copyRefs[b].chunk.offset
	})

	for ci, cr := range copyRefs {
		if state.chunkData[cr.idx] != nil && len(state.chunkData[cr.idx]) > 0 {
			continue
		}
		// subset optimisation
		if ci > 0 && cr.chunk.offset == copyRefs[ci-1].chunk.offset && cr.chunk.size < copyRefs[ci-1].chunk.size {
			prevData := state.chunkData[copyRefs[ci-1].idx]
			state.chunkData[cr.idx] = prevData[:cr.chunk.size]
			continue
		}

		if err := state.baseReader.Reset(); err != nil {
			return err
		}
		pos := state.baseReader.Position()
		if cr.chunk.offset > pos {
			if err := state.baseReader.SkipBytes(cr.chunk.offset - pos); err != nil {
				return err
			}
		}

		buf := make([]byte, cr.chunk.size)
		var readSoFar uint64
		for readSoFar < cr.chunk.size {
			amt := cr.chunk.size - readSoFar
			if amt > 2048 {
				amt = 2048
			}
			n, err := state.baseReader.Read(buf[readSoFar : readSoFar+amt])
			if n == 0 && err != nil {
				return fmt.Errorf("initCache: %w", err)
			}
			readSoFar += uint64(n)
		}
		state.chunkData[cr.idx] = buf
	}

	// free base delta cache if the base is itself a delta pack object
	if state.baseReader.kind == objectReaderPack && state.baseReader.pack != nil && state.baseReader.pack.mode != packObjectBasic {
		if bs := state.baseReader.pack.deltaState; bs != nil {
			bs.chunkData = nil
		}
	}
	return nil
}

func (por *PackObjectReader) Close() {
	por.stream.close()
	if por.deltaState != nil {
		por.deltaState.baseReader.Close()
	}
}

func (por *PackObjectReader) Header() ObjectHeader {
	if por.mode == packObjectBasic {
		return por.basicHeader
	}
	if por.deltaState != nil {
		return ObjectHeader{
			Kind: por.deltaState.baseReader.Header().Kind,
			Size: por.deltaState.reconSize,
		}
	}
	return ObjectHeader{}
}

func (por *PackObjectReader) Reset() error {
	if err := por.stream.reset(); err != nil {
		return err
	}
	por.relPos = 0
	if por.deltaState != nil {
		por.deltaState.chunkIndex = 0
		por.deltaState.chunkPosition = 0
		por.deltaState.realPosition = 0
		if err := por.deltaState.baseReader.Reset(); err != nil {
			return err
		}
	}
	return nil
}

func (por *PackObjectReader) Position() uint64 {
	if por.mode == packObjectBasic {
		return por.relPos
	}
	if por.deltaState != nil {
		return por.deltaState.realPosition
	}
	return 0
}

func (por *PackObjectReader) Read(p []byte) (int, error) {
	if por.mode == packObjectBasic {
		return por.readBasic(p)
	}
	return por.readDelta(p)
}

func (por *PackObjectReader) readBasic(p []byte) (int, error) {
	if por.relPos >= por.size {
		return 0, io.EOF
	}
	remaining := por.size - por.relPos
	if uint64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := por.stream.read(p)
	por.relPos += uint64(n)
	return n, err
}

func (por *PackObjectReader) readDelta(p []byte) (int, error) {
	state := por.deltaState
	if state == nil {
		return 0, io.EOF
	}
	var bytesRead int
	for bytesRead < len(p) {
		if state.chunkIndex >= len(state.chunks) {
			break
		}
		chunk := state.chunks[state.chunkIndex]
		destSlice := p[bytesRead:]
		chunkRemaining := chunk.size - state.chunkPosition
		bytesToRead := chunkRemaining
		if uint64(len(destSlice)) < bytesToRead {
			bytesToRead = uint64(len(destSlice))
		}

		switch chunk.kind {
		case deltaAddNew:
			cached := state.chunkData[state.chunkIndex]
			if cached != nil {
				copy(destSlice[:bytesToRead], cached[state.chunkPosition:state.chunkPosition+bytesToRead])
				bytesRead += int(bytesToRead)
				state.chunkPosition += bytesToRead
				state.realPosition += bytesToRead
			} else {
				offset := chunk.offset + state.chunkPosition
				if por.relPos > offset {
					if err := por.stream.reset(); err != nil {
						return bytesRead, err
					}
					por.relPos = 0
				}
				if por.relPos < offset {
					skip := offset - por.relPos
					if err := por.stream.skipBytes(skip); err != nil {
						return bytesRead, err
					}
					por.relPos += skip
				}
				n, err := por.stream.read(destSlice[:bytesToRead])
				if err != nil && err != io.EOF {
					return bytesRead, err
				}
				por.relPos += uint64(n)
				bytesRead += n
				state.chunkPosition += uint64(n)
				state.realPosition += uint64(n)
			}
		case deltaCopyFromBase:
			cached := state.chunkData[state.chunkIndex]
			if cached == nil {
				return bytesRead, ErrInvalidDeltaCache
			}
			copy(destSlice[:bytesToRead], cached[state.chunkPosition:state.chunkPosition+bytesToRead])
			bytesRead += int(bytesToRead)
			state.chunkPosition += bytesToRead
			state.realPosition += bytesToRead
		}

		if state.chunkPosition >= chunk.size {
			state.chunkIndex++
			state.chunkPosition = 0
		}
	}
	if bytesRead == 0 && state.chunkIndex >= len(state.chunks) {
		return 0, io.EOF
	}
	return bytesRead, nil
}

func (por *PackObjectReader) SkipBytes(n uint64) error {
	var buf [512]byte
	rem := n
	for rem > 0 {
		toRead := rem
		if toRead > uint64(len(buf)) {
			toRead = uint64(len(buf))
		}
		nr, err := por.Read(buf[:toRead])
		if nr == 0 && err != nil {
			return err
		}
		rem -= uint64(nr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// LooseOrPackObjectReader
// ---------------------------------------------------------------------------

type looseObjectReader struct {
	file       *os.File
	zlibReader io.ReadCloser
	header     ObjectHeader
}

func openLooseObject(repo *Repo, oidHex string) (*looseObjectReader, error) {
	objPath := filepath.Join(repo.repoDir, "objects", oidHex[:2], oidHex[2:])
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

type objectReaderKind int

const (
	objectReaderLoose objectReaderKind = iota
	objectReaderPack
)

// LooseOrPackObjectReader reads an object from loose storage or from a pack file.
type LooseOrPackObjectReader struct {
	kind  objectReaderKind
	loose *looseObjectReader
	pack  *PackObjectReader
}

func newLooseOrPackObjectReader(repo *Repo, oidHex string) (*LooseOrPackObjectReader, error) {
	loose, err := openLooseObject(repo, oidHex)
	if err == nil {
		return &LooseOrPackObjectReader{kind: objectReaderLoose, loose: loose}, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	pack, err := newPackObjectReaderFromIndex(repo, oidHex)
	if err != nil {
		return nil, err
	}
	return &LooseOrPackObjectReader{kind: objectReaderPack, pack: pack}, nil
}

func (r *LooseOrPackObjectReader) Close() {
	if r.kind == objectReaderLoose {
		r.loose.Close()
	} else {
		r.pack.Close()
	}
}

func (r *LooseOrPackObjectReader) Header() ObjectHeader {
	if r.kind == objectReaderLoose {
		return r.loose.header
	}
	return r.pack.Header()
}

func (r *LooseOrPackObjectReader) Reset() error {
	if r.kind == objectReaderLoose {
		return r.loose.Reset()
	}
	return r.pack.Reset()
}

func (r *LooseOrPackObjectReader) Read(p []byte) (int, error) {
	if r.kind == objectReaderLoose {
		return r.loose.Read(p)
	}
	return r.pack.Read(p)
}

func (r *LooseOrPackObjectReader) SkipBytes(n uint64) error {
	if r.kind == objectReaderLoose {
		var buf [512]byte
		rem := n
		for rem > 0 {
			toRead := rem
			if toRead > uint64(len(buf)) {
				toRead = uint64(len(buf))
			}
			nr, err := r.loose.Read(buf[:toRead])
			if nr == 0 && err != nil {
				return err
			}
			rem -= uint64(nr)
		}
		return nil
	}
	return r.pack.SkipBytes(n)
}

func (r *LooseOrPackObjectReader) Position() uint64 {
	if r.kind == objectReaderLoose {
		return 0
	}
	return r.pack.Position()
}

// ---------------------------------------------------------------------------
// PackIterator
// ---------------------------------------------------------------------------

type PackIterator struct {
	packReader  PackReader
	startPos    uint64
	objectCount uint32
	objectIndex uint32
	currentObj  *PackObjectReader
}

func NewPackIterator(pr PackReader) (*PackIterator, error) {
	var sig [4]byte
	if _, err := io.ReadFull(pr, sig[:]); err != nil {
		return nil, err
	}
	if string(sig[:]) != "PACK" {
		return nil, ErrInvalidPackSig
	}
	version, err := readPackUint32BE(pr)
	if err != nil {
		return nil, err
	}
	if version != 2 {
		return nil, ErrInvalidPackVersion
	}
	objCount, err := readPackUint32BE(pr)
	if err != nil {
		return nil, err
	}
	return &PackIterator{
		packReader:  pr,
		startPos:    pr.LogicalPos(),
		objectCount: objCount,
		objectIndex: 0,
	}, nil
}

func (it *PackIterator) StartPosition() uint64 {
	return it.startPos
}

// Next returns the next pack object, or nil when done.
// The caller must call Close() on the returned reader before calling Next again.
// offsetToOID maps pack file offsets to OID bytes, enabling ofs_delta→ref_delta conversion.
func (it *PackIterator) Next(repo *Repo, offsetToOID map[uint64][]byte) (*PackObjectReader, error) {
	if it.objectIndex >= it.objectCount {
		return nil, nil
	}

	// advance past previous object
	if it.currentObj != nil {
		if it.currentObj.stream.endPosSet {
			it.startPos = it.currentObj.stream.endPos
		} else {
			return nil, errors.New("previous pack object not closed")
		}
	}

	startPos := it.startPos
	por, err := initPackObjectReaderAtPosition(it.packReader, startPos)
	if err != nil {
		return nil, err
	}

	if por.mode == packObjectDelta {
		if por.deltaRefKind == deltaRefOfs {
			// try to convert ofs_delta to ref_delta using the offset→OID map
			if offsetToOID != nil {
				if oid, ok := offsetToOID[por.deltaOfsPos]; ok {
					por.deltaRefKind = deltaRefRef
					por.deltaRefOIDHex = hex.EncodeToString(oid)
				}
			}
		}
		if err := por.initDeltaAndCache(repo); err != nil {
			por.Close()
			return nil, err
		}
	}

	it.objectIndex++
	it.currentObj = por
	return por, nil
}

// ---------------------------------------------------------------------------
// PackWriter
// ---------------------------------------------------------------------------

type packWriterObj struct {
	kind   ObjectKind
	size   uint64
	reader *ObjectReader
}

type PackWriter struct {
	objects  []packWriterObj
	objIndex int
	hashKind HashKind
	buf      bytes.Buffer
	bufIdx   int
	hasher   interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	mode       int // 0=header, 1=object, 2=footer, 3=done
	zlibWriter *zlib.Writer
	readerDone bool
}

func (repo *Repo) NewPackWriter(iter *ObjectIterator) (*PackWriter, error) {
	hashKind := repo.opts.Hash
	var objects []packWriterObj
	for {
		obj, err := iter.Next()
		if err != nil {
			return nil, err
		}
		if obj == nil {
			break
		}
		objects = append(objects, packWriterObj{
			kind:   obj.Kind,
			size:   obj.Size,
			reader: obj.reader,
		})
		// don't close the object — we own the reader now
	}
	if len(objects) == 0 {
		return nil, nil
	}

	pw := &PackWriter{
		objects:  objects,
		hashKind: hashKind,
		hasher:   hashKind.NewHasher(),
	}

	// write pack header
	pw.buf.WriteString("PACK")
	binary.Write(&pw.buf, binary.BigEndian, uint32(2))
	binary.Write(&pw.buf, binary.BigEndian, uint32(len(objects)))

	// write first object's pack header
	pw.writeObjectHeader()
	pw.mode = 0 // header
	return pw, nil
}

func (pw *PackWriter) Close() {
	for _, obj := range pw.objects {
		obj.reader.Close()
	}
}

func (pw *PackWriter) Read(p []byte) (int, error) {
	var total int
	for total < len(p) && pw.mode != 3 {
		n, err := pw.readStep(p[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (pw *PackWriter) readStep(p []byte) (int, error) {
	switch pw.mode {
	case 0: // header
		data := pw.buf.Bytes()
		n := copy(p, data[pw.bufIdx:])
		pw.hasher.Write(p[:n])
		pw.bufIdx += n
		if pw.bufIdx >= len(data) {
			pw.buf.Reset()
			pw.bufIdx = 0
			pw.zlibWriter = zlib.NewWriter(&pw.buf)
			pw.readerDone = false
			pw.mode = 1
		}
		return n, nil

	case 1: // object
		data := pw.buf.Bytes()
		if pw.bufIdx < len(data) {
			n := copy(p, data[pw.bufIdx:])
			pw.hasher.Write(p[:n])
			pw.bufIdx += n
			return n, nil
		}

		// buffer consumed, produce more
		pw.buf.Reset()
		pw.bufIdx = 0

		if !pw.readerDone {
			var readBuf [4096]byte
			n, err := pw.objects[pw.objIndex].reader.Read(readBuf[:])
			if n > 0 {
				pw.zlibWriter.Write(readBuf[:n])
				return 0, nil
			}
			if err == io.EOF || n == 0 {
				pw.readerDone = true
				pw.zlibWriter.Close()
				pw.zlibWriter = nil
				if pw.buf.Len() > 0 {
					return 0, nil
				}
			} else if err != nil {
				return 0, err
			}
		}

		// object done
		pw.objIndex++
		if pw.objIndex < len(pw.objects) {
			pw.mode = 0
			pw.writeObjectHeader()
		} else {
			pw.mode = 2
			hashBytes := pw.hasher.Sum(nil)
			pw.buf.Write(hashBytes)
		}
		return 0, nil

	case 2: // footer
		data := pw.buf.Bytes()
		n := copy(p, data[pw.bufIdx:])
		pw.bufIdx += n
		if pw.bufIdx >= len(data) {
			pw.buf.Reset()
			pw.bufIdx = 0
			pw.mode = 3
		}
		return n, nil

	case 3:
		return 0, nil
	}
	return 0, nil
}

func (pw *PackWriter) writeObjectHeader() {
	obj := pw.objects[pw.objIndex]
	size := obj.size
	packKind := objectKindToPackObjectKind(obj.kind)

	// first byte: [extra:1][kind:3][size_low:4]
	lowBits := byte(size & 0x0F)
	highBits := size >> 4
	var extraBit byte
	if highBits > 0 {
		extraBit = 0x80
	}
	pw.buf.WriteByte(extraBit | (byte(packKind) << 4) | lowBits)

	rem := highBits
	for rem > 0 {
		val := byte(rem & 0x7F)
		rem >>= 7
		if rem > 0 {
			val |= 0x80
		}
		pw.buf.WriteByte(val)
	}
}

// ---------------------------------------------------------------------------
// Pack index search
// ---------------------------------------------------------------------------

func newPackObjectReaderFromIndex(repo *Repo, oidHex string) (*PackObjectReader, error) {
	packDir := filepath.Join(repo.repoDir, "objects", "pack")
	offset, packID, err := searchPackIndexes(repo.opts.Hash, packDir, oidHex)
	if err != nil {
		return nil, err
	}

	packFileName := fmt.Sprintf("pack-%s.pack", packID)
	packPath := filepath.Join(packDir, packFileName)

	pr, err := NewFilePackReader(packPath, 4096)
	if err != nil {
		return nil, err
	}
	defer pr.Close()

	// skip pack header
	var sig [4]byte
	if _, err := io.ReadFull(pr, sig[:]); err != nil {
		return nil, err
	}
	if string(sig[:]) != "PACK" {
		return nil, ErrInvalidPackSig
	}
	if v, err := readPackUint32BE(pr); err != nil {
		return nil, err
	} else if v != 2 {
		return nil, ErrInvalidPackVersion
	}
	if _, err := readPackUint32BE(pr); err != nil {
		return nil, err
	}

	por, err := initPackObjectReaderAtPosition(pr, offset)
	if err != nil {
		return nil, err
	}
	if err := por.initDeltaAndCache(repo); err != nil {
		por.Close()
		return nil, err
	}
	return por, nil
}

func searchPackIndexes(hashKind HashKind, packDir string, oidHex string) (uint64, string, error) {
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return 0, "", ErrObjectNotFound
	}

	oidBytes, err := hashKind.HexToBytes(oidHex)
	if err != nil {
		return 0, "", err
	}

	prefix := "pack-"
	suffix := ".idx"

	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() {
			continue
		}
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		packID := name[len(prefix) : len(name)-len(suffix)]
		if len(packID) != hashKind.HexLen() {
			continue
		}

		idxPath := filepath.Join(packDir, name)
		offset, err := searchPackIndex(hashKind, idxPath, oidBytes)
		if err != nil {
			continue
		}
		if offset != nil {
			return *offset, packID, nil
		}
	}
	return 0, "", ErrObjectNotFound
}

func searchPackIndex(hashKind HashKind, idxPath string, oidBytes []byte) (*uint64, error) {
	f, err := os.Open(idxPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := bufio.NewReader(f)

	// header
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	if header != [4]byte{255, 116, 79, 99} {
		return nil, errors.New("unsupported pack index version")
	}
	var vBuf [4]byte
	if _, err := io.ReadFull(r, vBuf[:]); err != nil {
		return nil, err
	}
	version := binary.BigEndian.Uint32(vBuf[:])
	if version != 2 {
		return nil, errors.New("unsupported pack index version")
	}

	// fanout table (256 x uint32)
	var fanout [256]uint32
	for i := range fanout {
		var buf [4]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return nil, err
		}
		fanout[i] = binary.BigEndian.Uint32(buf[:])
	}

	entryCount := fanout[255]
	byteLen := hashKind.ByteLen()
	// current position in file after header (8 bytes) + fanout (1024 bytes) = 1032
	oidListPos := uint64(8 + 256*4)

	// binary search for the OID
	first := oidBytes[0]
	var left uint32
	if first > 0 {
		left = fanout[first-1]
	}
	right := fanout[first]

	var foundIndex *uint32
	for left < right {
		mid := left + (right-left)/2
		midOID, err := readOIDAtIndex(f, oidListPos, uint64(mid), byteLen)
		if err != nil {
			return nil, err
		}
		cmp := bytes.Compare(oidBytes, midOID)
		if cmp == 0 {
			foundIndex = &mid
			break
		} else if cmp < 0 {
			if mid == 0 {
				break
			}
			right = mid
		} else {
			left = mid + 1
		}
	}

	if foundIndex == nil {
		// check right boundary
		if right < entryCount {
			rightOID, err := readOIDAtIndex(f, oidListPos, uint64(right), byteLen)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(oidBytes, rightOID) {
				foundIndex = &right
			}
		}
	}

	if foundIndex == nil {
		return nil, nil
	}

	// read offset
	crcSize := uint64(4)
	offsetSize := uint64(4)
	crcListPos := oidListPos + uint64(entryCount)*uint64(byteLen)
	offsetListPos := crcListPos + uint64(entryCount)*crcSize
	offsetPos := offsetListPos + uint64(*foundIndex)*offsetSize

	if _, err := f.Seek(int64(offsetPos), io.SeekStart); err != nil {
		return nil, err
	}
	var offBuf [4]byte
	if _, err := io.ReadFull(f, offBuf[:]); err != nil {
		return nil, err
	}
	rawOffset := binary.BigEndian.Uint32(offBuf[:])
	if rawOffset&0x80000000 == 0 {
		offset := uint64(rawOffset)
		return &offset, nil
	}

	// large offset
	offset64ListPos := offsetListPos + uint64(entryCount)*offsetSize
	offset64Idx := uint64(rawOffset & 0x7FFFFFFF)
	offset64Pos := offset64ListPos + offset64Idx*8
	if _, err := f.Seek(int64(offset64Pos), io.SeekStart); err != nil {
		return nil, err
	}
	var off64Buf [8]byte
	if _, err := io.ReadFull(f, off64Buf[:]); err != nil {
		return nil, err
	}
	offset := binary.BigEndian.Uint64(off64Buf[:])
	return &offset, nil
}

func readOIDAtIndex(f *os.File, oidListPos uint64, index uint64, byteLen int) ([]byte, error) {
	pos := oidListPos + index*uint64(byteLen)
	if _, err := f.Seek(int64(pos), io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, byteLen)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// parseObjectHeaderFromReader reads a git object header ("type size\0")
// from a stream, consuming the null terminator.
func parseObjectHeaderFromReader(r io.Reader) (ObjectHeader, error) {
	var typeBuf bytes.Buffer
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return ObjectHeader{}, err
		}
		if b[0] == ' ' {
			break
		}
		typeBuf.WriteByte(b[0])
	}
	kind, err := ObjectKindFromName(typeBuf.String())
	if err != nil {
		return ObjectHeader{}, err
	}

	var sizeBuf bytes.Buffer
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return ObjectHeader{}, err
		}
		if b[0] == 0 {
			break
		}
		sizeBuf.WriteByte(b[0])
	}

	var size uint64
	if _, err := fmt.Sscanf(sizeBuf.String(), "%d", &size); err != nil {
		return ObjectHeader{}, err
	}

	return ObjectHeader{Kind: kind, Size: size}, nil
}
