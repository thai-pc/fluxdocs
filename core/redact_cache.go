package core

import (
	"container/list"
	"sync"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// redact_cache.go keeps the strict redaction models of a document's pages
// (docs/design/redaction-hardening.md §6.4, step 2.10 brought forward): a
// page is read once per document, not once per RedactText call (AutoRedactPII
// calls it once per PII kind and page), and RedactAndFlatten — its caller
// pages, the automatic-extension pre-pass, the extension rounds — reuses
// what marking read. A model depends only on the page's content and
// resources, which never change for an open document (its bytes are
// immutable; Rotate clones the page dictionary but keeps the page object), so
// a model stays valid for the document's lifetime. Models are keyed by page
// object number (a page without one is not cached) and evicted least
// recently used past maxPageModelBytes of estimated memory.

// maxPageModelBytes bounds the estimated memory the cache retains.
const maxPageModelBytes = 128 << 20

// pageModelCache is a document's cache of page models. Safe for concurrent
// use.
type pageModelCache struct {
	mu    sync.Mutex
	items map[int]*list.Element // page object number -> element of order
	order *list.List            // most recently used first; values are *cachedModel
	bytes int64
}

// cachedModel is one cache entry.
type cachedModel struct {
	num   int
	page  *redact.Page
	bytes int64
}

// modelCache returns the document's cache, creating it.
func (d *Document) modelCache() *pageModelCache {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pageModels == nil {
		d.pageModels = &pageModelCache{items: map[int]*list.Element{}, order: list.New()}
	}
	return d.pageModels
}

// get returns the model of the page with object number num, or nil.
func (c *pageModelCache) get(num int) *redact.Page {
	if num <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[num]
	if !ok {
		return nil
	}
	c.order.MoveToFront(e)
	return e.Value.(*cachedModel).page
}

// put stores a page's model, evicting the least recently used models past
// the bound. A model larger than the bound is not stored.
func (c *pageModelCache) put(num int, p *redact.Page) {
	size := p.Size()
	if num <= 0 || size > maxPageModelBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[num]; ok {
		return
	}
	c.items[num] = c.order.PushFront(&cachedModel{num: num, page: p, bytes: size})
	c.bytes += size
	for c.bytes > maxPageModelBytes {
		last := c.order.Back()
		m := last.Value.(*cachedModel)
		c.order.Remove(last)
		delete(c.items, m.num)
		c.bytes -= m.bytes
	}
}

// cachedPage returns page i's cached model, or nil.
func (d *Document) cachedPage(i int) *redact.Page {
	return d.modelCache().get(d.pageInfos[i].ObjectNumber())
}

// pageObjectNumber is page i's object number (0 when it has none).
func pageObjectNumber(pi parse.PageInfo) int { return pi.ObjectNumber() }
