// Generated by tmpl
// https://github.com/benbjohnson/tmpl
//
// DO NOT EDIT!
// Source: iterator.gen.go.tmpl

package tsm1

import (
	"fmt"
	"sort"

	"github.com/influxdata/influxdb/influxql"
	"github.com/influxdata/influxdb/tsdb"
)

type cursor interface {
	next() (t int64, v interface{})
}

// cursorAt provides a bufferred cursor interface.
// This required for literal value cursors which don't have a time value.
type cursorAt interface {
	peek() (k int64, v interface{})
	nextAt(seek int64) interface{}
}

type nilCursor struct{}

func (nilCursor) next() (int64, interface{}) { return tsdb.EOF, nil }

// bufCursor implements a bufferred cursor.
type bufCursor struct {
	cur cursor
	buf struct {
		key    int64
		value  interface{}
		filled bool
	}
}

// newBufCursor returns a bufferred wrapper for cur.
func newBufCursor(cur cursor) *bufCursor {
	return &bufCursor{cur: cur}
}

// next returns the buffer, if filled. Otherwise returns the next key/value from the cursor.
func (c *bufCursor) next() (int64, interface{}) {
	if c.buf.filled {
		k, v := c.buf.key, c.buf.value
		c.buf.filled = false
		return k, v
	}
	return c.cur.next()
}

// unread pushes k and v onto the buffer.
func (c *bufCursor) unread(k int64, v interface{}) {
	c.buf.key, c.buf.value = k, v
	c.buf.filled = true
}

// peek reads next next key/value without removing them from the cursor.
func (c *bufCursor) peek() (k int64, v interface{}) {
	k, v = c.next()
	c.unread(k, v)
	return
}

// nextAt returns the next value where key is equal to seek.
// Skips over any keys that are less than seek.
// If the key doesn't exist then a nil value is returned instead.
func (c *bufCursor) nextAt(seek int64) interface{} {
	for {
		k, v := c.next()
		if k == tsdb.EOF || k == seek {
			return v
		} else if k < seek {
			continue
		}

		c.unread(k, v)

		// Return "nil" value for type.
		switch c.cur.(type) {
		case floatCursor:
			return (*float64)(nil)
		case integerCursor:
			return (*int64)(nil)
		case stringCursor:
			return (*string)(nil)
		case booleanCursor:
			return (*bool)(nil)
		default:
			panic("unreachable")
		}
	}
}

type floatIterator struct {
	cur   floatCursor
	aux   []cursorAt
	conds struct {
		names []string
		curs  []*bufCursor
	}
	opt influxql.IteratorOptions

	m     map[string]interface{} // map used for condition evaluation
	point influxql.FloatPoint    // reusable buffer

	stats influxql.IteratorStats
}

func newFloatIterator(name string, tags influxql.Tags, opt influxql.IteratorOptions, cur floatCursor, aux []cursorAt, conds []*bufCursor, condNames []string) *floatIterator {
	itr := &floatIterator{
		cur: cur,
		aux: aux,
		opt: opt,
		point: influxql.FloatPoint{
			Name: name,
			Tags: tags,
		},
		stats: influxql.IteratorStats{
			SeriesN: 1,
		},
	}

	if len(aux) > 0 {
		itr.point.Aux = make([]interface{}, len(aux))
	}

	if opt.Condition != nil {
		itr.m = make(map[string]interface{}, len(aux)+len(conds))
	}
	itr.conds.names = condNames
	itr.conds.curs = conds

	return itr
}

// Next returns the next point from the iterator.
func (itr *floatIterator) Next() *influxql.FloatPoint {
	for {
		seek := tsdb.EOF

		if itr.cur != nil {
			// Read from the main cursor if we have one.
			itr.point.Time, itr.point.Value = itr.cur.nextFloat()
			seek = itr.point.Time
		} else {
			// Otherwise find lowest aux timestamp.
			for i := range itr.aux {
				if k, _ := itr.aux[i].peek(); k != tsdb.EOF && (seek == tsdb.EOF || k < seek) {
					seek = k
				}
			}
			itr.point.Time = seek
		}

		// Exit if we have no more points or we are outside our time range.
		if itr.point.Time == tsdb.EOF {
			return nil
		} else if itr.opt.Ascending && itr.point.Time > itr.opt.EndTime {
			return nil
		} else if !itr.opt.Ascending && itr.point.Time < itr.opt.StartTime {
			return nil
		}

		// Read from each auxiliary cursor.
		for i := range itr.opt.Aux {
			itr.point.Aux[i] = itr.aux[i].nextAt(seek)
		}

		// Read from condition field cursors.
		for i := range itr.conds.curs {
			itr.m[itr.conds.names[i]] = itr.conds.curs[i].nextAt(seek)
		}

		// Evaluate condition, if one exists. Retry if it fails.
		if itr.opt.Condition != nil && !influxql.EvalBool(itr.opt.Condition, itr.m) {
			continue
		}

		// Track points returned.
		itr.stats.PointN++

		return &itr.point
	}
}

// Stats returns stats on the points processed.
func (itr *floatIterator) Stats() influxql.IteratorStats { return itr.stats }

// Close closes the iterator.
func (itr *floatIterator) Close() error { return nil }

// floatCursor represents an object for iterating over a single float field.
type floatCursor interface {
	cursor
	nextFloat() (t int64, v float64)
}

func newFloatCursor(seek int64, ascending bool, cacheValues Values, tsmKeyCursor *KeyCursor) floatCursor {
	if ascending {
		return newFloatAscendingCursor(seek, cacheValues, tsmKeyCursor)
	}
	return newFloatDescendingCursor(seek, cacheValues, tsmKeyCursor)
}

type floatAscendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []FloatValue
		values    []FloatValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newFloatAscendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *floatAscendingCursor {
	c := &floatAscendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]FloatValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadFloatBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})

	return c
}

// peekCache returns the current time/value from the cache.
func (c *floatAscendingCursor) peekCache() (t int64, v float64) {
	if c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, 0
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*FloatValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *floatAscendingCursor) peekTSM() (t int64, v float64) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, 0
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *floatAscendingCursor) next() (int64, interface{}) { return c.nextFloat() }

// nextFloat returns the next key/value for the cursor.
func (c *floatAscendingCursor) nextFloat() (int64, float64) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, 0
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey < tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *floatAscendingCursor) nextCache() {
	if c.cache.pos >= len(c.cache.values) {
		return
	}
	c.cache.pos++
}

// nextTSM returns the next value from the TSM files.
func (c *floatAscendingCursor) nextTSM() {
	c.tsm.pos++
	if c.tsm.pos >= len(c.tsm.values) {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadFloatBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = 0
	}
}

type floatDescendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []FloatValue
		values    []FloatValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newFloatDescendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *floatDescendingCursor {
	c := &floatDescendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekCache(); t != seek {
		c.cache.pos--
	}

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]FloatValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadFloatBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekTSM(); t != seek {
		c.tsm.pos--
	}

	return c
}

// peekCache returns the current time/value from the cache.
func (c *floatDescendingCursor) peekCache() (t int64, v float64) {
	if c.cache.pos < 0 || c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, 0
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*FloatValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *floatDescendingCursor) peekTSM() (t int64, v float64) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, 0
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *floatDescendingCursor) next() (int64, interface{}) { return c.nextFloat() }

// nextFloat returns the next key/value for the cursor.
func (c *floatDescendingCursor) nextFloat() (int64, float64) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, 0
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey > tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *floatDescendingCursor) nextCache() {
	if c.cache.pos < 0 {
		return
	}
	c.cache.pos--
}

// nextTSM returns the next value from the TSM files.
func (c *floatDescendingCursor) nextTSM() {
	c.tsm.pos--
	if c.tsm.pos < 0 {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadFloatBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = len(c.tsm.values) - 1
	}
}

// floatLiteralCursor represents a cursor that always returns a single value.
// It doesn't not have a time value so it can only be used with nextAt().
type floatLiteralCursor struct {
	value float64
}

func (c *floatLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *floatLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *floatLiteralCursor) nextAt(seek int64) interface{}  { return c.value }

// floatNilLiteralCursor represents a cursor that always returns a typed nil value.
// It doesn't not have a time value so it can only be used with nextAt().
type floatNilLiteralCursor struct{}

func (c *floatNilLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, (*float64)(nil) }
func (c *floatNilLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, (*float64)(nil) }
func (c *floatNilLiteralCursor) nextAt(seek int64) interface{}  { return (*float64)(nil) }

type integerIterator struct {
	cur   integerCursor
	aux   []cursorAt
	conds struct {
		names []string
		curs  []*bufCursor
	}
	opt influxql.IteratorOptions

	m     map[string]interface{} // map used for condition evaluation
	point influxql.IntegerPoint  // reusable buffer

	stats influxql.IteratorStats
}

func newIntegerIterator(name string, tags influxql.Tags, opt influxql.IteratorOptions, cur integerCursor, aux []cursorAt, conds []*bufCursor, condNames []string) *integerIterator {
	itr := &integerIterator{
		cur: cur,
		aux: aux,
		opt: opt,
		point: influxql.IntegerPoint{
			Name: name,
			Tags: tags,
		},
		stats: influxql.IteratorStats{
			SeriesN: 1,
		},
	}

	if len(aux) > 0 {
		itr.point.Aux = make([]interface{}, len(aux))
	}

	if opt.Condition != nil {
		itr.m = make(map[string]interface{}, len(aux)+len(conds))
	}
	itr.conds.names = condNames
	itr.conds.curs = conds

	return itr
}

// Next returns the next point from the iterator.
func (itr *integerIterator) Next() *influxql.IntegerPoint {
	for {
		seek := tsdb.EOF

		if itr.cur != nil {
			// Read from the main cursor if we have one.
			itr.point.Time, itr.point.Value = itr.cur.nextInteger()
			seek = itr.point.Time
		} else {
			// Otherwise find lowest aux timestamp.
			for i := range itr.aux {
				if k, _ := itr.aux[i].peek(); k != tsdb.EOF && (seek == tsdb.EOF || k < seek) {
					seek = k
				}
			}
			itr.point.Time = seek
		}

		// Exit if we have no more points or we are outside our time range.
		if itr.point.Time == tsdb.EOF {
			return nil
		} else if itr.opt.Ascending && itr.point.Time > itr.opt.EndTime {
			return nil
		} else if !itr.opt.Ascending && itr.point.Time < itr.opt.StartTime {
			return nil
		}

		// Read from each auxiliary cursor.
		for i := range itr.opt.Aux {
			itr.point.Aux[i] = itr.aux[i].nextAt(seek)
		}

		// Read from condition field cursors.
		for i := range itr.conds.curs {
			itr.m[itr.conds.names[i]] = itr.conds.curs[i].nextAt(seek)
		}

		// Evaluate condition, if one exists. Retry if it fails.
		if itr.opt.Condition != nil && !influxql.EvalBool(itr.opt.Condition, itr.m) {
			continue
		}

		// Track points returned.
		itr.stats.PointN++

		return &itr.point
	}
}

// Stats returns stats on the points processed.
func (itr *integerIterator) Stats() influxql.IteratorStats { return itr.stats }

// Close closes the iterator.
func (itr *integerIterator) Close() error { return nil }

// integerCursor represents an object for iterating over a single integer field.
type integerCursor interface {
	cursor
	nextInteger() (t int64, v int64)
}

func newIntegerCursor(seek int64, ascending bool, cacheValues Values, tsmKeyCursor *KeyCursor) integerCursor {
	if ascending {
		return newIntegerAscendingCursor(seek, cacheValues, tsmKeyCursor)
	}
	return newIntegerDescendingCursor(seek, cacheValues, tsmKeyCursor)
}

type integerAscendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []IntegerValue
		values    []IntegerValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newIntegerAscendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *integerAscendingCursor {
	c := &integerAscendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]IntegerValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadIntegerBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})

	return c
}

// peekCache returns the current time/value from the cache.
func (c *integerAscendingCursor) peekCache() (t int64, v int64) {
	if c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, 0
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*IntegerValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *integerAscendingCursor) peekTSM() (t int64, v int64) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, 0
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *integerAscendingCursor) next() (int64, interface{}) { return c.nextInteger() }

// nextInteger returns the next key/value for the cursor.
func (c *integerAscendingCursor) nextInteger() (int64, int64) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, 0
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey < tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *integerAscendingCursor) nextCache() {
	if c.cache.pos >= len(c.cache.values) {
		return
	}
	c.cache.pos++
}

// nextTSM returns the next value from the TSM files.
func (c *integerAscendingCursor) nextTSM() {
	c.tsm.pos++
	if c.tsm.pos >= len(c.tsm.values) {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadIntegerBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = 0
	}
}

type integerDescendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []IntegerValue
		values    []IntegerValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newIntegerDescendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *integerDescendingCursor {
	c := &integerDescendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekCache(); t != seek {
		c.cache.pos--
	}

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]IntegerValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadIntegerBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekTSM(); t != seek {
		c.tsm.pos--
	}

	return c
}

// peekCache returns the current time/value from the cache.
func (c *integerDescendingCursor) peekCache() (t int64, v int64) {
	if c.cache.pos < 0 || c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, 0
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*IntegerValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *integerDescendingCursor) peekTSM() (t int64, v int64) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, 0
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *integerDescendingCursor) next() (int64, interface{}) { return c.nextInteger() }

// nextInteger returns the next key/value for the cursor.
func (c *integerDescendingCursor) nextInteger() (int64, int64) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, 0
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey > tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *integerDescendingCursor) nextCache() {
	if c.cache.pos < 0 {
		return
	}
	c.cache.pos--
}

// nextTSM returns the next value from the TSM files.
func (c *integerDescendingCursor) nextTSM() {
	c.tsm.pos--
	if c.tsm.pos < 0 {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadIntegerBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = len(c.tsm.values) - 1
	}
}

// integerLiteralCursor represents a cursor that always returns a single value.
// It doesn't not have a time value so it can only be used with nextAt().
type integerLiteralCursor struct {
	value int64
}

func (c *integerLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *integerLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *integerLiteralCursor) nextAt(seek int64) interface{}  { return c.value }

// integerNilLiteralCursor represents a cursor that always returns a typed nil value.
// It doesn't not have a time value so it can only be used with nextAt().
type integerNilLiteralCursor struct{}

func (c *integerNilLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, (*int64)(nil) }
func (c *integerNilLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, (*int64)(nil) }
func (c *integerNilLiteralCursor) nextAt(seek int64) interface{}  { return (*int64)(nil) }

type stringIterator struct {
	cur   stringCursor
	aux   []cursorAt
	conds struct {
		names []string
		curs  []*bufCursor
	}
	opt influxql.IteratorOptions

	m     map[string]interface{} // map used for condition evaluation
	point influxql.StringPoint   // reusable buffer

	stats influxql.IteratorStats
}

func newStringIterator(name string, tags influxql.Tags, opt influxql.IteratorOptions, cur stringCursor, aux []cursorAt, conds []*bufCursor, condNames []string) *stringIterator {
	itr := &stringIterator{
		cur: cur,
		aux: aux,
		opt: opt,
		point: influxql.StringPoint{
			Name: name,
			Tags: tags,
		},
		stats: influxql.IteratorStats{
			SeriesN: 1,
		},
	}

	if len(aux) > 0 {
		itr.point.Aux = make([]interface{}, len(aux))
	}

	if opt.Condition != nil {
		itr.m = make(map[string]interface{}, len(aux)+len(conds))
	}
	itr.conds.names = condNames
	itr.conds.curs = conds

	return itr
}

// Next returns the next point from the iterator.
func (itr *stringIterator) Next() *influxql.StringPoint {
	for {
		seek := tsdb.EOF

		if itr.cur != nil {
			// Read from the main cursor if we have one.
			itr.point.Time, itr.point.Value = itr.cur.nextString()
			seek = itr.point.Time
		} else {
			// Otherwise find lowest aux timestamp.
			for i := range itr.aux {
				if k, _ := itr.aux[i].peek(); k != tsdb.EOF && (seek == tsdb.EOF || k < seek) {
					seek = k
				}
			}
			itr.point.Time = seek
		}

		// Exit if we have no more points or we are outside our time range.
		if itr.point.Time == tsdb.EOF {
			return nil
		} else if itr.opt.Ascending && itr.point.Time > itr.opt.EndTime {
			return nil
		} else if !itr.opt.Ascending && itr.point.Time < itr.opt.StartTime {
			return nil
		}

		// Read from each auxiliary cursor.
		for i := range itr.opt.Aux {
			itr.point.Aux[i] = itr.aux[i].nextAt(seek)
		}

		// Read from condition field cursors.
		for i := range itr.conds.curs {
			itr.m[itr.conds.names[i]] = itr.conds.curs[i].nextAt(seek)
		}

		// Evaluate condition, if one exists. Retry if it fails.
		if itr.opt.Condition != nil && !influxql.EvalBool(itr.opt.Condition, itr.m) {
			continue
		}

		// Track points returned.
		itr.stats.PointN++

		return &itr.point
	}
}

// Stats returns stats on the points processed.
func (itr *stringIterator) Stats() influxql.IteratorStats { return itr.stats }

// Close closes the iterator.
func (itr *stringIterator) Close() error { return nil }

// stringCursor represents an object for iterating over a single string field.
type stringCursor interface {
	cursor
	nextString() (t int64, v string)
}

func newStringCursor(seek int64, ascending bool, cacheValues Values, tsmKeyCursor *KeyCursor) stringCursor {
	if ascending {
		return newStringAscendingCursor(seek, cacheValues, tsmKeyCursor)
	}
	return newStringDescendingCursor(seek, cacheValues, tsmKeyCursor)
}

type stringAscendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []StringValue
		values    []StringValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newStringAscendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *stringAscendingCursor {
	c := &stringAscendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]StringValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadStringBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})

	return c
}

// peekCache returns the current time/value from the cache.
func (c *stringAscendingCursor) peekCache() (t int64, v string) {
	if c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, ""
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*StringValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *stringAscendingCursor) peekTSM() (t int64, v string) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, ""
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *stringAscendingCursor) next() (int64, interface{}) { return c.nextString() }

// nextString returns the next key/value for the cursor.
func (c *stringAscendingCursor) nextString() (int64, string) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, ""
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey < tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *stringAscendingCursor) nextCache() {
	if c.cache.pos >= len(c.cache.values) {
		return
	}
	c.cache.pos++
}

// nextTSM returns the next value from the TSM files.
func (c *stringAscendingCursor) nextTSM() {
	c.tsm.pos++
	if c.tsm.pos >= len(c.tsm.values) {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadStringBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = 0
	}
}

type stringDescendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []StringValue
		values    []StringValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newStringDescendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *stringDescendingCursor {
	c := &stringDescendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekCache(); t != seek {
		c.cache.pos--
	}

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]StringValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadStringBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekTSM(); t != seek {
		c.tsm.pos--
	}

	return c
}

// peekCache returns the current time/value from the cache.
func (c *stringDescendingCursor) peekCache() (t int64, v string) {
	if c.cache.pos < 0 || c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, ""
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*StringValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *stringDescendingCursor) peekTSM() (t int64, v string) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, ""
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *stringDescendingCursor) next() (int64, interface{}) { return c.nextString() }

// nextString returns the next key/value for the cursor.
func (c *stringDescendingCursor) nextString() (int64, string) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, ""
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey > tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *stringDescendingCursor) nextCache() {
	if c.cache.pos < 0 {
		return
	}
	c.cache.pos--
}

// nextTSM returns the next value from the TSM files.
func (c *stringDescendingCursor) nextTSM() {
	c.tsm.pos--
	if c.tsm.pos < 0 {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadStringBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = len(c.tsm.values) - 1
	}
}

// stringLiteralCursor represents a cursor that always returns a single value.
// It doesn't not have a time value so it can only be used with nextAt().
type stringLiteralCursor struct {
	value string
}

func (c *stringLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *stringLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *stringLiteralCursor) nextAt(seek int64) interface{}  { return c.value }

// stringNilLiteralCursor represents a cursor that always returns a typed nil value.
// It doesn't not have a time value so it can only be used with nextAt().
type stringNilLiteralCursor struct{}

func (c *stringNilLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, (*string)(nil) }
func (c *stringNilLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, (*string)(nil) }
func (c *stringNilLiteralCursor) nextAt(seek int64) interface{}  { return (*string)(nil) }

type booleanIterator struct {
	cur   booleanCursor
	aux   []cursorAt
	conds struct {
		names []string
		curs  []*bufCursor
	}
	opt influxql.IteratorOptions

	m     map[string]interface{} // map used for condition evaluation
	point influxql.BooleanPoint  // reusable buffer

	stats influxql.IteratorStats
}

func newBooleanIterator(name string, tags influxql.Tags, opt influxql.IteratorOptions, cur booleanCursor, aux []cursorAt, conds []*bufCursor, condNames []string) *booleanIterator {
	itr := &booleanIterator{
		cur: cur,
		aux: aux,
		opt: opt,
		point: influxql.BooleanPoint{
			Name: name,
			Tags: tags,
		},
		stats: influxql.IteratorStats{
			SeriesN: 1,
		},
	}

	if len(aux) > 0 {
		itr.point.Aux = make([]interface{}, len(aux))
	}

	if opt.Condition != nil {
		itr.m = make(map[string]interface{}, len(aux)+len(conds))
	}
	itr.conds.names = condNames
	itr.conds.curs = conds

	return itr
}

// Next returns the next point from the iterator.
func (itr *booleanIterator) Next() *influxql.BooleanPoint {
	for {
		seek := tsdb.EOF

		if itr.cur != nil {
			// Read from the main cursor if we have one.
			itr.point.Time, itr.point.Value = itr.cur.nextBoolean()
			seek = itr.point.Time
		} else {
			// Otherwise find lowest aux timestamp.
			for i := range itr.aux {
				if k, _ := itr.aux[i].peek(); k != tsdb.EOF && (seek == tsdb.EOF || k < seek) {
					seek = k
				}
			}
			itr.point.Time = seek
		}

		// Exit if we have no more points or we are outside our time range.
		if itr.point.Time == tsdb.EOF {
			return nil
		} else if itr.opt.Ascending && itr.point.Time > itr.opt.EndTime {
			return nil
		} else if !itr.opt.Ascending && itr.point.Time < itr.opt.StartTime {
			return nil
		}

		// Read from each auxiliary cursor.
		for i := range itr.opt.Aux {
			itr.point.Aux[i] = itr.aux[i].nextAt(seek)
		}

		// Read from condition field cursors.
		for i := range itr.conds.curs {
			itr.m[itr.conds.names[i]] = itr.conds.curs[i].nextAt(seek)
		}

		// Evaluate condition, if one exists. Retry if it fails.
		if itr.opt.Condition != nil && !influxql.EvalBool(itr.opt.Condition, itr.m) {
			continue
		}

		// Track points returned.
		itr.stats.PointN++

		return &itr.point
	}
}

// Stats returns stats on the points processed.
func (itr *booleanIterator) Stats() influxql.IteratorStats { return itr.stats }

// Close closes the iterator.
func (itr *booleanIterator) Close() error { return nil }

// booleanCursor represents an object for iterating over a single boolean field.
type booleanCursor interface {
	cursor
	nextBoolean() (t int64, v bool)
}

func newBooleanCursor(seek int64, ascending bool, cacheValues Values, tsmKeyCursor *KeyCursor) booleanCursor {
	if ascending {
		return newBooleanAscendingCursor(seek, cacheValues, tsmKeyCursor)
	}
	return newBooleanDescendingCursor(seek, cacheValues, tsmKeyCursor)
}

type booleanAscendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []BooleanValue
		values    []BooleanValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newBooleanAscendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *booleanAscendingCursor {
	c := &booleanAscendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]BooleanValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadBooleanBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})

	return c
}

// peekCache returns the current time/value from the cache.
func (c *booleanAscendingCursor) peekCache() (t int64, v bool) {
	if c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, false
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*BooleanValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *booleanAscendingCursor) peekTSM() (t int64, v bool) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, false
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *booleanAscendingCursor) next() (int64, interface{}) { return c.nextBoolean() }

// nextBoolean returns the next key/value for the cursor.
func (c *booleanAscendingCursor) nextBoolean() (int64, bool) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, false
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey < tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *booleanAscendingCursor) nextCache() {
	if c.cache.pos >= len(c.cache.values) {
		return
	}
	c.cache.pos++
}

// nextTSM returns the next value from the TSM files.
func (c *booleanAscendingCursor) nextTSM() {
	c.tsm.pos++
	if c.tsm.pos >= len(c.tsm.values) {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadBooleanBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = 0
	}
}

type booleanDescendingCursor struct {
	cache struct {
		values Values
		pos    int
	}

	tsm struct {
		buf       []BooleanValue
		values    []BooleanValue
		pos       int
		keyCursor *KeyCursor
	}
}

func newBooleanDescendingCursor(seek int64, cacheValues Values, tsmKeyCursor *KeyCursor) *booleanDescendingCursor {
	c := &booleanDescendingCursor{}

	c.cache.values = cacheValues
	c.cache.pos = sort.Search(len(c.cache.values), func(i int) bool {
		return c.cache.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekCache(); t != seek {
		c.cache.pos--
	}

	c.tsm.keyCursor = tsmKeyCursor
	c.tsm.buf = make([]BooleanValue, 10)
	c.tsm.values, _ = c.tsm.keyCursor.ReadBooleanBlock(c.tsm.buf)
	c.tsm.pos = sort.Search(len(c.tsm.values), func(i int) bool {
		return c.tsm.values[i].UnixNano() >= seek
	})
	if t, _ := c.peekTSM(); t != seek {
		c.tsm.pos--
	}

	return c
}

// peekCache returns the current time/value from the cache.
func (c *booleanDescendingCursor) peekCache() (t int64, v bool) {
	if c.cache.pos < 0 || c.cache.pos >= len(c.cache.values) {
		return tsdb.EOF, false
	}

	item := c.cache.values[c.cache.pos]
	return item.UnixNano(), item.(*BooleanValue).value
}

// peekTSM returns the current time/value from tsm.
func (c *booleanDescendingCursor) peekTSM() (t int64, v bool) {
	if c.tsm.pos < 0 || c.tsm.pos >= len(c.tsm.values) {
		return tsdb.EOF, false
	}

	item := c.tsm.values[c.tsm.pos]
	return item.UnixNano(), item.value
}

// next returns the next key/value for the cursor.
func (c *booleanDescendingCursor) next() (int64, interface{}) { return c.nextBoolean() }

// nextBoolean returns the next key/value for the cursor.
func (c *booleanDescendingCursor) nextBoolean() (int64, bool) {
	ckey, cvalue := c.peekCache()
	tkey, tvalue := c.peekTSM()

	// No more data in cache or in TSM files.
	if ckey == tsdb.EOF && tkey == tsdb.EOF {
		return tsdb.EOF, false
	}

	// Both cache and tsm files have the same key, cache takes precedence.
	if ckey == tkey {
		c.nextCache()
		c.nextTSM()
		return tkey, tvalue
	}

	// Buffered cache key precedes that in TSM file.
	if ckey != tsdb.EOF && (ckey > tkey || tkey == tsdb.EOF) {
		c.nextCache()
		return ckey, cvalue
	}

	// Buffered TSM key precedes that in cache.
	c.nextTSM()
	return tkey, tvalue
}

// nextCache returns the next value from the cache.
func (c *booleanDescendingCursor) nextCache() {
	if c.cache.pos < 0 {
		return
	}
	c.cache.pos--
}

// nextTSM returns the next value from the TSM files.
func (c *booleanDescendingCursor) nextTSM() {
	c.tsm.pos--
	if c.tsm.pos < 0 {
		c.tsm.keyCursor.Next()
		c.tsm.values, _ = c.tsm.keyCursor.ReadBooleanBlock(c.tsm.buf)
		if len(c.tsm.values) == 0 {
			return
		}
		c.tsm.pos = len(c.tsm.values) - 1
	}
}

// booleanLiteralCursor represents a cursor that always returns a single value.
// It doesn't not have a time value so it can only be used with nextAt().
type booleanLiteralCursor struct {
	value bool
}

func (c *booleanLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *booleanLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, c.value }
func (c *booleanLiteralCursor) nextAt(seek int64) interface{}  { return c.value }

// booleanNilLiteralCursor represents a cursor that always returns a typed nil value.
// It doesn't not have a time value so it can only be used with nextAt().
type booleanNilLiteralCursor struct{}

func (c *booleanNilLiteralCursor) peek() (t int64, v interface{}) { return tsdb.EOF, (*bool)(nil) }
func (c *booleanNilLiteralCursor) next() (t int64, v interface{}) { return tsdb.EOF, (*bool)(nil) }
func (c *booleanNilLiteralCursor) nextAt(seek int64) interface{}  { return (*bool)(nil) }

var _ = fmt.Print
