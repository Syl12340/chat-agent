// Package sandbox confines a subprocess to an explicit set of paths.
//
// The enforcement is Landlock: a process builds a ruleset describing which
// directories it may read and write, calls restrict_self, and from then on the
// kernel refuses everything else. It needs no root, no user namespace and no
// helper daemon, which matters because this host's AppArmor policy refuses
// unprivileged namespaces to the service account.
//
// Landlock is applied by the process being confined, never to it. That is why
// the same binary re-executes itself through the marker argument below: the
// child applies the ruleset and then becomes the requested command.
package sandbox

// Marker introduces the internal re-exec mode. It is not an API and must never
// be reachable from a request.
const Marker = "__sandbox"

// Policy is one confinement request.
type Policy struct {
	// ReadOnly directories are readable and executable, never writable.
	ReadOnly []string
	// Writable directories are readable, writable and executable.
	Writable []string
	// WritableFiles directories allow writing to files that already exist, but
	// not creating, renaming or deleting entries. /dev needs exactly this: a
	// write sink like /dev/null is expected to work, while nothing under /dev
	// should become creatable.
	WritableFiles []string
	// DenyNetwork refuses creating network and local IPC sockets. Existing
	// descriptors are not inherited by the sandbox runner.
	DenyNetwork bool
}

// Status describes what this host can enforce.
type Status struct {
	// ABI is the Landlock ABI version; 0 means unavailable.
	ABI int
	// Reason explains unavailability in a sentence a person can act on.
	Reason string
}
