package sequence

import "slices"

// Sequence table names.
const (
	InstallExecute = "InstallExecuteSequence"
	InstallUI      = "InstallUISequence"
	AdminExecute   = "AdminExecuteSequence"
	AdminUI        = "AdminUISequence"
	AdvtExecute    = "AdvtExecuteSequence"
)

// Standard action names used more than once below.
const (
	actCostInitialize    = "CostInitialize"
	actCostFinalize      = "CostFinalize"
	actInstallValidate   = "InstallValidate"
	actInstallInitialize = "InstallInitialize"
	actInstallFinalize   = "InstallFinalize"
	actExecuteAction     = "ExecuteAction"
	actFileCost          = "FileCost"
	actPublishFeatures   = "PublishFeatures"
	actPublishProduct    = "PublishProduct"
	actValidateProductID = "ValidateProductID"
	actInstallFiles      = "InstallFiles"
)

const (
	condNotInstalled = "NOT Installed"
	condVersionNT    = "VersionNT"
)

// Standard is a standard action's default number per table (section 9.2)
// and its condition.
type Standard struct {
	Condition string
	ies       int
	ius       int
	aes       int
	aus       int
	advt      int
}

// Number returns the default number in a sequence table, or 0 when the
// action has none there.
func (s Standard) Number(table string) int {
	switch table {
	case InstallExecute:
		return s.ies
	case InstallUI:
		return s.ius
	case AdminExecute:
		return s.aes
	case AdminUI:
		return s.aus
	case AdvtExecute:
		return s.advt
	default:
		return 0
	}
}

// LookupStandard returns the default numbers of a standard action that
// msi-builder may schedule; ok is false for any other name.
func LookupStandard(name string) (Standard, bool) {
	s, ok := standardActions[name]

	return s, ok
}

// Always returns the standard actions every product gets in a table
// (sections 9.2 and 11.7).
func Always(table string) []string {
	return slices.Clone(alwaysActions[table])
}

var alwaysActions = map[string][]string{ //nolint:gochecknoglobals // lookup table
	InstallExecute: {
		actValidateProductID, actCostInitialize, actFileCost, actCostFinalize, actInstallValidate,
		actInstallInitialize, "ProcessComponents", "UnpublishFeatures", "RegisterUser",
		"RegisterProduct", actPublishFeatures, actPublishProduct, actInstallFinalize,
	},
	InstallUI: {actValidateProductID, actCostInitialize, actFileCost, actCostFinalize, actExecuteAction},
	AdminExecute: {
		actCostInitialize, actFileCost, actCostFinalize, actInstallValidate, actInstallInitialize, "InstallAdminPackage", actInstallFiles, actInstallFinalize,
	},
	AdminUI:     {actCostInitialize, actFileCost, actCostFinalize, actExecuteAction},
	AdvtExecute: {actCostInitialize, actCostFinalize, actInstallValidate, actInstallInitialize, actPublishFeatures, actPublishProduct, actInstallFinalize},
}

// standardActions holds the default numbers of every standard action the
// builder may schedule. ScheduleReboot has none: the builder places it after
// InstallFinalize.
var standardActions = map[string]Standard{ //nolint:gochecknoglobals // lookup table
	"FindRelatedProducts":      {ies: 25, ius: 25},
	"AppSearch":                {ies: 50, ius: 50},
	"LaunchConditions":         {ies: 100, ius: 100, aes: 100, aus: 100},
	"CCPSearch":                {ies: 500, ius: 500, Condition: condNotInstalled},
	"RMCCPSearch":              {ies: 600, ius: 600, Condition: condNotInstalled},
	actValidateProductID:       {ies: 700, ius: 700},
	actCostInitialize:          {ies: 800, ius: 800, aes: 800, aus: 800, advt: 800},
	actFileCost:                {ies: 900, ius: 900, aes: 900, aus: 900},
	"IsolateComponents":        {ies: 950, ius: 950},
	actCostFinalize:            {ies: 1000, ius: 1000, aes: 1000, aus: 1000, advt: 1000},
	"SetODBCFolders":           {ies: 1100},
	"MigrateFeatureStates":     {ies: 1200, ius: 1200},
	actExecuteAction:           {ius: 1300, aus: 1300},
	actInstallValidate:         {ies: 1400, aes: 1400, advt: 1400},
	"RemoveExistingProducts":   {ies: 1401},
	actInstallInitialize:       {ies: 1500, aes: 1500, advt: 1500},
	"AllocateRegistrySpace":    {ies: 1550, Condition: condNotInstalled},
	"ProcessComponents":        {ies: 1600},
	"UnpublishComponents":      {ies: 1700},
	"MsiUnpublishAssemblies":   {ies: 1750},
	"UnpublishFeatures":        {ies: 1800},
	"StopServices":             {ies: 1900, Condition: condVersionNT},
	"DeleteServices":           {ies: 2000, Condition: condVersionNT},
	"UnregisterComPlus":        {ies: 2100},
	"SelfUnregModules":         {ies: 2200},
	"UnregisterTypeLibraries":  {ies: 2300},
	"RemoveODBC":               {ies: 2400},
	"UnregisterFonts":          {ies: 2500},
	"RemoveRegistryValues":     {ies: 2600},
	"UnregisterClassInfo":      {ies: 2700},
	"UnregisterExtensionInfo":  {ies: 2800},
	"UnregisterProgIdInfo":     {ies: 2900},
	"UnregisterMIMEInfo":       {ies: 3000},
	"RemoveIniValues":          {ies: 3100},
	"RemoveShortcuts":          {ies: 3200},
	"RemoveEnvironmentStrings": {ies: 3300},
	"RemoveDuplicateFiles":     {ies: 3400},
	"RemoveFiles":              {ies: 3500},
	"RemoveFolders":            {ies: 3600},
	"CreateFolders":            {ies: 3700},
	"MoveFiles":                {ies: 3800},
	"InstallAdminPackage":      {aes: 3900},
	actInstallFiles:            {ies: 4000, aes: 4000},
	"PatchFiles":               {ies: 4090, aes: 4090},
	"DuplicateFiles":           {ies: 4210},
	"BindImage":                {ies: 4300},
	"CreateShortcuts":          {ies: 4500, advt: 4500},
	"RegisterClassInfo":        {ies: 4600, advt: 4600},
	"RegisterExtensionInfo":    {ies: 4700, advt: 4700},
	"RegisterProgIdInfo":       {ies: 4800, advt: 4800},
	"RegisterMIMEInfo":         {ies: 4900, advt: 4900},
	"WriteRegistryValues":      {ies: 5000},
	"WriteIniValues":           {ies: 5100},
	"WriteEnvironmentStrings":  {ies: 5200},
	"RegisterFonts":            {ies: 5300},
	"InstallODBC":              {ies: 5400},
	"RegisterTypeLibraries":    {ies: 5500},
	"SelfRegModules":           {ies: 5600},
	"RegisterComPlus":          {ies: 5700},
	"InstallServices":          {ies: 5800, Condition: condVersionNT},
	"MsiConfigureServices":     {ies: 5850, Condition: "VersionNT>=600"},
	"StartServices":            {ies: 5900, Condition: condVersionNT},
	"RegisterUser":             {ies: 6000},
	"RegisterProduct":          {ies: 6100},
	"PublishComponents":        {ies: 6200, advt: 6200},
	"MsiPublishAssemblies":     {ies: 6250, advt: 6250},
	actPublishFeatures:         {ies: 6300, advt: 6300},
	actPublishProduct:          {ies: 6400, advt: 6400},
	"ScheduleReboot":           {},
	"InstallExecute":           {ies: 6500, Condition: condNotInstalled},
	"InstallExecuteAgain":      {ies: 6550, Condition: condNotInstalled},
	actInstallFinalize:         {ies: 6600, aes: 6600, advt: 6600},
}
