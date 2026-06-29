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

	// Pk transaction flags. SIMULATE resolves what would change and emits the
	// Package signals without installing — a safe, repeatable dry run.
	pkFlagNone     = uint64(0)
	pkFlagSimulate = uint64(1 << 1)
)

func (PackageKit) Name() string { return "system" }

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
	return ups, nil
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

		flags := pkFlagNone
		if plan.DryRun {
			flags = pkFlagSimulate // resolves only; no polkit prompt, no changes
			events <- core.ProgressEvent{Kind: core.EventLog, Source: "system",
				Text: "(dry run — simulating, nothing will change)"}
		}

		// the package_ids to update. polkit prompts here unless simulating.
		err = runTransaction(ctx, conn, tpath,
			pkTxIface+".UpdatePackages", []any{flags, ids},
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
				}
			})
		if err != nil {
			events <- core.ProgressEvent{Kind: core.EventError, Source: "system", Text: err.Error()}
		}
		events <- core.ProgressEvent{Kind: core.EventDone, Source: "system", OK: err == nil}
	}()

	return events, nil
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
