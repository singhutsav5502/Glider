package vendors

// AdapterForTest exposes the internal adapter registry lookup to tests in
// the external vendors_test package — test-only, not part of the public
// API (idiomatic Go export_test.go pattern; this file never compiles into
// a non-test build).
func AdapterForTest(vendorName string) VendorAdapter { return adapterFor(vendorName) }

// DirToFileURIForTest exposes dirToFileURI so tests can construct a
// project file whose folderUri will actually match a given test directory.
func DirToFileURIForTest(dir string) (string, error) { return dirToFileURI(dir) }
