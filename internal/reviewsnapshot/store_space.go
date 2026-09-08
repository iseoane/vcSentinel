package reviewsnapshot

// filesystemSpace is the capacity and currently available bytes of the
// filesystem containing the snapshot store.
type filesystemSpace struct {
	available uint64
	capacity  uint64
}

// storeFilesystemSpace is replaceable by focused tests. Production obtains both
// values from the filesystem that actually contains the store, rather than from
// a process-wide or guessed disk limit.
var storeFilesystemSpace = filesystemSpaceForPath

// storeFreeSpaceMargin keeps one tenth of the store filesystem available. This
// is a capacity-relative ceiling, not an absolute tree count or byte budget: it
// leaves room for the review's other temporary work on a small tmpfs and scales
// without arbitrary retuning on a large disk.
func storeFreeSpaceMargin(capacity uint64) uint64 {
	return capacity / 10
}
