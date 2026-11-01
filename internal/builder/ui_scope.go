package builder

import (
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

const (
	dlgInstallScope = "InstallScopeDlg"
	ctlScope        = "Scope"
	ctlInstallAdmin = "InstallElevated"

	// propScope is the scope page's choice. It stays private: the command
	// line chooses with MSIINSTALLPERUSER.
	propScope    = "BuilderInstallScope"
	scopeUser    = "perUser"
	scopeMachine = "perMachine"
	condMachine  = propScope + `="` + scopeMachine + `"`

	// ctlShield is the PushButton attribute that draws the UAC shield when
	// the installation is not yet elevated.
	ctlShield = 0x800000
)

// installScope adds the scope page of a dual package: a choice between the
// current user and all users. Next sets ALLUSERS and MSIINSTALLPERUSER as
// Microsoft's single package authoring describes (spec section 8.2,
// INST-02); the ready page shows Install with the UAC shield for all users.
func (b *build) installScope(wiz *wizard) {
	scope := b.cfg.Package.Scope
	if !scope.Dual() {
		return
	}

	initial := scopeUser
	if scope == config.ScopePerMachineOrUser {
		initial = scopeMachine
	}

	b.properties[propScope] = initial

	// The page follows AppSearch, which runs in every such package; AppSearch
	// fails with 2228 without a Signature table (SEARCH-03).
	b.table("Signature")

	// The engine fixes the client's context, and with it the folders, when
	// the package opens: ALLUSERS is then 1 per machine and unset per user
	// (INST-02). The page starts on that context, so a command-line
	// MSIINSTALLPERUSER shows as the selected option.
	b.uiAction("SetInstallScopeMachine", propScope, scopeMachine, "ALLUSERS=1")
	b.uiAction("SetInstallScopeUser", propScope, scopeUser, "NOT ALLUSERS")

	// A folder remembered in the other context only says where the
	// previous install is: the page starts there, so that Next keeps the
	// update in that context.
	if b.cfg.InstallDir.Remember {
		user, machine := rememberedUser(b.cfg.InstallDir.ID), rememberedMachine(b.cfg.InstallDir.ID)
		if initial == scopeUser {
			b.uiAction("SetInstallScopePrevious", propScope, scopeMachine, "NOT ALLUSERS AND "+machine+" AND NOT "+user)
		} else {
			b.uiAction("SetInstallScopePrevious", propScope, scopeUser, "ALLUSERS=1 AND "+user+" AND NOT "+machine)
		}
	}

	page := wiz.add(&uiDialog{name: dlgInstallScope, attrs: dlgPage, first: ctlScope})
	page.innerHeader(wiz.text("InstallScopeTitle"), wiz.text("InstallScopeText"))

	width := dlgWidth - 2*margin - 10
	page.control(&uiControl{
		name: ctlScope, typ: typRadioGroup, property: propScope, ring: true,
		x: margin + 5, y: 60, w: width, h: 2 * radioStep * 2, attrs: ctlActive,
	})
	page.control(text("ScopeHint", margin+5, 130, width, 30, wiz.text("InstallScopeHint")))
	page.navigation()
	// The page comes before the welcome page, so there is no way back.
	page.find(ctlBack).attrs = ctlDisabled

	wiz.radios = append(wiz.radios,
		uiRadio{property: propScope, value: scopeUser, text: wiz.text("InstallScopeUser"), y: 0, w: width, h: radioStep},
		uiRadio{property: propScope, value: scopeMachine, text: wiz.text("InstallScopeMachine"), y: 2 * radioStep, w: width, h: radioStep},
	)

	// Next sets the context the way the engine leaves it, ALLUSERS 1 per
	// machine and unset per user, so that FindRelatedProducts and the
	// launch conditions after the page see it as the server does (UI-11).
	next := page.find(ctlNext).
		on("[ALLUSERS]", "1", condMachine).
		on("[ALLUSERS]", "{}", "NOT "+condMachine).
		on("[MSIINSTALLPERUSER]", "1", "NOT "+condMachine).
		on("[MSIINSTALLPERUSER]", "{}", condMachine)

	b.scopeFolders(next)
	next.on(evEndDialog, endReturn, condAlways)

	// Two Install buttons in one place: the shield one for all users.
	ready := wiz.byName[dlgVerifyReady]
	install := ready.find(ctlInstall)
	install.when("Hide", condMachine)
	install.when("Show", "NOT "+condMachine)
	install.when("Default", "NOT "+condMachine)

	elevated := button(ctlInstallAdmin, nextX, buttonY, wiz.text("Install"))
	elevated.attrs |= ctlShield
	elevated.on(evEndDialog, endReturn, condAlways)
	elevated.when("Hide", "NOT "+condMachine)
	elevated.when("Show", condMachine)
	elevated.when("Default", condMachine)

	at := slices.Index(ready.controls, install)
	ready.controls = slices.Insert(ready.controls, at+1, elevated)

	b.scopeFirst()
}

// scopeFirst shows the scope page on its own before the wizard, after
// AppSearch and the actions that prepare the page, and runs
// FindRelatedProducts and LaunchConditions of the UI sequence after it, all
// before costing, where ICE27 wants them. In a full-UI install only the
// client's FindRelatedProducts counts, and it finds only products of the
// context it runs in: before the page it misses a per-machine install that
// the page then switches to, and both versions stay installed; launch
// conditions that test ALLUSERS see the context the package opened in
// (UI-11).
func (b *build) scopeFirst() {
	b.schedule(seqInstallUI, dlgInstallScope, condNotInstalled, "", b.lastScopeAction, 0)

	b.scopePage = true

	// LaunchConditions is scheduled later, by launchConditionsAction.
	for i := range b.schedules[seqInstallUI] {
		if action := &b.schedules[seqInstallUI][i]; action.Name == actFindRelatedProducts {
			action.Before, action.After, action.Sequence = "", dlgInstallScope, 0
		}
	}
}

// scopeAnchor adds FindRelatedProducts or LaunchConditions that an action
// of the UI sequence is anchored to, but that no table needs, after the
// scope page, where actions placed relative to them see the chosen context.
func (b *build) scopeAnchor(table, anchor string) bool {
	if table != seqInstallUI || !b.scopePage || anchor != actFindRelatedProducts && anchor != actLaunchConditions {
		return false
	}

	switch {
	case b.scheduled(table, anchor):
	case anchor == actFindRelatedProducts && b.scheduled(table, actLaunchConditions):
		b.schedule(table, anchor, standardCondition(anchor), actLaunchConditions, "", 0)
	default:
		b.schedule(table, anchor, standardCondition(anchor), "", b.afterScopePage(), 0)
	}

	return true
}

// afterScopePage returns the action that LaunchConditions follows in the
// UI sequence of a package with the scope page: FindRelatedProducts, or
// the page.
func (b *build) afterScopePage() string {
	if b.scheduled(seqInstallUI, actFindRelatedProducts) {
		return actFindRelatedProducts
	}

	return dlgInstallScope
}

// Where a folder that follows the context lies in each context. A client
// in one context has only that context's folder properties, so the other
// context's folder comes from the environment (per machine) or from
// LocalAppDataFolder (per user, Microsoft's single package authoring).
var scopeFolders = map[string]struct{ user, machine, machine32 string }{ //nolint:gochecknoglobals // lookup table
	"ProgramFiles64Folder": {`[LocalAppDataFolder]Programs\`, `[%ProgramW6432]\`, ""},
	"CommonFiles64Folder":  {`[LocalAppDataFolder]Programs\Common\`, `[%CommonProgramW6432]\`, ""},
	"ProgramFilesFolder":   {`[LocalAppDataFolder]Programs\`, `[%ProgramFiles]\`, `[%ProgramFiles(x86)]\`},
	"CommonFilesFolder":    {`[LocalAppDataFolder]Programs\Common\`, `[%CommonProgramFiles]\`, `[%CommonProgramFiles(x86)]\`},
}

// scopeFolders keeps the public folders under Program Files and Common
// Files in the context the scope page chose. The client resolves them for
// the context the package opened in and passes them to the server, which
// takes a set directory property as it is, so a switch would install per
// machine into the user's profile or per user into Program Files
// (INST-02). Next sets each such folder to its path in the chosen context
// unless it holds another path, typed on the command line or remembered;
// CostFinalize, which runs after the page, then resolves the folders below
// it.
func (b *build) scopeFolders(next *uiControl) {
	for _, dir := range b.scopeDirs() {
		top, rel := b.dirPath(dir)
		folder := scopeFolders[top]

		user, machine := scopeProperty("InstallScopeUser_", dir.id), scopeProperty("InstallScopeMachine_", dir.id)
		b.uiAction("Set"+user, user, folder.user+rel, "")
		b.uiAction("Set"+machine, machine, folder.machine+rel, "")

		if folder.machine32 != "" {
			b.uiAction("Set"+machine+"_WOW", machine, folder.machine32+rel, "VersionNT64")
		}

		if dir.id == b.cfg.InstallDir.ID && b.cfg.InstallDir.Remember {
			b.uiAction("Set"+user+"_Remembered", user, "["+rememberedUser(dir.id)+"]", rememberedUser(dir.id))
			b.uiAction("Set"+machine+"_Remembered", machine, "["+rememberedMachine(dir.id)+"]", rememberedMachine(dir.id))
		}

		unchanged := "(NOT " + dir.id + " OR " + dir.id + " ~= " + user + " OR " + dir.id + " ~= " + machine + ")"
		next.on("["+dir.id+"]", "["+user+"]", "NOT "+condMachine+" AND "+unchanged)
		next.on("["+dir.id+"]", "["+machine+"]", condMachine+" AND "+unchanged)
	}
}

// scopeProperty names a private property that holds a folder of dir; an id
// too long for the prefix gets a generated name.
func scopeProperty(prefix, dir string) string {
	if len(prefix+dir) > config.MaxIdentifierLength {
		return generatedID(prefix, dir)
	}

	return prefix + dir
}

// scopeDirs returns the topmost public directories below a folder that
// follows the context, in key order.
func (b *build) scopeDirs() []*dirInfo {
	var dirs []*dirInfo

	for _, dir := range b.dirs {
		if dir.standard || !config.IsPublicProperty(dir.id) {
			continue
		}

		if top, _ := b.dirPath(dir); scopeFolders[top].user != "" && !b.publicAbove(dir) {
			dirs = append(dirs, dir)
		}
	}

	slices.SortFunc(dirs, func(a, b *dirInfo) int { return strings.Compare(a.id, b.id) })

	return dirs
}

// publicAbove reports whether a public directory lies between dir and its
// standard folder: CostFinalize resolves dir below the one Next sets.
func (b *build) publicAbove(dir *dirInfo) bool {
	for parent := b.dirs[dir.parent]; parent != nil && !parent.standard; parent = b.dirs[parent.parent] {
		if config.IsPublicProperty(parent.id) {
			return true
		}
	}

	return false
}

// dirPath returns the standard folder above dir and the path below it,
// ending in a backslash.
func (b *build) dirPath(dir *dirInfo) (string, string) {
	var names []string

	for dir != nil && !dir.standard {
		// An alias (DefaultDir ".") adds no name.
		if dir.long != "" && dir.long != "." {
			names = append(names, dir.long)
		}

		dir = b.dirs[dir.parent]
	}

	if dir == nil {
		return "", ""
	}

	slices.Reverse(names)

	if len(names) == 0 {
		return dir.id, ""
	}

	return dir.id, strings.Join(names, `\`) + `\`
}

// uiAction sets property to value in the UI sequence after AppSearch, which
// finds the remembered folders, and before the scope page and costing, so
// that CostFinalize resolves the folders Next sets. The actions run in the
// order they are added, after the other actions placed after AppSearch. A
// name too long for the CustomAction table gets a generated one.
func (b *build) uiAction(name, property, value, cond string) {
	if len(name) > config.MaxIdentifierLength {
		name = generatedID("SetScope", name)
	}

	b.addAction(&actionInfo{name: name, typ: caSetProperty, source: property, target: value})

	after := actAppSearch
	if b.lastScopeAction != "" {
		after = b.lastScopeAction
	}

	b.schedule(seqInstallUI, name, cond, "", after, 0)
	b.lastScopeAction = name
}
