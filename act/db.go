package act

import (
	"errors"
	"fmt"
	"github.com/timshannon/bolthold"
	"regexp"
	"time"
)

func getCache(db *bolthold.Store, id int64, cache *Cache) error {
	return db.Get(id, cache)
}

func updateCache(db *bolthold.Store, id uint64, cache *Cache) error {
	return db.Update(id, cache)
}

// this function only used in gc
func deleteCache(db *bolthold.Store, id uint64, cache *Cache) error {
	return db.Delete(id, cache)
}

// findCache searches the cache database for a cache entry that matches the
func findCache(db *bolthold.Store, keys []string, version string) (*Cache, error) {
	// Create an empty Cache instance to store the fetched cache, if any.
	cache := &Cache{}

	// Iterate over all provided keys.
	for _, prefix := range keys {
		// Try to find a cache entry where Key and Version fields match with provided key and version respectively.
		// Query also checks if the cache is complete (Complete field is true).
		// Results are sorted in reverse order of the CreatedAt field.
		// So the most recent cache entry is returned if multiple entries are found.
		if err := db.FindOne(cache,
			bolthold.Where("Key").Eq(prefix).
				And("Version").Eq(version).
				And("Complete").Eq(true).
				SortBy("CreatedAt").Reverse()); err == nil || !errors.Is(err, bolthold.ErrNotFound) {
			if err != nil {
				// If an error occurred and it is not because the entry was not found, return error.
				return nil, fmt.Errorf("find cache: %w", err)
			}
			// Cache entry was found, return it.
			return cache, nil
		}

		// If no exact match is found, try to find a cache with Key field matching the prefix of provided key.
		// Construct a prefix pattern using provided key.
		prefixPattern := fmt.Sprintf("^%s", regexp.QuoteMeta(prefix))

		// Compile the prefix pattern into a regular expression.
		re, err := regexp.Compile(prefixPattern)
		if err != nil {
			// If an error occurred while compiling pattern (should be rare), skip this key and continue with the next key.
			continue
		}

		// Try to find a cache entry where Key field matches the prefix pattern, Version matches the provided version and the cache is complete.
		if err := db.FindOne(cache,
			bolthold.Where("Key").RegExp(re).
				And("Version").Eq(version).
				And("Complete").Eq(true).
				SortBy("CreatedAt").Reverse()); err != nil {
			if errors.Is(err, bolthold.ErrNotFound) {
				// If the entry was not found, continue with the next key.
				continue
			}
			// If an error occurred and it is not because the entry was not found, return error.
			return nil, fmt.Errorf("find cache: %w", err)
		}

		// Cache entry was found, return it.
		return cache, nil
	}

	// If no matching cache entry was found for any key, return nil.
	return nil, nil
}

func insertCache(db *bolthold.Store, cache *Cache) error {
	if err := db.Insert(bolthold.NextSequence(), cache); err != nil {
		return fmt.Errorf("insert cache: %w", err)
	}
	// write back id to db
	if err := db.Update(cache.ID, cache); err != nil {
		return fmt.Errorf("write back id to db: %w", err)
	}
	return nil
}

// gc cache functions

func findIncompleteCaches(db *bolthold.Store, caches []*Cache) error {
	err := db.Find(&caches, bolthold.
		Where("UsedAt").Lt(time.Now().Add(-keepTemp).Unix()).
		And("Complete").Eq(false),
	)
	return err
}

func findUnusedCaches(db *bolthold.Store, caches []*Cache) error {
	err := db.Find(&caches, bolthold.
		Where("UsedAt").Lt(time.Now().Add(-keepUnused).Unix()),
	)
	return err
}

func findOldCaches(db *bolthold.Store, caches []*Cache) error {
	err := db.Find(&caches, bolthold.
		Where("CreatedAt").Lt(time.Now().Add(-keepUsed).Unix()),
	)
	return err
}

func findCompletedCaches(db *bolthold.Store) ([]*bolthold.AggregateResult, error) {
	return db.FindAggregate(
		&Cache{},
		bolthold.Where("Complete").Eq(true),
		"Key", "Version",
	)
}
