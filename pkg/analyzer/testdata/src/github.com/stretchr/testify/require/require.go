package require

// These signatures are the small surface the analyzer fixtures need. The
// implementation is irrelevant: testdata packages are type-checked, not run.
func True(t any, value bool, msgAndArgs ...any) {}

func Truef(t any, value bool, msg string, args ...any) {}
