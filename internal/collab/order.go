package collab

import (
	"sort"
	"strings"

	"github.com/antst/go-yjs/crdt"
)

const orderDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func applyWidgetOrder(shared *crdt.YMap, next, base []contentObject) {
	index := make(map[string]int, len(base))
	for i, item := range base {
		index[itemID(item)] = i + 1
	}
	nextIDs := make([]string, 0, len(next))
	baseOrder := make([]int, 0, len(next))
	for _, item := range next {
		id := itemID(item)
		if _, ok := shared.Get(id).(*crdt.YMap); ok {
			nextIDs = append(nextIDs, id)
			baseOrder = append(baseOrder, index[id])
		}
	}
	kept := increasingIndexes(baseOrder, func(n int) bool { return n > 0 }, func(a, b int) bool { return a < b })
	moved := map[string]bool{}
	for i, id := range nextIDs {
		if !kept[i] {
			moved[id] = true
		}
	}
	if len(moved) == 0 {
		return
	}
	ordered := orderedWidgets(shared)
	current := []string{}
	for _, widget := range ordered {
		if id := itemID(widget); !moved[id] {
			current = append(current, id)
		}
	}
	var leading, trailing []string
	after := map[string][]string{}
	first, previous := "", ""
	last := -1
	for i, id := range nextIDs {
		if !moved[id] {
			last = i
		}
	}
	for i, id := range nextIDs {
		switch {
		case !moved[id]:
			if first == "" {
				first = id
			}
			previous = id
		case i > last:
			trailing = append(trailing, id)
		case previous == "":
			leading = append(leading, id)
		default:
			after[previous] = append(after[previous], id)
		}
	}
	wanted := make([]string, 0, len(ordered))
	for _, id := range current {
		if id == first {
			wanted = append(wanted, leading...)
		}
		wanted = append(wanted, id)
		wanted = append(wanted, after[id]...)
	}
	wanted = append(wanted, trailing...)
	keys := make([]string, len(wanted))
	for i, id := range wanted {
		if !moved[id] {
			key, _ := shared.Get(id).(*crdt.YMap).Get("$order").(string)
			if validOrderKey(key) {
				keys[i] = key
			}
		}
	}
	fixed := increasingIndexes(keys, func(s string) bool { return s != "" }, func(a, b string) bool { return a < b })
	for from := 0; from < len(keys); {
		if fixed[from] {
			from++
			continue
		}
		to := from + 1
		for to < len(keys) && !fixed[to] {
			to++
		}
		left, right := "", ""
		if from > 0 {
			left = keys[from-1]
		}
		if to < len(keys) {
			right = keys[to]
		}
		fillOrderKeys(keys[from:to], left, right)
		from = to
	}
	for i, id := range wanted {
		fields := shared.Get(id).(*crdt.YMap)
		if fields.Get("$order") != keys[i] {
			fields.Set("$order", keys[i])
		}
	}
}

func increasingIndexes[T any](values []T, present func(T) bool, before func(T, T) bool) []bool {
	tails := []int{}
	previous := make([]int, len(values))
	for index, value := range values {
		if !present(value) {
			continue
		}
		at := sort.Search(len(tails), func(i int) bool { return !before(values[tails[i]], value) })
		previous[index] = -1
		if at != 0 {
			previous[index] = tails[at-1]
		}
		if at == len(tails) {
			tails = append(tails, index)
		} else {
			tails[at] = index
		}
	}
	kept := make([]bool, len(values))
	if len(tails) != 0 {
		for i := tails[len(tails)-1]; i >= 0; i = previous[i] {
			kept[i] = true
		}
	}
	return kept
}

func validOrderKey(key string) bool {
	return key != "" && len(key) <= 256 && key[len(key)-1] != '0' &&
		strings.IndexFunc(key, func(r rune) bool { return !strings.ContainsRune(orderDigits, r) }) < 0
}

func fillOrderKeys(keys []string, a, b string) {
	if len(keys) == 0 {
		return
	}
	center := (len(keys) - 1) / 2
	middle := orderMidpoint(a, b)
	keys[center] = middle
	fillOrderKeys(keys[:center], a, middle)
	fillOrderKeys(keys[center+1:], middle, b)
}

func orderMidpoint(a, b string) string {
	if b != "" {
		i := 0
		for i < len(b) {
			digit := byte('0')
			if i < len(a) {
				digit = a[i]
			}
			if digit != b[i] {
				break
			}
			i++
		}
		if i != 0 {
			return b[:i] + orderMidpoint(a[min(i, len(a)):], b[i:])
		}
	}
	low, high := 0, len(orderDigits)
	if a != "" {
		low = strings.IndexByte(orderDigits, a[0])
	}
	if b != "" {
		high = strings.IndexByte(orderDigits, b[0])
	}
	if high-low > 1 {
		return string(orderDigits[(low+high+1)/2])
	}
	if len(b) > 1 {
		return b[:1]
	}
	return string(orderDigits[low]) + orderMidpoint(a[min(1, len(a)):], "")
}
