// Package procctl owns the process-control primitives the interrupt
// ladder consumes. Three of them are equal-strength on both platform
// families:
//
//   - ConfigureGroup: spawn-time process-group setup so the graceful rung
//     can be addressed to the harness tree (Windows) — a deliberate no-op
//     on POSIX, where the child must KEEP the foreground group.
//   - GracefulInterrupt: the soft rung (Windows: CTRL_BREAK_EVENT to the
//     group; POSIX: SIGINT), delivered on the first interrupt.
//   - ForceTree: the terminal rung (Windows: taskkill /T /F, walking the
//     descendant tree at kill time; POSIX: SIGKILL to the direct child —
//     daemonizing grandchildren are the documented 24h-sweep residue).
//
// The fourth, Terminate (the TERM rung), is DELIBERATELY asymmetric: POSIX
// has the rung, Windows does not — Terminate on Windows returns nil
// WITHOUT attempting delivery, where nil means "no TERM rung exists", not
// success. Do not symmetrize it into p.Signal to make the files pair:
// Signal(SIGTERM) returns EWINDOWS on Windows and the ladder's
// nil==delivered encoding would print a fake "TERM rung not delivered"
// diagnostic on every Windows interrupt (pinned by the ladder suite).
//
// The primitives are deliberately thin and GOOS-split files otherwise
// stay pairs: no new module dependency (pure syscall / os / os/exec).
package procctl
