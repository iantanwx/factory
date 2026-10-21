// Package acceptance is the parity suite (U17, R15). It starts the real
// composed server on a temporary instance, with the pinned bd and Dolt, a
// fake agent host, synthetic GitHub with a local git remote, and the
// project's own merge mechanism over that synthetic GitHub. Agents, the
// coordinator, rooms and the host drive it through the real loop binary
// over the socket.
//
// Every file but this one carries the build tag acceptance:
//
//	go test -tags acceptance ./acceptance/...
//
// LOOP_BD and LOOP_DOLT name the pinned binaries. Without them the suite
// looks in $HOME/.cache/loop-dev/bin, and it skips when neither is there.
package acceptance
