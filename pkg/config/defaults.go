package config

import (
	"strings"
	"time"
	"unicode"

	"github.com/jkroepke/msi-builder/internal/msidb"
)

// Defaults of docs/design/yaml-schema.md section 4.
const (
	DefaultLanguage         = 1033
	DefaultInstallerVersion = "5.0"
	DefaultCodepage         = 1252
	DefaultCompression      = "lzx"
	DefaultCabinet          = "product.cab"
	DefaultRemoveOld        = "afterInstallInitialize"
	PreserveRemoveOld       = "afterInstallExecute"
	DefaultUpgradeProperty  = "OLDERVERSIONBEINGUPGRADED"
	DefaultNewerProperty    = "NEWERVERSIONDETECTED"
	DefaultDowngradeMessage = "A later version of [ProductName] is already installed. Setup will now exit."
	DefaultRestartDelay     = time.Minute

	// downgradeError is the upgrade.downgrade value that refuses newer
	// versions with a launch condition.
	downgradeError = "error"

	// ShowNormal, ShowMaximized and ShowMinimized are the window states of a
	// shortcut's show setting.
	ShowNormal    = "normal"
	ShowMaximized = "maximized"
	ShowMinimized = "minimized"

	// Auto marks a value that the builder derives (GUIDs, file versions).
	Auto = "auto"
	// None marks an explicitly absent value.
	None = "none"

	// executeImmediate is the execute value of an action outside the
	// install script.
	executeImmediate = "immediate"
)

// ApplyDefaults fills in every unset field with its default. It only sets
// empty fields, so applying it twice changes nothing. Load and Validate call
// it; a Config built in Go can call it directly.
func ApplyDefaults(cfg *Config) { //nolint:cyclop,gocognit,gocyclo // a flat list of independent defaults
	product := &cfg.Product
	if product.Language == nil {
		product.Language = new(DefaultLanguage)
	}

	if product.ProductCode == "" {
		product.ProductCode = Auto
	}

	if product.PackageCode == "" {
		product.PackageCode = Auto
	}

	if product.Description == "" {
		product.Description = DefaultDescription(product)
	}

	if product.Comments == "" {
		product.Comments = DefaultComments(product)
	}

	pkg := &cfg.Package
	if pkg.Scope == "" {
		pkg.Scope = ScopePerMachine
	}

	if pkg.InstallerVersion == "" {
		pkg.InstallerVersion = DefaultInstallerVersion
	}

	if pkg.Codepage == 0 {
		pkg.Codepage = defaultCodepage(cfg)
	}

	if pkg.Compression == "" {
		pkg.Compression = DefaultCompression
	}

	if pkg.Cabinet == "" {
		// After its # the Cabinet value is an identifier (ICE03), so keep
		// only the product name's identifier characters.
		pkg.Cabinet = strings.Map(identifierRune, product.Name) + ".cab"
		if !IsIdentifier(pkg.Cabinet) || msidb.CheckStreamName(pkg.Cabinet) != nil ||
			strings.ContainsFunc(product.Name, func(r rune) bool { return r > unicode.MaxASCII }) {
			// The product name may be longer than a stream name, start
			// with a digit, hold no identifier characters at all or
			// letters that dropping would garble.
			pkg.Cabinet = DefaultCabinet
		}
	}

	if pkg.LongFileNames == nil {
		pkg.LongFileNames = new(true)
	}

	if pkg.Summary.Title == "" {
		pkg.Summary.Title = "Installation Database"
	}

	if pkg.Summary.Keywords == "" {
		pkg.Summary.Keywords = "Installer"
	}

	if pkg.Summary.Author == "" {
		pkg.Summary.Author = product.Manufacturer
	}

	expandShorthands(cfg)

	if cfg.Upgrade == nil {
		cfg.Upgrade = &Upgrade{}
	}

	// UPG-05: a late removal keeps a NeverOverwrite key file or value
	// without Permanent, also when the old version had the same component
	// without the flag.
	if cfg.Upgrade.RemoveOld == "" && preserves(cfg) {
		cfg.Upgrade.RemoveOld = PreserveRemoveOld
	}

	defaultUpgrade(cfg.Upgrade)

	eachFeature(cfg.Features, func(feature *Feature, _ int) {
		// A level filled in by an earlier run follows a default set since.
		if feature.Level == nil || feature.levelIsDefault() {
			level := defaultLevel(feature.Default)
			feature.Level = &level
			feature.defaultedLevel = feature.Level
			feature.defaultedValue = level
		}

		if feature.Display == "" {
			feature.Display = "collapsed"
		}

		if feature.AllowAbsent == nil {
			feature.AllowAbsent = new(true)
		}
	})

	for _, comp := range cfg.Components {
		defaultComponent(comp, pkg.Scope)

		defaultFileTypes(comp.FileTypes)

		for _, file := range comp.Files {
			defaultFileTypes(file.FileTypes)

			for _, shortcut := range file.Shortcuts {
				if shortcut != nil {
					defaultShortcut(shortcut, product, comp)
				}
			}
		}

		for _, shortcut := range comp.Shortcuts {
			if shortcut != nil {
				defaultComponentShortcut(shortcut, product)
			}
		}
	}

	for _, action := range cfg.CustomActions {
		if action.Execute == "" {
			action.Execute = executeImmediate
		}

		if action.Impersonate == nil {
			action.Impersonate = new(true)
		}

		// exec actions must state how their exit code is handled; the
		// validator reports a missing value instead of defaulting it.
		if action.Return == "" && action.Exec == nil && action.ExecProperty == nil && action.Run == nil && action.EXE == nil {
			action.Return = "check"
		}

		if action.Exec != nil && action.Exec.Directory == "" {
			action.Exec.Directory = targetDir
		}
	}

	for _, rule := range cfg.Firewall {
		if rule.Protocol == "" {
			rule.Protocol = "tcp"
		}

		if len(rule.Profiles) == 0 {
			rule.Profiles = StringList{"all"}
		}
	}
}

// defaultShortcut names a shortcut after the product and starts it in its
// file's folder.
func defaultShortcut(shortcut *Shortcut, product *Product, comp *Component) {
	if shortcut.Name == "" {
		shortcut.Name = product.Name
	}

	if shortcut.WorkingDirectory == "" {
		shortcut.WorkingDirectory = comp.Directory
	}

	if shortcut.Show == "" {
		shortcut.Show = ShowNormal
	}
}

// defaultComponentShortcut names a component's shortcut after the product.
// Its working directory stays empty: the builder starts a file shortcut in
// the file's folder and leaves the others without one.
func defaultComponentShortcut(shortcut *Shortcut, product *Product) {
	if shortcut.Name == "" {
		shortcut.Name = product.Name
	}

	if shortcut.Show == "" {
		shortcut.Show = ShowNormal
	}
}

// defaultIncludeMin makes upgrade.minVersion inclusive, only when it is
// set, so that the automatic ProductCode of a configuration without one
// stays the same.
func defaultIncludeMin(upg *Upgrade) {
	if upg.MinVersion != "" && upg.IncludeMin == nil {
		upg.IncludeMin = new(true)
	}
}

func defaultUpgrade(upg *Upgrade) {
	if upg.AllowSameVersion == nil {
		upg.AllowSameVersion = new(true)
	}

	defaultIncludeMin(upg)

	if upg.Downgrade == "" {
		upg.Downgrade = downgradeError
	}

	if upg.DowngradeMessage == "" {
		upg.DowngradeMessage = DefaultDowngradeMessage
	}

	if upg.RemoveOld == "" {
		upg.RemoveOld = DefaultRemoveOld
	}

	if upg.MigrateFeatures == nil {
		upg.MigrateFeatures = new(true)
	}

	if upg.Property == "" {
		upg.Property = DefaultUpgradeProperty
	}

	if upg.NewerProperty == "" {
		upg.NewerProperty = DefaultNewerProperty
	}

	for i := range upg.Related {
		if upg.Related[i].IncludeMin == nil {
			upg.Related[i].IncludeMin = new(true)
		}
	}
}

// defaultLevel is the feature level for a default: on installs the feature
// at the default INSTALLLEVEL of 1, off needs a higher INSTALLLEVEL or
// ADDLOCAL, and disabled is level 0. An invalid value, which the validator
// reports, gives 1.
func defaultLevel(value string) int {
	switch value {
	case FeatureOff:
		return featureLevelOff
	case FeatureDisabled:
		return 0
	default:
		return 1
	}
}

// preserves reports whether any component keeps user-edited resources.
func preserves(cfg *Config) bool {
	for _, c := range cfg.Components {
		if c.Preserve {
			return true
		}
	}

	return false
}

// defaultRemoveOn removes on uninstall where an entry does not say when.
func defaultRemoveOn(comp *Component) {
	for _, entry := range comp.RemoveFiles {
		if entry.On == "" {
			entry.On = RemoveOnUninstall
		}
	}

	for _, entry := range comp.RemoveFolders {
		if entry.On == "" {
			entry.On = RemoveOnUninstall
		}
	}
}

func defaultComponent(comp *Component, scope Scope) { //nolint:cyclop,gocognit // a flat list of independent defaults
	if comp.Preserve {
		comp.NeverOverwrite = true
	}

	if comp.GUID == "" {
		comp.GUID = Auto
	}

	if comp.Bitness == "" {
		comp.Bitness = Auto
	}

	for _, file := range comp.Files {
		if file.Version == "" {
			file.Version = Auto
		}

		if file.Language == "" {
			file.Language = Auto
		}

		if file.Vital == nil {
			file.Vital = new(true)
		}
	}

	defaultRemoveOn(comp)

	for _, r := range comp.Registry {
		if r.Type == "" {
			r.Type = "string"
		}
	}

	for _, svc := range comp.Services {
		if svc.DisplayName == "" {
			svc.DisplayName = svc.Name
		}

		if svc.Type == "" {
			svc.Type = "ownProcess"
		}

		if svc.Start == "" {
			svc.Start = "auto"
		}

		if svc.ErrorControl == "" {
			svc.ErrorControl = "normal"
		}

		ctl := &svc.Control
		// StartServices fails a disabled service with 1920 (SVC-05).
		if ctl.StartOnInstall == nil && svc.Start == "disabled" {
			ctl.StartOnInstall = new(false)
		}

		for _, b := range []**bool{&ctl.StartOnInstall, &ctl.StopOnInstall, &ctl.StopOnUninstall, &ctl.DeleteOnUninstall, &ctl.Wait} {
			if *b == nil {
				*b = new(true)
			}
		}

		if svc.Recovery != nil && svc.Recovery.RestartDelay == nil {
			svc.Recovery.RestartDelay = new(Duration(DefaultRestartDelay))
		}
	}

	defaultServiceControl(comp.ServiceControl)

	for _, source := range comp.EventLog {
		if source.Log == "" {
			source.Log = "Application"
		}

		if source.TypesSupported == nil {
			source.TypesSupported = new(7)
		}
	}

	for _, env := range comp.Environment {
		if env.Action == "" {
			env.Action = "set"
		}

		env.Scope = environmentScope(env.Scope, scope, comp.Scope)

		// A removal has nothing to undo at uninstall.
		if env.RemoveOnUninstall == nil {
			env.RemoveOnUninstall = new(env.Action != "remove")
		}
	}
}

// eachFeature calls fn for every feature, depth first, with its depth
// starting at 1.
func eachFeature(features []*Feature, fn func(*Feature, int)) {
	var walk func([]*Feature, int)

	walk = func(fs []*Feature, depth int) {
		for _, f := range fs {
			fn(f, depth)
			walk(f.Features, depth+1)
		}
	}

	walk(features, 1)
}

// DefaultDescription is the description, and so the summary subject, of a
// product that sets none.
func DefaultDescription(p *Product) string {
	return p.Name + " " + p.Version + " installer"
}

// DefaultComments are the summary comments of a product that sets none.
func DefaultComments(p *Product) string {
	return "This installer database contains the logic and data required to install " + p.Name + "."
}

// identifierRune keeps the ASCII letters, digits, underscores and periods
// of an identifier and drops every other rune.
func identifierRune(r rune) rune {
	if r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
		return r
	}

	return -1
}

// defaultServiceControl waits for the events of services a component
// controls without installing them, unless told otherwise.
func defaultServiceControl(controls []*ControlledService) {
	for _, ctl := range controls {
		if ctl.Wait == nil {
			ctl.Wait = new(true)
		}
	}
}

// environmentScope resolves an environment variable's scope, empty or
// auto, to the context its component installs in: user in a per-user
// package or component, auto in a dual package's component without a
// scope, whose variable the builder writes per machine or per user as the
// context turns out (ENVVAR-03), and machine otherwise. An explicit
// machine or user stays.
func environmentScope(env string, pkg, comp Scope) string {
	if env != "" && env != envAuto {
		return env
	}

	if comp == "" {
		comp = pkg
	}

	switch {
	case comp.Dual():
		return envAuto
	case comp == ScopePerUser:
		return envUser
	default:
		return envMachine
	}
}
