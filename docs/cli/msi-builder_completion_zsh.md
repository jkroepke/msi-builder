# `msi-builder completion zsh`

Print the completion script for Zsh

Prints the completion script for Zsh.

Completion must be enabled in Zsh ("autoload -U compinit; compinit").
Load it in the current shell with:

    source <(msi-builder completion zsh)

or install it into a directory of $fpath for every new shell:

    msi-builder completion zsh > "${fpath[1]}/_msi-builder"

## Usage

```text
msi-builder completion zsh [flags]
```

## Flags

```text
  -h, --help[=true|false]              help for zsh
      --no-descriptions[=true|false]   leave out the descriptions of the completions
```

## See also

- [`msi-builder completion`](msi-builder_completion.md): Print a shell completion script
