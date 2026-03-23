package repomofo

import (
	"fmt"
	"io"
	"strings"
)

type commandKind int

const (
	commandInit commandKind = iota
	commandAdd
	commandUnadd
	commandUntrack
	commandRm
	commandCommit
	commandTag
	commandStatus
	commandBranch
	commandSwitchDir
	commandReset
	commandResetDir
	commandResetAdd
	commandRestore
	commandLog
	commandConfig
	commandRemote
	commandMerge
	commandCherryPick
	commandReceivePack
	commandUploadPack
	commandHTTPBackend
)

var commandNames = map[commandKind]string{
	commandInit:        "init",
	commandAdd:         "add",
	commandUnadd:       "unadd",
	commandUntrack:     "untrack",
	commandRm:          "rm",
	commandCommit:      "commit",
	commandTag:         "tag",
	commandStatus:      "status",
	commandBranch:      "branch",
	commandSwitchDir:   "switch",
	commandReset:       "reset",
	commandResetDir:    "reset-dir",
	commandResetAdd:    "reset-add",
	commandRestore:     "restore",
	commandLog:         "log",
	commandConfig:      "config",
	commandRemote:      "remote",
	commandMerge:       "merge",
	commandCherryPick:  "cherry-pick",
	commandReceivePack: "receive-pack",
	commandUploadPack:  "upload-pack",
	commandHTTPBackend: "http-backend",
}

var commandDescrips = map[commandKind]string{
	commandInit:        "create an empty repository.",
	commandAdd:         "add file contents to the index.",
	commandUnadd:       "remove any changes to a file that were added to the index.\nsimilar to `git reset HEAD`.",
	commandUntrack:     "no longer track file in the index, but leave it in the work dir.\nsimilar to `git rm --cached`.",
	commandRm:          "no longer track file in the index *and* remove it from the work dir.",
	commandCommit:      "create a new commit.",
	commandTag:         "add, remove, and list tags.",
	commandStatus:      "show the status of uncommitted changes.",
	commandBranch:      "add, remove, and list branches.",
	commandSwitchDir:   "switch to a branch or commit id.",
	commandReset:       "make the current branch point to a new commit id.\nupdates the index, but the files in the work dir are left alone.",
	commandResetDir:    "make the current branch point to a new commit id.\nupdates both the index and the work dir.\nsimilar to `git reset --hard`.",
	commandResetAdd:    "make the current branch point to a new commit id.\ndoes not update the index or the work dir.\nthis is like calling reset and then adding everything to the index.\nsimilar to `git reset --soft`.",
	commandRestore:     "restore files in the work dir.",
	commandLog:         "show commit logs.",
	commandConfig:      "add, remove, and list config options.",
	commandRemote:      "add, remove, and list remotes.",
	commandMerge:       "join two or more development histories together.",
	commandCherryPick:  "apply the changes introduced by some existing commits.",
	commandReceivePack: "receive what is pushed into the repository.",
	commandUploadPack:  "send what is fetched from the repository.",
	commandHTTPBackend: "a CGI program forwarding receive-pack and upload-pack over HTTP.",
}

var commandExamples = map[commandKind]string{
	commandInit: `in the current dir:
    repomofo init
in a new dir:
    repomofo init myproject`,
	commandAdd: `repomofo add myfile.txt`,
	commandUnadd: `repomofo unadd myfile.txt
repomofo unadd -r mydir`,
	commandUntrack: `repomofo untrack myfile.txt
repomofo untrack -r mydir`,
	commandRm: `repomofo rm myfile.txt
repomofo rm -r mydir`,
	commandCommit: `repomofo commit -m "my commit message"`,
	commandTag: `add tag:
    repomofo tag add mytag
remove tag:
    repomofo tag rm mytag
list tags:
    repomofo tag list`,
	commandStatus: `repomofo status`,
	commandBranch: `add branch:
    repomofo branch add mybranch
remove branch:
    repomofo branch rm mybranch
list branches:
    repomofo branch list`,
	commandSwitchDir: `switch to branch:
    repomofo switch mybranch
switch to commit id:
    repomofo switch a1b2c3...`,
	commandReset: `reset current branch to match another branch:
    repomofo reset mybranch
reset current branch to point to a new commit id:
    repomofo reset a1b2c3...`,
	commandResetDir: `reset current branch to match another branch:
    repomofo reset-dir mybranch
reset current branch to point to a new commit id:
    repomofo reset-dir a1b2c3...`,
	commandResetAdd: `reset current branch to point to a new commit id:
    repomofo reset-add a1b2c3...`,
	commandRestore: `repomofo restore myfile.txt`,
	commandLog: `display log:
    repomofo log
display specified branch:
    repomofo log branch_name`,
	commandConfig: `add config:
    repomofo config add core.editor vim
remove config:
    repomofo config rm core.editor
list configs:
    repomofo config list`,
	commandRemote: `add remote:
    repomofo remote add origin https://github.com/...
remove remote:
    repomofo remote rm origin
list remotes:
    repomofo remote list`,
	commandMerge: `merge branch:
    repomofo merge mybranch
continue after merge conflict resolution:
    repomofo merge --continue
abort merge:
    repomofo merge --abort`,
	commandCherryPick: `cherry pick a commit:
    repomofo cherry-pick a1b2c3...
continue after conflict resolution:
    repomofo cherry-pick --continue
abort cherry-pick:
    repomofo cherry-pick --abort`,
	commandReceivePack: `repomofo receive-pack <directory>`,
	commandUploadPack:  `repomofo upload-pack <directory>`,
	commandHTTPBackend: `repomofo http-backend`,
}

// valueFlags are flags that can have a value associated with them.
var valueFlags = map[string]bool{
	"-m": true,
}

type commandArgs struct {
	commandKind    *commandKind
	CommandName    *string
	PositionalArgs []string
	MapArgs        map[string]*string // nil value means flag present but no value
	UnusedArgs     map[string]bool
}

func parseCommandArgs(args []string) *commandArgs {
	var positionalArgs []string
	mapArgs := make(map[string]*string)
	unusedArgs := make(map[string]bool)
	var orderedKeys []string

	for _, arg := range args {
		if len(arg) > 1 && arg[0] == '-' {
			mapArgs[arg] = nil
			unusedArgs[arg] = true
			orderedKeys = append(orderedKeys, arg)
		} else {
			// if the last key is a value flag and doesn't have a value yet,
			// set this arg as its value
			if len(orderedKeys) > 0 {
				lastKey := orderedKeys[len(orderedKeys)-1]
				if valueFlags[lastKey] && mapArgs[lastKey] == nil {
					val := arg
					mapArgs[lastKey] = &val
					continue
				}
			}
			positionalArgs = append(positionalArgs, arg)
		}
	}

	ca := &commandArgs{
		PositionalArgs: positionalArgs,
		MapArgs:        mapArgs,
		UnusedArgs:     unusedArgs,
	}

	if len(positionalArgs) == 0 {
		return ca
	}

	cmdName := positionalArgs[0]
	ca.CommandName = &cmdName
	ca.PositionalArgs = positionalArgs[1:]

	for kind, name := range commandNames {
		if name == cmdName {
			k := kind
			ca.commandKind = &k
			break
		}
	}

	return ca
}

func (ca *commandArgs) Contains(arg string) bool {
	delete(ca.UnusedArgs, arg)
	_, ok := ca.MapArgs[arg]
	return ok
}

// Get returns (value, true) if the flag is present. value may be "" if
// the flag was present but had no associated value.
// Returns ("", false) if the flag is not present.
func (ca *commandArgs) Get(arg string) (string, bool) {
	delete(ca.UnusedArgs, arg)
	val, ok := ca.MapArgs[arg]
	if !ok {
		return "", false
	}
	if val == nil {
		return "", true // flag present but no value
	}
	return *val, true
}

var errCommitMessageNotFound = fmt.Errorf("commit message not found")

// refOrOidFromUser parses a user-supplied string as either a hex OID or a branch ref.
func refOrOidFromUser(s string, hashKind HashKind) RefOrOid {
	if isHexString(s) && len(s) == hashKind.HexLen() {
		h, err := hashKind.HashFromHex(s)
		if err != nil {
			return nil
		}
		return OIDValue{OID: h}
	}
	if validateRefName(s) {
		return RefValue{Ref: Ref{Kind: RefHead, Name: s}}
	}
	return nil
}

type initCommand struct {
	Dir string
}

type addCommand struct {
	Paths []string
}

type unaddCommand struct {
	Paths []string
	Opts  UnaddOptions
}

type untrackCommand struct {
	Paths     []string
	Force     bool
	Recursive bool
}

type rmCommand struct {
	Paths []string
	Opts  RemoveOptions
}

type commitCommand struct {
	Message    string
	AllowEmpty bool
}

type tagCommandKind int

const (
	tagList tagCommandKind = iota
	tagAdd
	tagRemove
)

type tagCommand struct {
	SubKind tagCommandKind
	Name    string // for add/remove
	Message string // for add (optional)
}

type branchCommandKind int

const (
	branchList branchCommandKind = iota
	branchAdd
	branchRemove
)

type branchCommand struct {
	SubKind branchCommandKind
	Name    string // for add/remove
}

type switchCommand struct {
	Target RefOrOid
	Force  bool
}

type resetAddCommand struct {
	Target RefOrOid // OID only
}

type mergeCommand struct {
	Input MergeInput
}

type receivePackCommand struct {
	Dir     string
	Options ReceivePackOptions
}

type uploadPackCommand struct {
	Dir     string
	Options UploadPackOptions
}

type command struct {
	Kind        commandKind
	Init        *initCommand
	Add         *addCommand
	Unadd       *unaddCommand
	Untrack     *untrackCommand
	Rm          *rmCommand
	Commit      *commitCommand
	Tag         *tagCommand
	Branch      *branchCommand
	Switch      *switchCommand
	ResetAdd    *resetAddCommand
	Restore     *restoreCommand
	Log         *logCommand
	Config      *configCommand
	Remote      *configCommand
	Merge       *mergeCommand
	ReceivePack *receivePackCommand
	UploadPack  *uploadPackCommand
}

type restoreCommand struct {
	Path string
}

type logCommand struct {
	Targets []RefOrOid
}

type configCommandKind int

const (
	configList configCommandKind = iota
	configAdd
	configRemove
)

type configCommand struct {
	SubKind configCommandKind
	Name    string // for add/remove
	Value   string // for add
}

func parseCommand(cmdArgs *commandArgs, hashKind HashKind) *command {
	if cmdArgs.commandKind == nil {
		return nil
	}
	switch *cmdArgs.commandKind {
	case commandInit:
		if len(cmdArgs.PositionalArgs) == 0 {
			return &command{Kind: commandInit, Init: &initCommand{Dir: "."}}
		} else if len(cmdArgs.PositionalArgs) == 1 {
			return &command{Kind: commandInit, Init: &initCommand{Dir: cmdArgs.PositionalArgs[0]}}
		}
		return nil

	case commandAdd:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &command{Kind: commandAdd, Add: &addCommand{Paths: cmdArgs.PositionalArgs}}

	case commandUnadd:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &command{Kind: commandUnadd, Unadd: &unaddCommand{
			Paths: cmdArgs.PositionalArgs,
			Opts:  UnaddOptions{Recursive: cmdArgs.Contains("-r")},
		}}

	case commandUntrack:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &command{Kind: commandUntrack, Untrack: &untrackCommand{
			Paths:     cmdArgs.PositionalArgs,
			Force:     cmdArgs.Contains("-f"),
			Recursive: cmdArgs.Contains("-r"),
		}}

	case commandRm:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &command{Kind: commandRm, Rm: &rmCommand{
			Paths: cmdArgs.PositionalArgs,
			Opts: RemoveOptions{
				Force:         cmdArgs.Contains("-f"),
				Recursive:     cmdArgs.Contains("-r"),
				UpdateWorkDir: true,
			},
		}}

	case commandCommit:
		if len(cmdArgs.PositionalArgs) > 0 {
			return nil
		}
		var message string
		if val, ok := cmdArgs.Get("-m"); ok {
			if val == "" {
				return nil // -m present but no value — error
			}
			message = val
		}
		return &command{Kind: commandCommit, Commit: &commitCommand{
			Message:    message,
			AllowEmpty: cmdArgs.Contains("--allow-empty"),
		}}

	case commandTag:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		subCmd := cmdArgs.PositionalArgs[0]
		switch subCmd {
		case "list":
			return &command{Kind: commandTag, Tag: &tagCommand{SubKind: tagList}}
		case "add":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			var message string
			if val, ok := cmdArgs.Get("-m"); ok {
				if val == "" {
					return nil
				}
				message = val
			}
			return &command{Kind: commandTag, Tag: &tagCommand{
				SubKind: tagAdd,
				Name:    cmdArgs.PositionalArgs[1],
				Message: message,
			}}
		case "rm":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			return &command{Kind: commandTag, Tag: &tagCommand{
				SubKind: tagRemove,
				Name:    cmdArgs.PositionalArgs[1],
			}}
		default:
			cmdArgs.UnusedArgs[subCmd] = true
			return nil
		}

	case commandStatus:
		return &command{Kind: commandStatus}

	case commandBranch:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		subCmd := cmdArgs.PositionalArgs[0]
		switch subCmd {
		case "list":
			return &command{Kind: commandBranch, Branch: &branchCommand{SubKind: branchList}}
		case "add":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			return &command{Kind: commandBranch, Branch: &branchCommand{
				SubKind: branchAdd,
				Name:    cmdArgs.PositionalArgs[1],
			}}
		case "rm":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			return &command{Kind: commandBranch, Branch: &branchCommand{
				SubKind: branchRemove,
				Name:    cmdArgs.PositionalArgs[1],
			}}
		default:
			cmdArgs.UnusedArgs[subCmd] = true
			return nil
		}

	case commandSwitchDir:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := refOrOidFromUser(cmdArgs.PositionalArgs[0], hashKind)
		if target == nil {
			return nil
		}
		return &command{Kind: commandSwitchDir, Switch: &switchCommand{
			Target: target,
			Force:  cmdArgs.Contains("-f"),
		}}

	case commandReset:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := refOrOidFromUser(cmdArgs.PositionalArgs[0], hashKind)
		if target == nil {
			return nil
		}
		return &command{Kind: commandReset, Switch: &switchCommand{
			Target: target,
			Force:  cmdArgs.Contains("-f"),
		}}

	case commandResetDir:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := refOrOidFromUser(cmdArgs.PositionalArgs[0], hashKind)
		if target == nil {
			return nil
		}
		return &command{Kind: commandResetDir, Switch: &switchCommand{
			Target: target,
			Force:  cmdArgs.Contains("-f"),
		}}

	case commandResetAdd:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := refOrOidFromUser(cmdArgs.PositionalArgs[0], hashKind)
		if target == nil {
			return nil
		}
		// reset-add only accepts OIDs
		if _, ok := target.(RefValue); ok {
			return nil
		}
		return &command{Kind: commandResetAdd, ResetAdd: &resetAddCommand{
			Target: target,
		}}

	case commandRestore:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		return &command{Kind: commandRestore, Restore: &restoreCommand{
			Path: cmdArgs.PositionalArgs[0],
		}}

	case commandLog:
		var targets []RefOrOid
		for _, arg := range cmdArgs.PositionalArgs {
			target := refOrOidFromUser(arg, hashKind)
			if target == nil {
				return nil
			}
			targets = append(targets, target)
		}
		return &command{Kind: commandLog, Log: &logCommand{Targets: targets}}

	case commandMerge, commandCherryPick:
		kind := MergeKindFull
		if *cmdArgs.commandKind == commandCherryPick {
			kind = MergeKindPick
		}

		if cmdArgs.Contains("--continue") {
			if len(cmdArgs.PositionalArgs) != 0 {
				return nil
			}
			return &command{Kind: *cmdArgs.commandKind, Merge: &mergeCommand{
				Input: MergeInput{Kind: kind, Action: MergeActionCont{}},
			}}
		} else if cmdArgs.Contains("--abort") {
			if len(cmdArgs.PositionalArgs) != 0 {
				return nil
			}
			return &command{Kind: *cmdArgs.commandKind, Merge: &mergeCommand{
				Input: MergeInput{Kind: kind, Action: MergeActionAbort{}},
			}}
		} else {
			if len(cmdArgs.PositionalArgs) != 1 {
				return nil
			}
			source := refOrOidFromUser(cmdArgs.PositionalArgs[0], hashKind)
			if source == nil {
				return nil
			}
			return &command{Kind: *cmdArgs.commandKind, Merge: &mergeCommand{
				Input: MergeInput{Kind: kind, Action: MergeActionNew{Source: source}},
			}}
		}

	case commandReceivePack:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		return &command{Kind: commandReceivePack, ReceivePack: &receivePackCommand{
			Dir: cmdArgs.PositionalArgs[0],
			Options: ReceivePackOptions{
				SkipConnectivityCheck: cmdArgs.Contains("--skip-connectivity-check"),
				AdvertiseRefs:         cmdArgs.Contains("--http-backend-info-refs"),
				IsStateless:           cmdArgs.Contains("--stateless-rpc"),
			},
		}}

	case commandUploadPack:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		return &command{Kind: commandUploadPack, UploadPack: &uploadPackCommand{
			Dir: cmdArgs.PositionalArgs[0],
			Options: UploadPackOptions{
				AdvertiseRefs: cmdArgs.Contains("--http-backend-info-refs"),
				IsStateless:   cmdArgs.Contains("--stateless-rpc"),
			},
		}}

	case commandHTTPBackend:
		if len(cmdArgs.PositionalArgs) != 0 {
			return nil
		}
		return &command{Kind: commandHTTPBackend}

	case commandConfig, commandRemote:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		subCmd := cmdArgs.PositionalArgs[0]
		var cc *configCommand
		switch subCmd {
		case "list":
			cc = &configCommand{SubKind: configList}
		case "add":
			if len(cmdArgs.PositionalArgs) < 3 {
				return nil
			}
			cc = &configCommand{
				SubKind: configAdd,
				Name:    cmdArgs.PositionalArgs[1],
				Value:   strings.Join(cmdArgs.PositionalArgs[2:], " "),
			}
		case "rm":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			cc = &configCommand{
				SubKind: configRemove,
				Name:    cmdArgs.PositionalArgs[1],
			}
		default:
			cmdArgs.UnusedArgs[subCmd] = true
			return nil
		}
		if *cmdArgs.commandKind == commandConfig {
			return &command{Kind: commandConfig, Config: cc}
		}
		return &command{Kind: commandRemote, Remote: cc}
	}
	return nil
}

type dispatch interface {
	dispatch()
}

type dispatchInvalidCommand struct {
	InvalidName string
}

type dispatchInvalidArgument struct {
	InvalidName string
	InvalidCmd  commandKind
}

type dispatchHelp struct {
	HelpCmd *commandKind
}

type dispatchCLI struct {
	command *command
}

func (dispatchInvalidCommand) dispatch()  {}
func (dispatchInvalidArgument) dispatch() {}
func (dispatchHelp) dispatch()            {}
func (dispatchCLI) dispatch()             {}

func newDispatch(cmdArgs *commandArgs, hashKind HashKind) dispatch {
	showHelp := cmdArgs.Contains("--help")
	cmdArgs.Contains("--cli") // consume it

	if cmdArgs.commandKind != nil {
		if showHelp {
			return dispatchHelp{HelpCmd: cmdArgs.commandKind}
		}
		if cmd := parseCommand(cmdArgs, hashKind); cmd != nil {
			// check for unused args
			for arg := range cmdArgs.UnusedArgs {
				return dispatchInvalidArgument{
					InvalidCmd:  *cmdArgs.commandKind,
					InvalidName: arg,
				}
			}
			return dispatchCLI{command: cmd}
		}
		return dispatchHelp{HelpCmd: cmdArgs.commandKind}
	} else if cmdArgs.CommandName != nil {
		return dispatchInvalidCommand{InvalidName: *cmdArgs.CommandName}
	} else if showHelp {
		return dispatchHelp{}
	}
	return dispatchHelp{}
}

func maxCommandNameLen() int {
	max := 0
	for _, name := range commandNames {
		if len(name) > max {
			max = len(name)
		}
	}
	return max
}

func printAligned(w io.Writer, name, text string, indent int) {
	fmt.Fprintf(w, "%s", name)
	for i := len(name); i < indent; i++ {
		fmt.Fprintf(w, " ")
	}
	lines := strings.Split(text, "\n")
	fmt.Fprintf(w, "%s\n", lines[0])
	for _, line := range lines[1:] {
		for i := 0; i < indent; i++ {
			fmt.Fprintf(w, " ")
		}
		fmt.Fprintf(w, "%s\n", line)
	}
}

func printHelp(cmdKind *commandKind, w io.Writer) {
	indent := maxCommandNameLen() + 2

	if cmdKind != nil {
		name := commandNames[*cmdKind]
		descrip := commandDescrips[*cmdKind]
		example := commandExamples[*cmdKind]
		printAligned(w, name, descrip, indent)
		fmt.Fprintf(w, "\n")
		for _, line := range strings.Split(example, "\n") {
			for i := 0; i < indent; i++ {
				fmt.Fprintf(w, " ")
			}
			fmt.Fprintf(w, "%s\n", line)
		}
	} else {
		fmt.Fprintf(w, "help: repomofo <command> [<args>]\n\n")
		for kind := commandInit; kind <= commandHTTPBackend; kind++ {
			name := commandNames[kind]
			printAligned(w, name, commandDescrips[kind], indent)
		}
	}
}
