package tui

// openScreen is the screen on show as type T, when it is one.
func openScreen[T screen](m manageModel) (T, bool) {
	open, ok := m.top().(T)
	return open, ok
}

func isBusy(m manageModel) bool {
	_, ok := openScreen[busyScreen](m)
	return ok
}

func isBrowsing(m manageModel) bool {
	_, ok := openScreen[browseScreen](m)
	return ok
}
