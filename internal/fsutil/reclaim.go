package fsutil

type reclaimEstimate struct {
	singleLinkBytes      int64
	links                map[fileIdentity]reclaimLink
	measured, incomplete bool
}

type reclaimLink struct {
	bytes         int64
	inside, nlink uint32
}

// Count a linked inode once in the allocation total and compare the observed
// names with its link count when estimating what removal will reclaim.
func (counter *sizeCounter) addReclaimLink(id fileIdentity, bytes int64, nlink uint32) {
	estimate := counter.reclaim
	if estimate == nil || nlink == 0 {
		counter.add(bytes, 0, id, nil)
		return
	}
	link, ok := estimate.links[id]
	if !ok {
		if estimate.links == nil {
			estimate.links = make(map[fileIdentity]reclaimLink)
		}
		if _, counted := counter.seen[id]; !counted {
			counter.add(bytes, 0, fileIdentity{}, nil)
		}
		link = reclaimLink{bytes: bytes, nlink: nlink}
	} else if link.nlink != nlink || link.bytes != bytes {
		link.nlink = 0 // The inode changed while the scan was in progress.
	}
	link.inside++
	estimate.links[id] = link
}

func (estimate *reclaimEstimate) bytes() int64 {
	bytes := estimate.singleLinkBytes
	for _, link := range estimate.links {
		if link.inside == link.nlink {
			bytes += link.bytes
		}
	}
	return bytes
}

func markReclaimIncomplete(counts []*sizeCounter) {
	for _, counter := range counts {
		if counter.reclaim != nil {
			counter.reclaim.incomplete = true
		}
	}
}
