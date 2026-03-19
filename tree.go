package repodojo

// TreeEntry represents an entry in a git tree object.
type TreeEntry struct {
	OID  []byte
	Mode Mode
}
