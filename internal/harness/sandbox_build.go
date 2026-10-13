package harness

import (
	"kyacode/internal/harness/sandbox"
)

// buildSandbox is the shared body of every sandbox runner's BuildEnv (the
// Spec's BuildEnv closure): it creates the sandbox handle, hands it to
// wiring (which declares the runner's config files, env rows, and own
// argv), and owns the failure path — a wiring error reclaims the
// half-built sandbox (it holds the real key) and comes back as the zero
// Wire (no cleanup to call, no residue left). The per-runner "on every
// error branch, delete the key-bearing dir" discipline lives here once
// instead of in eight closures; the returned Wire carries the launch's
// full wiring: the handle's root, the wiring's env rows and args, and the
// idempotent reclaim as Cleanup.
func buildSandbox(prefix string, channel sandbox.Channel, token string, wiring func(sb *sandbox.Sandbox) (env []string, args []string, err error)) (Wire, error) {
	sb, err := sandbox.New(prefix, sandbox.Key(channel, token))
	if err != nil {
		return Wire{}, err
	}
	// The wiring closures run must-idiom code that can panic (pi's
	// mustIndentJSON, the marshal-guard family): a panic must reclaim the
	// key-bearing dir exactly like an error return, not leave it for the
	// orphan sweep's 24h mercy. The success gate makes the deferred
	// reclaim a no-op on the success path — there the launch boundary
	// owns the cleanup (idempotent), and the child needs the sandbox
	// alive until it exits.
	succeeded := false
	defer func() {
		if !succeeded {
			sb.Cleanup()
		}
	}()
	env, args, err := wiring(sb)
	if err != nil {
		return Wire{}, err
	}
	succeeded = true
	return Wire{Root: sb.Root(), Env: env, Args: args, Cleanup: sb.Cleanup}, nil
}
