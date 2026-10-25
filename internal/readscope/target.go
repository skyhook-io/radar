package readscope

type Target struct {
	Namespaces []string // nil: all; non-nil empty: nothing.
	Explicit   bool
}
