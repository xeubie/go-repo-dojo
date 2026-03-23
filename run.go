package repomofo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RunOpts struct {
	Out io.Writer
	Err io.Writer
}

var (
	ErrRepoNotFound                                 = fmt.Errorf("repo not found")
	ErrRepoAlreadyExists                            = fmt.Errorf("repo already exists")
	ErrHandled                                      = fmt.Errorf("handled error")
	ErrAddIndexPathNotFound                         = fmt.Errorf("add index path not found")
	ErrRemoveIndexPathNotFound                      = fmt.Errorf("remove index path not found")
	ErrRecursiveOptionRequired                      = fmt.Errorf("recursive option required")
	ErrCannotRemoveFileWithStagedAndUnstagedChanges = fmt.Errorf("cannot remove file with staged and unstaged changes")
	ErrCannotRemoveFileWithStagedChanges            = fmt.Errorf("cannot remove file with staged changes")
	ErrCannotRemoveFileWithUnstagedChanges          = fmt.Errorf("cannot remove file with unstaged changes")
	ErrInvalidSwitchTarget                          = fmt.Errorf("invalid switch target")
	ErrPathIsOutsideRepo                            = fmt.Errorf("path is outside repo")
)

func Run(opts RepoOpts, args []string, cwdPath string, runOpts RunOpts) error {
	cmdArgs := parseCommandArgs(args)
	d := newDispatch(cmdArgs, opts.Hash)

	switch d := d.(type) {
	case dispatchInvalidCommand:
		fmt.Fprintf(runOpts.Err, "\"%s\" is not a valid command\n\n", d.InvalidName)
		printHelp(nil, runOpts.Err)
		return ErrHandled

	case dispatchInvalidArgument:
		fmt.Fprintf(runOpts.Err, "\"%s\" is not a valid argument\n\n", d.InvalidName)
		printHelp(&d.InvalidCmd, runOpts.Err)
		return ErrHandled

	case dispatchHelp:
		printHelp(d.HelpCmd, runOpts.Out)
		return nil

	case dispatchCLI:
		return runCommand(opts, d.command, cwdPath, runOpts)
	}
	return nil
}

func RunPrint(opts RepoOpts, args []string, cwdPath string, runOpts RunOpts) error {
	err := Run(opts, args, cwdPath, runOpts)
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
		printHelp(&[]commandKind{commandInit}[0], runOpts.Err)
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

func runCommand(opts RepoOpts, cmd *command, cwdPath string, runOpts RunOpts) error {
	switch cmd.Kind {
	case commandInit:
		workPath, err := filepath.Abs(filepath.Join(cwdPath, cmd.Init.Dir))
		if err != nil {
			return err
		}
		_, err = InitRepo(workPath, opts)
		if err != nil {
			return err
		}

		fmt.Fprintf(runOpts.Out,
			"congrats, you just created a new repo! aren't you special.\n"+
				"try setting your name and email like this:\n\n"+
				"    repomofo config add user.name foo\n"+
				"    repomofo config add user.email foo@bar\n")
		return nil

	case commandAdd:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		return repo.Add(cmd.Add.Paths)

	case commandUnadd:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		return repo.Unadd(cmd.Unadd.Paths, cmd.Unadd.Opts)

	case commandUntrack:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		return repo.Untrack(cmd.Untrack.Paths, cmd.Untrack.Force, cmd.Untrack.Recursive)

	case commandRm:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		return repo.Remove(cmd.Rm.Paths, cmd.Rm.Opts)

	case commandCommit:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		_, err = repo.Commit(CommitMetadata{
			Message:    cmd.Commit.Message,
			AllowEmpty: cmd.Commit.AllowEmpty,
		})
		return err

	case commandTag:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}

		switch cmd.Tag.SubKind {
		case tagList:
			iter, err := repo.ListTags()
			if err != nil {
				return err
			}
			defer iter.Close()
			for {
				ref, err := iter.Next()
				if err != nil {
					return err
				}
				if ref == nil {
					break
				}
				fmt.Fprintf(runOpts.Out, "%s\n", ref.Name)
			}
			return nil
		case tagAdd:
			_, err := repo.AddTag(AddTagInput{
				Name:    cmd.Tag.Name,
				Message: cmd.Tag.Message,
			})
			return err
		case tagRemove:
			return repo.RemoveTag(RemoveTagInput{Name: cmd.Tag.Name})
		}

	case commandStatus:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}

		head, err := repo.Head()
		if err == nil {
			switch h := head.(type) {
			case RefValue:
				fmt.Fprintf(runOpts.Out, "on branch %s\n\n", h.Ref.Name)
			case OIDValue:
				fmt.Fprintf(runOpts.Out, "HEAD detached at %s\n\n", h.OID.Hex())
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
		for _, path := range sortedKeys(st.UnresolvedConflicts) {
			c := st.UnresolvedConflicts[path]
			var code string
			if c.Base {
				if c.Target {
					if c.Source {
						code = "UU" // both modified
					} else {
						code = "UD" // deleted by them
					}
				} else {
					if c.Source {
						code = "DU" // deleted by us
					} else {
						code = "??" // invalid
					}
				}
			} else {
				if c.Target {
					if c.Source {
						code = "AA" // both added
					} else {
						code = "AU" // added by us
					}
				} else {
					if c.Source {
						code = "UA" // added by them
					} else {
						code = "??" // invalid
					}
				}
			}
			fmt.Fprintf(runOpts.Out, "%s %s\n", code, path)
		}
		return nil

	case commandBranch:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}

		switch cmd.Branch.SubKind {
		case branchList:
			head, err := repo.Head()
			if err != nil {
				return err
			}
			currentBranch := ""
			if h, ok := head.(RefValue); ok {
				currentBranch = h.Ref.Name
			}

			iter, err := repo.ListBranches()
			if err != nil {
				return err
			}
			defer iter.Close()
			for {
				ref, err := iter.Next()
				if err != nil {
					return err
				}
				if ref == nil {
					break
				}
				prefix := " "
				if ref.Name == currentBranch {
					prefix = "*"
				}
				fmt.Fprintf(runOpts.Out, "%s %s\n", prefix, ref.Name)
			}
			return nil
		case branchAdd:
			return repo.AddBranch(AddBranchInput{Name: cmd.Branch.Name})
		case branchRemove:
			return repo.RemoveBranch(RemoveBranchInput{Name: cmd.Branch.Name})
		}

	case commandSwitchDir, commandReset, commandResetDir:
		kind := SwitchKindSwitch
		if cmd.Kind != commandSwitchDir {
			kind = SwitchKindReset
		}
		updateWorkDir := cmd.Kind != commandReset

		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}

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

	case commandResetAdd:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		return repo.ResetAdd(cmd.ResetAdd.Target)

	case commandRestore:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		return repo.Restore(cmd.Restore.Path)

	case commandLog:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}

		// resolve targets to OIDs
		var startOIDs []Hash
		for _, target := range cmd.Log.Targets {
			oid, err := repo.readRefRecur(target)
			if err != nil || oid == nil {
				switch v := target.(type) {
				case OIDValue:
					fmt.Fprintf(runOpts.Err, "invalid ref: %s\n", v.OID.Hex())
				case RefValue:
					fmt.Fprintf(runOpts.Err, "invalid ref: %s\n", v.Ref.ToPath())
				}
				return ErrHandled
			}
			startOIDs = append(startOIDs, oid)
		}

		iter, err := repo.Log(startOIDs)
		if err != nil {
			return err
		}
		for {
			rawObj, err := iter.Next()
			if err != nil {
				return err
			}
			if rawObj == nil {
				break
			}
			rawObj.Close()

			// read the full commit
			obj, err := repo.NewObject(rawObj.OID, true)
			if err != nil {
				return err
			}

			fmt.Fprintf(runOpts.Out, "commit %s\n", obj.OID.Hex())
			if obj.Commit.Author != "" {
				fmt.Fprintf(runOpts.Out, "author: %s\n", obj.Commit.Author)
			}
			fmt.Fprintf(runOpts.Out, "\n")

			for _, line := range strings.Split(obj.Commit.Message, "\n") {
				fmt.Fprintf(runOpts.Out, "    %s\n", line)
			}
			fmt.Fprintf(runOpts.Out, "\n")

			obj.Close()
		}
		return nil

	case commandConfig:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}

		switch cmd.Config.SubKind {
		case configList:
			config, err := repo.ListConfig()
			if err != nil {
				return err
			}
			for _, sectionName := range config.sectionOrder {
				vars := config.sections[sectionName]
				for varName, varValue := range vars {
					fmt.Fprintf(runOpts.Out, "%s.%s=%s\n", sectionName, varName, varValue)
				}
			}
			return nil
		case configAdd:
			return repo.AddConfig(AddConfigInput{
				Name:  cmd.Config.Name,
				Value: cmd.Config.Value,
			})
		case configRemove:
			return repo.RemoveConfig(RemoveConfigInput{
				Name: cmd.Config.Name,
			})
		}
	case commandRemote:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}

		switch cmd.Remote.SubKind {
		case configList:
			remotes, err := repo.ListRemotes()
			if err != nil {
				return err
			}
			for _, sectionName := range remotes.sectionOrder {
				vars := remotes.sections[sectionName]
				for varName, varValue := range vars {
					fmt.Fprintf(runOpts.Out, "%s.%s=%s\n", sectionName, varName, varValue)
				}
			}
			return nil
		case configAdd:
			return repo.AddRemote(cmd.Remote.Name, cmd.Remote.Value)
		case configRemove:
			return repo.RemoveRemote(cmd.Remote.Name)
		}
	case commandMerge, commandCherryPick:
		repo, err := OpenRepo(cwdPath, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		data, err := repo.Merge(cmd.Merge.Input)
		if err != nil {
			return err
		}
		switch r := data.Result.(type) {
		case MergeResultSuccess:
			// nothing to print
		case MergeResultNothing:
			fmt.Fprintf(runOpts.Out, "already up to date\n")
		case MergeResultFastForward:
			fmt.Fprintf(runOpts.Out, "fast-forward\n")
		case MergeResultConflict:
			for path, conflict := range r.Conflicts {
				if conflict.Renamed != nil {
					conflictType := "file/directory"
					if conflict.Target == nil {
						conflictType = "directory/file"
					}
					fmt.Fprintf(runOpts.Err, "CONFLICT (%s): there is a directory with name %s. adding %s as %s\n", conflictType, path, path, conflict.Renamed.Path)
				} else if conflict.Target != nil && conflict.Source != nil {
					conflictType := "content"
					if conflict.Base == nil {
						conflictType = "add/add"
					}
					fmt.Fprintf(runOpts.Err, "CONFLICT (%s): merge conflict in %s\n", conflictType, path)
				} else {
					conflictType := "modify/delete"
					if conflict.Target == nil {
						conflictType = "delete/modify"
					}
					fmt.Fprintf(runOpts.Err, "CONFLICT (%s): %s\n", conflictType, path)
				}
			}
			return ErrHandled
		}
		return nil

	case commandReceivePack:
		dir, err := filepath.Abs(cmd.ReceivePack.Dir)
		if err != nil {
			return err
		}
		repo, err := OpenRepo(dir, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		options := cmd.ReceivePack.Options
		options.ProtocolVersion = detectProtocolVersion()
		return repo.ReceivePack(os.Stdin, os.Stdout, options)
	case commandUploadPack:
		dir, err := filepath.Abs(cmd.UploadPack.Dir)
		if err != nil {
			return err
		}
		repo, err := OpenRepo(dir, opts)
		if err != nil {
			return ErrRepoNotFound
		}
		options := cmd.UploadPack.Options
		options.ProtocolVersion = detectProtocolVersion()
		return repo.UploadPack(os.Stdin, os.Stdout, options)
	case commandHTTPBackend:
		path := os.Getenv("PATH_TRANSLATED")
		if root := os.Getenv("GIT_PROJECT_ROOT"); root != "" {
			pathInfo := os.Getenv("PATH_INFO")
			if pathInfo == "" {
				sendNotFound(os.Stdout)
				return nil
			}
			if strings.Contains(pathInfo, "..") {
				sendNotFound(os.Stdout)
				return nil
			}
			path = root + pathInfo
		} else if strings.Contains(path, "..") {
			sendNotFound(os.Stdout)
			return nil
		}

		handler, suffix, ok := matchHTTPRoute(path)
		if !ok {
			sendNotFound(os.Stdout)
			return nil
		}

		dir, err := resolveHTTPBackendDir(path)
		if err != nil {
			sendNotFound(os.Stdout)
			return nil
		}

		dir, err = filepath.Abs(dir)
		if err != nil {
			sendNotFound(os.Stdout)
			return nil
		}

		repo, err := OpenRepo(dir, opts)
		if err != nil {
			sendNotFound(os.Stdout)
			return nil
		}

		requestMethod := os.Getenv("REQUEST_METHOD")
		if requestMethod == "" {
			requestMethod = "GET"
		}
		if requestMethod == "HEAD" {
			requestMethod = "GET"
		}

		return repo.HTTPBackend(os.Stdin, os.Stdout, HTTPBackendOptions{
			RequestMethod:   requestMethod,
			Handler:         handler,
			Suffix:          suffix,
			QueryString:     os.Getenv("QUERY_STRING"),
			ContentType:     os.Getenv("CONTENT_TYPE"),
			HasRemoteUser:   os.Getenv("REMOTE_USER") != "",
			ProtocolVersion: detectProtocolVersion(),
		})
	}
	return fmt.Errorf("unknown command")
}

func detectProtocolVersion() int {
	gitProtocol := os.Getenv("GIT_PROTOCOL")
	if gitProtocol == "" {
		return 0
	}
	version := 0
	for _, entry := range strings.Split(gitProtocol, ":") {
		value := strings.TrimLeft(entry, " ")
		if strings.HasPrefix(value, "version=") {
			v := value[len("version="):]
			switch v {
			case "2":
				version = 2
			case "1":
				if version != 2 {
					version = 1
				}
			}
		}
	}
	return version
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
