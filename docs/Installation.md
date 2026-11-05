# Installation

msi-builder is one static binary for Linux, macOS and Windows on amd64 and arm64.

## Release binaries

Download an archive from the
[releases page](https://github.com/jkroepke/msi-builder/releases/latest): a `.tar.gz`
for Linux and macOS, a `.zip` for Windows. Extract `msi-builder` and put it on your
`PATH`.

## Go

```shell
go install github.com/jkroepke/msi-builder/cmd/msi-builder@latest
```

## Container image

The image runs on Linux for amd64 and arm64. Mount the directory with the
configuration and its files at `/work`, and run the container as your own
user, so that it can write the package into that directory:

```shell
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" \
  ghcr.io/jkroepke/msi-builder build -o hello.msi msi.yaml
```

Without `--user`, msi-builder runs as the image's unprivileged user
(65532), which usually cannot write to a directory mounted from the host.

## Shell completion

`msi-builder completion <shell>` prints a completion script for Bash, Zsh, fish or
PowerShell, and `msi-builder completion <shell> --help` shows how to load it. See
[`msi-builder completion`](cli/msi-builder_completion.md).

## Check the installation

```shell
msi-builder version
```

Continue with [Getting Started](Getting%20Started.md).
