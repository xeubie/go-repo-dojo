package repomofo

import (
	"container/heap"
	"errors"
)

var errDescendentNotFound = errors.New("descendent not found")

type commitParentKind int

const (
	commitParentOne commitParentKind = iota
	commitParentTwo
)

type commitParent struct {
	oid       string
	kind      commitParentKind
	timestamp uint64
}

// commitParentsQueue is a max-heap ordered by timestamp (newest first).
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

// getDescendent determines which of oid1 or oid2 is a descendant of the other
// by walking both commit histories simultaneously. Returns the descendant OID,
// or errDescendentNotFound if neither is an ancestor of the other.
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
