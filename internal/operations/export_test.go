package operations

// SealForTest creates valid persisted fixtures for expiry checks without changing production clocks.
func SealForTest(op Operation) (string, error) { return seal(op) }
