package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/internal/sequence"
)

// actCostFinalize resolves the folders; the builder chains actions after it.
const actCostFinalize = "CostFinalize"

// builderChains returns the actions the builder chains to CostFinalize and
// AppSearch in a table, with a note on each that says why the build adds
// it. A custom action at a number such a chain takes, such as at: 1001 in
// a package with arp.installLocation or at: 51 in one with a property
// search, fails the build; with these actions in the numbering it fails
// validate at its placement. Validate adds only the actions it can be sure
// of, so its chains are never longer than the build's:
//
//   - after AppSearch, in both install sequences: one action per search of
//     a searched property and the one that applies its value, and the
//     actions that restore a remembered install dir;
//   - after CostFinalize, in both install sequences: SetARPINSTALLLOCATION;
//   - after CostFinalize, in the execute sequence: the two actions of each
//     firewall rule whose remoteAddresses name a property;
//   - after CostFinalize, in the UI sequence of a dual-scope wizard: the
//     actions that preset the scope page, then the page,
//     FindRelatedProducts and LaunchConditions.
//
// The names are the builder's, so that a placement anchored to one of them
// is numbered too; a note is keyed by the name.
func builderChains(cfg *Config, table string) ([]sequence.Action, map[string]string) {
	if table != sequenceTables[sequenceExecute] && table != sequenceTables["ui"] {
		return nil, nil
	}

	found := &chains{notes: make(map[string]string)}

	found.searches(cfg)

	if cfg.Product.ARP.InstallLocation != "" {
		found.add(actCostFinalize, "product.arp.installLocation", "SetARPINSTALLLOCATION")
	}

	if table == sequenceTables[sequenceExecute] {
		found.firewall(cfg)
	}

	if scopePageIn(cfg, table) {
		found.scopePage(cfg)
	}

	return found.actions, found.notes
}

// chains collects the actions of builderChains.
type chains struct {
	notes   map[string]string
	actions []sequence.Action
}

// add chains names one after the other after anchor, for what note names.
func (c *chains) add(anchor, note string, names ...string) {
	for _, name := range names {
		c.actions = append(c.actions, sequence.Action{Name: name, After: anchor})
		c.notes[name] = fmt.Sprintf("%s comes from %s: the build chains it after %s",
			strings.TrimSuffix(name, "."), note, strings.TrimSuffix(anchor, "."))
		anchor = name
	}
}

// searches adds the actions that apply property searches and restore a
// remembered install dir after AppSearch.
func (c *chains) searches(cfg *Config) {
	for _, name := range cfg.Properties.Names() {
		if prop := cfg.Properties[name]; prop != nil && len(prop.Search) > 0 {
			c.add(actAppSearch, "properties."+name+".search", searchActions(name, prop)...)
		}
	}

	if !cfg.InstallDir.Remember {
		return
	}

	if !cfg.Package.Scope.Dual() {
		c.add(actAppSearch, "installDir.remember", RememberActionID)

		return
	}

	c.add(actAppSearch, "installDir.remember", RememberActionID+"User")
	c.add(actAppSearch, "installDir.remember", RememberActionID+"Machine")
}

// firewall adds the actions that set the remote addresses of a firewall
// rule from a property after CostFinalize.
func (c *chains) firewall(cfg *Config) {
	for i, rule := range cfg.Firewall {
		if rule == nil || !namesProperty(rule.RemoteAddresses) {
			continue
		}

		path := fmt.Sprintf("firewall[%d]", i)
		if rule.origin != "" {
			path = rule.origin
		}

		id := firewallRuleID(rule)
		c.add(actCostFinalize, path+".remoteAddresses", "SetFwRemoteIPDefault"+id, "SetFwRemoteIP"+id)
	}
}

// scopePage adds the actions that preset the scope page, the page and,
// when the package runs them or a placement is anchored to them,
// FindRelatedProducts and LaunchConditions after CostFinalize. The
// actions that keep each folder in the chosen context are left out.
func (c *chains) scopePage(cfg *Config) {
	page := []string{"SetInstallScopeMachine", "SetInstallScopeUser"}
	if cfg.InstallDir.Remember {
		page = append(page, "SetInstallScopePrevious")
	}

	page = append(page, scopePageNode)

	if upgrades(cfg) || anchoredInUI(cfg, actFindRelatedProducts) {
		page = append(page, actFindRelatedProducts)
	}

	if hasLaunchConditions(cfg) || anchoredInUI(cfg, actLaunchConditions) {
		page = append(page, actLaunchConditions)
	}

	c.add(actCostFinalize, "the scope page of package.scope "+string(cfg.Package.Scope), page...)
}

// searchActions returns the actions that apply the searches of a searched
// property, in order: SearchApply_<NAME>, SearchApply2_<NAME> and so on,
// then SearchDefault_<NAME> when it has a value. A name too long for the
// CustomAction table gets the builder's generated one.
func searchActions(name string, prop *Property) []string {
	names := make([]string, 0, len(prop.Search)+1)

	for i := range prop.Search {
		suffix := ""
		if i > 0 {
			suffix = strconv.Itoa(i + 1)
		}

		names = append(names, builderSearchName("SearchApply"+suffix+"_", name))
	}

	if prop.Value != "" {
		names = append(names, builderSearchName("SearchDefault_", name))
	}

	return names
}

// anchoredInUI reports whether a custom action is placed before or after
// action in the UI sequence.
func anchoredInUI(cfg *Config, action string) bool {
	for _, custom := range cfg.CustomActions {
		for _, place := range custom.Schedule {
			if place.Sequence == "ui" && (place.Before == action || place.After == action) {
				return true
			}
		}
	}

	return false
}

// builderSearchName is the builder's name for a search action: prefix and
// property, or, when that is too long, the prefix without its underscore
// and a hash of the property.
func builderSearchName(prefix, name string) string {
	if len(prefix)+len(name) <= MaxIdentifierLength {
		return prefix + name
	}

	sum := sha256.Sum256([]byte(name))

	return strings.TrimSuffix(prefix, "_") + strings.ToUpper(hex.EncodeToString(sum[:16]))
}

// firewallRuleID is the builder's id of a firewall rule in the names of
// its actions.
func firewallRuleID(rule *FirewallRule) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"Firewall", rule.Name, rule.Protocol, rule.Port, rule.Program}, "\x1f")))

	return strings.ToUpper(hex.EncodeToString(sum[:4]))
}

// namesProperty reports whether text names a property in brackets, which
// makes the builder set a firewall rule's remote addresses at run time.
func namesProperty(text string) bool {
	for {
		i := strings.IndexByte(text, '[')
		if i < 0 {
			return false
		}

		end := strings.IndexByte(text[i:], ']')
		if end < 0 {
			return false
		}

		if IsIdentifier(text[i+1 : i+end]) {
			return true
		}

		text = text[i+end+1:]
	}
}
