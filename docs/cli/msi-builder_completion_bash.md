# `msi-builder completion bash`

Print the completion script for Bash

Prints the completion script for Bash.

The script needs the bash-completion package. Load it in the current
shell with:

    source <(msi-builder completion bash)

or install it for every new shell, for example on Linux:

    msi-builder completion bash > /etc/bash_completion.d/msi-builder

## Usage

```text
msi-builder completion bash [flags]
```

## Flags

```text
  -h, --help[=true|false]              help for bash
      --no-descriptions[=true|false]   leave out the descriptions of the completions
```

## See also

- [`msi-builder completion`](msi-builder_completion.md): Print a shell completion script
