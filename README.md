```
░▒▓█▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀█▓▒░

                        ▄██▄    ▄██▄
                       ██  ▀▀  ██  ██
                       ██ ▀██  ██  ██
                       ▀█▄ ██  ██  ██
                        ▀██▀    ▀██▀

               █▀▀█▄   ▄████▄  █▀▀█▄    ▄██▄
               ██  ██  ██  ▀▀  ██  ██  ██  ██
               █▀██▀   ████    █▀██▀   ██  ██
               ██ ▀█▄  ██  ▄▄  ██      ██  ██
               ▀▀  ▀▀  ▀████▀  ▀▀       ▀██▀

               ██▄ ▄██   ▄██▄   ▄████▄   ▄██▄
               ███▄███  ██  ██  ██  ▀▀  ██  ██
               ██▀█▀██  ██  ██  ████    ██  ██
               ██ ▀ ██  ██  ██  ██      ██  ██
               ▀▀   ▀▀   ▀██▀   ▀▀       ▀██▀

               a pure Go implementation of Git

░▒▓█▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄█▓▒░
```

GoRepoMofo implements Git in Go. See below for the functionality it currently has.

## Comparison to [go-git](https://github.com/go-git/go-git)

* GoRepoMofo has no dependencies (versus go-git's two dozen deps) and a much smaller codebase
* GoRepoMofo supports three-way merge and conflict resolution (go-git only supports simple fast-forward merges)
* go-git supports more client-side features (client-side networking, stash, blame, etc)
* Both support server-side networking (`receive-pack` and `upload-pack`)
* Both support custom object stores (see the [memoryObjectStore](object_memory.go) and how it is used in the [repo test](repo_test.go))
* GoRepoMofo is a much cooler name

## Using as a library

See the [API docs](https://pkg.go.dev/github.com/xeubie/go-repo-mofo). All of GoRepoMofo's functionality is exposed through the [Repo](https://pkg.go.dev/github.com/xeubie/go-repo-mofo#Repo) struct. See the [repo test](repo_test.go) for examples of how to use it.

## Running as an executable

The built-in command line interface is not meant to perfectly match git's diabolically inconsistent CLI:

```
> go run cmd/repomofo/main.go
help: repomofo <command> [<args>]

init          create an empty repository.
add           add file contents to the index.
unadd         remove any changes to a file that were added to the index.
              similar to `git reset HEAD`.
untrack       no longer track file in the index, but leave it in the work dir.
              similar to `git rm --cached`.
rm            no longer track file in the index *and* remove it from the work dir.
commit        create a new commit.
tag           add, remove, and list tags.
status        show the status of uncommitted changes.
branch        add, remove, and list branches.
switch        switch to a branch or commit id.
reset         make the current branch point to a new commit id.
              updates the index, but the files in the work dir are left alone.
reset-dir     make the current branch point to a new commit id.
              updates both the index and the work dir.
              similar to `git reset --hard`.
reset-add     make the current branch point to a new commit id.
              does not update the index or the work dir.
              this is like calling reset and then adding everything to the index.
              similar to `git reset --soft`.
restore       restore files in the work dir.
log           show commit logs.
config        add, remove, and list config options.
remote        add, remove, and list remotes.
merge         join two or more development histories together.
cherry-pick   apply the changes introduced by some existing commits.
receive-pack  receive what is pushed into the repository.
upload-pack   send what is fetched from the repository.
http-backend  a CGI program forwarding receive-pack and upload-pack over HTTP.
```

## Running the tests

```
> go test
> go test -tags net
```
