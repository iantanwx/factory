package pipeline

// SetCrash stops the next calls at a kill point while f returns true.
func (p *Pipeline) SetCrash(f func(point string) bool) { p.crash = f }
