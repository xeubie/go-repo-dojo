package repomofo

import (
	"encoding/hex"
	"fmt"
	"io"
)

const (
	pktLenSize       = 4
	largePacketMax   = 65520
	largePacketDMax  = largePacketMax - pktLenSize
	sidebandMaxChunk = largePacketMax - 5
)

func pktLineHeader(length int) [4]byte {
	var header [4]byte
	h := fmt.Sprintf("%04x", length)
	copy(header[:], h)
	return header
}

func writePktLine(w io.Writer, data []byte) error {
	length := len(data) + pktLenSize
	header := pktLineHeader(length)
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}

func writePktFlush(w io.Writer) error {
	_, err := w.Write([]byte("0000"))
	return err
}

func writePktDelim(w io.Writer) error {
	_, err := w.Write([]byte("0001"))
	return err
}

// writePktLineSB writes a pkt-line with a sideband byte prefix.
func writePktLineSB(w io.Writer, band byte, data []byte) error {
	length := len(data) + pktLenSize + 1
	header := pktLineHeader(length)
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write([]byte{band}); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}

// readPktLine reads a pkt-line. Returns nil for flush packets.
func readPktLine(r io.Reader) ([]byte, error) {
	var header [pktLenSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}

	dst := make([]byte, 2)
	_, err := hex.Decode(dst, header[:])
	if err != nil {
		return nil, fmt.Errorf("invalid pkt-line header: %w", err)
	}
	length := int(dst[0])<<8 | int(dst[1])

	if length == 0 {
		return nil, nil // flush packet
	}
	if length < pktLenSize {
		return nil, fmt.Errorf("invalid pkt-line length: %d", length)
	}

	dataLen := length - pktLenSize
	buf := make([]byte, dataLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	// chomp trailing newline
	if dataLen > 0 && buf[dataLen-1] == '\n' {
		buf = buf[:dataLen-1]
	}
	return buf, nil
}

type pktLineResult interface {
	pktLineResult()
}

type pktLineData struct {
	data []byte
}

type pktLineFlush struct{}
type pktLineDelim struct{}
type pktLineResponseEnd struct{}
type pktLineEOF struct{}

func (pktLineData) pktLineResult()        {}
func (pktLineFlush) pktLineResult()       {}
func (pktLineDelim) pktLineResult()       {}
func (pktLineResponseEnd) pktLineResult() {}
func (pktLineEOF) pktLineResult()         {}

// readPktLineEx reads a pkt-line and distinguishes flush/delim/response_end/eof/data.
func readPktLineEx(r io.Reader) (pktLineResult, error) {
	var header [pktLenSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return pktLineEOF{}, nil
		}
		return nil, err
	}

	dst := make([]byte, 2)
	_, err := hex.Decode(dst, header[:])
	if err != nil {
		return nil, fmt.Errorf("invalid pkt-line header: %w", err)
	}
	length := int(dst[0])<<8 | int(dst[1])

	switch length {
	case 0:
		return pktLineFlush{}, nil
	case 1:
		return pktLineDelim{}, nil
	case 2:
		return pktLineResponseEnd{}, nil
	}

	if length < pktLenSize {
		return nil, fmt.Errorf("invalid pkt-line length: %d", length)
	}

	dataLen := length - pktLenSize
	buf := make([]byte, dataLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	// chomp trailing newline
	if dataLen > 0 && buf[dataLen-1] == '\n' {
		buf = buf[:dataLen-1]
	}
	return pktLineData{data: buf}, nil
}

func sendSideband(w io.Writer, band byte, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	for len(data) > 0 {
		n := len(data)
		if n > sidebandMaxChunk {
			n = sidebandMaxChunk
		}
		header := pktLineHeader(n + 5)
		if _, err := w.Write(header[:]); err != nil {
			return err
		}
		if _, err := w.Write([]byte{band}); err != nil {
			return err
		}
		if _, err := w.Write(data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func bufPktLine(buf *[]byte, data []byte) {
	length := len(data) + pktLenSize
	header := pktLineHeader(length)
	*buf = append(*buf, header[:]...)
	*buf = append(*buf, data...)
}

func bufPktFlush(buf *[]byte) {
	*buf = append(*buf, []byte("0000")...)
}
