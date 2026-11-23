# `msi-builder completion powershell`

Print the completion script for PowerShell

Prints the completion script for PowerShell.

Load it in the current session with:

    msi-builder completion powershell | Out-String | Invoke-Expression

and add that line to your PowerShell profile to load it in every session.

## Usage

```text
msi-builder completion powershell [flags]
```

## Flags

```text
  -h, --help[=true|false]              help for powershell
      --no-descriptions[=true|false]   leave out the descriptions of the completions
```

## See also

- [`msi-builder completion`](msi-builder_completion.md): Print a shell completion script
