package backend

import (
	"time"

	"github.com/godbus/dbus/v5"

	"go.dalton.dog/spruce/internal/core"
)

// PkInfoEnum values carried in a Package signal's first field during a transaction.
const (
	pkInfoDownloading  = 10
	pkInfoUpdating     = 11
	pkInfoInstalling   = 12
	pkInfoRemoving     = 13
	pkInfoCleanup      = 14
	pkInfoObsoleting   = 15
	pkInfoFinished     = 18
	pkInfoReinstalling = 19
	pkInfoDowngrading  = 20
	pkInfoPreparing    = 21
	pkInfoDecompress   = 22
)

// pkApply turns an UpdatePackages transaction's signals into ProgressEvents.
// PackageKit downloads everything, then installs, and never reports a package
// as finished, so completion is inferred from the install stage alone.
type pkApply struct {
	emit       func(core.ProgressEvent)
	selected   map[string]bool
	done       map[string]bool
	installing string // package currently in the install stage
	stage      core.Stage
	remaining  time.Duration
}

func newPkApply(selected []core.Update, emit func(core.ProgressEvent)) *pkApply {
	a := &pkApply{emit: emit, selected: map[string]bool{}, done: map[string]bool{}}
	for _, u := range selected {
		a.selected[u.Name] = true
	}
	return a
}

func (a *pkApply) send(ev core.ProgressEvent) {
	ev.Source = "system"
	a.emit(ev)
}

// finish reports a selected package as done, once. Dependencies are skipped.
func (a *pkApply) finish(name string) {
	if name == "" || !a.selected[name] || a.done[name] {
		return
	}
	a.done[name] = true
	a.send(core.ProgressEvent{Kind: core.EventItemDone, Item: name, OK: true})
}

func (a *pkApply) signal(name string, body []any) {
	switch name {
	case "Package":
		if len(body) >= 2 {
			info, _ := toUint(body[0])
			id, _ := body[1].(string)
			n, _ := parsePackageID(id)
			a.pkg(info, n)
		}
	case "ItemProgress":
		if len(body) >= 3 {
			id, _ := body[0].(string)
			n, _ := parsePackageID(id)
			status, _ := toUint(body[1])
			pct, _ := toUint(body[2])
			if pct > 100 { // 101 = unknown
				return
			}
			stage := a.stage
			if pkStatusIsDownload(status) {
				stage = core.StageDownload
			}
			a.send(core.ProgressEvent{Kind: core.EventProgress, Item: n,
				Fraction: float64(pct) / 100.0, Stage: stage})
		}
	case "PropertiesChanged":
		// sa{sv}as: [iface, changed, invalidated].
		if len(body) >= 2 {
			if props, ok := body[1].(map[string]dbus.Variant); ok {
				a.props(props)
			}
		}
	}
}

func (a *pkApply) pkg(info uint64, n string) {
	switch info {
	case pkInfoDownloading:
		a.stage = core.StageDownload
		a.send(core.ProgressEvent{Kind: core.EventPhase, Item: n, Phase: "Downloading", Stage: core.StageDownload})
	case pkInfoUpdating, pkInfoInstalling, pkInfoReinstalling, pkInfoDowngrading:
		if a.installing != n {
			a.finish(a.installing)
		}
		a.installing = n
		a.stage = core.StageInstall
		a.send(core.ProgressEvent{Kind: core.EventPhase, Item: n, Phase: pkInfoLabel(info), Stage: core.StageInstall})
	case pkInfoFinished:
		a.finish(n)
	case pkInfoCleanup, pkInfoRemoving, pkInfoObsoleting:
		// Erasing a package's old version means its new one is already in place.
		a.finish(n)
		a.send(core.ProgressEvent{Kind: core.EventStatus, Phase: pkInfoLabel(info) + " " + n + "…"})
	case pkInfoPreparing, pkInfoDecompress:
		// Sub-steps of the install stage; not worth moving the active row for.
	default:
		// Unrecognized info: keep the old unstaged behaviour rather than guess.
		a.send(core.ProgressEvent{Kind: core.EventPhase, Item: n, Phase: "Updating"})
	}
}

func (a *pkApply) props(props map[string]dbus.Variant) {
	if v, has := props["Status"]; has {
		if s, ok := toUint(v.Value()); ok {
			if a.stage == core.StageDownload && pkStatusIsPostDownload(s) {
				// Downloads are over; stop showing the last fetched package as active.
				a.stage = core.StageInstall
				a.send(core.ProgressEvent{Kind: core.EventPhase, Stage: core.StageInstall})
			}
			if label := pkStatusLabel(s); label != "" {
				a.send(core.ProgressEvent{Kind: core.EventStatus, Phase: label})
			}
		}
	}
	if v, has := props["RemainingTime"]; has {
		if secs, ok := toUint(v.Value()); ok {
			a.remaining = time.Duration(secs) * time.Second
		}
	}
	if v, has := props["Percentage"]; has {
		if pct, ok := toUint(v.Value()); ok && pct <= 100 {
			a.send(core.ProgressEvent{Kind: core.EventOverall,
				Fraction: float64(pct) / 100.0, Remaining: a.remaining})
		}
	}
}

func pkInfoLabel(info uint64) string {
	switch info {
	case pkInfoInstalling:
		return "Installing"
	case pkInfoReinstalling:
		return "Reinstalling"
	case pkInfoDowngrading:
		return "Downgrading"
	case pkInfoCleanup:
		return "cleaning up"
	case pkInfoRemoving:
		return "removing"
	case pkInfoObsoleting:
		return "obsoleting"
	default:
		return "Updating"
	}
}

// pkStatusIsDownload reports DOWNLOAD and its DOWNLOAD_* variants.
func pkStatusIsDownload(s uint64) bool {
	return s == 8 || (s >= 20 && s <= 25)
}

// pkStatusIsPostDownload reports the statuses dnf5 moves to once fetching is
// done: signature check, test commit, commit, install/update, cleanup, hooks.
func pkStatusIsPostDownload(s uint64) bool {
	switch s {
	case 9, 10, 11, 14, 15, 16, 36:
		return true
	}
	return false
}
