package gitgonano

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"hash"
)

type HashKind int

const (
	SHA1Hash HashKind = iota
	SHA256Hash
)

func (h HashKind) ByteLen() int {
	switch h {
	case SHA1Hash:
		return sha1.Size
	case SHA256Hash:
		return sha256.Size
	}
	panic("invalid hash kind")
}

func (h HashKind) HexLen() int {
	return h.ByteLen() * 2
}

func (h HashKind) NewHasher() hash.Hash {
	switch h {
	case SHA1Hash:
		return sha1.New()
	case SHA256Hash:
		return sha256.New()
	}
	panic("invalid hash kind")
}

func (h HashKind) HashBytes(data []byte) []byte {
	hasher := h.NewHasher()
	hasher.Write(data)
	return hasher.Sum(nil)
}

func (h HashKind) HexToBytes(hexStr string) ([]byte, error) {
	return hex.DecodeString(hexStr)
}
