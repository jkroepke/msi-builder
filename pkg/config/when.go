package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/internal/condition"
)

// Placement.When values.
const (
	WhenInstall     = "install"
	WhenUpgrade     = "upgrade"
	WhenUninstall   = "uninstall"
	WhenMaintenance = "maintenance"
)

// whenUninstall is the condition of a real uninstall that FILE-03 tested:
// true when the product is removed, false on the removal of the old
// product during a major upgrade.
const whenUninstall = `REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE`

// WhenCondition returns the condition a schedule's when names, or "" for
// none. upgrade and install tell a major upgrade from a first install by the
// upgrade properties that FindRelatedProducts sets.
func WhenCondition(cfg *Config, when string) string {
	upgradeProperty := upgradeProperties(cfg)

	switch when {
	case WhenInstall:
		if !upgrades(cfg) {
			return "NOT Installed"
		}

		return "NOT Installed AND NOT " + upgradeProperty
	case WhenUpgrade:
		return "NOT Installed AND " + upgradeProperty
	case WhenUninstall:
		return whenUninstall
	case WhenMaintenance:
		return `Installed AND NOT REMOVE~="ALL"`
	default:
		return ""
	}
}

// FullCondition returns the placement's condition with its when ANDed on.
func (p *Placement) FullCondition(cfg *Config) string {
	when := WhenCondition(cfg, p.When)

	switch {
	case when == "":
		return p.Condition
	case p.Condition == "":
		return when
	default:
		return fmt.Sprintf("(%s) AND (%s)", when, p.Condition)
	}
}

// ScheduleCondition returns the Condition of the sequence row that
// placement p of action c writes: the placement's full condition with the
// context of the action's scope ANDed on, which keeps a perMachine action
// out of a per-user install of a dual package (CA-09).
func (c *CustomAction) ScheduleCondition(cfg *Config, p *Placement) string {
	cond := p.FullCondition(cfg)

	var context string

	switch c.Scope {
	case ScopePerMachine:
		context = "ALLUSERS=1"
	case ScopePerUser:
		context = "NOT ALLUSERS"
	case "", ScopePerUserOrMachine, ScopePerMachineOrUser:
		return cond
	}

	if cond == "" {
		return context
	}

	return fmt.Sprintf("(%s) AND %s", cond, context)
}

// upgradeProperties returns the properties FindRelatedProducts sets for a
// product that RemoveExistingProducts removes: upgrade.property and those of
// the related rows that are not onlyDetect, ORed in brackets when there are
// several.
func upgradeProperties(cfg *Config) string {
	props := []string{DefaultUpgradeProperty}
	if cfg.Upgrade == nil {
		return props[0]
	}

	if cfg.Upgrade.Property != "" {
		props[0] = cfg.Upgrade.Property
	}

	for _, related := range cfg.Upgrade.Related {
		if !related.OnlyDetect && related.Property != "" && !slices.Contains(props, related.Property) {
			props = append(props, related.Property)
		}
	}

	if len(props) == 1 {
		return props[0]
	}

	return "(" + strings.Join(props, " OR ") + ")"
}

// upgrades reports whether the package finds and removes older versions.
func upgrades(cfg *Config) bool {
	return cfg.Upgrade != nil && !cfg.Upgrade.Disabled
}

// placementWhen checks a schedule's when.
func (v *validator) placementWhen(placementPath string, place Placement, cfg *Config) {
	v.oneOf(placementPath+".when", place.When, "", WhenInstall, WhenUpgrade, WhenUninstall, WhenMaintenance)

	if place.When == WhenUpgrade && !upgrades(cfg) {
		v.errorf(placementPath+".when", "upgrade needs the upgrade section, which finds older versions")
	}

	if place.When != "" {
		v.whenSequence(placementPath, place)
	}
}

// whenSequence checks that a when sits in a sequence that sets the
// properties it tests.
func (v *validator) whenSequence(placementPath string, place Placement) {
	switch place.Sequence {
	case "ui":
		if place.When == WhenUninstall || place.When == WhenMaintenance {
			v.errorf(placementPath+".when", "%s tests REMOVE, which is reliable only after InstallValidate, and the ui "+
				"sequence has no InstallValidate", place.When)
		}
	case "adminExecute", sequenceAdminUI:
		v.errorf(placementPath+".when", "an administrative install has no Installed, REMOVE or upgrade properties")
	}
}

// scheduleReboot checks the condition and when of scheduleReboot.
func (v *validator) scheduleReboot(cfg *Config) {
	if cfg.ScheduleReboot == nil {
		return
	}

	place := cfg.ScheduleReboot.Placement()
	v.placementWhen("scheduleReboot", *place, cfg)

	if err := condition.Check(place.Condition); err != nil {
		v.errorf("scheduleReboot.condition", "%v", err)
	}
}
