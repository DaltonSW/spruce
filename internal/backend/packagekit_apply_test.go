package backend

import (
	"testing"

	"github.com/godbus/dbus/v5"

	"go.dalton.dog/spruce/internal/core"
)

func pkgSig(info uint32, name string) []any {
	return []any{info, name + ";1.0;x86_64;fedora", ""}
}

// A download-then-install transaction must report a selected package done only
// once its install stage is over, and never report unselected deps.
func TestPkApplyStagedCompletion(t *testing.T) {
	var evs []core.ProgressEvent
	a := newPkApply([]core.Update{{Name: "a"}, {Name: "b"}}, func(ev core.ProgressEvent) { evs = append(evs, ev) })

	a.signal("Package", pkgSig(pkInfoDownloading, "a"))
	a.signal("Package", pkgSig(pkInfoDownloading, "b"))
	a.signal("PropertiesChanged", []any{pkTxIface, map[string]dbus.Variant{"Status": dbus.MakeVariant(uint32(15))}})
	a.signal("Package", pkgSig(pkInfoUpdating, "a"))
	a.signal("Package", pkgSig(pkInfoInstalling, "libdep"))
	a.signal("Package", pkgSig(pkInfoUpdating, "b"))
	a.signal("Package", pkgSig(pkInfoCleanup, "b"))
	a.signal("PropertiesChanged", []any{pkTxIface, map[string]dbus.Variant{
		"Percentage": dbus.MakeVariant(uint32(80)), "RemainingTime": dbus.MakeVariant(uint32(12))}})

	var done []string
	var sawGap, sawOverall bool
	for _, ev := range evs {
		switch ev.Kind {
		case core.EventItemDone:
			done = append(done, ev.Item)
		case core.EventPhase:
			if ev.Item == "" && ev.Stage == core.StageInstall {
				sawGap = true
			}
			if ev.Phase == "Downloading" && ev.Stage != core.StageDownload {
				t.Errorf("download phase for %s not tagged StageDownload", ev.Item)
			}
		case core.EventOverall:
			sawOverall = ev.Fraction == 0.8 && ev.Remaining.Seconds() == 12
		}
	}
	if len(done) != 2 || done[0] != "a" || done[1] != "b" {
		t.Errorf("done = %v, want [a b]", done)
	}
	if !sawGap {
		t.Error("no item-less StageInstall phase when downloads ended")
	}
	if !sawOverall {
		t.Error("Percentage/RemainingTime not surfaced as EventOverall")
	}
}
