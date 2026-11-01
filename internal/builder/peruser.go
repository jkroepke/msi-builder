package builder

import (
	"fmt"

	"github.com/jkroepke/msi-builder/internal/sequence"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// checkPerUserRollback warns when a deferred action that can fail a per-user
// install, of a perUser or a dual package, runs after RegisterProduct. A
// non-administrator's rollback cannot remove the registration (1403, access
// denied), so the product stays registered and a reinstall fails; a failure
// before PublishProduct leaves an entry only an administrator can remove
// (CA-01, specification section 8.2).
func (b *build) checkPerUserRollback() {
	scope := b.cfg.Package.Scope
	if !scope.MayBePerUser() {
		return
	}

	kind := "a per-user package"
	if scope.Dual() {
		kind = "a " + string(scope) + " package"
	}

	seq := b.numbered[seqInstallExecute]

	register := sequence.Find(seq, "RegisterProduct")
	if register == 0 {
		return
	}

	for actionIndex, custom := range b.cfg.CustomActions {
		if !runsPerUserAndCanFail(custom) {
			continue
		}

		for placementIndex, placement := range custom.Schedule {
			if table, ok := config.SequenceTable(placement.Sequence); !ok || table != seqInstallExecute {
				continue
			}

			if position := sequence.Find(seq, custom.ID); position > register {
				path := fmt.Sprintf("customActions[%d].schedule[%d]", actionIndex, placementIndex)
				b.warnf(path, "%s at %d runs after RegisterProduct (%d) in %s; if it fails during a per-user install, "+
					"a non-administrator's rollback cannot remove the product registration (CA-01); "+
					"schedule it before RegisterProduct",
					custom.ID, position, register, kind)
			}
		}
	}
}

// runsPerUserAndCanFail reports whether a custom action can fail a per-user
// install: a perMachine action never runs in one (CA-09).
func runsPerUserAndCanFail(custom *config.CustomAction) bool {
	return canFailInstall(custom) && custom.Scope != config.ScopePerMachine
}

// canFailInstall reports whether a custom action is deferred and its result
// can fail the install: asyncNoWait and ignore actions never do.
func canFailInstall(custom *config.CustomAction) bool {
	return custom.Execute == "deferred" && custom.Return != returnIgnore && custom.Return != "asyncNoWait"
}
