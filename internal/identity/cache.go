package identity

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

const permissionCacheEntries = 10000
const permissionCacheFlights = 64

type freshIdentityKey struct{}

// RequireFresh bypasses cached token authority for sensitive operations.
func RequireFresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshIdentityKey{}, true)
}

type cacheEntry struct {
	key           [32]byte
	info          tokenInfo
	stored, until time.Time
}
type cacheFlight struct {
	done    chan struct{}
	info    tokenInfo
	err     error
	invalid bool
}
type permissionsCache struct {
	mu                    sync.Mutex
	ttl                   time.Duration
	epoch                 int64
	active                int
	entries               map[[32]byte]*list.Element
	order                 *list.List
	flights               map[[32]byte]*cacheFlight
	readEpoch, purgeEpoch func(context.Context) (int64, error)
	observe               func(context.Context, string, time.Duration)
}

func newPermissionsCache(ttl time.Duration) *permissionsCache {
	return &permissionsCache{
		ttl:     ttl,
		entries: make(map[[32]byte]*list.Element),
		order:   list.New(),
		flights: make(map[[32]byte]*cacheFlight),
	}
}

// UseCacheControl binds shared invalidation and metrics before serving requests.
// Only the generation is persisted; credentials and cached claims stay in memory.
func (a *Authenticator) UseCacheControl(
	read, purge func(context.Context) (int64, error),
	observe func(context.Context, string, time.Duration),
) {
	a.cache.readEpoch, a.cache.purgeEpoch, a.cache.observe = read, purge, observe
}
func (c *permissionsCache) event(ctx context.Context, result string, age time.Duration) {
	if c.observe != nil {
		c.observe(ctx, result, age)
	}
}
func (c *permissionsCache) generation(ctx context.Context) (int64, error) {
	if c.readEpoch != nil {
		bounded, cancel := context.WithTimeout(ctx, identityRequestTimeout)
		defer cancel()
		epoch, err := c.readEpoch(bounded)
		if err != nil {
			return 0, ErrUnavailable
		}
		return epoch, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch, nil
}
func (c *permissionsCache) reset(epoch int64) {
	c.epoch = epoch
	c.entries = make(map[[32]byte]*list.Element)
	c.order.Init()
	for _, f := range c.flights {
		f.invalid = true
	}
	c.flights = make(map[[32]byte]*cacheFlight)
}
func (c *permissionsCache) invalidate(key [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil {
		delete(c.entries, key)
		c.order.Remove(e)
	}
	if f := c.flights[key]; f != nil {
		f.invalid = true
		delete(c.flights, key)
	}
}

func (c *permissionsCache) load(
	ctx context.Context,
	token string,
	fetch func(context.Context, string) (tokenInfo, error),
) (tokenInfo, error) {
	if err := ctx.Err(); err != nil {
		return tokenInfo{}, err
	}
	epoch, err := c.generation(ctx)
	if err != nil {
		c.event(ctx, "error", 0)
		return tokenInfo{}, err
	}
	key := sha256.Sum256([]byte(token))
	if fresh, _ := ctx.Value(freshIdentityKey{}).(bool); fresh || c.ttl == 0 {
		c.event(ctx, "fresh", 0)
		defer c.invalidate(key)
		info, fetchErr := fetch(ctx, token)
		return c.result(ctx, info, fetchErr)
	}
	c.mu.Lock()
	if epoch < c.epoch {
		c.mu.Unlock()
		return tokenInfo{}, ErrUnavailable
	}
	if epoch != c.epoch {
		c.reset(epoch)
	}
	if e := c.entries[key]; e != nil {
		item, _ := e.Value.(cacheEntry) // This private list contains cacheEntry values only.
		if time.Now().Before(item.until) {
			c.order.MoveToFront(e)
			c.mu.Unlock()
			c.event(ctx, "hit", time.Since(item.stored))
			return item.info, nil
		}
		c.order.Remove(e)
		delete(c.entries, key)
	}
	f := c.flights[key]
	event := "joined"
	if f == nil {
		if c.active >= permissionCacheFlights {
			c.mu.Unlock()
			c.event(ctx, "error", 0)
			return tokenInfo{}, ErrUnavailable
		}
		f = &cacheFlight{done: make(chan struct{})}
		c.flights[key] = f
		c.active++
		event = "miss"
		go c.fill(context.WithoutCancel(ctx), token, key, epoch, f, fetch)
	}
	c.mu.Unlock()
	c.event(ctx, event, 0)
	select {
	case <-ctx.Done():
		return tokenInfo{}, ctx.Err()
	case <-f.done:
		return c.result(ctx, f.info, f.err)
	}
}
func (c *permissionsCache) result(ctx context.Context, info tokenInfo, err error) (tokenInfo, error) {
	if err == nil && info.Expires <= time.Now().Unix() {
		err = ErrUnauthorized
	}
	if err != nil {
		event := "error"
		if errors.Is(err, ErrUnauthorized) {
			event = "denied"
		}
		c.event(ctx, event, 0)
		return tokenInfo{}, err
	}
	return info, nil
}

func (c *permissionsCache) fill(
	ctx context.Context,
	token string,
	key [32]byte,
	epoch int64,
	f *cacheFlight,
	fetch func(context.Context, string) (tokenInfo, error),
) {
	ctx, cancel := context.WithTimeout(ctx, identityRequestTimeout)
	defer cancel()
	info, err := fetch(ctx, token)
	latest, epochErr := c.generation(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if epochErr != nil || latest != epoch || c.epoch != epoch || f.invalid {
		err = ErrUnavailable
	}
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	if err == nil && info.Expires > time.Now().Unix() {
		now := time.Now()
		until := now.Add(c.ttl)
		expiry := time.Unix(info.Expires, 0)
		if expiry.Before(until) {
			until = expiry
		}
		item := cacheEntry{key: key, info: info, stored: now, until: until}
		if old := c.entries[key]; old != nil {
			c.order.Remove(old)
		}
		c.entries[key] = c.order.PushFront(item)
		if c.order.Len() > permissionCacheEntries {
			old := c.order.Back()
			oldest, _ := old.Value.(cacheEntry)
			delete(c.entries, oldest.key)
			c.order.Remove(old)
		}
	}
	f.info, f.err = info, err
	close(f.done)
}

// ClearPermissionsCache invalidates all replicas through the shared generation.
// Checks already authorized before the purge are not cancelled.
func (a *Authenticator) ClearPermissionsCache(ctx context.Context) (int64, error) {
	c := a.cache
	var epoch int64
	if c.purgeEpoch != nil {
		value, err := c.purgeEpoch(ctx)
		if err != nil {
			return 0, err
		}
		epoch = value
		c.mu.Lock()
		if epoch >= c.epoch {
			c.reset(epoch)
		}
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		epoch = c.epoch + 1
		c.reset(epoch)
		c.mu.Unlock()
	}
	c.event(ctx, "cleared", 0)
	return epoch, nil
}
