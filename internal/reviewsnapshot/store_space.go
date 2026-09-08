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

// storeCapacityLimit bounds review snapshot data to one tenth of the filesystem
// that contains it. This is a capacity-relative ceiling, not an absolute tree
// count or byte budget, so it scales without arbitrary retuning on small tmpfses
// and large disks. The previous free-space margin was wrong: global available
// space includes unrelated data, and evicting this store cannot repair another
// user's disk consumption. Measuring only entries this package can remove keeps
// reusable snapshots on an otherwise-full filesystem while preventing this store
// from accumulating enough committed trees to fill its own filesystem share.
func storeCapacityLimit(capacity uint64) uint64 {
	return capacity / 10
}
