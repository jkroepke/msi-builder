package config

import (
	"embed"
	"maps"
	"path"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"
)

// translations are the built-in translations of the wizard texts, one YAML
// file per language tag, with the keys of uiStrings. They are msi-builder's
// own texts (spec section 7.6).
//
//go:embed wizardtexts/*.yaml
var translations embed.FS

// catalogs maps an LCID to its built-in translation, loaded once.
var catalogs = sync.OnceValue(func() map[int]map[string]string { //nolint:gochecknoglobals // loaded once
	out := make(map[int]map[string]string)

	entries, err := translations.ReadDir("wizardtexts")
	if err != nil {
		panic("config: " + err.Error())
	}

	for _, entry := range entries {
		data, err := translations.ReadFile(path.Join("wizardtexts", entry.Name()))
		if err != nil {
			panic("config: " + err.Error())
		}

		texts := make(map[string]string)
		if err := yaml.Unmarshal(data, &texts); err != nil {
			panic("config: " + entry.Name() + ": " + err.Error())
		}

		lcid, err := LanguageID(strings.TrimSuffix(entry.Name(), ".yaml"))
		if err != nil {
			panic("config: " + entry.Name() + ": " + err.Error())
		}

		out[lcid] = texts
	}

	return out
})

// primaryLanguage is the primary language of an LCID, without its region.
func primaryLanguage(lcid int) int {
	return lcid & 0x3FF
}

// englishPrimary is the primary language of the built-in texts.
const englishPrimary = 0x09

// scriptFallbacks name the translation of a locale whose primary language
// is shared by several scripts or languages, where the lowest LCID with
// that primary language would pick the wrong one: primary 0x1A holds
// Croatian, Serbian and Bosnian, and 0x04 Simplified and Traditional
// Chinese.
var scriptFallbacks = map[int]int{ //nolint:gochecknoglobals // lookup table
	0x241A: 0x081A, // sr-Latn-RS: sr-Latn-CS
	0x2C1A: 0x081A, // sr-Latn-ME: sr-Latn-CS
	0x0C1A: 0x081A, // sr-Cyrl-CS: sr-Latn-CS, the only Serbian texts
	0x281A: 0x081A, // sr-Cyrl-RS: sr-Latn-CS
	0x301A: 0x081A, // sr-Cyrl-ME: sr-Latn-CS
	0x1004: 0x0804, // zh-SG: zh-CN, both Simplified
	0x1404: 0x0C04, // zh-MO: zh-HK, both Traditional
}

// Translation returns the built-in wizard texts for an LCID: its own
// translation, else the one scriptFallbacks names, else the one of the
// lowest LCID with the same primary language (de-DE for de-AT), else nil.
// English returns the built-in texts.
func Translation(lcid int) map[string]string {
	if primaryLanguage(lcid) == englishPrimary {
		return UIStrings()
	}

	all := catalogs()
	if texts, ok := all[lcid]; ok {
		return maps.Clone(texts)
	}

	if texts, ok := all[scriptFallbacks[lcid]]; ok {
		return maps.Clone(texts)
	}

	best := 0

	for other := range all {
		if primaryLanguage(other) == primaryLanguage(lcid) && (best == 0 || other < best) {
			best = other
		}
	}

	if best == 0 {
		return nil
	}

	return maps.Clone(all[best])
}

// TranslatedLanguages returns the LCIDs that have a built-in translation,
// besides English.
func TranslatedLanguages() []int {
	out := make([]int, 0, len(catalogs()))
	for lcid := range catalogs() {
		out = append(out, lcid)
	}

	return out
}

// WizardTexts returns every wizard text of the package in language lcid, by
// ui.strings key. For the base language (product.language) it is ui.strings
// over the built-in translation; for a language of ui.languages it is that
// entry's strings over the built-in translation over the base language's
// texts.
func (c *Config) WizardTexts(lcid int) map[string]string {
	texts := UIStrings()

	if c.Product.Language == nil || lcid == *c.Product.Language {
		maps.Copy(texts, Translation(lcid))
		maps.Copy(texts, c.UI.Strings)

		return texts
	}

	maps.Copy(texts, c.WizardTexts(*c.Product.Language))
	maps.Copy(texts, Translation(lcid))

	if key, ok := c.UI.LanguageIDs()[lcid]; ok && c.UI.Languages[key] != nil {
		maps.Copy(texts, c.UI.Languages[key].Strings)
	}

	return texts
}
