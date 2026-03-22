package repomofo

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

type UploadPackOptions struct {
	ProtocolVersion int // 0, 1, or 2
	AdvertiseRefs   bool
	IsStateless     bool
}

type symref struct {
	name   string
	target string
}

type multiAck int

const (
	multiAckNone     multiAck = iota
	multiAckBasic             // multi_ack
	multiAckDetailed          // multi_ack_detailed
)

type filterOptions int

const (
	filterNone filterOptions = iota
	filterBlobNone
	filterBlobLimit
	filterTreeDepth
)

type allowUor struct {
	tipSHA1       bool
	reachableSHA1 bool
	anySHA1       bool
}

type uploadPackSession struct {
	// config
	allowUor          allowUor
	allowFilter       bool
	allowRefInWant    bool
	allowSidebandAll  bool
	allowPackfileURIs bool

	// capabilities negotiated with client
	multiAck           multiAck
	useSideband        bool
	writerUseSideband  bool
	noDone             bool
	isStateless        bool
	filterCapRequested bool
	sentCapabilities   bool
	symrefs            []symref

	// shallow/deepen state
	depth          int
	deepenSince    uint64
	deepenRevList  bool
	deepenRelative bool

	// request state
	filterOpts  filterOptions
	waitForDone bool
	done        bool
	seenHaves   bool
}

func newUploadPackSession() *uploadPackSession {
	return &uploadPackSession{}
}

func (up *uploadPackSession) readConfig(repo *Repo) error {
	config, err := repo.loadConfig()
	if err != nil {
		return err
	}

	if vars := config.GetSection("uploadpack"); vars != nil {
		if v, ok := vars["allowtipsha1inwant"]; ok {
			up.allowUor.tipSHA1 = parseBoolConfig(v)
		}
		if v, ok := vars["allowreachablesha1inwant"]; ok {
			up.allowUor.reachableSHA1 = parseBoolConfig(v)
		}
		if v, ok := vars["allowanysha1inwant"]; ok {
			allow := parseBoolConfig(v)
			up.allowUor.tipSHA1 = allow
			up.allowUor.reachableSHA1 = allow
			up.allowUor.anySHA1 = allow
		}
		if v, ok := vars["allowfilter"]; ok {
			up.allowFilter = parseBoolConfig(v)
		}
		if v, ok := vars["allowrefinwant"]; ok {
			up.allowRefInWant = parseBoolConfig(v)
		}
		if v, ok := vars["allowsidebandall"]; ok {
			up.allowSidebandAll = parseBoolConfig(v)
		}
		if _, ok := vars["blobpackfileuri"]; ok {
			up.allowPackfileURIs = true
		}
	}
	return nil
}

// writeV0Ref writes a ref advertisement line for v0/v1 protocol.
func (up *uploadPackSession) writeV0Ref(repo *Repo, w io.Writer, ourRefs map[string]bool, refName string, oid string) error {
	ourRefs[oid] = true

	if !up.sentCapabilities {
		caps := "multi_ack thin-pack side-band side-band-64k ofs-delta shallow deepen-since deepen-not deepen-relative no-progress include-tag multi_ack_detailed"
		if up.allowUor.tipSHA1 {
			caps += " allow-tip-sha1-in-want"
		}
		if up.allowUor.reachableSHA1 {
			caps += " allow-reachable-sha1-in-want"
		}
		if up.noDone {
			caps += " no-done"
		}
		for _, sr := range up.symrefs {
			caps += fmt.Sprintf(" symref=%s:%s", sr.name, sr.target)
		}
		if up.allowFilter {
			caps += " filter"
		}
		caps += fmt.Sprintf(" object-format=%s agent=git/2.51.2", repo.opts.Hash.Name())

		line := fmt.Sprintf("%s %s\x00%s\n", oid, refName, caps)
		if err := writePktLine(w, []byte(line)); err != nil {
			return err
		}
		up.sentCapabilities = true
	} else {
		line := fmt.Sprintf("%s %s\n", oid, refName)
		if err := writePktLine(w, []byte(line)); err != nil {
			return err
		}
	}

	// peel tags
	peeledOID, peeled := peelToNonTag(repo, oid)
	if peeled {
		line := fmt.Sprintf("%s %s^{}\n", peeledOID, refName)
		if err := writePktLine(w, []byte(line)); err != nil {
			return err
		}
	}

	return nil
}

func peelToNonTag(repo *Repo, oid string) (string, bool) {
	orig := oid
	for i := 0; i < 64; i++ {
		obj, err := repo.NewObject(oid, true)
		if err != nil {
			return oid, oid != orig
		}
		defer obj.Close()
		if obj.Kind != ObjectKindTag || obj.Tag == nil {
			return oid, oid != orig
		}
		oid = obj.Tag.Target
	}
	return oid, oid != orig
}

func (up *uploadPackSession) receiveNeeds(
	w io.Writer, repo *Repo,
	ourRefs map[string]bool,
	r io.Reader,
	shallowOIDs map[string]bool,
	deepenNot map[string]bool,
	wantObj *[]string,
) error {
	hexLen := repo.opts.Hash.HexLen()
	wantedOIDs := make(map[string]bool)

	for {
		line, err := readPktLine(r)
		if err != nil {
			return err
		}
		if line == nil {
			break // flush
		}
		lineStr := string(line)

		if oid, ok := processShallow(repo, lineStr); ok {
			if oid != "" {
				shallowOIDs[oid] = true
			}
			continue
		}
		if val, ok := processDeepen(lineStr); ok {
			up.depth = val
			continue
		}
		if val, ok := processDeepenSince(lineStr); ok {
			up.deepenSince = val
			up.deepenRevList = true
			continue
		}
		if oid, ok := processDeepenNot(repo, lineStr); ok {
			deepenNot[oid] = true
			up.deepenRevList = true
			continue
		}

		if strings.HasPrefix(lineStr, "filter ") {
			if !up.filterCapRequested {
				return fmt.Errorf("filter not negotiated")
			}
			continue
		}

		if !strings.HasPrefix(lineStr, "want ") {
			return fmt.Errorf("protocol error: expected want line")
		}

		afterWant := lineStr[5:]
		if len(afterWant) < hexLen {
			return fmt.Errorf("protocol error: expected OID")
		}
		oidStr := afterWant[:hexLen]
		features := afterWant[hexLen:]

		if hasFeature(features, "deepen-relative") {
			up.deepenRelative = true
		}
		if hasFeature(features, "multi_ack_detailed") {
			up.multiAck = multiAckDetailed
		} else if hasFeature(features, "multi_ack") {
			up.multiAck = multiAckBasic
		}
		if hasFeature(features, "no-done") {
			up.noDone = true
		}
		if hasFeature(features, "side-band-64k") || hasFeature(features, "side-band") {
			up.useSideband = true
		}
		if up.allowFilter && hasFeature(features, "filter") {
			up.filterCapRequested = true
		}

		if !objectExists(repo, oidStr) {
			writePktError(w, up.writerUseSideband, fmt.Sprintf("upload-pack: not our ref %s", oidStr))
			return fmt.Errorf("client error")
		}
		if !wantedOIDs[oidStr] {
			wantedOIDs[oidStr] = true
			*wantObj = append(*wantObj, oidStr)
		}
	}

	if up.depth == 0 && !up.deepenRevList && len(shallowOIDs) == 0 {
		return nil
	}

	sent, err := up.sendShallowList(w, repo, ourRefs, shallowOIDs, deepenNot, wantObj)
	if err != nil {
		return err
	}
	if sent {
		return writePktFlush(w)
	}
	return nil
}

func (up *uploadPackSession) sendShallowList(
	w io.Writer, repo *Repo,
	ourRefs map[string]bool,
	shallowOIDs map[string]bool,
	deepenNot map[string]bool,
	wantObj *[]string,
) (bool, error) {
	if up.depth > 0 && up.deepenRevList {
		return false, fmt.Errorf("conflicting deepen options")
	}

	if up.depth > 0 {
		if err := up.deepen(w, repo, ourRefs, up.depth, shallowOIDs, wantObj); err != nil {
			return false, err
		}
		return true, nil
	} else if up.deepenRevList {
		if err := up.deepenByRevList(w, repo, shallowOIDs, deepenNot, wantObj); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

func (up *uploadPackSession) deepen(
	w io.Writer, repo *Repo,
	ourRefs map[string]bool,
	depth int,
	shallowOIDs map[string]bool,
	wantObj *[]string,
) error {
	notShallowOIDs := make(map[string]bool)
	infiniteDepth := math.MaxInt

	if depth == infiniteDepth {
		for oid := range shallowOIDs {
			notShallowOIDs[oid] = true
		}
	} else if up.deepenRelative {
		// find reachable shallows
		reachableShallows := getReachableShallows(repo, shallowOIDs, ourRefs)
		maxDepth := depth
		depthIter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit, MaxDepth: &maxDepth, Full: true})
		for _, oid := range reachableShallows {
			depthIter.IncludeAtDepth(oid, 0)
		}
		for {
			obj, err := depthIter.Next()
			if err != nil {
				return err
			}
			if obj == nil {
				break
			}
			if depthIter.Depth == depth {
				if !shallowOIDs[obj.OID] && !notShallowOIDs[obj.OID] {
					writePktResponse(w, up.writerUseSideband, fmt.Sprintf("shallow %s\n", obj.OID))
				}
			} else {
				notShallowOIDs[obj.OID] = true
			}
			obj.Close()
		}
	} else {
		maxDepth := depth - 1
		depthIter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit, MaxDepth: &maxDepth, Full: true})
		for _, oid := range *wantObj {
			depthIter.IncludeAtDepth(oid, 0)
		}
		for {
			obj, err := depthIter.Next()
			if err != nil {
				return err
			}
			if obj == nil {
				break
			}
			if depthIter.Depth == maxDepth {
				if !shallowOIDs[obj.OID] && !notShallowOIDs[obj.OID] {
					writePktResponse(w, up.writerUseSideband, fmt.Sprintf("shallow %s\n", obj.OID))
				}
			} else {
				notShallowOIDs[obj.OID] = true
			}
			obj.Close()
		}
	}

	return up.sendUnshallow(w, repo, shallowOIDs, notShallowOIDs, wantObj)
}

func (up *uploadPackSession) deepenByRevList(
	w io.Writer, repo *Repo,
	shallowOIDs map[string]bool,
	deepenNot map[string]bool,
	wantObj *[]string,
) error {
	objIter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit, Full: true})

	// build exclude set from deepen-not refs
	if len(deepenNot) > 0 {
		excludeIter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit})
		for oid := range deepenNot {
			excludeIter.Include(oid)
		}
		for {
			obj, err := excludeIter.Next()
			if err != nil {
				return err
			}
			if obj == nil {
				break
			}
			obj.Close()
		}
		// transfer excludes
		for oid := range excludeIter.Excludes {
			objIter.Excludes[oid] = true
		}
	}

	for _, oid := range *wantObj {
		objIter.Include(oid)
	}

	notShallowOIDs := make(map[string]bool)
	type parentEntry struct {
		oid     string
		parents []string
	}
	var reachableCommits []parentEntry

	for {
		obj, err := objIter.Next()
		if err != nil {
			return err
		}
		if obj == nil {
			break
		}

		if up.deepenSince != 0 && obj.Commit != nil && obj.Commit.Timestamp < up.deepenSince {
			obj.Close()
			continue
		}

		notShallowOIDs[obj.OID] = true

		var parents []string
		if obj.Commit != nil {
			parents = append([]string{}, obj.Commit.ParentOIDs...)
		}
		reachableCommits = append(reachableCommits, parentEntry{oid: obj.OID, parents: parents})
		obj.Close()
	}

	if len(notShallowOIDs) == 0 {
		return fmt.Errorf("no commits for shallow")
	}

	// boundary: commits with at least one parent not in notShallowOIDs
	var boundaryOIDs []string
	for _, entry := range reachableCommits {
		for _, parent := range entry.parents {
			if !notShallowOIDs[parent] {
				boundaryOIDs = append(boundaryOIDs, entry.oid)
				break
			}
		}
	}

	// send shallow markers
	for _, oid := range boundaryOIDs {
		delete(notShallowOIDs, oid)
		if !shallowOIDs[oid] {
			writePktResponse(w, up.writerUseSideband, fmt.Sprintf("shallow %s\n", oid))
		}
	}

	return up.sendUnshallow(w, repo, shallowOIDs, notShallowOIDs, wantObj)
}

func (up *uploadPackSession) sendUnshallow(
	w io.Writer, repo *Repo,
	shallowOIDs map[string]bool,
	notShallowOIDs map[string]bool,
	wantObj *[]string,
) error {
	for oid := range shallowOIDs {
		if notShallowOIDs[oid] {
			writePktResponse(w, up.writerUseSideband, fmt.Sprintf("unshallow %s\n", oid))
			obj, err := repo.NewObject(oid, true)
			if err != nil {
				continue
			}
			if obj.Commit != nil {
				for _, parent := range obj.Commit.ParentOIDs {
					*wantObj = append(*wantObj, parent)
				}
			}
			obj.Close()
		}
	}
	return nil
}

func (up *uploadPackSession) getCommonCommits(
	repo *Repo, w io.Writer, r io.Reader,
	haveObj *[]string, wantObj *[]string,
	firstLine []byte,
) error {
	hexLen := repo.opts.Hash.HexLen()
	var lastHex string
	gotCommon := false
	gotOther := false
	sentReady := false
	pendingLine := firstLine

	for {
		var lineData []byte
		if pendingLine != nil {
			lineData = pendingLine
			pendingLine = nil
		} else {
			data, err := readPktLine(r)
			if err != nil {
				return err
			}
			lineData = data
		}

		if lineData == nil {
			// flush
			if up.multiAck == multiAckDetailed && gotCommon && !gotOther &&
				allWantsReachable(repo, haveObj, wantObj) {
				sentReady = true
				if err := writePktLine(w, []byte(fmt.Sprintf("ACK %s ready\n", lastHex))); err != nil {
					return err
				}
			}
			if len(*haveObj) == 0 || up.multiAck != multiAckNone {
				if err := writePktLine(w, []byte("NAK\n")); err != nil {
					return err
				}
			}

			if up.noDone && sentReady {
				return writePktLine(w, []byte(fmt.Sprintf("ACK %s\n", lastHex)))
			}
			if up.isStateless {
				return fmt.Errorf("stateless service done")
			}
			gotCommon = false
			gotOther = false
			continue
		}

		line := string(lineData)

		if strings.HasPrefix(line, "have ") {
			haveArg := line[5:]
			if len(haveArg) < hexLen {
				return fmt.Errorf("protocol error: expected sha1")
			}
			haveHex := haveArg[:hexLen]
			if appendIfExists(repo, haveHex, haveObj) {
				gotCommon = true
				lastHex = haveHex
				if up.multiAck == multiAckDetailed {
					if err := writePktLine(w, []byte(fmt.Sprintf("ACK %s common\n", lastHex))); err != nil {
						return err
					}
				} else if up.multiAck != multiAckNone {
					if err := writePktLine(w, []byte(fmt.Sprintf("ACK %s continue\n", lastHex))); err != nil {
						return err
					}
				} else if len(*haveObj) == 1 {
					if err := writePktLine(w, []byte(fmt.Sprintf("ACK %s\n", lastHex))); err != nil {
						return err
					}
				}
			} else {
				gotOther = true
				if up.multiAck != multiAckNone && allWantsReachable(repo, haveObj, wantObj) {
					if up.multiAck == multiAckDetailed {
						sentReady = true
						if err := writePktLine(w, []byte(fmt.Sprintf("ACK %s ready\n", haveHex))); err != nil {
							return err
						}
					} else {
						if err := writePktLine(w, []byte(fmt.Sprintf("ACK %s continue\n", haveHex))); err != nil {
							return err
						}
					}
				}
			}
			continue
		}
		if line == "done" {
			if len(*haveObj) > 0 {
				if up.multiAck != multiAckNone {
					return writePktLine(w, []byte(fmt.Sprintf("ACK %s\n", lastHex)))
				}
				return nil
			}
			return writePktLine(w, []byte("NAK\n"))
		}
		return fmt.Errorf("protocol error: expected sha1")
	}
}

// --- v2 protocol ---

type v2Config struct {
	advertiseSID        bool
	advertiseObjectInfo bool
	advertiseBundleURIs bool
	advertiseUnborn     bool
	allowFilter         bool
	allowRefInWant      bool
	allowSidebandAll    bool
	allowPackfileURIs   bool
}

func (repo *Repo) UploadPack(r io.Reader, w io.Writer, options UploadPackOptions) error {
	switch options.ProtocolVersion {
	case 2:
		v2cfg := v2Config{advertiseUnborn: true}
		config, err := repo.loadConfig()
		if err != nil {
			return err
		}
		if vars := config.GetSection("uploadpack"); vars != nil {
			if v, ok := vars["allowfilter"]; ok {
				v2cfg.allowFilter = parseBoolConfig(v)
			}
			if v, ok := vars["allowrefinwant"]; ok {
				v2cfg.allowRefInWant = parseBoolConfig(v)
			}
			if v, ok := vars["allowsidebandall"]; ok {
				v2cfg.allowSidebandAll = parseBoolConfig(v)
			}
			if _, ok := vars["blobpackfileuri"]; ok {
				v2cfg.allowPackfileURIs = true
			}
			if v, ok := vars["advertisebundleuris"]; ok {
				v2cfg.advertiseBundleURIs = parseBoolConfig(v)
			}
		}
		if vars := config.GetSection("lsrefs"); vars != nil {
			if v, ok := vars["unborn"]; ok {
				v2cfg.advertiseUnborn = (v == "advertise")
			}
		}
		if vars := config.GetSection("transfer"); vars != nil {
			if v, ok := vars["advertisesid"]; ok {
				v2cfg.advertiseSID = parseBoolConfig(v)
			}
			if v, ok := vars["advertiseobjectinfo"]; ok {
				v2cfg.advertiseObjectInfo = parseBoolConfig(v)
			}
		}

		if options.AdvertiseRefs {
			return protocolV2AdvertiseCapabilities(w, repo.opts.Hash, &v2cfg)
		}
		if !options.IsStateless {
			if err := protocolV2AdvertiseCapabilities(w, repo.opts.Hash, &v2cfg); err != nil {
				return err
			}
		}
		if options.IsStateless {
			_, err := processV2Request(w, repo, &v2cfg, r)
			return err
		}
		for {
			done, err := processV2Request(w, repo, &v2cfg, r)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}

	case 1:
		if options.AdvertiseRefs || !options.IsStateless {
			if err := writePktLine(w, []byte("version 1\n")); err != nil {
				return err
			}
		}
		return uploadPack(w, repo, options, r)

	default: // v0
		return uploadPack(w, repo, options, r)
	}
}

func uploadPack(w io.Writer, repo *Repo, options UploadPackOptions, r io.Reader) error {
	up := newUploadPackSession()
	ourRefs := make(map[string]bool)
	shallowOIDs := make(map[string]bool)
	deepenNot := make(map[string]bool)
	var wantObj []string
	var haveObj []string

	if err := up.readConfig(repo); err != nil {
		return err
	}
	up.isStateless = options.IsStateless

	// read HEAD for symref
	headResult, err := repo.readRef("HEAD")
	if err == nil && headResult != nil && headResult.IsRef {
		up.symrefs = append(up.symrefs, symref{
			name:   "HEAD",
			target: headResult.Ref.ToPath(),
		})
	}

	if options.AdvertiseRefs || !up.isStateless {
		if options.AdvertiseRefs {
			up.noDone = true
		}

		// HEAD
		headOID, err := repo.ReadHeadRecurMaybe()
		if err == nil && headOID != "" {
			if err := up.writeV0Ref(repo, w, ourRefs, "HEAD", headOID); err != nil {
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
			if err != nil || oid == "" {
				continue
			}
			if err := up.writeV0Ref(repo, w, ourRefs, ref.ToPath(), oid); err != nil {
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
			if err != nil || oid == "" {
				continue
			}
			if err := up.writeV0Ref(repo, w, ourRefs, ref.ToPath(), oid); err != nil {
				return err
			}
		}

		if !up.sentCapabilities {
			nullOID := strings.Repeat("0", repo.opts.Hash.HexLen())
			if err := up.writeV0Ref(repo, w, ourRefs, "capabilities^{}", nullOID); err != nil {
				return err
			}
		}

		if err := writePktFlush(w); err != nil {
			return err
		}
	} else {
		// stateless: just mark our refs
		headOID, err := repo.ReadHeadRecurMaybe()
		if err == nil && headOID != "" {
			ourRefs[headOID] = true
		}
		collectAllRefs(repo, ourRefs)
	}

	if options.AdvertiseRefs {
		return nil
	}

	if err := up.receiveNeeds(w, repo, ourRefs, r, shallowOIDs, deepenNot, &wantObj); err != nil {
		return err
	}

	if !up.useSideband {
		return fmt.Errorf("sideband protocol required")
	}

	if len(wantObj) != 0 {
		// peek for EOF before entering common commit negotiation
		result, err := readPktLineEx(r)
		if err != nil {
			return err
		}
		switch result.kind {
		case pktLineEOF:
			// no negotiation needed
		case pktLineData:
			if err := up.getCommonCommits(repo, w, r, &haveObj, &wantObj, result.data); err != nil {
				return err
			}
			if err := writePack(repo, w, wantObj); err != nil {
				return err
			}
		case pktLineFlush:
			// flush with no negotiation; proceed directly to pack
			if err := up.getCommonCommits(repo, w, r, &haveObj, &wantObj, nil); err != nil {
				return err
			}
			if err := writePack(repo, w, wantObj); err != nil {
				return err
			}
		case pktLineDelim:
			return fmt.Errorf("unexpected delim")
		case pktLineResponseEnd:
			return fmt.Errorf("unexpected response end")
		}
	}

	return nil
}

func writePack(repo *Repo, w io.Writer, wantObj []string) error {
	objIter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterAll})
	for _, oid := range wantObj {
		objIter.Include(oid)
	}

	pw, err := repo.NewPackWriter(objIter)
	if err != nil {
		return err
	}
	if pw != nil {
		defer pw.Close()
		buf := make([]byte, repo.opts.bufferSize())
		for {
			n, err := pw.Read(buf)
			if n > 0 {
				if err := writePktLineSB(w, 1, buf[:n]); err != nil {
					return err
				}
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				return err
			}
			if n == 0 {
				break
			}
		}
	}

	return writePktFlush(w)
}

func collectAllRefs(repo *Repo, refs map[string]bool) {
	headsDir := filepath.Join(repo.repoPath, "refs", "heads")
	headIter, err := newRefIterator(headsDir, RefHead)
	if err == nil {
		defer headIter.Close()
		for {
			ref, err := headIter.Next()
			if err != nil || ref == nil {
				break
			}
			if oid, err := repo.ReadRef(*ref); err == nil && oid != "" {
				refs[oid] = true
			}
		}
	}
	tagsDir := filepath.Join(repo.repoPath, "refs", "tags")
	tagIter, err := newRefIterator(tagsDir, RefTag)
	if err == nil {
		defer tagIter.Close()
		for {
			ref, err := tagIter.Next()
			if err != nil || ref == nil {
				break
			}
			if oid, err := repo.ReadRef(*ref); err == nil && oid != "" {
				refs[oid] = true
			}
		}
	}
}

// --- v2 capabilities and request processing ---

type v2Capability int

const (
	v2CapAgent v2Capability = iota
	v2CapLsRefs
	v2CapFetch
	v2CapServerOption
	v2CapObjectFormat
	v2CapSessionID
	v2CapObjectInfo
	v2CapBundleURI
)

var v2CapAll = []v2Capability{
	v2CapAgent, v2CapLsRefs, v2CapFetch, v2CapServerOption,
	v2CapObjectFormat, v2CapSessionID, v2CapObjectInfo, v2CapBundleURI,
}

func v2CapName(c v2Capability) string {
	switch c {
	case v2CapAgent:
		return "agent"
	case v2CapLsRefs:
		return "ls-refs"
	case v2CapFetch:
		return "fetch"
	case v2CapServerOption:
		return "server-option"
	case v2CapObjectFormat:
		return "object-format"
	case v2CapSessionID:
		return "session-id"
	case v2CapObjectInfo:
		return "object-info"
	case v2CapBundleURI:
		return "bundle-uri"
	}
	return ""
}

func v2CapHasCommand(c v2Capability) bool {
	switch c {
	case v2CapLsRefs, v2CapFetch, v2CapObjectInfo, v2CapBundleURI:
		return true
	}
	return false
}

func v2CapAdvertise(c v2Capability, hashKind HashKind, cfg *v2Config) (string, bool) {
	switch c {
	case v2CapAgent:
		return "git/2.51.2", true
	case v2CapLsRefs:
		if cfg.advertiseUnborn {
			return "unborn", true
		}
		return "", true
	case v2CapFetch:
		val := "shallow wait-for-done"
		if cfg.allowFilter {
			val += " filter"
		}
		if cfg.allowRefInWant {
			val += " ref-in-want"
		}
		if cfg.allowSidebandAll {
			val += " sideband-all"
		}
		if cfg.allowPackfileURIs {
			val += " packfile-uris"
		}
		return val, true
	case v2CapServerOption:
		return "", true
	case v2CapObjectFormat:
		return hashKind.Name(), true
	case v2CapSessionID:
		return "", cfg.advertiseSID
	case v2CapObjectInfo:
		return "", cfg.advertiseObjectInfo
	case v2CapBundleURI:
		return "", cfg.advertiseBundleURIs
	}
	return "", false
}

func protocolV2AdvertiseCapabilities(w io.Writer, hashKind HashKind, cfg *v2Config) error {
	if err := writePktLine(w, []byte("version 2\n")); err != nil {
		return err
	}
	for _, cap := range v2CapAll {
		val, advertised := v2CapAdvertise(cap, hashKind, cfg)
		if !advertised {
			continue
		}
		line := v2CapName(cap)
		if val != "" {
			line += "=" + val
		}
		line += "\n"
		if err := writePktLine(w, []byte(line)); err != nil {
			return err
		}
	}
	return writePktFlush(w)
}

func processV2Request(w io.Writer, repo *Repo, cfg *v2Config, r io.Reader) (bool, error) {
	done := false
	seenCapOrCmd := false
	var command *v2Capability

	for !done {
		result, err := readPktLineEx(r)
		if err != nil {
			return false, err
		}
		switch result.kind {
		case pktLineEOF:
			if !seenCapOrCmd {
				return true, nil
			}
			return false, fmt.Errorf("unexpected EOF")
		case pktLineData:
			line := string(result.data)
			if strings.HasPrefix(line, "command=") {
				if command != nil {
					return false, fmt.Errorf("duplicate command")
				}
				cmdName := line[8:]
				cmd := parseV2Command(cmdName, repo.opts.Hash, cfg)
				if cmd == nil {
					return false, fmt.Errorf("invalid command: %s", cmdName)
				}
				command = cmd
				seenCapOrCmd = true
			} else if receiveClientCapability(line, repo.opts.Hash, cfg) {
				seenCapOrCmd = true
			} else {
				return false, fmt.Errorf("unknown capability: %s", line)
			}
		case pktLineFlush:
			if !seenCapOrCmd {
				return true, nil
			}
			done = true
		case pktLineDelim:
			done = true
		case pktLineResponseEnd:
			return false, fmt.Errorf("unexpected response end")
		}
	}

	if command == nil {
		return false, fmt.Errorf("no command requested")
	}

	switch *command {
	case v2CapLsRefs:
		if err := lsRefs(w, repo, r); err != nil {
			return false, err
		}
	case v2CapFetch:
		if err := uploadPackV2(w, repo, r); err != nil {
			return false, err
		}
	case v2CapObjectInfo:
		if err := objectInfo(w, repo, r); err != nil {
			return false, err
		}
	case v2CapBundleURI:
		// consume args
		for {
			result, err := readPktLineEx(r)
			if err != nil {
				return false, err
			}
			if result.kind == pktLineFlush {
				break
			}
		}
		if err := writePktFlush(w); err != nil {
			return false, err
		}
	}

	return false, nil
}

func parseV2Command(name string, hashKind HashKind, cfg *v2Config) *v2Capability {
	for _, cap := range v2CapAll {
		capName := v2CapName(cap)
		if !v2CapHasCommand(cap) {
			continue
		}
		if name == capName {
			_, advertised := v2CapAdvertise(cap, hashKind, cfg)
			if !advertised {
				return nil
			}
			return &cap
		}
	}
	return nil
}

func receiveClientCapability(line string, hashKind HashKind, cfg *v2Config) bool {
	for _, cap := range v2CapAll {
		capName := v2CapName(cap)
		if !strings.HasPrefix(line, capName) {
			continue
		}
		rest := line[len(capName):]
		if rest != "" && rest[0] != '=' {
			continue
		}
		if v2CapHasCommand(cap) {
			continue
		}
		_, advertised := v2CapAdvertise(cap, hashKind, cfg)
		if !advertised {
			continue
		}
		return true
	}
	return false
}

func lsRefs(w io.Writer, repo *Repo, r io.Reader) error {
	hexLen := repo.opts.Hash.HexLen()
	shouldPeel := false
	shouldSymrefs := false
	shouldUnborn := false
	var prefixes []string

	for {
		result, err := readPktLineEx(r)
		if err != nil {
			return err
		}
		if result.kind == pktLineFlush {
			break
		}
		if result.kind != pktLineData {
			return fmt.Errorf("unexpected pkt-line in ls-refs")
		}
		arg := string(result.data)
		switch {
		case arg == "peel":
			shouldPeel = true
		case arg == "symrefs":
			shouldSymrefs = true
		case strings.HasPrefix(arg, "ref-prefix "):
			if len(prefixes) < 65536 {
				prefixes = append(prefixes, arg[11:])
			}
		case arg == "unborn":
			shouldUnborn = true
		}
	}

	if len(prefixes) >= 65536 {
		prefixes = nil
	}

	// HEAD
	headResult, err := repo.readRef("HEAD")
	if err == nil && headResult != nil {
		if headResult.IsRef {
			// symref HEAD
			if refMatch(prefixes, "HEAD") {
				headOID, _ := repo.readRefRecur(*headResult)
				if headOID != "" {
					if err := sendLsRef(w, hexLen, "HEAD", headOID, shouldPeel, shouldSymrefs, headResult.Ref.ToPath(), repo); err != nil {
						return err
					}
				} else if shouldUnborn && shouldSymrefs {
					line := fmt.Sprintf("unborn HEAD symref-target:%s\n", headResult.Ref.ToPath())
					if err := writePktLine(w, []byte(line)); err != nil {
						return err
					}
				}
			}
		} else {
			if refMatch(prefixes, "HEAD") {
				if err := sendLsRef(w, hexLen, "HEAD", headResult.OID, shouldPeel, shouldSymrefs, "", repo); err != nil {
					return err
				}
			}
		}
	}

	// heads
	headsDir := filepath.Join(repo.repoPath, "refs", "heads")
	headIter, err := newRefIterator(headsDir, RefHead)
	if err == nil {
		defer headIter.Close()
		for {
			ref, err := headIter.Next()
			if err != nil || ref == nil {
				break
			}
			refPath := ref.ToPath()
			if !refMatch(prefixes, refPath) {
				continue
			}
			if oid, err := repo.ReadRef(*ref); err == nil && oid != "" {
				if err := sendLsRef(w, hexLen, refPath, oid, shouldPeel, shouldSymrefs, "", repo); err != nil {
					return err
				}
			}
		}
	}

	// tags
	tagsDir := filepath.Join(repo.repoPath, "refs", "tags")
	tagIter, err := newRefIterator(tagsDir, RefTag)
	if err == nil {
		defer tagIter.Close()
		for {
			ref, err := tagIter.Next()
			if err != nil || ref == nil {
				break
			}
			refPath := ref.ToPath()
			if !refMatch(prefixes, refPath) {
				continue
			}
			if oid, err := repo.ReadRef(*ref); err == nil && oid != "" {
				if err := sendLsRef(w, hexLen, refPath, oid, shouldPeel, shouldSymrefs, "", repo); err != nil {
					return err
				}
			}
		}
	}

	return writePktFlush(w)
}

func refMatch(prefixes []string, refname string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(refname, prefix) {
			return true
		}
	}
	return false
}

func sendLsRef(w io.Writer, hexLen int, refname string, oid string, shouldPeel, shouldSymrefs bool, symrefTarget string, repo *Repo) error {
	line := oid + " " + refname
	if shouldSymrefs && symrefTarget != "" {
		line += " symref-target:" + symrefTarget
	}
	if shouldPeel {
		peeledOID, peeled := peelToNonTag(repo, oid)
		if peeled {
			line += " peeled:" + peeledOID
		}
	}
	line += "\n"
	return writePktLine(w, []byte(line))
}

func objectInfo(w io.Writer, repo *Repo, r io.Reader) error {
	hexLen := repo.opts.Hash.HexLen()
	wantSize := false
	var oidStrs []string

	for {
		result, err := readPktLineEx(r)
		if err != nil {
			return err
		}
		if result.kind == pktLineFlush {
			break
		}
		if result.kind != pktLineData {
			return fmt.Errorf("unexpected pkt-line in object-info")
		}
		arg := string(result.data)
		switch {
		case arg == "size":
			wantSize = true
		case strings.HasPrefix(arg, "oid "):
			oidStrs = append(oidStrs, arg[4:])
		default:
			return writePktLine(w, []byte(fmt.Sprintf("ERR object-info: unexpected line: '%s'", arg)))
		}
	}

	if len(oidStrs) == 0 {
		return nil
	}

	if wantSize {
		if err := writePktLine(w, []byte("size")); err != nil {
			return err
		}
	}

	for _, oidStr := range oidStrs {
		if len(oidStr) != hexLen {
			if err := writePktLine(w, []byte(fmt.Sprintf("ERR object-info: protocol error, expected to get oid, not '%s'", oidStr))); err != nil {
				return err
			}
			continue
		}
		if wantSize {
			rdr, err := repo.NewObjectReader(oidStr)
			if err != nil {
				if err := writePktLine(w, []byte(fmt.Sprintf("%s ", oidStr))); err != nil {
					return err
				}
				continue
			}
			size := rdr.Header().Size
			rdr.Close()
			if err := writePktLine(w, []byte(fmt.Sprintf("%s %d", oidStr, size))); err != nil {
				return err
			}
		} else {
			if err := writePktLine(w, []byte(oidStr)); err != nil {
				return err
			}
		}
	}

	return writePktFlush(w)
}

func uploadPackV2(w io.Writer, repo *Repo, r io.Reader) error {
	hexLen := repo.opts.Hash.HexLen()
	up := newUploadPackSession()
	ourRefs := make(map[string]bool)
	shallowOIDs := make(map[string]bool)
	deepenNot := make(map[string]bool)
	wantedRefs := make(map[string]string) // refname -> oid
	var wantObj []string
	var haveObj []string

	up.useSideband = true
	if err := up.readConfig(repo); err != nil {
		return err
	}

	// process args
	wantedOIDs := make(map[string]bool)
	for {
		result, err := readPktLineEx(r)
		if err != nil {
			return err
		}
		if result.kind == pktLineFlush {
			break
		}
		if result.kind != pktLineData {
			return fmt.Errorf("unexpected pkt-line")
		}
		arg := string(result.data)

		if strings.HasPrefix(arg, "want ") {
			wantArg := arg[5:]
			if len(wantArg) < hexLen {
				return fmt.Errorf("protocol error: expected OID")
			}
			oid := wantArg[:hexLen]
			if !objectExists(repo, oid) {
				writePktError(w, up.writerUseSideband, fmt.Sprintf("upload-pack: not our ref %s", oid))
				return fmt.Errorf("client error")
			}
			if !wantedOIDs[oid] {
				wantedOIDs[oid] = true
				wantObj = append(wantObj, oid)
			}
			continue
		}
		if up.allowRefInWant && strings.HasPrefix(arg, "want-ref ") {
			refName := arg[9:]
			ref := RefFromPath(refName, nil)
			if ref == nil {
				writePktError(w, up.writerUseSideband, fmt.Sprintf("unknown ref %s", refName))
				return fmt.Errorf("client error")
			}
			oid, err := repo.ReadRef(*ref)
			if err != nil || oid == "" {
				writePktError(w, up.writerUseSideband, fmt.Sprintf("unknown ref %s", refName))
				return fmt.Errorf("client error")
			}
			if _, exists := wantedRefs[refName]; exists {
				writePktError(w, up.writerUseSideband, fmt.Sprintf("duplicate want-ref %s", refName))
				return fmt.Errorf("client error")
			}
			wantedRefs[refName] = oid
			if !wantedOIDs[oid] {
				wantedOIDs[oid] = true
				wantObj = append(wantObj, oid)
			}
			continue
		}
		if strings.HasPrefix(arg, "have ") {
			haveArg := arg[5:]
			if len(haveArg) < hexLen {
				return fmt.Errorf("invalid object id")
			}
			appendIfExists(repo, haveArg[:hexLen], &haveObj)
			up.seenHaves = true
			continue
		}
		if arg == "thin-pack" || arg == "ofs-delta" || arg == "no-progress" || arg == "include-tag" {
			continue
		}
		if arg == "done" {
			up.done = true
			continue
		}
		if arg == "wait-for-done" {
			up.waitForDone = true
			continue
		}
		if oid, ok := processShallow(repo, arg); ok {
			if oid != "" {
				shallowOIDs[oid] = true
			}
			continue
		}
		if val, ok := processDeepen(arg); ok {
			up.depth = val
			continue
		}
		if val, ok := processDeepenSince(arg); ok {
			up.deepenSince = val
			up.deepenRevList = true
			continue
		}
		if oid, ok := processDeepenNot(repo, arg); ok {
			deepenNot[oid] = true
			up.deepenRevList = true
			continue
		}
		if arg == "deepen-relative" {
			up.deepenRelative = true
			continue
		}
		if up.allowFilter && strings.HasPrefix(arg, "filter ") {
			continue
		}
		if up.allowSidebandAll && arg == "sideband-all" {
			up.writerUseSideband = true
			continue
		}
		// unknown arg, skip
	}

	if len(wantObj) == 0 && !up.waitForDone {
		return nil
	}

	// send acks or proceed to pack
	if up.seenHaves {
		if up.done {
			// send pack directly
		} else if sendAcksV2(w, up, repo, &haveObj, &wantObj) {
			// ready -> send pack
		} else {
			// not ready, send flush and return
			return writePktFlush(w)
		}
	}

	// send wanted-ref info
	if len(wantedRefs) > 0 {
		writePktResponse(w, up.writerUseSideband, "wanted-refs\n")
		for refName, oid := range wantedRefs {
			writePktResponse(w, up.writerUseSideband, fmt.Sprintf("%s %s\n", oid, refName))
		}
		if err := writePktDelim(w); err != nil {
			return err
		}
	}

	// send shallow info
	if up.depth > 0 || up.deepenRevList || len(shallowOIDs) > 0 {
		writePktResponse(w, up.writerUseSideband, "shallow-info\n")
		up.sendShallowList(w, repo, ourRefs, shallowOIDs, deepenNot, &wantObj)
		if err := writePktDelim(w); err != nil {
			return err
		}
	}

	// send packfile
	writePktResponse(w, up.writerUseSideband, "packfile\n")
	return writePack(repo, w, wantObj)
}

func sendAcksV2(w io.Writer, up *uploadPackSession, repo *Repo, haveObj *[]string, wantObj *[]string) bool {
	writePktResponse(w, up.writerUseSideband, "acknowledgments\n")

	if len(*haveObj) == 0 {
		writePktResponse(w, up.writerUseSideband, "NAK\n")
	}

	for _, ack := range *haveObj {
		writePktResponse(w, up.writerUseSideband, fmt.Sprintf("ACK %s\n", ack))
	}

	if !up.waitForDone && allWantsReachable(repo, haveObj, wantObj) {
		writePktResponse(w, up.writerUseSideband, "ready\n")
		writePktDelim(w)
		return true
	}

	writePktFlush(w)
	return false
}

// --- helpers ---

func objectExists(repo *Repo, oidHex string) bool {
	rdr, err := repo.NewObjectReader(oidHex)
	if err != nil {
		return false
	}
	rdr.Close()
	return true
}

func appendIfExists(repo *Repo, oidHex string, list *[]string) bool {
	if !objectExists(repo, oidHex) {
		return false
	}
	*list = append(*list, oidHex)
	return true
}

func processShallow(repo *Repo, line string) (string, bool) {
	if !strings.HasPrefix(line, "shallow ") {
		return "", false
	}
	hexLen := repo.opts.Hash.HexLen()
	arg := line[8:]
	if len(arg) < hexLen {
		return "", true
	}
	oid := arg[:hexLen]
	rdr, err := repo.NewObjectReader(oid)
	if err != nil {
		return "", true
	}
	defer rdr.Close()
	if rdr.Header().Kind != ObjectKindCommit {
		return "", true
	}
	return oid, true
}

func processDeepen(line string) (int, bool) {
	if !strings.HasPrefix(line, "deepen ") {
		return 0, false
	}
	val, err := strconv.Atoi(line[7:])
	if err != nil || val == 0 {
		return 0, true
	}
	return val, true
}

func processDeepenSince(line string) (uint64, bool) {
	if !strings.HasPrefix(line, "deepen-since ") {
		return 0, false
	}
	val, err := strconv.ParseUint(line[13:], 10, 64)
	if err != nil || val == 0 || val == math.MaxUint64 {
		return 0, true
	}
	return val, true
}

func processDeepenNot(repo *Repo, line string) (string, bool) {
	if !strings.HasPrefix(line, "deepen-not ") {
		return "", false
	}
	arg := line[11:]

	// try as full ref path
	ref := RefFromPath(arg, nil)
	if ref != nil {
		oid, err := repo.ReadRef(*ref)
		if err == nil && oid != "" {
			return oid, true
		}
	}
	// try as branch
	oid, err := repo.ReadRef(Ref{Kind: RefHead, Name: arg})
	if err == nil && oid != "" {
		return oid, true
	}
	// try as tag
	oid, err = repo.ReadRef(Ref{Kind: RefTag, Name: arg})
	if err == nil && oid != "" {
		return oid, true
	}
	return "", true
}

func allWantsReachable(repo *Repo, haveObj *[]string, wantObj *[]string) bool {
	if len(*haveObj) == 0 {
		return false
	}

	haveSet := make(map[string]bool)
	for _, oid := range *haveObj {
		haveSet[oid] = true
	}

	for _, wantOID := range *wantObj {
		if haveSet[wantOID] {
			continue
		}
		// walk ancestors looking for a have
		iter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit})
		iter.Include(wantOID)
		found := false
		for {
			obj, err := iter.Next()
			if err != nil || obj == nil {
				break
			}
			obj.Close()
			if haveSet[obj.OID] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func getReachableShallows(repo *Repo, shallowOIDs map[string]bool, ourRefs map[string]bool) []string {
	var reachable []string
	remaining := make(map[string]bool)

	for oid := range shallowOIDs {
		if ourRefs[oid] {
			reachable = append(reachable, oid)
		} else {
			remaining[oid] = true
		}
	}

	if len(remaining) == 0 {
		return reachable
	}

	iter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit})
	for oid := range ourRefs {
		iter.Include(oid)
	}
	for {
		obj, err := iter.Next()
		if err != nil || obj == nil {
			break
		}
		if remaining[obj.OID] {
			reachable = append(reachable, obj.OID)
			delete(remaining, obj.OID)
			if len(remaining) == 0 {
				obj.Close()
				break
			}
		}
		obj.Close()
	}

	return reachable
}

func writePktResponse(w io.Writer, useSideband bool, data string) error {
	if useSideband {
		return writePktLineSB(w, 1, []byte(data))
	}
	return writePktLine(w, []byte(data))
}

func writePktError(w io.Writer, useSideband bool, msg string) error {
	if useSideband {
		return writePktLineSB(w, 3, []byte(msg))
	}
	line := "ERR " + msg
	return writePktLine(w, []byte(line))
}
