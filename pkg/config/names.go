package config

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}

	return m
}

// standardFolders are the Windows Installer folder properties a top-level
// directory may name.
var standardFolders = set( //nolint:gochecknoglobals // lookup table
	"TARGETDIR", "AdminToolsFolder", "AppDataFolder", "CommonAppDataFolder",
	"CommonFiles64Folder", "CommonFilesFolder", "DesktopFolder", "FavoritesFolder",
	"FontsFolder", "LocalAppDataFolder", "MyPicturesFolder", "NetHoodFolder",
	"PersonalFolder", "PrintHoodFolder", "ProgramFiles64Folder", "ProgramFilesFolder",
	"ProgramMenuFolder", "RecentFolder", "SendToFolder", "StartMenuFolder",
	"StartupFolder", "System16Folder", "System64Folder", "SystemFolder",
	"TempFolder", "TemplateFolder", "WindowsFolder", "WindowsVolume",
)

// profileFolders are below the user profile; components there need an HKCU
// key path (ICE38) and RemoveFolder rows (ICE64), which the builder writes.
var profileFolders = set( //nolint:gochecknoglobals // lookup table
	"AppDataFolder", "DesktopFolder", "FavoritesFolder", "LocalAppDataFolder",
	"MyPicturesFolder", "NetHoodFolder", "PersonalFolder", "PrintHoodFolder",
	"ProgramMenuFolder", "RecentFolder", "SendToFolder", "StartMenuFolder",
	"StartupFolder", "TemplateFolder",
)

// folders32 are the 32-bit locations on 64-bit Windows (ICE80).
var folders32 = set("ProgramFilesFolder", "CommonFilesFolder", "SystemFolder") //nolint:gochecknoglobals // lookup table

// programFilesFolders are the folders programs are installed in.
var programFilesFolders = set("ProgramFilesFolder", "ProgramFiles64Folder") //nolint:gochecknoglobals // lookup table

// perMachineFolders cannot hold resources of a per-user package (ICE105).
var perMachineFolders = set( //nolint:gochecknoglobals // lookup table
	"CommonAppDataFolder", "CommonFiles64Folder", "CommonFilesFolder",
	"ProgramFiles64Folder", "ProgramFilesFolder", "System16Folder",
	"System64Folder", "SystemFolder", "WindowsFolder", "FontsFolder",
)

// ice105Folders are the Directory keys ICE105 rejects. A dual package may
// use the Program Files and Common Files folders, which follow the install
// context (spec section 8.2).
var ice105Folders = set( //nolint:gochecknoglobals // lookup table
	"AdminToolsFolder", "CommonAppDataFolder", "FontsFolder", "System16Folder",
	"System64Folder", "SystemFolder", "TempFolder", "WindowsFolder", "WindowsVolume",
)

// contextShellFolders follow the install context like Program Files, but
// the client has no property for the other context's folder, so a dual
// package cannot move a public directory below them on the scope page
// (ALLUSERS property, spec section 8.2).
var contextShellFolders = set( //nolint:gochecknoglobals // lookup table
	"DesktopFolder", "ProgramMenuFolder", "StartMenuFolder", "StartupFolder", "TemplateFolder",
)

// dualActionPrefixes start the names of the actions a dual package's
// scope page and installDir.remember add.
var dualActionPrefixes = []string{"SetInstallScope", "SetScope", "RestoreInstallDir"} //nolint:gochecknoglobals // lookup table

// standardActions may not be used as custom action ids (ICE93) and are valid
// placement anchors.
var standardActions = set( //nolint:gochecknoglobals // lookup table
	"ADMIN", "ADVERTISE", "AllocateRegistrySpace", "AppSearch", "BindImage",
	"CCPSearch", "CostFinalize", "CostInitialize", "CreateFolders", "CreateShortcuts",
	"DeleteServices", "DisableRollback", "DuplicateFiles", "ExecuteAction", "FileCost",
	"FindRelatedProducts", "ForceReboot", "INSTALL", "InstallAdminPackage",
	"InstallExecute", "InstallExecuteAgain", "InstallFiles", "InstallFinalize",
	"InstallInitialize", "InstallODBC", "InstallServices", "InstallSFPCatalogFile",
	"InstallValidate", "IsolateComponents", "LaunchConditions", "MigrateFeatureStates",
	"MoveFiles", "MsiConfigureServices", "MsiPublishAssemblies", "MsiUnpublishAssemblies",
	"PatchFiles", "ProcessComponents", "PublishComponents", "PublishFeatures",
	"PublishProduct", "RegisterClassInfo", "RegisterComPlus", "RegisterExtensionInfo",
	"RegisterFonts", "RegisterMIMEInfo", "RegisterProduct", "RegisterProgIdInfo",
	"RegisterTypeLibraries", "RegisterUser", "RemoveDuplicateFiles",
	"RemoveEnvironmentStrings", "RemoveExistingProducts", "RemoveFiles", "RemoveFolders",
	"RemoveIniValues", "RemoveODBC", "RemoveRegistryValues", "RemoveShortcuts",
	"ResolveSource", "RMCCPSearch", "ScheduleReboot", "SelfRegModules",
	"SelfUnregModules", "SetODBCFolders", "StartServices", "StopServices",
	"UnpublishComponents", "UnpublishFeatures", "UnregisterClassInfo",
	"UnregisterComPlus", "UnregisterExtensionInfo", "UnregisterFonts",
	"UnregisterMIMEInfo", "UnregisterProgIdInfo", "UnregisterTypeLibraries",
	"ValidateProductID", "WriteEnvironmentStrings", "WriteIniValues",
	"WriteRegistryValues",
)

// builderProperties are written by the builder or set by the engine, so a
// package cannot define them in properties (ICE87 covers the request ones).
// The JSON Schema refuses the same names (TestJSONSchemaBuilderProperties).
var builderProperties = set( //nolint:gochecknoglobals // lookup table
	"ProductCode", "ProductName", "ProductVersion", "ProductLanguage", "Manufacturer",
	"UpgradeCode", "ALLUSERS", "SecureCustomProperties", "MsiHiddenProperties",
	"ADDLOCAL", "ADDSOURCE", "ADDDEFAULT", "ADVERTISE", "COMPADDLOCAL",
	"COMPADDSOURCE", "COMPADDDEFAULT", "FILEADDLOCAL", "FILEADDSOURCE",
	"FILEADDDEFAULT", "REINSTALL", "REMOVE", "ARPPRODUCTICON", "ARPNOREPAIR",
	"ARPNOMODIFY", "ARPNOREMOVE", "ARPHELPLINK", "ARPURLINFOABOUT", "ARPURLUPDATEINFO",
	"ARPCOMMENTS", "ARPCONTACT", "ARPSIZE", "ARPINSTALLLOCATION",
)

// standardTables are written by the builder and cannot be redefined in tables.
var standardTables = set( //nolint:gochecknoglobals // lookup table
	"_Columns", "_Storages", "_Streams", "_StringData", "_StringPool", "_Tables",
	"_Validation", "ActionText", "AdminExecuteSequence", "AdminUISequence",
	"AdvtExecuteSequence", "AdvtUISequence", "AppSearch", "Binary", "BBControl",
	"Billboard", "CheckBox", "ComboBox", "Component", "Condition", "Control",
	"ControlCondition", "ControlEvent", "CreateFolder", "CustomAction", "Dialog",
	"Directory", "DrLocator", "Environment", "Error", "EventMapping", "Feature",
	"FeatureComponents", "File", "Icon", "InstallExecuteSequence", "InstallUISequence",
	"LaunchCondition", "ListBox", "ListView", "Media", "MsiFileHash",
	"MsiServiceConfig", "MsiServiceConfigFailureActions", "Property", "RadioButton",
	"RegLocator", "Registry", "RemoveFile", "RemoveRegistry", "ServiceControl",
	"ServiceInstall", "Shortcut", "Signature", "TextStyle", "UIText", "Upgrade",
)

// sequenceAdminUI is the schedule name of the AdminUISequence.
const sequenceAdminUI = "adminUI"

// sequenceExecute is the schedule name of the InstallExecuteSequence.
const sequenceExecute = "execute"

// sequenceTables maps the schedule names to the sequence tables.
var sequenceTables = map[string]string{ //nolint:gochecknoglobals // lookup table
	sequenceExecute: "InstallExecuteSequence",
	"ui":            "InstallUISequence",
	"adminExecute":  "AdminExecuteSequence",
	sequenceAdminUI: "AdminUISequence",
}

// peExtensions mark files that are expected to carry a version resource.
var peExtensions = set(".exe", ".dll", ".sys", ".ocx", ".cpl", ".drv", ".scr", ".efi", ".mui") //nolint:gochecknoglobals // lookup table
