```
░░▒▒▓▓██████████████████████████████████████████████████████▓▓▒▒░░

                         ██████╗  ██████╗
                        ██╔════╝ ██╔═══██╗
                        ██║  ███╗██║   ██║
                        ██║   ██║██║   ██║
                        ╚██████╔╝╚██████╔╝
                         ╚═════╝  ╚═════╝
                ██████╗ ███████╗██████╗  ██████╗
                ██╔══██╗██╔════╝██╔══██╗██╔═══██╗
                ██████╔╝█████╗  ██████╔╝██║   ██║
                ██╔══██╗██╔══╝  ██╔═══╝ ██║   ██║
                ██║  ██║███████╗██║     ╚██████╔╝
                ╚═╝  ╚═╝╚══════╝╚═╝      ╚═════╝
               ███╗   ███╗ ██████╗ ███████╗ ██████╗
               ████╗ ████║██╔═══██╗██╔════╝██╔═══██╗
               ██╔████╔██║██║   ██║█████╗  ██║   ██║
               ██║╚██╔╝██║██║   ██║██╔══╝  ██║   ██║
               ██║ ╚═╝ ██║╚██████╔╝██║     ╚██████╔╝
               ╚═╝     ╚═╝ ╚═════╝ ╚═╝      ╚═════╝

                 a pure Go implementation of Git

░░▒▒▓▓██████████████████████████████████████████████████████▓▓▒▒░░
```

GoRepoMofo implements Git in Go. All of its functionality is exposed through the [Repo](repo.go) struct. See the [repo test](repo_test.go) for an example of using it as a library.

You can also run it as an executable:

```
> go run cmd/repomofo/main.go
help: repomofo <command> [<args>]

init       create an empty repository.
add        add file contents to the index.
unadd      remove any changes to a file that were added to the index.
untrack    no longer track file in the index, but leave it in the work dir.
rm         no longer track file in the index *and* remove it from the work dir.
commit     create a new commit.
tag        add, remove, and list tags.
status     show the status of uncommitted changes.
branch     add, remove, and list branches.
switch     switch to a branch or commit id.
reset      make the current branch point to a new commit id.
           updates the index, but the files in the work dir are left alone.
reset-dir  make the current branch point to a new commit id.
           updates both the index and the work dir.
           similar to `git reset --hard`.
reset-add  make the current branch point to a new commit id.
           does not update the index or the work dir.
           this is like calling reset and then adding everything to the index.
           similar to `git reset --soft`.
restore    restore files in the work dir.
log        show commit logs.
config     add, remove, and list config options.
remote     add, remove, and list remotes.
```

To run the tests:

```
> go test
> go test -tags net
```
