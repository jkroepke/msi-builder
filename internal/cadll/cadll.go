// Package cadll holds the custom action DLL that msi-builder embeds into packages
// for `run` actions (docs/design/yaml-schema.md section 4.9). The DLL is
// built from src/builderrun.c by build.sh (go generate), once per platform,
// and committed; msi-builder itself stays free of cgo because it only copies the
// bytes into the Binary table. SHA256SUMS pins the committed builds and the
// test checks them, their PE machine, their exports and their imports.
package cadll

// The rebuild needs clang 18, lld and llvm-dlltool (CLANG and DLLTOOL name
// the binaries); build.sh refuses another clang, whose bytes would differ.
//go:generate sh ./build.sh

import (
	"embed"
	"fmt"
	"strings"
)

// Key is the Binary table key of the DLL and Entry the exported entry point
// of run actions.
const (
	Key   = "BuilderRun"
	Entry = "BuilderRun"

	// DataProperty is the property an immediate action reads its command
	// from; deferred actions read CustomActionData, which the engine takes
	// from a property named like the action.
	DataProperty = "BuilderRun"

	// Separator ends the working directory in the data. It cannot occur in
	// a Windows path, so a directory never needs escaping; what follows the
	// first separator is the command line.
	Separator = "|"
)

//go:embed builderrun-x86.dll builderrun-x64.dll builderrun-arm64.dll
var dlls embed.FS

// Binary returns the DLL for a Template platform token: Intel, x64 or Arm64.
func Binary(platform string) ([]byte, error) {
	var name string

	switch platform {
	case "Intel":
		name = "builderrun-x86.dll"
	case "x64":
		name = "builderrun-x64.dll"
	case "Arm64":
		name = "builderrun-arm64.dll"
	default:
		return nil, fmt.Errorf("cadll: no custom action DLL for platform %q", platform)
	}

	data, err := dlls.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("cadll: %w", err)
	}

	return data, nil
}

// Permission grants: the property the engine builds their data in, the
// actions that apply them (named like their entry points where they have
// their own) and the kinds of object.
const (
	GrantData       = "BuilderGrantData"
	GrantCount      = "BuilderGrantCount"
	GrantEntryPoint = "BuilderGrant"
	GrantExec       = "BuilderGrantExec"
	GrantRollback   = "BuilderGrantRollback"
	GrantCommit     = "BuilderGrantCommit"

	GrantFile    = 'f'
	GrantService = 's'

	// GrantInstall and GrantMaintenance follow the operation and the count
	// (`:` and one `#` per entry, from GrantCount) in the head line of the
	// data: a first install or a maintenance install, which falls back to
	// the remembered accounts.
	GrantInstall     = 'i'
	GrantMaintenance = 'm'

	// GrantUser follows the ProductCode in the head line of a per-user
	// install: the accounts are remembered in the hive of the engine's
	// UserSID, which the DLL reads itself, instead of HKLM.
	GrantUser = '|'

	// GrantForget is the operation of the commit of a removal, which
	// forgets the remembered accounts. The head line of the data follows
	// it without a count.
	GrantForget = 'x'
)

// GrantEntry encodes one entry of the grant data, which a type-51 action
// formats: the kind of object, the access mask, the ACE inheritance flags,
// the account template (escaped, so that it arrives as authored), the object
// and the account. The account comes last, so that a `|` in its value
// cannot move the object.
func GrantEntry(kind byte, mask, flags uint32, template, object, account string) string {
	return fmt.Sprintf("%c|%x|%x|%s|%s|%s", kind, mask, flags, template, object, account)
}

// RemoveFolders starts the log lines and the names of folder removal.
// RemoveFoldersExec is the entry point of the deferred, rollback and commit
// actions, which are named RemoveFoldersExec, RemoveFoldersUndo and
// RemoveFoldersCommit and take the properties of the same names as
// CustomActionData. The engine builds that data with type-51 actions in
// the private properties RemoveFoldersData (the lines), RemoveFoldersCount
// (`:` and one `#` per entry line) and RemoveFoldersScope (RemoveFoldersKeep,
// or RemoveFoldersAll when the whole product is removed).
const (
	RemoveFolders       = "BuilderRemoveFolders"
	RemoveFoldersExec   = "BuilderRemoveFoldersExec"
	RemoveFoldersUndo   = "BuilderRemoveFoldersUndo"
	RemoveFoldersCommit = "BuilderRemoveFoldersCommit"
	RemoveFoldersData   = "BuilderRemoveFoldersData"
	RemoveFoldersCount  = "BuilderRemoveFoldersCount"
	RemoveFoldersScope  = "BuilderRemoveFoldersScope"

	RemoveFoldersKeep = 'k'
	RemoveFoldersAll  = 'a'

	// RemoveFoldersRollback, RemoveFoldersMove, RemoveFoldersDelete and
	// RemoveFoldersFinish are the operations: the rollback, the deferred
	// move, the deferred delete with rollback disabled and the commit.
	RemoveFoldersRollback = 'u'
	RemoveFoldersMove     = 'm'
	RemoveFoldersDelete   = 'd'
	RemoveFoldersFinish   = 'c'
)

// RemoveFoldersStandard lists the standard folder properties a removal must
// not reach: neither the folder itself nor a folder that holds it may be
// removed, and nothing inside WindowsFolder.
func RemoveFoldersStandard() []string {
	return []string{
		"WindowsVolume", "WindowsFolder", "SystemFolder", "System64Folder", "System16Folder",
		"ProgramFilesFolder", "ProgramFiles64Folder", "CommonFilesFolder", "CommonFiles64Folder",
		"CommonAppDataFolder", "AppDataFolder", "LocalAppDataFolder", "TempFolder",
		"PersonalFolder", "DesktopFolder", "FavoritesFolder", "MyPicturesFolder", "TemplateFolder",
		"ProgramMenuFolder", "StartMenuFolder", "StartupFolder", "SendToFolder", "RecentFolder",
		"NetHoodFolder", "PrintHoodFolder", "AdminToolsFolder", "FontsFolder",
	}
}

// RemoveFoldersEntry is the line of an entry in the removal data, which a
// type-51 action formats: `f`, the property's name, `|` and its value.
func RemoveFoldersEntry(property string) string {
	return "f" + property + "|[" + property + "]"
}

// RemoveFoldersStandardLine is the line of a standard folder: `s`, its
// name, `|` and its value.
func RemoveFoldersStandardLine(property string) string {
	return "s" + property + "|[" + property + "]"
}

// RemoveFoldersPackageLine is the line of a directory of the package, which
// a removal may reach only when the whole product goes: `p` and its path.
func RemoveFoldersPackageLine(directory string) string {
	return "p[" + directory + "]"
}

// Closing running programs: the property the builder lists the files in,
// the immediate action that asks them to close, and the entry point and
// the deferred action that end what is left. CloseExecSeparator ends each
// path in the deferred action's data.
const (
	CloseProperty      = "BuilderCloseApps"
	CloseSchedule      = "BuilderCloseSchedule"
	CloseEntryPoint    = "BuilderClose"
	CloseExec          = "BuilderCloseExec"
	CloseExecSeparator = "|"

	// closeSeparator separates the fields of a BuilderCloseApps line (the
	// condition, the timeout and the path).
	closeSeparator = "\x1f"
)

// CloseEntry encodes one line of the BuilderCloseApps property: the
// condition under which it applies, the time in milliseconds the programs
// get to close and the path of the program, which BuilderCloseSchedule
// formats. None of the texts may contain a line break or U+001F.
func CloseEntry(condition string, timeoutMS uint32, path string) string {
	return strings.Join([]string{condition, fmt.Sprintf("%x|", timeoutMS), path}, closeSeparator)
}

// Registering resources with the engine's Restart Manager session: the
// property the builder lists them in and the immediate action, named like
// its entry point, that registers them before InstallValidate. The kinds
// are a file, a program (its running processes) and a service.
const (
	RestartProperty = "BuilderRestartResources"
	RestartRegister = "BuilderRestartRegister"

	RestartFile    = 'f'
	RestartProgram = 'p'
	RestartService = 's'
)

// RestartEntry encodes one line of the BuilderRestartResources property:
// the condition under which it applies, the kind and the value, which
// BuilderRestartRegister formats. None of the texts may contain a line
// break or U+001F.
func RestartEntry(condition string, kind byte, value string) string {
	return strings.Join([]string{condition, string(kind), value}, closeSeparator)
}

// Data encodes the working directory and command line the DLL expects.
func Data(directory, command string) string {
	return directory + Separator + command
}
