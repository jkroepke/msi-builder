package builder

import (
	"strings"

	"github.com/jkroepke/msi-builder/internal/cadll"
)

// restartResources writes the restartResources of every component into
// the BuilderRestartResources property, one line per entry with the
// condition that its component is installed, repaired or removed, and adds
// BuilderRestartRegister on the embedded DLL (specification section
// 17.11): an immediate action before InstallValidate that joins the
// engine's Restart Manager session, whose key the engine clears in
// InstallValidate, and registers each entry, so that the engine's
// files-in-use handling shuts down what holds or runs it. It runs after
// BuilderCloseSchedule, so programs that close by themselves are gone
// before it registers running programs.
func (b *build) restartResources() error {
	lines := b.restartLines()

	if len(lines) == 0 || !b.useRunDLL() {
		return nil
	}

	if _, ok := b.cfg.Properties[cadll.RestartProperty]; ok {
		b.errorf("properties."+cadll.RestartProperty, "the property %s is reserved for restartResources", cadll.RestartProperty)

		return nil
	}

	b.properties[cadll.RestartProperty] = strings.Join(lines, "\n")

	b.addAction(&actionInfo{name: cadll.RestartRegister, typ: caDLL, source: cadll.Key, target: cadll.RestartRegister})
	b.standardAt(seqInstallExecute, actInstallValidate)

	if b.scheduled(seqInstallExecute, cadll.CloseSchedule) {
		b.schedule(seqInstallExecute, cadll.RestartRegister, "", "", cadll.CloseSchedule, 0)
	} else {
		b.schedule(seqInstallExecute, cadll.RestartRegister, "", actInstallValidate, "", 0)
	}

	return nil
}

// restartLines returns the lines of BuilderRestartResources, in the order
// of the components and their entries.
func (b *build) restartLines() []string {
	var lines []string

	for _, comp := range b.cfg.Components {
		cond := "$" + comp.ID + "=2 OR $" + comp.ID + "=3"

		for _, res := range comp.RestartResources {
			switch {
			case res.File != "":
				lines = append(lines, cadll.RestartEntry(cond, cadll.RestartFile, res.File))
			case res.Program != "":
				lines = append(lines, cadll.RestartEntry(cond, cadll.RestartProgram, res.Program))
			case res.Service != "":
				lines = append(lines, cadll.RestartEntry(cond, cadll.RestartService, res.Service))
			}
		}
	}

	return lines
}
