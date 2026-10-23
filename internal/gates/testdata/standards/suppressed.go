// Package fixture holds the Go suppressions the standards test counts. It
// sits under testdata, which the scan skips, so the loop's own run of the
// gate does not count them.
package fixture

func f() error { return nil }

func g() {
	f() //nolint:errcheck // the fixture's reason
	f() //nolint
	//lint:ignore SA4006 the fixture's reason
	_ = f()
}
