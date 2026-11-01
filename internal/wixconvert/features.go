package wixconvert

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/internal/condition"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// addFeature indexes a Feature where it sits: in a feature, in a feature
// group or at the top.
func (c *converter) addFeature(elm *elem, within scope) {
	feat := &feature{e: elm}
	if id := elm.attr("Id"); id != "" {
		feat = c.featureFor(id)
		if feat.e != nil {
			c.todof(nil, "", elm.line, "the feature %s is defined twice; only the first one was converted", id)

			return
		}

		feat.e = elm
	}

	switch {
	case within.feature != nil:
		within.feature.kids = append(within.feature.kids, feat)
	case within.featureGroup != nil:
		within.featureGroup.features = append(within.featureGroup.features, feat)
	default:
		c.features = append(c.features, feat)
	}

	c.walk(elm, scope{dir: within.dir, files: within.files, feature: feat})
}

// featureFor returns the feature with an ID, which a FeatureRef may name
// before the Feature defines it.
func (c *converter) featureFor(id string) *feature {
	feat, ok := c.featureByID[id]
	if !ok {
		feat = &feature{}
		c.featureByID[id] = feat
	}

	return feat
}

func (c *converter) featureGroup(id string) *featureGroup {
	grp, ok := c.featureGrps[id]
	if !ok {
		grp = &featureGroup{}
		c.featureGrps[id] = grp
	}

	return grp
}

// resolveFeatures puts the content of each FeatureGroupRef in its place
// and notes the FeatureRefs to features the file does not define.
func (c *converter) resolveFeatures() {
	for _, id := range c.topGroupRefs {
		c.spliceGroup(nil, id)
	}

	c.eachFeature(func(feat *feature) {
		for _, id := range feat.groupRefs {
			c.spliceGroup(feat, id)
		}
	})

	for _, id := range slices.Sorted(maps.Keys(c.featureByID)) {
		if feat := c.featureByID[id]; feat.e == nil {
			c.todof(nil, "", feat.refLine, "FeatureRef %s names no feature of this file; its components are in no feature", id)
		}
	}
}

// spliceGroup adds a feature group's features and components to a feature,
// or its features to the top level when feat is nil.
func (c *converter) spliceGroup(feat *feature, groupID string) {
	grp := c.featureGrps[groupID]
	if grp == nil {
		c.todof(nil, "", 0, "FeatureGroupRef %s names no feature group of this file", groupID)

		return
	}

	if grp.used {
		c.todof(nil, "", 0, "the feature group %s is referenced twice; a feature has one parent, so only the first reference was converted", groupID)

		return
	}

	grp.used = true

	if feat == nil {
		c.features = append(c.features, grp.features...)
	} else {
		feat.kids = append(feat.kids, grp.features...)
		feat.members = append(feat.members, grp.members...)
	}

	for _, ref := range grp.groupRefs {
		c.spliceGroup(feat, ref)
	}
}

func (c *converter) eachFeature(fn func(*feature)) {
	var walk func([]*feature)

	walk = func(fs []*feature) {
		for _, f := range fs {
			fn(f)
			walk(f.kids)
		}
	}

	walk(c.features)
}

// members resolves the components of a member list.
func (c *converter) members(ms []member, seen map[string]bool) []*comp {
	var out []*comp

	for _, member := range ms {
		switch {
		case member.comp != nil:
			out = append(out, member.comp)
		case member.ref != "":
			if cp := c.compByID[member.ref]; cp != nil {
				out = append(out, cp)
			} else {
				c.todof(nil, "", 0, "ComponentRef %s names no component of this file", member.ref)
			}
		case member.group != "":
			group := c.groups[member.group]
			if group == nil {
				c.todof(nil, "", 0, "ComponentGroupRef %s names no component group of this file", member.group)

				continue
			}

			if !seen[member.group] {
				seen[member.group] = true
				out = append(out, c.members(group.members, seen)...)
			}
		}
	}

	return out
}

// needFeatures reports whether the features say more than msi-builder's
// one default feature does.
func (c *converter) needFeatures() bool {
	if len(c.features) != 1 || c.chooseFeatures() {
		return len(c.features) > 0
	}

	// An upgrade from the WiX package and ADDLOCAL name the feature by
	// its Id, so a feature other than msi-builder's own keeps it.
	first := c.features[0].e
	if len(c.features[0].kids) > 0 || first.attr("Id") != config.DefaultFeatureID {
		return true
	}

	return !c.plainFeature(first)
}

// plainFeature reports whether a feature sets nothing that msi-builder's
// default feature does not: no title or other attribute, level 1, and the
// install folder, if any, as its configurable folder.
func (c *converter) plainFeature(elm *elem) bool {
	if slices.ContainsFunc([]string{"Title", "Description", "Display", "AllowAbsent", "Absent"}, elm.has) {
		return false
	}

	if l := elm.attr("Level"); l != "" && l != "1" {
		return false
	}

	d := elm.attr("ConfigurableDirectory")

	return d == "" || c.installDir != nil && d == c.installDir.id
}

func (c *converter) emitFeatures() {
	c.eachFeature(func(f *feature) {
		for _, cp := range c.members(f.members, make(map[string]bool)) {
			cp.inFeature = true
		}
	})

	if !c.needFeatures() {
		return
	}

	for _, f := range c.features {
		c.out.add("features", c.feature(f))
	}

	for _, cp := range c.comps {
		if !cp.inFeature && cp.out != nil {
			c.todof(cp.out, "", cp.e.line, "no feature lists this component; add it to one")
		}
	}
}

// featureLevel converts the Level of a feature, with its defines filled in.
func (c *converter) featureLevel(out *omap, elm *elem) {
	text := strings.TrimSpace(c.literal(elm.attr("Level")))
	if text == "" {
		return
	}

	if !validLevel(text) {
		out.set("default", "off")
		c.todof(out, "default", elm.line, "the feature level %q is not a number from 0 to %d; convert assumed default off", text, maxLevel)

		return
	}

	if text != "1" {
		conditionLevel(out, text)
	}
}

// validLevel reports whether text is a feature level, a number from 0 to
// maxLevel.
func validLevel(text string) bool {
	level, err := strconv.Atoi(text)

	return err == nil && level >= 0 && level <= maxLevel
}

func (c *converter) feature(feat *feature) *omap { //nolint:cyclop // a flat list of independent WiX attributes
	elm := feat.e
	out := newMap()
	out.order("id", "title", "description", "default", "level", "conditions", "display", "directory", "allowAbsent", "components", "features")
	out.head = comments(elm)
	out.set("id", elm.attr("Id"))
	out.set("title", c.value(elm, "Title"))
	out.set("description", c.value(elm, "Description"))

	// default: off is level 2; other levels keep their number, which the
	// package's INSTALLLEVEL property, if any, still compares.
	c.featureLevel(out, elm)

	switch display := elm.attr("Display"); display {
	case "":
	case "expand":
		out.set("display", "expanded")
	case "collapse":
		out.set("display", "collapsed")
	case "hidden":
		out.set("display", "hidden")
	default:
		c.todof(out, "display", elm.line, "Display=%q has no counterpart", display)
	}

	if d := elm.attr("ConfigurableDirectory"); d != "" {
		out.set("directory", c.dirRef(d))
	}

	if elm.no("AllowAbsent") || elm.attr("Absent") == "disallow" {
		out.set("allowAbsent", false)
	}

	for _, a := range []string{"InstallDefault", "TypicalDefault"} {
		if v := elm.attr(a); v != "" && v != "local" && v != onInstall {
			c.todof(out, "", elm.line, "%s=%q has no counterpart", a, v)
		}
	}

	if elm.yes("AllowAdvertise") {
		c.todof(out, "", elm.line, "advertised features are never supported")
	}

	c.featureConditions(out, elm)

	var ids []string

	for _, cp := range c.members(feat.members, make(map[string]bool)) {
		if cp.out != nil {
			ids = append(ids, c.compID(cp))
		}
	}

	out.set("components", ids)

	for _, k := range feat.kids {
		out.add("features", c.feature(k))
	}

	return out
}

// conditionLevel sets default for the levels it names (0, 1 and 2) and
// level for any other; conditionalLevelProblem has checked the number.
func conditionLevel(out *omap, level string) {
	switch number, _ := strconv.Atoi(level); number {
	case 0:
		out.set("default", "disabled")
	case 1:
		out.set("default", "on")
	case 2:
		out.set("default", "off")
	default:
		out.set("level", number)
	}
}

// maxLevel is the largest feature level (Feature.Level and Condition.Level
// are i2 columns between 0 and 32767).
const maxLevel = 32767

// featureConditions converts the conditional levels of a feature: WiX v3
// writes <Condition Level="0">text</Condition>, WiX v4 and later
// <Level Value="0" Condition="..."/>.
func (c *converter) featureConditions(out *omap, elm *elem) {
	for _, kid := range elm.kids {
		var level, cond string

		switch {
		case kid.is("Condition"):
			level, cond = strings.TrimSpace(c.literal(kid.attr("Level"))), c.expand(kid, strings.TrimSpace(kid.text))
		case kid.is("Level"):
			level, cond = strings.TrimSpace(c.literal(kid.attr("Value"))), c.value(kid, "Condition")
		default:
			continue
		}

		if reason := conditionalLevelProblem(level, cond); reason != "" {
			c.todof(out, "default", kid.line, "the conditional level %q with condition %q was not converted: %s", level, cond, reason)

			continue
		}

		entry := newMap()
		entry.order("condition", "default", "level")
		entry.set("condition", cond)
		conditionLevel(entry, level)
		out.add("conditions", entry)
	}
}

// conditionalLevelProblem says why a conditional level cannot become a
// conditions entry, or returns "". A condition with {{ }} actions is only
// checked once the template is rendered.
func conditionalLevelProblem(level, cond string) string {
	if !validLevel(level) {
		return fmt.Sprintf("the level is not a number from 0 to %d", maxLevel)
	}

	if strings.TrimSpace(cond) == "" {
		return "the condition is empty"
	}

	if states := condition.States(cond); len(states) > 0 {
		return "a feature condition cannot ask for feature or component states (" + strings.Join(states, ", ") + ")"
	}

	if slices.Contains(condition.Properties(cond), "Installed") {
		return "msi-builder adds the Installed checks of feature conditions itself (NOT Installed for first installs, INST-05/INST-06)"
	}

	if strings.Contains(cond, "{{") {
		return ""
	}

	if err := condition.Check(cond); err != nil {
		return err.Error()
	}

	return ""
}
