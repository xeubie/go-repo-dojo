package repomofo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type ReceivePackOptions struct {
	ProtocolVersion       int // 0, 1, or 2
	SkipConnectivityCheck bool
	AdvertiseRefs         bool
	IsStateless           bool
}

type refUpdate struct {
	errorMessage string
	skipUpdate   bool
	oldOID       Hash
	newOID       Hash
	refName      string
}

type denyPolicy int

const (
	denyUnconfigured denyPolicy = iota
	denyIgnore
	denyWarn
	denyRefuse
	denyUpdateInstead
)

func parseDenyPolicy(value string) denyPolicy {
	lower := strings.ToLower(value)
	switch lower {
	case "ignore":
		return denyIgnore
	case "warn":
		return denyWarn
	case "refuse":
		return denyRefuse
	case "updateinstead":
		return denyUpdateInstead
	}
	if parseBoolConfig(value) {
		return denyRefuse
	}
	return denyIgnore
}

func parseBoolConfig(value string) bool {
	lower := strings.ToLower(value)
	return lower == "true" || lower == "yes" || lower == "on" || value == "1"
}

type receivePack struct {
	// config
	preferOfsDelta      bool
	isBare              bool
	denyDeletes         bool
	denyNonFastForwards bool
	denyCurrentBranch   denyPolicy
	denyDeleteCurrent   denyPolicy

	// protocol state
	sentCapabilities bool
	useSideband      bool
	reportStatus     bool
	reportStatusV2   bool
	headName         string // empty means no HEAD
}

func newReceivePack() *receivePack {
	return &receivePack{
		preferOfsDelta:    true,
		denyCurrentBranch: denyUnconfigured,
		denyDeleteCurrent: denyUnconfigured,
	}
}

func (rp *receivePack) readConfig(repo *Repo) error {
	config, err := repo.loadConfig()
	if err != nil {
		return err
	}

	if vars := config.GetSection("receive"); vars != nil {
		if v, ok := vars["denydeletes"]; ok {
			rp.denyDeletes = parseBoolConfig(v)
		}
		if v, ok := vars["denynonfastforwards"]; ok {
			rp.denyNonFastForwards = parseBoolConfig(v)
		}
		if v, ok := vars["denycurrentbranch"]; ok {
			rp.denyCurrentBranch = parseDenyPolicy(v)
		}
		if v, ok := vars["denydeletecurrent"]; ok {
			rp.denyDeleteCurrent = parseDenyPolicy(v)
		}
	}
	if vars := config.GetSection("repack"); vars != nil {
		if v, ok := vars["usedeltabaseoffset"]; ok {
			rp.preferOfsDelta = parseBoolConfig(v)
		}
	}
	if vars := config.GetSection("core"); vars != nil {
		if v, ok := vars["bare"]; ok {
			rp.isBare = parseBoolConfig(v)
		}
	}
	return nil
}

func (rp *receivePack) advertiseRef(w io.Writer, hashKind HashKind, path string, oid Hash) error {
	if rp.sentCapabilities {
		line := fmt.Sprintf("%s %s\n", oid.Hex(), path)
		return writePktLine(w, []byte(line))
	}

	caps := "report-status report-status-v2 delete-refs side-band-64k quiet atomic"
	if rp.preferOfsDelta {
		caps += " ofs-delta"
	}
	caps += " object-format=" + hashKind.Name()

	line := fmt.Sprintf("%s %s\x00%s\n", oid.Hex(), path, caps)
	err := writePktLine(w, []byte(line))
	if err == nil {
		rp.sentCapabilities = true
	}
	return err
}

func (rp *receivePack) advertiseRefs(repo *Repo, w io.Writer) error {
	hashKind := repo.opts.Hash

	// HEAD
	headOID, err := repo.ReadHeadRecurMaybe()
	if err == nil && headOID != nil {
		if err := rp.advertiseRef(w, hashKind, "HEAD", headOID); err != nil {
			return err
		}
	}

	// heads
	headsDir := filepath.Join(repo.repoPath, "refs", "heads")
	headIter, err := newRefIterator(headsDir, RefHead)
	if err != nil {
		return err
	}
	defer headIter.Close()
	for {
		ref, err := headIter.Next()
		if err != nil {
			return err
		}
		if ref == nil {
			break
		}
		oid, err := repo.ReadRef(*ref)
		if err != nil || oid == nil {
			continue
		}
		if err := rp.advertiseRef(w, hashKind, ref.ToPath(), oid); err != nil {
			return err
		}
	}

	// tags
	tagsDir := filepath.Join(repo.repoPath, "refs", "tags")
	tagIter, err := newRefIterator(tagsDir, RefTag)
	if err != nil {
		return err
	}
	defer tagIter.Close()
	for {
		ref, err := tagIter.Next()
		if err != nil {
			return err
		}
		if ref == nil {
			break
		}
		oid, err := repo.ReadRef(*ref)
		if err != nil || oid == nil {
			continue
		}
		if err := rp.advertiseRef(w, hashKind, ref.ToPath(), oid); err != nil {
			return err
		}
	}

	// if no refs were advertised, send capabilities line with zero OID
	if !rp.sentCapabilities {
		nullOID := hashKind.NullHash()
		if err := rp.advertiseRef(w, hashKind, "capabilities^{}", nullOID); err != nil {
			return err
		}
	}

	return writePktFlush(w)
}

func (rp *receivePack) readRefUpdates(hashKind HashKind, r io.Reader) ([]refUpdate, error) {
	hexLen := hashKind.HexLen()
	var updates []refUpdate

	for {
		line, err := readPktLine(r)
		if err != nil {
			return nil, err
		}
		if line == nil {
			break // flush
		}

		// split on null byte for features
		nullPos := -1
		for i, b := range line {
			if b == 0 {
				nullPos = i
				break
			}
		}

		lineData := line
		if nullPos >= 0 {
			lineData = line[:nullPos]
			if nullPos < len(line) {
				features := string(line[nullPos+1:])
				if hasFeature(features, "report-status") {
					rp.reportStatus = true
				}
				if hasFeature(features, "report-status-v2") {
					rp.reportStatusV2 = true
				}
				if hasFeature(features, "side-band-64k") {
					rp.useSideband = true
				}
				objHash := getFeatureValue(features, "object-format")
				if objHash == "" {
					objHash = "sha1"
				}
				if objHash != hashKind.Name() {
					return nil, fmt.Errorf("unsupported object format: %s", objHash)
				}
			}
		}

		// parse "<old_oid> <new_oid> <refname>"
		minLen := hexLen + 1 + hexLen + 1
		if len(lineData) < minLen {
			return nil, fmt.Errorf("invalid ref update line")
		}
		oldOID, err := hashKind.HashFromHex(string(lineData[:hexLen]))
		if err != nil {
			return nil, fmt.Errorf("invalid old OID in ref update line: %w", err)
		}
		if lineData[hexLen] != ' ' {
			return nil, fmt.Errorf("invalid ref update line")
		}
		newOID, err := hashKind.HashFromHex(string(lineData[hexLen+1 : hexLen+1+hexLen]))
		if err != nil {
			return nil, fmt.Errorf("invalid new OID in ref update line: %w", err)
		}
		if lineData[hexLen+1+hexLen] != ' ' {
			return nil, fmt.Errorf("invalid ref update line")
		}
		refName := string(lineData[hexLen+1+hexLen+1:])

		updates = append(updates, refUpdate{
			oldOID:  oldOID,
			newOID:  newOID,
			refName: refName,
		})
	}

	if !rp.useSideband {
		return nil, fmt.Errorf("sideband protocol not supported by client")
	}

	return updates, nil
}

func (rp *receivePack) executeRefUpdates(w io.Writer, repo *Repo, updates []refUpdate, options ReceivePackOptions) error {
	if !options.SkipConnectivityCheck {
		allConnected := func() bool {
			iter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterAll})
			for i := range updates {
				if !updates[i].newOID.IsNull() && !updates[i].skipUpdate {
					iter.Include(updates[i].newOID)
				}
			}
			for {
				obj, err := iter.Next()
				if err != nil {
					return false
				}
				if obj == nil {
					break
				}
				obj.Close()
			}
			return true
		}()

		if !allConnected {
			for i := range updates {
				if updates[i].newOID.IsNull() {
					continue
				}
				connected := func() bool {
					iter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterAll})
					iter.Include(updates[i].newOID)
					for {
						obj, err := iter.Next()
						if err != nil {
							return false
						}
						if obj == nil {
							break
						}
						obj.Close()
					}
					return true
				}()
				if !connected {
					updates[i].errorMessage = "missing necessary objects"
				}
			}
		}
	}

	// skip if all ref updates already have errors
	allErrors := true
	for i := range updates {
		if updates[i].errorMessage == "" {
			allErrors = false
			break
		}
	}
	if allErrors {
		return nil
	}

	// detect conflicting symref updates
	refIndex := make(map[string]int, len(updates))
	for i := range updates {
		refIndex[updates[i].refName] = i
	}
	for i := range updates {
		if updates[i].errorMessage != "" {
			continue
		}
		result, err := repo.readRef(updates[i].refName)
		if err != nil {
			continue // ref doesn't exist yet, no symref conflict possible
		}
		rv, ok := result.(RefValue)
		if !ok {
			continue // not a symref
		}
		dstName := rv.Ref.ToPath()
		dstIdx, found := refIndex[dstName]
		if !found {
			continue
		}
		dst := &updates[dstIdx]

		updates[i].skipUpdate = true

		if HashEqual(updates[i].oldOID, dst.oldOID) && HashEqual(updates[i].newOID, dst.newOID) {
			continue
		}

		dst.skipUpdate = true

		abbrev := 7
		oldAbbrevI := updates[i].oldOID.Hex()
		if len(oldAbbrevI) > abbrev {
			oldAbbrevI = oldAbbrevI[:abbrev]
		}
		newAbbrevI := updates[i].newOID.Hex()
		if len(newAbbrevI) > abbrev {
			newAbbrevI = newAbbrevI[:abbrev]
		}
		oldAbbrevD := dst.oldOID.Hex()
		if len(oldAbbrevD) > abbrev {
			oldAbbrevD = oldAbbrevD[:abbrev]
		}
		newAbbrevD := dst.newOID.Hex()
		if len(newAbbrevD) > abbrev {
			newAbbrevD = newAbbrevD[:abbrev]
		}

		writeReceiveError(w, fmt.Sprintf(
			"refusing inconsistent update between symref '%s' (%s..%s) and its target '%s' (%s..%s)",
			updates[i].refName, oldAbbrevI, newAbbrevI,
			dst.refName, oldAbbrevD, newAbbrevD,
		))

		updates[i].errorMessage = "inconsistent aliased update"
		dst.errorMessage = "inconsistent aliased update"
	}

	// read HEAD to know current branch
	headResult, err := repo.readRef("HEAD")
	if rv, ok := headResult.(RefValue); err == nil && ok {
		rp.headName = rv.Ref.ToPath()
	}

	for i := range updates {
		if updates[i].errorMessage != "" || updates[i].skipUpdate {
			continue
		}
		errMsg := rp.applyRefUpdate(w, repo, &updates[i])
		if errMsg != "" {
			updates[i].errorMessage = errMsg
		}
	}
	return nil
}

func (rp *receivePack) applyRefUpdate(w io.Writer, repo *Repo, update *refUpdate) string {
	name := update.refName

	nameAfterRefs := ""
	if strings.HasPrefix(name, "refs/") {
		nameAfterRefs = name[len("refs/"):]
	}

	if !strings.HasPrefix(name, "refs/") ||
		!validateRefName(nameAfterRefs) ||
		(!update.newOID.IsNull() && !strings.Contains(nameAfterRefs, "/")) {
		writeReceiveError(w, fmt.Sprintf("refusing to update funny ref '%s' remotely", name))
		return "funny refname"
	}

	shouldUpdateWorktree := false

	if rp.headName != "" && name == rp.headName {
		switch rp.denyCurrentBranch {
		case denyIgnore:
			// ok
		case denyWarn:
			writeReceiveWarning(w, "updating the current branch")
		case denyRefuse, denyUnconfigured:
			writeReceiveError(w, fmt.Sprintf("refusing to update checked out branch: %s", name))
			if rp.denyCurrentBranch == denyUnconfigured {
				writeReceiveError(w, denyCurrentBranchMsg)
			}
			return "branch is currently checked out"
		case denyUpdateInstead:
			shouldUpdateWorktree = true
		}
	}

	if !update.newOID.IsNull() {
		_, err := repo.NewObject(update.newOID, false)
		if err != nil {
			return "bad pack"
		}
	}

	if !update.oldOID.IsNull() && update.newOID.IsNull() {
		if rp.denyDeletes && strings.HasPrefix(name, "refs/heads/") {
			writeReceiveError(w, fmt.Sprintf("denying ref deletion for %s", name))
			return "deletion prohibited"
		}

		if rp.headName != "" && name == rp.headName {
			switch rp.denyDeleteCurrent {
			case denyIgnore:
				// ok
			case denyWarn:
				writeReceiveWarning(w, "deleting the current branch")
			case denyRefuse, denyUnconfigured, denyUpdateInstead:
				if rp.denyDeleteCurrent == denyUnconfigured {
					writeReceiveError(w, denyDeleteCurrentMsg)
				}
				writeReceiveError(w, fmt.Sprintf("refusing to delete the current branch: %s", name))
				return "deletion of the current branch prohibited"
			}
		}
	}

	if rp.denyNonFastForwards && !update.newOID.IsNull() &&
		!update.oldOID.IsNull() && strings.HasPrefix(name, "refs/heads/") {
		descendent, err := getDescendent(repo, update.oldOID, update.newOID)
		if err != nil {
			return "bad ref"
		}
		if !HashEqual(descendent, update.newOID) {
			writeReceiveError(w, fmt.Sprintf("denying non-fast-forward %s (you should pull first)", name))
			return "non-fast-forward"
		}
	}

	if shouldUpdateWorktree {
		if rp.isBare {
			return "denyCurrentBranch = updateInstead needs a worktree"
		}
		_, err := repo.ResetDir(ResetInput{
			Target:        OIDValue{OID: update.newOID},
			Force:         true,
		})
		if err != nil {
			return "failed to update worktree"
		}
	}

	if update.newOID.IsNull() {
		if err := repo.removeRef(name); err != nil {
			return "failed to remove ref"
		}
	} else {
		if err := repo.writeRef(name, OIDValue{OID: update.newOID}); err != nil {
			return "failed to write ref"
		}
	}

	return ""
}

func hasFeature(features, name string) bool {
	for _, f := range strings.Split(features, " ") {
		if f == name || strings.HasPrefix(f, name+"=") {
			return true
		}
	}
	return false
}

func getFeatureValue(features, name string) string {
	for _, f := range strings.Split(features, " ") {
		if strings.HasPrefix(f, name+"=") {
			return f[len(name)+1:]
		}
	}
	return ""
}

func writeReceiveMessage(w io.Writer, prefix, msg string) {
	data := []byte(prefix + msg + "\n")
	sendSideband(w, 2, data)
}

func writeReceiveWarning(w io.Writer, msg string) {
	writeReceiveMessage(w, "warning: ", msg)
}

func writeReceiveError(w io.Writer, msg string) {
	writeReceiveMessage(w, "error: ", msg)
}

// removeRef removes a ref file from the repo.
func (repo *Repo) removeRef(refPath string) error {
	fullPath := filepath.Join(repo.repoPath, refPath)
	err := os.Remove(fullPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Runs the server-side receive-pack protocol to accept objects from a client.
func (repo *Repo) ReceivePack(r io.Reader, w io.Writer, options ReceivePackOptions) error {
	rp := newReceivePack()

	if err := rp.readConfig(repo); err != nil {
		return err
	}

	if options.ProtocolVersion == 1 {
		if options.AdvertiseRefs || !options.IsStateless {
			if err := writePktLine(w, []byte("version 1\n")); err != nil {
				return err
			}
		}
	}

	if options.AdvertiseRefs || !options.IsStateless {
		if err := rp.advertiseRefs(repo, w); err != nil {
			return err
		}
	}
	if options.AdvertiseRefs {
		return nil
	}

	updates, err := rp.readRefUpdates(repo.opts.Hash, r)
	if err != nil {
		return err
	}

	if len(updates) != 0 {
		// check if this is a delete-only push
		deleteOnly := true
		for _, u := range updates {
			if !u.newOID.IsNull() {
				deleteOnly = false
				break
			}
		}

		if !deleteOnly {
			packReader := NewStreamPackReader(r, repo.opts.bufferSize())
			defer packReader.Close()

			iter, err := NewPackIterator(packReader)
			if err != nil {
				return err
			}

			if err := repo.CopyFromPackIterator(iter); err != nil {
				return err
			}
		}

		if err := rp.executeRefUpdates(w, repo, updates, options); err != nil {
			return err
		}

		if rp.reportStatusV2 || rp.reportStatus {
			var buf []byte
			bufPktLine(&buf, []byte("unpack ok\n"))

			for _, u := range updates {
				if u.errorMessage != "" {
					bufPktLine(&buf, []byte(fmt.Sprintf("ng %s %s\n", u.refName, u.errorMessage)))
				} else {
					bufPktLine(&buf, []byte(fmt.Sprintf("ok %s\n", u.refName)))
				}
			}
			bufPktFlush(&buf)

			if err := sendSideband(w, 1, buf); err != nil {
				return err
			}
		}
	}

	return writePktFlush(w)
}

const denyCurrentBranchMsg = `By default, updating the current branch in a non-bare repository
is denied, because it will make the index and work tree inconsistent
with what you pushed, and will require 'git reset --hard' to match
the work tree to HEAD.

You can set the 'receive.denyCurrentBranch' configuration variable
to 'ignore' or 'warn' in the remote repository to allow pushing into
its current branch; however, this is not recommended unless you
arranged to update its work tree to match what you pushed in some
other way.

To squelch this message and still keep the default behaviour, set
'receive.denyCurrentBranch' configuration variable to 'refuse'.`

const denyDeleteCurrentMsg = `By default, deleting the current branch is denied, because the next
'git clone' won't result in any file checked out, causing confusion.

You can set 'receive.denyDeleteCurrent' configuration variable to
'warn' or 'ignore' in the remote repository to allow deleting the
current branch, with or without a warning message.

To squelch this message, you can set it to 'refuse'.`
