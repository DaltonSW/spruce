package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"

	"go.dalton.dog/spruce/internal/core"
)

// PackageKit drives the system package manager (dnf/apt/zypper/...) through the
// PackageKit daemon over D-Bus. This gives us structured progress signals and
// polkit-handled authentication — no sudo, no output parsing.
type PackageKit struct{}

const (
	pkService  = "org.freedesktop.PackageKit"
	pkRootPath = "/org/freedesktop/PackageKit"
	pkTxIface  = "org.freedesktop.PackageKit.Transaction"

	// pkFlagOnlyTrusted (PK_TRANSACTION_FLAG_ENUM_ONLY_TRUSTED) is REQUIRED for a
	// real upgrade. Without it PackageKit treats the transaction as installing
	// UNTRUSTED packages and demands the org.freedesktop.packagekit.
	// package-install-untrusted polkit authorization, which the standard auth
	// agent won't grant — so the update fails instantly ("Failed to obtain
	// authentication"). Every Fedora repo package is GPG-signed, so requiring
	// trust is correct here; it's the same flag dnf and GNOME Software use, and
	// it routes to the local-session-granted system-update action instead.
	//
	// The wire value is a PkBitfield: the bit index is the enum's (sequential)
	// value, NOT the enum value itself. PK_TRANSACTION_FLAG_ENUM_ONLY_TRUSTED is
	// enum 1 (NONE=0, ONLY_TRUSTED=1, SIMULATE=2, ...), so its bit is 1<<1. Using
	// 1<<0 sends bit 0 (= NONE), which the daemon reads as only_trusted:0 and
	// rejects with the auth failure above — the exact symptom this once caused.
	//
	// We deliberately do NOT add the SIMULATE flag (1<<2) for dry runs: the dnf5
	// PackageKit backend has been observed to ignore it and apply the transaction
	// for real, so dry runs never call the mutating UpdatePackages method at all
	// (see Apply).
	pkFlagOnlyTrusted = uint64(1 << 1)

	// Pk filter bitfield, encoded the same way: the filter enum is sequential
	// (UNKNOWN=0, NONE=1, INSTALLED=2, ...) and the wire value is 1<<enum.
	pkFilterInstalled = uint64(1 << 2) // PK_FILTER_ENUM_INSTALLED
)

func (PackageKit) Name() string  { return "system" }
func (PackageKit) Icon() string  { return "" }       // nf-fa-linux (Tux)
func (PackageKit) Color() string { return "#a3be8c" } // green — core system packages

func (PackageKit) Available() bool {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return false
	}
	defer conn.Close()

	bus := conn.BusObject()
	// The daemon is D-Bus activated on demand, so it usually isn't running yet.
	// Treat it as available if it's either running or activatable.
	var running bool
	if err := bus.Call("org.freedesktop.DBus.NameHasOwner", 0, pkService).Store(&running); err == nil && running {
		return true
	}
	var activatable []string
	if err := bus.Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err != nil {
		return false
	}
	for _, n := range activatable {
		if n == pkService {
			return true
		}
	}
	return false
}

// createTransaction asks the daemon for a fresh transaction object path.
func createTransaction(ctx context.Context, conn *dbus.Conn) (dbus.ObjectPath, error) {
	root := conn.Object(pkService, pkRootPath)
	var tpath dbus.ObjectPath
	err := root.CallWithContext(ctx, pkService+".CreateTransaction", 0).Store(&tpath)
	return tpath, err
}

// runTransaction subscribes to a transaction's signals, invokes one of its
// methods, and dispatches signals to onSignal until Finished/ErrorCode or ctx
// cancellation. It blocks until the transaction completes.
func runTransaction(
	ctx context.Context,
	conn *dbus.Conn,
	tpath dbus.ObjectPath,
	method string,
	args []any,
	onSignal func(name string, body []any),
) error {
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchObjectPath(tpath),
		dbus.WithMatchInterface(pkTxIface),
	); err != nil {
		return err
	}
	defer conn.RemoveMatchSignal(
		dbus.WithMatchObjectPath(tpath),
		dbus.WithMatchInterface(pkTxIface),
	)

	// Also receive D-Bus property changes on this transaction: the Status
	// property carries the live phase (DEP_RESOLVE, DOWNLOAD, INSTALL, ...),
	// which is the only signal during dnf5's long silent depsolve. Best-effort —
	// status labels are cosmetic, so a failure here must not fail the upgrade.
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchObjectPath(tpath),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
	); err == nil {
		defer conn.RemoveMatchSignal(
			dbus.WithMatchObjectPath(tpath),
			dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		)
	}

	sigs := make(chan *dbus.Signal, 128)
	conn.Signal(sigs)
	defer conn.RemoveSignal(sigs)

	// Method returns once the request is accepted; results arrive as signals.
	tx := conn.Object(pkService, tpath)
	if call := tx.CallWithContext(ctx, method, 0, args...); call.Err != nil {
		return call.Err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig := <-sigs:
			if sig.Path != tpath {
				continue
			}
			short := sig.Name[strings.LastIndexByte(sig.Name, '.')+1:]
			switch short {
			case "ErrorCode":
				details := ""
				if len(sig.Body) > 1 {
					details, _ = sig.Body[1].(string)
				}
				return fmt.Errorf("%s", details)
			case "Finished":
				return nil
			default:
				onSignal(short, sig.Body)
			}
		}
	}
}

func (PackageKit) Check(ctx context.Context) ([]core.Update, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	tpath, err := createTransaction(ctx, conn)
	if err != nil {
		return nil, err
	}

	var ups []core.Update
	err = runTransaction(ctx, conn, tpath,
		pkTxIface+".GetUpdates", []any{uint64(0)},
		func(name string, body []any) {
			if name != "Package" || len(body) < 2 {
				return
			}
			pkgID, _ := body[1].(string)
			n, v := parsePackageID(pkgID)
			ups = append(ups, core.Update{
				Name:       n,
				NewVersion: v,
				Source:     "system",
				Ref:        pkgID,
				Kind:       "package",
			})
		})
	if err != nil {
		return nil, err
	}

	// GetUpdates only reports the candidate (new) version. Resolve the installed
	// versions in a second transaction on the same warm daemon and join them on.
	// Best-effort: if it fails we simply leave CurrentVersion empty rather than
	// failing the whole check.
	if len(ups) > 0 {
		names := make([]string, len(ups))
		for i, u := range ups {
			names[i] = u.Name
		}
		if installed := resolveInstalled(ctx, conn, names); installed != nil {
			for i := range ups {
				ups[i].CurrentVersion = installed[pkgKey(ups[i].Ref)]
			}
		}

		// Join on each update's download size via GetDetails. Best-effort: on any
		// error sizes simply stay unknown (0) and the UI omits them.
		ids := make([]string, len(ups))
		for i, u := range ups {
			ids[i] = u.Ref
		}
		if sizes := resolveSizes(ctx, conn, ids); sizes != nil {
			for i := range ups {
				ups[i].SizeBytes = sizes[ups[i].Ref]
			}
		}
	}
	return ups, nil
}

// resolveSizes returns each package's download size (bytes) keyed by package_id,
// fetched in one GetDetails transaction. Returns nil on any error.
func resolveSizes(ctx context.Context, conn *dbus.Conn, ids []string) map[string]int64 {
	if len(ids) == 0 {
		return nil
	}
	tpath, err := createTransaction(ctx, conn)
	if err != nil {
		return nil
	}
	sizes := map[string]int64{}
	err = runTransaction(ctx, conn, tpath,
		pkTxIface+".GetDetails", []any{ids},
		func(name string, body []any) {
			if name != "Details" {
				return
			}
			if id, sz, ok := parseDetails(body); ok {
				sizes[id] = sz
			}
		})
	if err != nil {
		return nil
	}
	return sizes
}

// parseDetails pulls a package_id and its download size from a PackageKit
// Details signal, handling both the modern a{sv} dict form (dnf5) and the older
// positional form (id, license, group, detail, url, size). It prefers the
// download size; for an already-cached update that is 0 and we leave it unknown
// rather than substituting the (much larger) installed size.
func parseDetails(body []any) (id string, size int64, ok bool) {
	if len(body) == 1 {
		m, isMap := body[0].(map[string]dbus.Variant)
		if !isMap {
			return "", 0, false
		}
		if v, has := m["package-id"]; has {
			id, _ = v.Value().(string)
		}
		if v, has := m["download-size"]; has {
			if n, good := toUint(v.Value()); good {
				size = int64(n)
			}
		}
		return id, size, id != ""
	}
	if len(body) >= 6 {
		id, _ = body[0].(string)
		if n, good := toUint(body[5]); good {
			size = int64(n)
		}
		return id, size, id != ""
	}
	return "", 0, false
}

// resolveInstalled returns installed versions for the given package names,
// keyed by name;arch so multilib packages (same name, different arch) don't
// clobber each other. Returns nil on any error.
func resolveInstalled(ctx context.Context, conn *dbus.Conn, names []string) map[string]string {
	tpath, err := createTransaction(ctx, conn)
	if err != nil {
		return nil
	}
	versions := map[string]string{}
	err = runTransaction(ctx, conn, tpath,
		pkTxIface+".Resolve", []any{pkFilterInstalled, names},
		func(name string, body []any) {
			if name != "Package" || len(body) < 2 {
				return
			}
			id, _ := body[1].(string)
			_, v := parsePackageID(id)
			versions[pkgKey(id)] = v
		})
	if err != nil {
		return nil
	}
	return versions
}

func (p PackageKit) Plan(ctx context.Context, selected []core.Update) (core.Plan, error) {
	return core.Plan{Backend: p.Name(), Selected: selected, NeedsRoot: true}, nil
}

func (PackageKit) Apply(ctx context.Context, plan core.Plan) (<-chan core.ProgressEvent, error) {
	events := make(chan core.ProgressEvent, 64)

	ids := make([]string, 0, len(plan.Selected))
	for _, u := range plan.Selected {
		if u.Ref != "" {
			ids = append(ids, u.Ref)
		}
	}

	go func() {
		defer close(events)

		// Dry run: never enter PackageKit's update path. The SIMULATE flag is not
		// reliably honoured by the dnf5 backend (it has applied transactions for
		// real), so we refuse to call the mutating UpdatePackages at all and just
		// report what would be updated from the already-resolved selection. This
		// touches nothing on the system — no daemon call, no polkit prompt.
		if plan.DryRun {
			events <- core.ProgressEvent{Kind: core.EventLog, Source: "system",
				Text: "(dry run — preview only, PackageKit is not invoked)"}
			for _, u := range plan.Selected {
				events <- core.ProgressEvent{Kind: core.EventPhase, Source: "system",
					Item: u.Name, Phase: "Would update"}
				events <- core.ProgressEvent{Kind: core.EventItemDone, Source: "system", OK: true}
			}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "system", OK: true}
			return
		}

		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "system", Text: err.Error()}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "system"}
			return
		}
		defer conn.Close()

		tpath, err := createTransaction(ctx, conn)
		if err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "system", Text: err.Error()}
			events <- core.ProgressEvent{Kind: core.EventDone, Source: "system"}
			return
		}

		// A real upgrade: polkit prompts here. (Dry runs returned above and never
		// reach this mutating call.)
		err = runTransaction(ctx, conn, tpath,
			pkTxIface+".UpdatePackages", []any{pkFlagOnlyTrusted, ids},
			func(name string, body []any) {
				switch name {
				case "Package":
					if len(body) >= 2 {
						id, _ := body[1].(string)
						n, _ := parsePackageID(id)
						events <- core.ProgressEvent{Kind: core.EventPhase, Source: "system", Item: n, Phase: "Updating"}
					}
				case "ItemProgress":
					if len(body) >= 3 {
						id, _ := body[0].(string)
						n, _ := parsePackageID(id)
						pct, _ := toUint(body[2])
						events <- core.ProgressEvent{Kind: core.EventProgress, Source: "system",
							Item: n, Fraction: float64(pct) / 100.0}
					}
				case "PropertiesChanged":
					// sa{sv}as: [iface, changed, invalidated]. The Status property
					// is the transaction-wide phase; surface it as a status label.
					if len(body) >= 2 {
						if props, ok := body[1].(map[string]dbus.Variant); ok {
							if v, has := props["Status"]; has {
								if s, ok := toUint(v.Value()); ok {
									if label := pkStatusLabel(s); label != "" {
										events <- core.ProgressEvent{Kind: core.EventStatus,
											Source: "system", Phase: label}
									}
								}
							}
						}
					}
				}
			})
		if err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "system", Text: err.Error()}
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "system", OK: err == nil}
	}()

	return events, nil
}

// pkStatusLabel maps a PkStatusEnum (the transaction's Status property, a
// sequential enum) to a short lowercase phase label. Returns "" for statuses
// not worth surfacing (UNKNOWN/RUNNING/FINISHED/...), so the UI keeps its prior
// label. These are the phases dnf5 moves through during a system update.
func pkStatusLabel(status uint64) string {
	switch status {
	case 1, 30: // WAIT, WAITING_FOR_LOCK
		return "waiting…"
	case 31: // WAITING_FOR_AUTH
		return "waiting for authorization…"
	case 2: // SETUP
		return "setting up…"
	case 7: // REFRESH_CACHE
		return "refreshing cache…"
	case 27: // LOADING_CACHE
		return "loading cache…"
	case 13: // DEP_RESOLVE
		return "resolving dependencies…"
	case 8, 20, 21, 22, 23, 24, 25: // DOWNLOAD + DOWNLOAD_*
		return "downloading…"
	case 14: // SIG_CHECK
		return "checking signatures…"
	case 15: // TEST_COMMIT
		return "testing changes…"
	case 9: // INSTALL
		return "installing…"
	case 10: // UPDATE
		return "updating…"
	case 16: // COMMIT
		return "applying changes…"
	case 6: // REMOVE
		return "removing…"
	case 11: // CLEANUP
		return "cleaning up…"
	case 36: // RUN_HOOK
		return "running hooks…"
	default:
		return ""
	}
}

// parsePackageID splits a PackageKit package_id ("name;version;arch;data").
func parsePackageID(id string) (name, version string) {
	parts := strings.Split(id, ";")
	if len(parts) > 0 {
		name = parts[0]
	}
	if len(parts) > 1 {
		version = parts[1]
	}
	return
}

// pkgKey is the "name;arch" portion of a package_id. It is stable across a
// package's installed and candidate versions, so it joins an update to its
// installed counterpart even on multilib systems where one name spans arches.
func pkgKey(id string) string {
	parts := strings.Split(id, ";")
	name, arch := "", ""
	if len(parts) > 0 {
		name = parts[0]
	}
	if len(parts) > 2 {
		arch = parts[2]
	}
	return name + ";" + arch
}

// toUint coerces the various unsigned integer types D-Bus may hand back.
func toUint(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint32:
		return uint64(n), true
	case uint64:
		return n, true
	case int32:
		return uint64(n), true
	case int64:
		return uint64(n), true
	default:
		return 0, false
	}
}
