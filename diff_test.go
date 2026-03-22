package repomofo

import (
	"testing"
)

func TestMyersDiff(t *testing.T) {
	{
		lines1 := "A\nB\nC\nA\nB\nB\nA"
		lines2 := "C\nB\nA\nB\nA\nC"
		lineIter1 := newLineIteratorFromBuffer(lines1)
		lineIter2 := newLineIteratorFromBuffer(lines2)
		expectedDiff := []edit{
			{kind: editDel, oldLineNum: 0},
			{kind: editIns, newLineNum: 0},
			{kind: editEql, oldLineNum: 1, newLineNum: 1},
			{kind: editDel, oldLineNum: 2},
			{kind: editEql, oldLineNum: 3, newLineNum: 2},
			{kind: editEql, oldLineNum: 4, newLineNum: 3},
			{kind: editDel, oldLineNum: 5},
			{kind: editEql, oldLineNum: 6, newLineNum: 4},
			{kind: editIns, newLineNum: 5},
		}
		myersDiffIter := newMyersDiffIterator(lineIter1, lineIter2)
		var actualDiff []edit
		for {
			edit, err := myersDiffIter.next()
			if err != nil {
				t.Fatal(err)
			}
			if edit == nil {
				break
			}
			actualDiff = append(actualDiff, *edit)
		}
		if len(expectedDiff) != len(actualDiff) {
			t.Fatalf("expected %d edits, got %d", len(expectedDiff), len(actualDiff))
		}
		for i, expected := range expectedDiff {
			actual := actualDiff[i]
			if expected.kind != actual.kind || expected.oldLineNum != actual.oldLineNum || expected.newLineNum != actual.newLineNum {
				t.Fatalf("edit %d: expected %+v, got %+v", i, expected, actual)
			}
		}
	}
	{
		lines1 := "hello, world!"
		lines2 := "goodbye, world!"
		lineIter1 := newLineIteratorFromBuffer(lines1)
		lineIter2 := newLineIteratorFromBuffer(lines2)
		expectedDiff := []edit{
			{kind: editDel, oldLineNum: 0},
			{kind: editIns, newLineNum: 0},
		}
		myersDiffIter := newMyersDiffIterator(lineIter1, lineIter2)
		var actualDiff []edit
		for {
			edit, err := myersDiffIter.next()
			if err != nil {
				t.Fatal(err)
			}
			if edit == nil {
				break
			}
			actualDiff = append(actualDiff, *edit)
		}
		if len(expectedDiff) != len(actualDiff) {
			t.Fatalf("expected %d edits, got %d", len(expectedDiff), len(actualDiff))
		}
		for i, expected := range expectedDiff {
			actual := actualDiff[i]
			if expected.kind != actual.kind || expected.oldLineNum != actual.oldLineNum || expected.newLineNum != actual.newLineNum {
				t.Fatalf("edit %d: expected %+v, got %+v", i, expected, actual)
			}
		}
	}
}

func TestDiff3(t *testing.T) {
	origLines := "celery\ngarlic\nonions\nsalmon\ntomatoes\nwine"
	aliceLines := "celery\nsalmon\ntomatoes\ngarlic\nonions\nwine\nbeer"
	bobLines := "celery\nsalmon\ngarlic\nonions\ntomatoes\nwine\nbeer"

	origIter := newLineIteratorFromBuffer(origLines)
	aliceIter := newLineIteratorFromBuffer(aliceLines)
	bobIter := newLineIteratorFromBuffer(bobLines)
	diff3Iter := newDiff3Iterator(origIter, aliceIter, bobIter)

	chunk, err := diff3Iter.next()
	if err != nil {
		t.Fatal(err)
	}
	if chunk == nil || chunk.Kind != diff3Clean {
		t.Fatalf("expected clean chunk, got %+v", chunk)
	}

	chunk, err = diff3Iter.next()
	if err != nil {
		t.Fatal(err)
	}
	if chunk == nil || chunk.Kind != diff3Conflict {
		t.Fatalf("expected conflict chunk, got %+v", chunk)
	}

	chunk, err = diff3Iter.next()
	if err != nil {
		t.Fatal(err)
	}
	if chunk == nil || chunk.Kind != diff3Clean {
		t.Fatalf("expected clean chunk, got %+v", chunk)
	}

	chunk, err = diff3Iter.next()
	if err != nil {
		t.Fatal(err)
	}
	if chunk == nil || chunk.Kind != diff3Conflict {
		t.Fatalf("expected conflict chunk, got %+v", chunk)
	}

	chunk, err = diff3Iter.next()
	if err != nil {
		t.Fatal(err)
	}
	if chunk == nil || chunk.Kind != diff3Clean {
		t.Fatalf("expected clean chunk, got %+v", chunk)
	}

	// this is a conflict even though a and b are both "beer",
	// because the original does not contain it.
	// it is only marked as clean if all three are matches.
	// when outputting the conflict lines this should be
	// auto-resolved since we can compare a and b at that point.
	chunk, err = diff3Iter.next()
	if err != nil {
		t.Fatal(err)
	}
	if chunk == nil || chunk.Kind != diff3Conflict {
		t.Fatalf("expected conflict chunk, got %+v", chunk)
	}

	chunk, err = diff3Iter.next()
	if err != nil {
		t.Fatal(err)
	}
	if chunk != nil {
		t.Fatalf("expected nil, got %+v", chunk)
	}
}
