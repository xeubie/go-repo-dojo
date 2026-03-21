package repomofo

import (
	"fmt"
	"io"
	"strings"
)

type CommandKind int

const (
	CommandInit CommandKind = iota
	CommandAdd
	CommandUnadd
	CommandUntrack
	CommandRm
	CommandCommit
	CommandTag
	CommandStatus
	CommandBranch
	CommandSwitchDir
	CommandReset
	CommandResetDir
	CommandResetAdd
	CommandRestore
	CommandLog
	CommandConfig
	CommandRemote
)

var commandNames = map[CommandKind]string{
	CommandInit:      "init",
	CommandAdd:       "add",
	CommandUnadd:     "unadd",
	CommandUntrack:   "untrack",
	CommandRm:        "rm",
	CommandCommit:    "commit",
	CommandTag:       "tag",
	CommandStatus:    "status",
	CommandBranch:    "branch",
	CommandSwitchDir: "switch",
	CommandReset:     "reset",
	CommandResetDir:  "reset-dir",
	CommandResetAdd:  "reset-add",
	CommandRestore:   "restore",
	CommandLog:       "log",
	CommandConfig:    "config",
	CommandRemote:    "remote",
}

var commandDescrips = map[CommandKind]string{
	CommandInit:      "create an empty repository.",
	CommandAdd:       "add file contents to the index.",
	CommandUnadd:     "remove any changes to a file that were added to the index.",
	CommandUntrack:   "no longer track file in the index, but leave it in the work dir.",
	CommandRm:        "no longer track file in the index *and* remove it from the work dir.",
	CommandCommit:    "create a new commit.",
	CommandTag:       "add, remove, and list tags.",
	CommandStatus:    "show the status of uncommitted changes.",
	CommandBranch:    "add, remove, and list branches.",
	CommandSwitchDir: "switch to a branch or commit id.",
	CommandReset:     "make the current branch point to a new commit id.\nupdates the index, but the files in the work dir are left alone.",
	CommandResetDir:  "make the current branch point to a new commit id.\nupdates both the index and the work dir.\nsimilar to `git reset --hard`.",
	CommandResetAdd:  "make the current branch point to a new commit id.\ndoes not update the index or the work dir.\nthis is like calling reset and then adding everything to the index.\nsimilar to `git reset --soft`.",
	CommandRestore:   "restore files in the work dir.",
	CommandLog:       "show commit logs.",
	CommandConfig:    "add, remove, and list config options.",
	CommandRemote:    "add, remove, and list remotes.",
}

var commandExamples = map[CommandKind]string{
	CommandInit: `in the current dir:
    repomofo init
in a new dir:
    repomofo init myproject`,
	CommandAdd: `repomofo add myfile.txt`,
	CommandUnadd: `repomofo unadd myfile.txt
repomofo unadd -r mydir`,
	CommandUntrack: `repomofo untrack myfile.txt
repomofo untrack -r mydir`,
	CommandRm: `repomofo rm myfile.txt
repomofo rm -r mydir`,
	CommandCommit: `repomofo commit -m "my commit message"`,
	CommandTag: `add tag:
    repomofo tag add mytag
remove tag:
    repomofo tag rm mytag
list tags:
    repomofo tag list`,
	CommandStatus: `repomofo status`,
	CommandBranch: `add branch:
    repomofo branch add mybranch
remove branch:
    repomofo branch rm mybranch
list branches:
    repomofo branch list`,
	CommandSwitchDir: `switch to branch:
    repomofo switch mybranch
switch to commit id:
    repomofo switch a1b2c3...`,
	CommandReset: `reset current branch to match another branch:
    repomofo reset mybranch
reset current branch to point to a new commit id:
    repomofo reset a1b2c3...`,
	CommandResetDir: `reset current branch to match another branch:
    repomofo reset-dir mybranch
reset current branch to point to a new commit id:
    repomofo reset-dir a1b2c3...`,
	CommandResetAdd: `reset current branch to point to a new commit id:
    repomofo reset-add a1b2c3...`,
	CommandRestore: `repomofo restore myfile.txt`,
	CommandLog: `display log:
    repomofo log
display specified branch:
    repomofo log branch_name`,
	CommandConfig: `add config:
    repomofo config add core.editor vim
remove config:
    repomofo config rm core.editor
list configs:
    repomofo config list`,
	CommandRemote: `add remote:
    repomofo remote add origin https://github.com/...
remove remote:
    repomofo remote rm origin
list remotes:
    repomofo remote list`,
}

// valueFlags are flags that can have a value associated with them.
var valueFlags = map[string]bool{
	"-m": true,
}

type CommandArgs struct {
	CommandKind    *CommandKind
	CommandName    *string
	PositionalArgs []string
	MapArgs        map[string]*string // nil value means flag present but no value
	UnusedArgs     map[string]bool
}

func ParseCommandArgs(args []string) *CommandArgs {
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

	ca := &CommandArgs{
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
			ca.CommandKind = &k
			break
		}
	}

	return ca
}

func (ca *CommandArgs) Contains(arg string) bool {
	delete(ca.UnusedArgs, arg)
	_, ok := ca.MapArgs[arg]
	return ok
}

// Get returns (value, true) if the flag is present. value may be "" if
// the flag was present but had no associated value.
// Returns ("", false) if the flag is not present.
func (ca *CommandArgs) Get(arg string) (string, bool) {
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

var ErrCommitMessageNotFound = fmt.Errorf("commit message not found")

// RefOrOidFromUser parses a user-supplied string as either a hex OID or a branch ref.
func RefOrOidFromUser(s string, hashKind HashKind) *RefOrOid {
	if isHexString(s) && len(s) == hashKind.HexLen() {
		return &RefOrOid{OID: s}
	}
	if ValidateRefName(s) {
		return &RefOrOid{IsRef: true, Ref: Ref{Kind: RefHead, Name: s}}
	}
	return nil
}

type InitCommand struct {
	Dir string
}

type AddCommand struct {
	Paths []string
}

type UnaddCommand struct {
	Paths []string
	Opts  UnaddOptions
}

type UntrackCommand struct {
	Paths     []string
	Force     bool
	Recursive bool
}

type RmCommand struct {
	Paths []string
	Opts  RemoveOptions
}

type CommitCommand struct {
	Message    string
	AllowEmpty bool
}

type TagCommandKind int

const (
	TagList TagCommandKind = iota
	TagAdd
	TagRemove
)

type TagCommand struct {
	SubKind TagCommandKind
	Name    string // for add/remove
	Message string // for add (optional)
}

type BranchCommandKind int

const (
	BranchList BranchCommandKind = iota
	BranchAdd
	BranchRemove
)

type BranchCommand struct {
	SubKind BranchCommandKind
	Name    string // for add/remove
}

type SwitchCommand struct {
	Target RefOrOid
	Force  bool
}

type ResetAddCommand struct {
	Target RefOrOid // OID only
}

type Command struct {
	Kind     CommandKind
	Init     *InitCommand
	Add      *AddCommand
	Unadd    *UnaddCommand
	Untrack  *UntrackCommand
	Rm       *RmCommand
	Commit   *CommitCommand
	Tag      *TagCommand
	Branch   *BranchCommand
	Switch   *SwitchCommand
	ResetAdd *ResetAddCommand
	Restore  *RestoreCommand
	Log      *LogCommand
	Config   *ConfigCommand
	Remote   *ConfigCommand
}

type RestoreCommand struct {
	Path string
}

type LogCommand struct {
	Targets []RefOrOid
}

type ConfigCommandKind int

const (
	ConfigList ConfigCommandKind = iota
	ConfigAdd
	ConfigRemove
)

type ConfigCommand struct {
	SubKind ConfigCommandKind
	Name    string // for add/remove
	Value   string // for add
}

func parseCommand(cmdArgs *CommandArgs) *Command {
	if cmdArgs.CommandKind == nil {
		return nil
	}
	switch *cmdArgs.CommandKind {
	case CommandInit:
		if len(cmdArgs.PositionalArgs) == 0 {
			return &Command{Kind: CommandInit, Init: &InitCommand{Dir: "."}}
		} else if len(cmdArgs.PositionalArgs) == 1 {
			return &Command{Kind: CommandInit, Init: &InitCommand{Dir: cmdArgs.PositionalArgs[0]}}
		}
		return nil

	case CommandAdd:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &Command{Kind: CommandAdd, Add: &AddCommand{Paths: cmdArgs.PositionalArgs}}

	case CommandUnadd:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &Command{Kind: CommandUnadd, Unadd: &UnaddCommand{
			Paths: cmdArgs.PositionalArgs,
			Opts:  UnaddOptions{Recursive: cmdArgs.Contains("-r")},
		}}

	case CommandUntrack:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &Command{Kind: CommandUntrack, Untrack: &UntrackCommand{
			Paths:     cmdArgs.PositionalArgs,
			Force:     cmdArgs.Contains("-f"),
			Recursive: cmdArgs.Contains("-r"),
		}}

	case CommandRm:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		return &Command{Kind: CommandRm, Rm: &RmCommand{
			Paths: cmdArgs.PositionalArgs,
			Opts: RemoveOptions{
				Force:         cmdArgs.Contains("-f"),
				Recursive:     cmdArgs.Contains("-r"),
				UpdateWorkDir: true,
			},
		}}

	case CommandCommit:
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
		return &Command{Kind: CommandCommit, Commit: &CommitCommand{
			Message:    message,
			AllowEmpty: cmdArgs.Contains("--allow-empty"),
		}}

	case CommandTag:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		subCmd := cmdArgs.PositionalArgs[0]
		switch subCmd {
		case "list":
			return &Command{Kind: CommandTag, Tag: &TagCommand{SubKind: TagList}}
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
			return &Command{Kind: CommandTag, Tag: &TagCommand{
				SubKind: TagAdd,
				Name:    cmdArgs.PositionalArgs[1],
				Message: message,
			}}
		case "rm":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			return &Command{Kind: CommandTag, Tag: &TagCommand{
				SubKind: TagRemove,
				Name:    cmdArgs.PositionalArgs[1],
			}}
		default:
			cmdArgs.UnusedArgs[subCmd] = true
			return nil
		}

	case CommandStatus:
		return &Command{Kind: CommandStatus}

	case CommandBranch:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		subCmd := cmdArgs.PositionalArgs[0]
		switch subCmd {
		case "list":
			return &Command{Kind: CommandBranch, Branch: &BranchCommand{SubKind: BranchList}}
		case "add":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			return &Command{Kind: CommandBranch, Branch: &BranchCommand{
				SubKind: BranchAdd,
				Name:    cmdArgs.PositionalArgs[1],
			}}
		case "rm":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			return &Command{Kind: CommandBranch, Branch: &BranchCommand{
				SubKind: BranchRemove,
				Name:    cmdArgs.PositionalArgs[1],
			}}
		default:
			cmdArgs.UnusedArgs[subCmd] = true
			return nil
		}

	case CommandSwitchDir:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := RefOrOidFromUser(cmdArgs.PositionalArgs[0], SHA1Hash)
		if target == nil {
			return nil
		}
		return &Command{Kind: CommandSwitchDir, Switch: &SwitchCommand{
			Target: *target,
			Force:  cmdArgs.Contains("-f"),
		}}

	case CommandReset:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := RefOrOidFromUser(cmdArgs.PositionalArgs[0], SHA1Hash)
		if target == nil {
			return nil
		}
		return &Command{Kind: CommandReset, Switch: &SwitchCommand{
			Target: *target,
			Force:  cmdArgs.Contains("-f"),
		}}

	case CommandResetDir:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := RefOrOidFromUser(cmdArgs.PositionalArgs[0], SHA1Hash)
		if target == nil {
			return nil
		}
		return &Command{Kind: CommandResetDir, Switch: &SwitchCommand{
			Target: *target,
			Force:  cmdArgs.Contains("-f"),
		}}

	case CommandResetAdd:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		target := RefOrOidFromUser(cmdArgs.PositionalArgs[0], SHA1Hash)
		if target == nil {
			return nil
		}
		// reset-add only accepts OIDs
		if target.IsRef {
			return nil
		}
		return &Command{Kind: CommandResetAdd, ResetAdd: &ResetAddCommand{
			Target: *target,
		}}

	case CommandRestore:
		if len(cmdArgs.PositionalArgs) != 1 {
			return nil
		}
		return &Command{Kind: CommandRestore, Restore: &RestoreCommand{
			Path: cmdArgs.PositionalArgs[0],
		}}

	case CommandLog:
		var targets []RefOrOid
		for _, arg := range cmdArgs.PositionalArgs {
			target := RefOrOidFromUser(arg, SHA1Hash)
			if target == nil {
				return nil
			}
			targets = append(targets, *target)
		}
		return &Command{Kind: CommandLog, Log: &LogCommand{Targets: targets}}

	case CommandConfig, CommandRemote:
		if len(cmdArgs.PositionalArgs) == 0 {
			return nil
		}
		subCmd := cmdArgs.PositionalArgs[0]
		var cc *ConfigCommand
		switch subCmd {
		case "list":
			cc = &ConfigCommand{SubKind: ConfigList}
		case "add":
			if len(cmdArgs.PositionalArgs) < 3 {
				return nil
			}
			cc = &ConfigCommand{
				SubKind: ConfigAdd,
				Name:    cmdArgs.PositionalArgs[1],
				Value:   strings.Join(cmdArgs.PositionalArgs[2:], " "),
			}
		case "rm":
			if len(cmdArgs.PositionalArgs) != 2 {
				return nil
			}
			cc = &ConfigCommand{
				SubKind: ConfigRemove,
				Name:    cmdArgs.PositionalArgs[1],
			}
		default:
			cmdArgs.UnusedArgs[subCmd] = true
			return nil
		}
		if *cmdArgs.CommandKind == CommandConfig {
			return &Command{Kind: CommandConfig, Config: cc}
		}
		return &Command{Kind: CommandRemote, Remote: cc}
	}
	return nil
}

type DispatchKind int

const (
	DispatchInvalidCommand DispatchKind = iota
	DispatchInvalidArgument
	DispatchHelp
	DispatchCLI
)

type Dispatch struct {
	Kind        DispatchKind
	InvalidName string
	InvalidCmd  *CommandKind
	HelpCmd     *CommandKind
	Command     *Command
}

func NewDispatch(cmdArgs *CommandArgs) *Dispatch {
	showHelp := cmdArgs.Contains("--help")
	cmdArgs.Contains("--cli") // consume it

	if cmdArgs.CommandKind != nil {
		if showHelp {
			return &Dispatch{Kind: DispatchHelp, HelpCmd: cmdArgs.CommandKind}
		}
		if cmd := parseCommand(cmdArgs); cmd != nil {
			// check for unused args
			for arg := range cmdArgs.UnusedArgs {
				return &Dispatch{
					Kind:        DispatchInvalidArgument,
					InvalidCmd:  cmdArgs.CommandKind,
					InvalidName: arg,
				}
			}
			return &Dispatch{Kind: DispatchCLI, Command: cmd}
		}
		return &Dispatch{Kind: DispatchHelp, HelpCmd: cmdArgs.CommandKind}
	} else if cmdArgs.CommandName != nil {
		return &Dispatch{Kind: DispatchInvalidCommand, InvalidName: *cmdArgs.CommandName}
	} else if showHelp {
		return &Dispatch{Kind: DispatchHelp}
	}
	return &Dispatch{Kind: DispatchHelp}
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

func PrintHelp(cmdKind *CommandKind, w io.Writer) {
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
		for kind := CommandInit; kind <= CommandRemote; kind++ {
			name := commandNames[kind]
			printAligned(w, name, commandDescrips[kind], indent)
		}
	}
}
