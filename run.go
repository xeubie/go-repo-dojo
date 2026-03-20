package repodojo

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
)

type RunOpts struct {
	Out io.Writer
	Err io.Writer
}

var (
	ErrRepoNotFound                            = fmt.Errorf("repo not found")
	ErrRepoAlreadyExists                       = fmt.Errorf("repo already exists")
	ErrHandled                                 = fmt.Errorf("handled error")
	ErrAddIndexPathNotFound                    = fmt.Errorf("add index path not found")
	ErrRemoveIndexPathNotFound                 = fmt.Errorf("remove index path not found")
	ErrRecursiveOptionRequired                 = fmt.Errorf("recursive option required")
	ErrCannotRemoveFileWithStagedAndUnstagedChanges = fmt.Errorf("cannot remove file with staged and unstaged changes")
	ErrCannotRemoveFileWithStagedChanges       = fmt.Errorf("cannot remove file with staged changes")
	ErrCannotRemoveFileWithUnstagedChanges     = fmt.Errorf("cannot remove file with unstaged changes")
	ErrInvalidSwitchTarget                     = fmt.Errorf("invalid switch target")
)

func Run(opts RepoOpts, args []string, cwdPath string, runOpts RunOpts) error {
	cmdArgs := ParseCommandArgs(args)
	dispatch := NewDispatch(cmdArgs)

	switch dispatch.Kind {
	case DispatchInvalidCommand:
		fmt.Fprintf(runOpts.Err, "\"%s\" is not a valid command\n\n", dispatch.InvalidName)
		PrintHelp(nil, runOpts.Err)
		return ErrHandled

	case DispatchInvalidArgument:
		fmt.Fprintf(runOpts.Err, "\"%s\" is not a valid argument\n\n", dispatch.InvalidName)
		PrintHelp(dispatch.InvalidCmd, runOpts.Err)
		return ErrHandled

	case DispatchHelp:
		PrintHelp(dispatch.HelpCmd, runOpts.Out)
		return nil

	case DispatchCLI:
		return runPrint(opts, dispatch.Command, cwdPath, runOpts)
	}
	return nil
}

func runPrint(opts RepoOpts, cmd *Command, cwdPath string, runOpts RunOpts) error {
	err := runCommand(opts, cmd, cwdPath, runOpts)
	if err == nil {
		return nil
	}
	switch err {
	case ErrRepoAlreadyExists:
		fmt.Fprintf(runOpts.Err,
			"repo already exists, dummy.\n"+
				"two repos in the same directory makes no sense.\n"+
				"think about it.\n")
	case ErrRepoNotFound:
		fmt.Fprintf(runOpts.Err,
			"repo not found, dummy.\n"+
				"either you're in the wrong place or you need to make a new one like this:\n\n")
		PrintHelp(&[]CommandKind{CommandInit}[0], runOpts.Err)
	case ErrAddIndexPathNotFound:
		fmt.Fprintf(runOpts.Err, "a path you are adding does not exist\n")
	case ErrRemoveIndexPathNotFound:
		fmt.Fprintf(runOpts.Err, "a path you are removing does not exist\n")
	case ErrRecursiveOptionRequired:
		fmt.Fprintf(runOpts.Err, "to do this on a dir, add the -r flag\n")
	case ErrBranchAlreadyExists:
		fmt.Fprintf(runOpts.Err, "branch already exists\n")
	case ErrCannotDeleteCurrentBranch:
		fmt.Fprintf(runOpts.Err, "cannot delete the current branch\n")
	case ErrInvalidSwitchTarget:
		fmt.Fprintf(runOpts.Err, "your switch target doesn't look right and you should feel bad\n")
	case ErrCannotRemoveFileWithStagedAndUnstagedChanges,
		ErrCannotRemoveFileWithStagedChanges,
		ErrCannotRemoveFileWithUnstagedChanges:
		fmt.Fprintf(runOpts.Err, "a file has uncommitted changes. if you really want to do it, throw caution into the wind by adding the -f flag.\n")
	default:
		return err
	}
	return ErrHandled
}

func runCommand(opts RepoOpts, cmd *Command, cwdPath string, runOpts RunOpts) error {
	switch cmd.Kind {
	case CommandInit:
		workPath, err := filepath.Abs(filepath.Join(cwdPath, cmd.Init.Dir))
		if err != nil {
			return err
		}
		repo, err := InitRepo(workPath, opts)
		if err != nil {
			return err
		}
		defer repo.Close()

		fmt.Fprintf(runOpts.Out,
			"congrats, you just created a new repo! aren't you special.\n"+
				"try setting your name and email like this:\n\n"+
				"    repodojo config add user.name foo\n"+
				"    repodojo config add user.email foo@bar\n")
		return nil

	case CommandAdd:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()
		return repo.Add(cmd.Add.Paths)

	case CommandUnadd:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()
		return repo.Unadd(cmd.Unadd.Paths, cmd.Unadd.Opts)

	case CommandUntrack:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()
		return repo.Untrack(cmd.Untrack.Paths, cmd.Untrack.Force, cmd.Untrack.Recursive)

	case CommandRm:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()
		return repo.Remove(cmd.Rm.Paths, cmd.Rm.Opts)

	case CommandCommit:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()
		_, err = repo.Commit(CommitMetadata{
			Message: cmd.Commit.Message,
			AllowEmpty: cmd.Commit.AllowEmpty,
		})
		return err

	case CommandTag:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()

		switch cmd.Tag.SubKind {
		case TagList:
			tags, err := repo.ListTags()
			if err != nil {
				return err
			}
			for _, name := range tags {
				fmt.Fprintf(runOpts.Out, "%s\n", name)
			}
			return nil
		case TagAdd:
			_, err := repo.AddTag(AddTagInput{
				Name:    cmd.Tag.Name,
				Message: cmd.Tag.Message,
			})
			return err
		case TagRemove:
			return repo.RemoveTag(RemoveTagInput{Name: cmd.Tag.Name})
		}

	case CommandStatus:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()

		head, err := repo.Head()
		if err == nil {
			if head.IsRef {
				fmt.Fprintf(runOpts.Out, "on branch %s\n\n", head.Ref.Name)
			} else {
				fmt.Fprintf(runOpts.Out, "HEAD detached at %s\n\n", head.OID)
			}
		}

		st, err := repo.Status()
		if err != nil {
			return err
		}

		for _, path := range sortedKeys(st.Untracked) {
			fmt.Fprintf(runOpts.Out, "?? %s\n", path)
		}
		for _, path := range sortedKeys(st.WorkDirModified) {
			fmt.Fprintf(runOpts.Out, " M %s\n", path)
		}
		for _, path := range sortedKeys(st.WorkDirDeleted) {
			fmt.Fprintf(runOpts.Out, " D %s\n", path)
		}
		for _, path := range sortedKeys(st.IndexAdded) {
			fmt.Fprintf(runOpts.Out, "A  %s\n", path)
		}
		for _, path := range sortedKeys(st.IndexModified) {
			fmt.Fprintf(runOpts.Out, "M  %s\n", path)
		}
		for _, path := range sortedKeys(st.IndexDeleted) {
			fmt.Fprintf(runOpts.Out, "D  %s\n", path)
		}
		return nil

	case CommandBranch:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()

		switch cmd.Branch.SubKind {
		case BranchList:
			head, err := repo.Head()
			if err != nil {
				return err
			}
			currentBranch := ""
			if head.IsRef {
				currentBranch = head.Ref.Name
			}

			branches, err := repo.ListBranches()
			if err != nil {
				return err
			}
			for _, name := range branches {
				prefix := " "
				if name == currentBranch {
					prefix = "*"
				}
				fmt.Fprintf(runOpts.Out, "%s %s\n", prefix, name)
			}
			return nil
		case BranchAdd:
			return repo.AddBranch(AddBranchInput{Name: cmd.Branch.Name})
		case BranchRemove:
			return repo.RemoveBranch(RemoveBranchInput{Name: cmd.Branch.Name})
		}

	case CommandSwitchDir, CommandReset, CommandResetDir:
		kind := SwitchKindSwitch
		if cmd.Kind != CommandSwitchDir {
			kind = SwitchKindReset
		}
		updateWorkDir := cmd.Kind != CommandReset

		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()

		result, err := repo.Switch(SwitchInput{
			Kind:          kind,
			Target:        cmd.Switch.Target,
			UpdateWorkDir: updateWorkDir,
			Force:         cmd.Switch.Force,
		})
		if err != nil {
			return err
		}
		if !result.Success && result.Conflict != nil {
			fmt.Fprintf(runOpts.Err, "conflicts detected in the following file paths:\n")
			for _, p := range result.Conflict.StaleFiles {
				fmt.Fprintf(runOpts.Err, "  %s\n", p)
			}
			for _, p := range result.Conflict.StaleDirs {
				fmt.Fprintf(runOpts.Err, "  %s\n", p)
			}
			for _, p := range result.Conflict.UntrackedOverwritten {
				fmt.Fprintf(runOpts.Err, "  %s\n", p)
			}
			for _, p := range result.Conflict.UntrackedRemoved {
				fmt.Fprintf(runOpts.Err, "  %s\n", p)
			}
			fmt.Fprintf(runOpts.Err, "if you really want to continue, throw caution into the wind by adding the -f flag\n")
			return ErrHandled
		}
		return nil

	case CommandResetAdd:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()
		return repo.ResetAdd(cmd.ResetAdd.Target)

	case CommandRestore:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		defer repo.Close()
		return repo.Restore(cmd.Restore.Path)
	}
	return fmt.Errorf("unknown command")
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
