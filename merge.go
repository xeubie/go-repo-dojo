package repomofo

import (
	"bytes"
	"container/heap"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	errDescendentNotFound = errors.New("descendent not found")
	errNoCommonAncestor   = errors.New("no common ancestor")
)

// ---------------------------------------------------------------------------
// CommitParentsQueue
// ---------------------------------------------------------------------------

type commitParentKind int

const (
	commitParentOne commitParentKind = iota
	commitParentTwo
	commitParentStale
)

type commitParent struct {
	oid       string
	kind      commitParentKind
	timestamp uint64
}

type commitParentsQueue []commitParent

func (q commitParentsQueue) Len() int            { return len(q) }
func (q commitParentsQueue) Less(i, j int) bool  { return q[i].timestamp > q[j].timestamp }
func (q commitParentsQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *commitParentsQueue) Push(x interface{}) { *q = append(*q, x.(commitParent)) }
func (q *commitParentsQueue) Pop() interface{} {
	old := *q
	n := len(old)
	item := old[n-1]
	*q = old[:n-1]
	return item
}

// ---------------------------------------------------------------------------
// getDescendent
// ---------------------------------------------------------------------------

func getDescendent(repo *Repo, oid1, oid2 string) (string, error) {
	if oid1 == oid2 {
		return oid1, nil
	}

	pushParents := func(q *commitParentsQueue, oid string, kind commitParentKind) error {
		obj, err := repo.NewObject(oid, true)
		if err != nil {
			return err
		}
		defer obj.Close()
		if obj.Commit == nil {
			return nil
		}
		for _, parentOID := range obj.Commit.ParentOIDs {
			pObj, err := repo.NewObject(parentOID, true)
			if err != nil {
				return err
			}
			var ts uint64
			if pObj.Commit != nil {
				ts = pObj.Commit.Timestamp
			}
			pObj.Close()
			heap.Push(q, commitParent{oid: parentOID, kind: kind, timestamp: ts})
		}
		return nil
	}

	q := &commitParentsQueue{}
	heap.Init(q)

	if err := pushParents(q, oid1, commitParentOne); err != nil {
		return "", err
	}
	if err := pushParents(q, oid2, commitParentTwo); err != nil {
		return "", err
	}

	for q.Len() > 0 {
		node := heap.Pop(q).(commitParent)

		switch node.kind {
		case commitParentOne:
			if node.oid == oid2 {
				return oid1, nil
			}
			if node.oid == oid1 {
				continue
			}
		case commitParentTwo:
			if node.oid == oid1 {
				return oid2, nil
			}
			if node.oid == oid2 {
				continue
			}
		}

		if err := pushParents(q, node.oid, node.kind); err != nil {
			return "", err
		}
	}

	return "", errDescendentNotFound
}

// ---------------------------------------------------------------------------
// commonAncestor
// ---------------------------------------------------------------------------

func commonAncestor(repo *Repo, oid1, oid2 string) (string, error) {
	if oid1 == oid2 {
		return oid1, nil
	}

	q := &commitParentsQueue{}
	heap.Init(q)

	obj1, err := repo.NewObject(oid1, true)
	if err != nil {
		return "", err
	}
	if obj1.Commit != nil {
		heap.Push(q, commitParent{oid: oid1, kind: commitParentOne, timestamp: obj1.Commit.Timestamp})
	}
	obj1.Close()

	obj2, err := repo.NewObject(oid2, true)
	if err != nil {
		return "", err
	}
	if obj2.Commit != nil {
		heap.Push(q, commitParent{oid: oid2, kind: commitParentTwo, timestamp: obj2.Commit.Timestamp})
	}
	obj2.Close()

	parentsOf1 := map[string]bool{}
	parentsOf2 := map[string]bool{}
	var parentsOfBoth []string
	parentsOfBothSet := map[string]bool{}
	staleOIDs := map[string]bool{}

	for q.Len() > 0 {
		node := heap.Pop(q).(commitParent)

		switch node.kind {
		case commitParentOne:
			if node.oid == oid2 {
				return oid2, nil
			} else if parentsOf2[node.oid] {
				if !parentsOfBothSet[node.oid] {
					parentsOfBoth = append(parentsOfBoth, node.oid)
					parentsOfBothSet[node.oid] = true
				}
			} else if parentsOf1[node.oid] {
				continue
			} else {
				parentsOf1[node.oid] = true
			}
		case commitParentTwo:
			if node.oid == oid1 {
				return oid1, nil
			} else if parentsOf1[node.oid] {
				if !parentsOfBothSet[node.oid] {
					parentsOfBoth = append(parentsOfBoth, node.oid)
					parentsOfBothSet[node.oid] = true
				}
			} else if parentsOf2[node.oid] {
				continue
			} else {
				parentsOf2[node.oid] = true
			}
		case commitParentStale:
			staleOIDs[node.oid] = true
		}

		isBaseAncestor := parentsOfBothSet[node.oid]

		obj, err := repo.NewObject(node.oid, true)
		if err != nil {
			return "", err
		}
		if obj.Commit != nil {
			for _, parentOID := range obj.Commit.ParentOIDs {
				isStale := isBaseAncestor || staleOIDs[parentOID]
				if isStale {
					// update any matching node in the queue to stale
					found := false
					for i := 0; i < q.Len(); i++ {
						if (*q)[i].oid == parentOID {
							(*q)[i].kind = commitParentStale
							heap.Fix(q, i)
							found = true
							break
						}
					}
					if found {
						continue
					}
				}
				pObj, err := repo.NewObject(parentOID, true)
				if err != nil {
					obj.Close()
					return "", err
				}
				var ts uint64
				if pObj.Commit != nil {
					ts = pObj.Commit.Timestamp
				}
				pObj.Close()
				kind := node.kind
				if isStale {
					kind = commitParentStale
				}
				heap.Push(q, commitParent{oid: parentOID, kind: kind, timestamp: ts})
			}
		}
		obj.Close()

		// stop if queue only has stale nodes
		allStale := true
		for i := 0; i < q.Len(); i++ {
			if (*q)[i].kind != commitParentStale {
				allStale = false
				break
			}
		}
		if allStale {
			break
		}
	}

	if len(parentsOfBoth) > 1 {
		result := parentsOfBoth[0]
		for _, nextOID := range parentsOfBoth[1:] {
			result, err = getDescendent(repo, result, nextOID)
			if err != nil {
				return "", err
			}
		}
		return result, nil
	} else if len(parentsOfBoth) == 1 {
		return parentsOfBoth[0], nil
	}
	return "", errNoCommonAncestor
}

// ---------------------------------------------------------------------------
// MergeConflict
// ---------------------------------------------------------------------------

type RenamedEntry struct {
	Path      string
	TreeEntry TreeEntry
}

type MergeConflict struct {
	Base    *TreeEntry
	Target  *TreeEntry
	Source  *TreeEntry
	Renamed *RenamedEntry
}

// ---------------------------------------------------------------------------
// writeBlobWithDiff3
// ---------------------------------------------------------------------------

func writeBlobWithDiff3(
	repo *Repo,
	baseFileOID []byte, // nil if no base
	targetFileOID []byte,
	sourceFileOID []byte,
	baseOIDHex string,
	targetName string,
	sourceName string,
	hasConflict *bool,
) ([]byte, error) {
	// create line iterators from object store
	var baseIter *LineIterator
	if baseFileOID != nil {
		rdr, err := repo.store.ReadObject(hex.EncodeToString(baseFileOID))
		if err != nil {
			return nil, err
		}
		baseIter, err = newLineIteratorFromObject(rdr)
		if err != nil {
			return nil, err
		}
		defer baseIter.close()
	} else {
		baseIter = newLineIteratorFromNothing()
	}

	targetRdr, err := repo.store.ReadObject(hex.EncodeToString(targetFileOID))
	if err != nil {
		return nil, err
	}
	targetIter, err := newLineIteratorFromObject(targetRdr)
	if err != nil {
		return nil, err
	}
	defer targetIter.close()

	sourceRdr, err := repo.store.ReadObject(hex.EncodeToString(sourceFileOID))
	if err != nil {
		return nil, err
	}
	sourceIter, err := newLineIteratorFromObject(sourceRdr)
	if err != nil {
		return nil, err
	}
	defer sourceIter.close()

	// if any file is binary, just return the source oid
	if baseIter.source.isBinary() || targetIter.source.isBinary() || sourceIter.source.isBinary() {
		*hasConflict = true
		result := make([]byte, len(sourceFileOID))
		copy(result, sourceFileOID)
		return result, nil
	}

	diff3Iter := newDiff3Iterator(baseIter, targetIter, sourceIter)

	stream := &diff3Stream{
		baseIter:       baseIter,
		targetIter:     targetIter,
		sourceIter:     sourceIter,
		diff3Iter:      diff3Iter,
		targetMarker:   fmt.Sprintf("<<<<<<< target (%s)", targetName),
		baseMarker:     fmt.Sprintf("||||||| base (%s)", baseOIDHex),
		separateMarker: "=======",
		sourceMarker:   fmt.Sprintf(">>>>>>> source (%s)", sourceName),
	}

	// first pass: count bytes
	size, err := stream.count()
	if err != nil {
		return nil, err
	}
	*hasConflict = stream.hasConflict

	// second pass: stream content into writeObject
	stream.seekTo()

	return repo.store.WriteObject(
		ObjectHeader{Kind: ObjectKindBlob, Size: uint64(size)},
		stream,
	)
}

// diff3Stream implements io.Reader, producing merged content on-the-fly.
type diff3Stream struct {
	baseIter       *LineIterator
	targetIter     *LineIterator
	sourceIter     *LineIterator
	diff3Iter      *Diff3Iterator
	targetMarker   string
	baseMarker     string
	separateMarker string
	sourceMarker   string
	lineBuffer     []string
	currentLine    *string
	hasConflict    bool
}

func (s *diff3Stream) Read(buf []byte) (int, error) {
	size := 0
	for size < len(buf) {
		n, err := s.readStep(buf[size:])
		if err != nil {
			return size, err
		}
		if n == 0 {
			if size == 0 {
				return 0, io.EOF
			}
			break
		}
		size += n
	}
	return size, nil
}

func (s *diff3Stream) readStep(buf []byte) (int, error) {
	if s.currentLine != nil {
		line := *s.currentLine
		size := len(line)
		if size > len(buf) {
			size = len(buf)
		}
		lineFinished := len(line) == 0
		if size > 0 {
			copy(buf[:size], line[:size])
			remaining := line[size:]
			lineFinished = len(remaining) == 0
			if lineFinished {
				s.currentLine = nil
			} else {
				s.currentLine = &remaining
			}
		}
		// if we have copied the entire line
		if lineFinished {
			// if there is room for the newline character
			if len(buf) > size {
				// remove the line from the line buffer
				s.lineBuffer = s.lineBuffer[1:]
				if len(s.lineBuffer) > 0 {
					s.currentLine = &s.lineBuffer[0]
				} else {
					s.currentLine = nil
				}
				// if we aren't at the very last line, add a newline character
				if s.currentLine != nil || !s.diff3Iter.Finished {
					buf[size] = '\n'
					return size + 1, nil
				}
			}
		}
		return size, nil
	}

	chunk, err := s.diff3Iter.next()
	if err != nil {
		return 0, err
	}
	if chunk == nil {
		return 0, nil
	}

	switch chunk.Kind {
	case Diff3Clean:
		if chunk.ORange != nil {
			for lineNum := chunk.ORange.Begin; lineNum < chunk.ORange.End; lineNum++ {
				line, err := s.baseIter.get(lineNum)
				if err != nil {
					return 0, err
				}
				s.lineBuffer = append(s.lineBuffer, line)
			}
			if len(s.lineBuffer) > 0 {
				s.currentLine = &s.lineBuffer[0]
			}
		}
	case Diff3Conflict:
		baseLines := linesFromRange(s.baseIter, chunk.ORange)
		targetLines := linesFromRange(s.targetIter, chunk.ARange)
		sourceLines := linesFromRange(s.sourceIter, chunk.BRange)

		// auto-resolve: if base == target or target == source, use source
		if sliceEqual(baseLines, targetLines) || sliceEqual(targetLines, sourceLines) {
			s.lineBuffer = append(s.lineBuffer, sourceLines...)
			if len(s.lineBuffer) > 0 {
				s.currentLine = &s.lineBuffer[0]
			}
			return s.readStep(buf)
		}
		// auto-resolve: if base == source, use target
		if sliceEqual(baseLines, sourceLines) {
			s.lineBuffer = append(s.lineBuffer, targetLines...)
			if len(s.lineBuffer) > 0 {
				s.currentLine = &s.lineBuffer[0]
			}
			return s.readStep(buf)
		}

		// real conflict
		s.lineBuffer = append(s.lineBuffer, s.targetMarker)
		s.lineBuffer = append(s.lineBuffer, targetLines...)
		s.lineBuffer = append(s.lineBuffer, s.baseMarker)
		s.lineBuffer = append(s.lineBuffer, baseLines...)
		s.lineBuffer = append(s.lineBuffer, s.separateMarker)
		s.lineBuffer = append(s.lineBuffer, sourceLines...)
		s.lineBuffer = append(s.lineBuffer, s.sourceMarker)
		if len(s.lineBuffer) > 0 {
			s.currentLine = &s.lineBuffer[0]
		}
		s.hasConflict = true
	}

	return s.readStep(buf)
}

func (s *diff3Stream) seekTo() {
	s.baseIter.reset()
	s.targetIter.reset()
	s.sourceIter.reset()
	s.diff3Iter.reset()
	s.lineBuffer = nil
	s.currentLine = nil
	s.hasConflict = false
}

func (s *diff3Stream) count() (int, error) {
	s.seekTo()
	n := 0
	buf := make([]byte, 4096)
	for {
		size, err := s.Read(buf)
		n += size
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if size == 0 {
			break
		}
	}
	return n, nil
}

func linesFromRange(iter *LineIterator, r *Diff3Range) []string {
	if r == nil {
		return nil
	}
	var lines []string
	for lineNum := r.Begin; lineNum < r.End; lineNum++ {
		line, err := iter.get(lineNum)
		if err != nil {
			break
		}
		lines = append(lines, line)
	}
	return lines
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// samePathConflict
// ---------------------------------------------------------------------------

type SamePathConflictResult struct {
	Change   *TreeChange
	Conflict *MergeConflict
}

func samePathConflict(
	repo *Repo,
	baseOID, targetOID, sourceOID string,
	targetName, sourceName string,
	targetChangeMaybe *TreeChange,
	sourceChange TreeChange,
	path string,
) (*SamePathConflictResult, error) {
	if targetChangeMaybe != nil {
		targetChange := *targetChangeMaybe
		baseEntryMaybe := sourceChange.Old

		if targetChange.New != nil && sourceChange.New != nil {
			targetEntry := targetChange.New
			sourceEntry := sourceChange.New

			if bytes.Equal(targetEntry.OID, sourceEntry.OID) && targetEntry.Mode == sourceEntry.Mode {
				return &SamePathConflictResult{}, nil
			}

			// three-way merge of OIDs
			var oidMaybe []byte
			if bytes.Equal(targetEntry.OID, sourceEntry.OID) {
				oidMaybe = targetEntry.OID
			} else if baseEntryMaybe != nil {
				if bytes.Equal(baseEntryMaybe.OID, targetEntry.OID) {
					oidMaybe = sourceEntry.OID
				} else if bytes.Equal(baseEntryMaybe.OID, sourceEntry.OID) {
					oidMaybe = targetEntry.OID
				}
			}

			// three-way merge of modes
			var modeMaybe *Mode
			if targetEntry.Mode == sourceEntry.Mode {
				m := targetEntry.Mode
				modeMaybe = &m
			} else if baseEntryMaybe != nil {
				if baseEntryMaybe.Mode == targetEntry.Mode {
					m := sourceEntry.Mode
					modeMaybe = &m
				} else if baseEntryMaybe.Mode == sourceEntry.Mode {
					m := targetEntry.Mode
					modeMaybe = &m
				}
			}

			hasConflict := oidMaybe == nil || modeMaybe == nil

			oid := oidMaybe
			if oid == nil {
				var baseFileOID []byte
				if baseEntryMaybe != nil {
					baseFileOID = baseEntryMaybe.OID
				}
				var err error
				oid, err = writeBlobWithDiff3(repo, baseFileOID, targetEntry.OID, sourceEntry.OID, baseOID, targetName, sourceName, &hasConflict)
				if err != nil {
					return nil, err
				}
			}

			mode := targetEntry.Mode
			if modeMaybe != nil {
				mode = *modeMaybe
			}

			result := &SamePathConflictResult{
				Change: &TreeChange{
					Old: targetChange.New,
					New: &TreeEntry{OID: oid, Mode: mode},
				},
			}
			if hasConflict {
				result.Conflict = &MergeConflict{
					Base:   baseEntryMaybe,
					Target: targetEntry,
					Source: sourceEntry,
				}
			}
			return result, nil
		} else if targetChange.New != nil && sourceChange.New == nil {
			return &SamePathConflictResult{
				Change: &TreeChange{
					Old: targetChange.New,
					New: &TreeEntry{OID: targetChange.New.OID, Mode: targetChange.New.Mode},
				},
				Conflict: &MergeConflict{
					Base:   baseEntryMaybe,
					Target: targetChange.New,
					Source: nil,
				},
			}, nil
		} else if targetChange.New == nil && sourceChange.New != nil {
			return &SamePathConflictResult{
				Change: &TreeChange{
					Old: targetChange.New,
					New: &TreeEntry{OID: sourceChange.New.OID, Mode: sourceChange.New.Mode},
				},
				Conflict: &MergeConflict{
					Base:   baseEntryMaybe,
					Target: nil,
					Source: sourceChange.New,
				},
			}, nil
		} else {
			// deleted in both
			return &SamePathConflictResult{}, nil
		}
	}

	// no conflict because the target diff doesn't touch this path
	return &SamePathConflictResult{
		Change: &TreeChange{Old: sourceChange.Old, New: sourceChange.New},
	}, nil
}

// ---------------------------------------------------------------------------
// fileDirConflict
// ---------------------------------------------------------------------------

type diffKind int

const (
	diffKindTarget diffKind = iota
	diffKindSource
)

func fileDirConflict(
	path string,
	diff map[string]TreeChange,
	kind diffKind,
	branchName string,
	conflicts map[string]*MergeConflict,
	cleanDiff map[string]TreeChange,
) {
	parentPath := path
	for {
		idx := strings.LastIndex(parentPath, "/")
		if idx < 0 {
			break
		}
		parentPath = parentPath[:idx]

		change, ok := diff[parentPath]
		if !ok {
			continue
		}
		if change.New == nil {
			continue
		}

		newPath := fmt.Sprintf("%s~%s", parentPath, branchName)
		switch kind {
		case diffKindTarget:
			conflicts[parentPath] = &MergeConflict{
				Base:   change.Old,
				Target: change.New,
				Source: nil,
				Renamed: &RenamedEntry{
					Path:      newPath,
					TreeEntry: *change.New,
				},
			}
			cleanDiff[parentPath] = TreeChange{Old: change.New, New: nil}
		case diffKindSource:
			conflicts[parentPath] = &MergeConflict{
				Base:   change.Old,
				Target: nil,
				Source: change.New,
				Renamed: &RenamedEntry{
					Path:      newPath,
					TreeEntry: *change.New,
				},
			}
			delete(cleanDiff, parentPath)
		}
	}
}

// ---------------------------------------------------------------------------
// Merge state helpers
// ---------------------------------------------------------------------------

var mergeHeadNames = []string{"MERGE_HEAD", "CHERRY_PICK_HEAD"}

const mergeMsgName = "MERGE_MSG"

func checkForOtherMerge(repo *Repo, mergeHeadName string) error {
	for _, name := range mergeHeadNames {
		if name == mergeHeadName {
			continue
		}
		oid, err := repo.readRefRecur(RefOrOid{IsRef: true, Ref: Ref{Kind: RefNone, Name: name}})
		if err != nil && !errors.Is(err, ErrRefNotFound) {
			return err
		}
		if oid != "" {
			return errors.New("other merge in progress")
		}
	}
	return nil
}

func readAnyMergeHead(repo *Repo) (string, error) {
	for _, name := range mergeHeadNames {
		oid, err := repo.readRefRecur(RefOrOid{IsRef: true, Ref: Ref{Kind: RefNone, Name: name}})
		if err != nil && !errors.Is(err, ErrRefNotFound) {
			return "", err
		}
		if oid != "" {
			return oid, nil
		}
	}
	return "", nil
}

func removeMergeState(repo *Repo) {
	for _, name := range mergeHeadNames {
		repo.removeRef(name)
	}
	os.Remove(filepath.Join(repo.repoPath, mergeMsgName))
}

// ---------------------------------------------------------------------------
// MergeKind, MergeInput, MergeResult
// ---------------------------------------------------------------------------

type MergeKind int

const (
	MergeKindFull MergeKind = iota // merge
	MergeKindPick                  // cherry-pick
)

type MergeAction int

const (
	MergeActionNew   MergeAction = iota
	MergeActionCont              // --continue
	MergeActionAbort             // --abort
)

type MergeInput struct {
	Kind     MergeKind
	Action   MergeAction
	Source   RefOrOid // used when Action == MergeActionNew
	Metadata *CommitMetadata
}

type MergeResultKind int

const (
	MergeResultSuccess     MergeResultKind = iota
	MergeResultNothing                     // already merged
	MergeResultFastForward                 // fast-forwarded
	MergeResultConflict                    // conflicts exist
)

type MergeResult struct {
	Kind      MergeResultKind
	OID       string // commit OID for success
	Conflicts map[string]*MergeConflict
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

func (repo *Repo) Merge(input MergeInput) (*MergeResult, error) {
	// get the current branch name and oid
	headRef, err := repo.readRef("HEAD")
	if err != nil {
		return nil, fmt.Errorf("target not found: %w", err)
	}
	targetName := ""
	if headRef.IsRef {
		targetName = headRef.Ref.Name
	} else {
		targetName = headRef.OID
	}
	targetOIDMaybe, err := repo.readRefRecur(*headRef)
	if err != nil && !errors.Is(err, ErrRefNotFound) {
		return nil, err
	}

	mergeHeadName := mergeHeadNames[0]
	if input.Kind == MergeKindPick {
		mergeHeadName = mergeHeadNames[1]
	}

	cleanDiff := make(map[string]TreeChange)
	conflicts := make(map[string]*MergeConflict)

	switch input.Action {
	case MergeActionNew:
		// make sure there is no unfinished merge in progress
		if err := repo.checkForUnfinishedMerge(); err != nil {
			return nil, err
		}

		// get the source and target oid
		sourceOID, err := repo.readRefRecur(input.Source)
		if err != nil {
			return nil, fmt.Errorf("invalid merge source: %w", err)
		}
		if sourceOID == "" {
			return nil, errors.New("invalid merge source")
		}

		sourceName := ""
		if input.Source.IsRef {
			sourceName = input.Source.Ref.Name
		} else {
			sourceName = input.Source.OID
		}

		targetOID := targetOIDMaybe
		if targetOID == "" {
			// the target branch is completely empty, so just set it to the source oid
			if err := repo.writeRefRecur("HEAD", sourceOID); err != nil {
				return nil, err
			}

			// make a TreeDiff that adds all files from source
			sourceDiff, err := repo.treeDiff("", sourceOID)
			if err != nil {
				return nil, err
			}

			idx, err := repo.readIndex()
			if err != nil {
				return nil, err
			}

			if err := repo.migrate(sourceDiff, idx, true, nil); err != nil {
				return nil, err
			}

			return &MergeResult{Kind: MergeResultFastForward}, nil
		}

		// get the base oid
		var baseOID string
		switch input.Kind {
		case MergeKindFull:
			baseOID, err = commonAncestor(repo, targetOID, sourceOID)
			if err != nil {
				return nil, err
			}
		case MergeKindPick:
			obj, err := repo.NewObject(sourceOID, true)
			if err != nil {
				return nil, err
			}
			if obj.Commit == nil || len(obj.Commit.ParentOIDs) == 0 {
				obj.Close()
				return nil, errors.New("cherry-pick commit must have at least one parent")
			}
			baseOID = obj.Commit.ParentOIDs[0]
			obj.Close()
		}

		// if the base ancestor is the source oid, do nothing
		if sourceOID == baseOID {
			return &MergeResult{Kind: MergeResultNothing}, nil
		}

		// diff the base ancestor with the target oid
		targetDiff, err := repo.treeDiff(baseOID, targetOID)
		if err != nil {
			return nil, err
		}

		// diff the base ancestor with the source oid
		sourceDiff, err := repo.treeDiff(baseOID, sourceOID)
		if err != nil {
			return nil, err
		}

		// look for same path conflicts while populating the clean diff
		for path, sourceChange := range sourceDiff {
			var targetChangeMaybe *TreeChange
			if tc, ok := targetDiff[path]; ok {
				targetChangeMaybe = &tc
			}
			result, err := samePathConflict(repo, baseOID, targetOID, sourceOID, targetName, sourceName, targetChangeMaybe, sourceChange, path)
			if err != nil {
				return nil, err
			}
			if result.Change != nil {
				cleanDiff[path] = *result.Change
			}
			if result.Conflict != nil {
				conflicts[path] = result.Conflict
			}
		}

		// look for file/dir conflicts
		for path, sourceChange := range sourceDiff {
			if sourceChange.New != nil {
				fileDirConflict(path, targetDiff, diffKindTarget, targetName, conflicts, cleanDiff)
			}
		}
		for path, targetChange := range targetDiff {
			if targetChange.New != nil {
				fileDirConflict(path, sourceDiff, diffKindSource, sourceName, conflicts, cleanDiff)
			}
		}

		// create commit message
		metadata := input.Metadata
		if metadata == nil {
			switch input.Kind {
			case MergeKindFull:
				metadata = &CommitMetadata{
					Message: fmt.Sprintf("merge from %s", sourceName),
				}
			case MergeKindPick:
				obj, err := repo.NewObject(sourceOID, true)
				if err != nil {
					return nil, err
				}
				if obj.Commit != nil {
					metadata = &CommitMetadata{
						Message: obj.Commit.Message,
						Author:  obj.Commit.Author,
					}
				} else {
					metadata = &CommitMetadata{Message: "cherry-pick"}
				}
				obj.Close()
			}
		}

		// update the index under a lock
		if err := func() error {
			lock, err := NewLockFile(repo.repoPath, "index")
			if err != nil {
				return err
			}
			defer lock.Close()

			idx, err := repo.readIndex()
			if err != nil {
				return err
			}

			if err := repo.migrate(cleanDiff, idx, true, nil); err != nil {
				return err
			}

			for path, conflict := range conflicts {
				idx.addConflictEntries(path, [3]*TreeEntry{conflict.Base, conflict.Target, conflict.Source})
				if conflict.Renamed != nil {
					repo.objectToFile(conflict.Renamed.Path, conflict.Renamed.TreeEntry)
				}
			}

			if err := idx.Write(lock.File); err != nil {
				return err
			}
			lock.Success = true
			return nil
		}(); err != nil {
			return nil, err
		}

		// exit early if there were conflicts
		if len(conflicts) > 0 {
			repo.writeRef(mergeHeadName, RefOrOid{OID: sourceOID})

			msgPath := filepath.Join(repo.repoPath, mergeMsgName)
			msg := ""
			if metadata != nil {
				msg = metadata.Message
			}
			os.WriteFile(msgPath, []byte(msg), 0644)

			return &MergeResult{
				Kind:      MergeResultConflict,
				Conflicts: conflicts,
			}, nil
		}

		if targetOID == baseOID {
			// the base ancestor is the target oid, so just update HEAD
			if err := repo.writeRefRecur("HEAD", sourceOID); err != nil {
				return nil, err
			}
			return &MergeResult{Kind: MergeResultFastForward}, nil
		}

		// commit the change
		switch input.Kind {
		case MergeKindFull:
			metadata.ParentOIDs = []string{targetOID, sourceOID}
		case MergeKindPick:
			metadata.ParentOIDs = []string{targetOID}
		}
		commitOID, err := repo.writeCommit(*metadata)
		if err != nil {
			return nil, err
		}

		return &MergeResult{Kind: MergeResultSuccess, OID: commitOID}, nil

	case MergeActionCont:
		// ensure there are no conflict entries in the index
		idx, err := repo.readIndex()
		if err != nil {
			return nil, err
		}
		for _, entries := range idx.entries {
			if entries[0] == nil {
				return nil, errors.New("cannot continue merge with unresolved conflicts")
			}
		}

		// make sure there isn't another kind of merge in progress
		if err := checkForOtherMerge(repo, mergeHeadName); err != nil {
			return nil, err
		}

		sourceOID, err := repo.readRefRecur(RefOrOid{IsRef: true, Ref: Ref{Kind: RefNone, Name: mergeHeadName}})
		if err != nil || sourceOID == "" {
			return nil, errors.New("merge head not found")
		}

		targetOID := targetOIDMaybe
		if targetOID == "" {
			return nil, errors.New("target oid not found")
		}

		// read commit message
		metadata := input.Metadata
		if metadata == nil {
			metadata = &CommitMetadata{}
		}
		if metadata.Message == "" {
			msgPath := filepath.Join(repo.repoPath, mergeMsgName)
			data, err := os.ReadFile(msgPath)
			if err != nil {
				return nil, errors.New("merge message not found")
			}
			metadata.Message = string(data)
		}

		sourceName := sourceOID

		// get the base oid
		var baseOID string
		switch input.Kind {
		case MergeKindFull:
			baseOID, err = commonAncestor(repo, targetOID, sourceOID)
			if err != nil {
				return nil, err
			}
		case MergeKindPick:
			obj, err := repo.NewObject(sourceOID, true)
			if err != nil {
				return nil, err
			}
			if obj.Commit == nil || len(obj.Commit.ParentOIDs) == 0 {
				obj.Close()
				return nil, errors.New("cherry-pick commit must have at least one parent")
			}
			baseOID = obj.Commit.ParentOIDs[0]
			obj.Close()
		}
		_ = sourceName

		// clean up the stored merge state
		removeMergeState(repo)

		// commit the change
		switch input.Kind {
		case MergeKindFull:
			metadata.ParentOIDs = []string{targetOID, sourceOID}
		case MergeKindPick:
			metadata.ParentOIDs = []string{targetOID}
		}
		_ = baseOID
		commitOID, err := repo.writeCommit(*metadata)
		if err != nil {
			return nil, err
		}

		return &MergeResult{Kind: MergeResultSuccess, OID: commitOID}, nil

	case MergeActionAbort:
		targetOID := targetOIDMaybe
		if targetOID == "" {
			return nil, errors.New("target oid not found")
		}
		removeMergeState(repo)
		_, err := repo.Switch(SwitchInput{
			Kind:          SwitchKindReset,
			Target:        RefOrOid{OID: targetOID},
			UpdateWorkDir: true,
			Force:         true,
		})
		if err != nil {
			return nil, err
		}
		return &MergeResult{Kind: MergeResultSuccess}, nil
	}

	return nil, errors.New("invalid merge action")
}
