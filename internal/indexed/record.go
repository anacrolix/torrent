package indexed

type PrimaryKey[T any] interface {
	comparable
}

type RecordCmpFunc[R any] func(a, b R) int

type Record[K PrimaryKey[K]] = any

// Optional Record interface for triggering updates on modification. Convenience rather than
// "forgetting" to include something in an update function or on insertion.
type OnModified interface {
	OnModified()
}
