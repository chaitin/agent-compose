package main

import "fmt"

// collectAllPages fetches offset-paged results until every page the daemon
// reports as available has been read.
//
// fetch receives the offset to request and returns how many entries it
// consumed from the page and the total the daemon reported. A page that
// consumes nothing while the total says more remain means the daemon is not
// advancing; without this check the caller would spin forever.
func collectAllPages(what string, fetch func(offset uint32) (size int, total uint32, err error)) (uint32, error) {
	var offset, total uint32
	for {
		size, pageTotal, err := fetch(offset)
		if err != nil {
			return 0, err
		}
		total = pageTotal
		offset += uint32(size)
		if offset >= total {
			break
		}
		if size == 0 {
			return 0, fmt.Errorf("%s pagination did not advance", what)
		}
	}
	return total, nil
}
