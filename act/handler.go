package act

import (
	"act-nexus-cache/nexus"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/sirupsen/logrus"
	"github.com/timshannon/bolthold"
	"go.etcd.io/bbolt"

	"github.com/nektos/act/pkg/common"
)

const (
	urlBase = "/_apis/artifactcache"
)

type Handler struct {
	dir      string
	storage  *Storage
	router   *httprouter.Router
	listener net.Listener
	server   *http.Server
	logger   logrus.FieldLogger
	nexus    *nexus.CacheService
	gcing    atomic.Bool
	gcAt     time.Time

	outboundIP string
}

func StartHandler(dir, outboundIP string, port uint16, logger logrus.FieldLogger) (*Handler, error) {
	if os.Getenv("NEXUS_STORE_ENDPOINT") == "" {
		os.Setenv("NEXUS_STORE_ENDPOINT", "https://nxrm.mobilesolutionworks.com/repository/gh-action-cache/act-nexus-cache")
	}

	if os.Getenv("NEXUS_USERNAME") == "" {
		os.Setenv("NEXUS_USERNAME", "gh")
	}

	if os.Getenv("NEXUS_SECRET") == "" {
		os.Setenv("NEXUS_SECRET", "gh")
	}

	nexusStoreEndpoint := os.Getenv("NEXUS_STORE_ENDPOINT")

	h := &Handler{}
	h.nexus = nexus.NewCacheService(nexusStoreEndpoint)

	if logger == nil {
		discard := logrus.New()
		discard.Out = io.Discard
		logger = discard
	}
	logger = logger.WithField("module", "artifactcache")
	h.logger = logger

	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".cache", "actcache")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	h.dir = dir

	storage, err := NewStorage(filepath.Join(dir, "cache"))
	if err != nil {
		return nil, err
	}
	h.storage = storage

	if outboundIP != "" {
		h.outboundIP = outboundIP
	} else if ip := common.GetOutboundIP(); ip == nil {
		return nil, fmt.Errorf("unable to determine outbound IP address")
	} else {
		h.outboundIP = ip.String()
	}

	router := httprouter.New()
	router.GET(urlBase+"/cache", h.middleware(h.routeFind))
	router.POST(urlBase+"/caches", h.middleware(h.routeReserve))
	router.PATCH(urlBase+"/caches/:id", h.middleware(h.routeUpload))
	router.POST(urlBase+"/caches/:id", h.middleware(h.routeCommit))
	router.GET(urlBase+"/artifacts/:id", h.middleware(h.routeGet))
	router.POST(urlBase+"/clean", h.middleware(h.routeClean))

	h.router = router

	h.gcCache()

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port)) // listen on all interfaces
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		ReadHeaderTimeout: 2 * time.Second,
		Handler:           router,
	}
	//go func() {
	//	if err := server.Serve(listener); err != nil && errors.Is(err, net.ErrClosed) {
	//		logger.Errorf("http serve: %v", err)
	//	}
	//}()
	h.listener = listener
	h.server = server

	return h, nil
}

func (h *Handler) Serve() {
	if err := h.server.Serve(h.listener); err != nil && errors.Is(err, net.ErrClosed) {
		h.logger.Errorf("http serve: %v", err)
	}
}

func (h *Handler) ExternalURL() string {
	// TODO: make the external url configurable if necessary
	if os.Getenv("EXTERNAL_URL") != "" {
		return os.Getenv("EXTERNAL_URL")
	} else {
		return fmt.Sprintf("http://%s:%d",
			h.outboundIP,
			h.listener.Addr().(*net.TCPAddr).Port)
	}
}

func (h *Handler) Close() error {
	if h == nil {
		return nil
	}
	var retErr error
	if h.server != nil {
		err := h.server.Close()
		if err != nil {
			retErr = err
		}
		h.server = nil
	}
	if h.listener != nil {
		err := h.listener.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
		if err != nil {
			retErr = err
		}
		h.listener = nil
	}
	return retErr
}

func (h *Handler) openDB() (*bolthold.Store, error) {
	return bolthold.Open(filepath.Join(h.dir, "bolt.db"), 0o644, &bolthold.Options{
		Encoder: json.Marshal,
		Decoder: json.Unmarshal,
		Options: &bbolt.Options{
			Timeout:      5 * time.Second,
			NoGrowSync:   bbolt.DefaultOptions.NoGrowSync,
			FreelistType: bbolt.DefaultOptions.FreelistType,
		},
	})
}

// GET /_apis/artifactcache/cache
func (h *Handler) routeFind(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	// Splitting cache keys gotten from URL
	keys := strings.Split(r.URL.Query().Get("keys"), ",")
	// Cache keys are case insensitive
	for i, key := range keys {
		keys[i] = strings.ToLower(key)
	}
	// Finding version from URL
	version := r.URL.Query().Get("version")

	// Attempt to open database
	db, err := h.openDB()
	if err != nil {
		// Error opening DB - send 500 error
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()

	nexusCache, err := h.nexus.FindCache(keys, version)
	if nexusCache != nil {
		fmt.Printf("Cache hit: %s", nexusCache.ArchiveLocation)
		fmt.Printf("Cache hit: %s", nexusCache.CacheKey)
		h.responseJSON(w, r, 200, map[string]any{
			"result":          "hit",
			"archiveLocation": nexusCache.ArchiveLocation,
			"cacheKey":        nexusCache.CacheKey,
		})
		return
	}

	// Attempt to find cache in db
	cache, err := findCache(db, keys, version)
	if err != nil {
		// Error fetching cache - send 500 error
		h.responseJSON(w, r, 500, err)
		return
	}
	if cache == nil {
		// Cache not found - send 204 status
		h.responseJSON(w, r, 204)
		return
	}

	// Cache found, check if it actually exists in storage
	if ok, err := h.storage.Exist(cache.ID); err != nil {
		// Error checking cache existence - send 500 error
		h.responseJSON(w, r, 500, err)
		return
	} else if !ok {
		// Cache does not exist in storage - delete the cache from DB and send 204 status
		_ = db.Delete(cache.ID, cache)
		h.responseJSON(w, r, 204)
		return
	}

	// TODO Cache found and exists in storage, return cache details
	h.responseJSON(w, r, 200, map[string]any{
		"result":          "hit",
		"archiveLocation": fmt.Sprintf("%s%s/artifacts/%d", h.ExternalURL(), urlBase, cache.ID),
		"cacheKey":        cache.Key,
	})
}

// POST /_apis/artifactcache/caches
func (h *Handler) routeReserve(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	api := &Request{}
	if err := json.NewDecoder(r.Body).Decode(api); err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}
	// cache keys are case insensitive
	api.Key = strings.ToLower(api.Key)

	cache := api.ToCache()
	db, err := h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()

	now := time.Now().Unix()
	cache.CreatedAt = now
	cache.UsedAt = now
	if err := insertCache(db, cache); err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}

	// TODO return response
	h.responseJSON(w, r, 200, map[string]any{
		"cacheId": cache.ID,
	})
}

// PATCH /_apis/artifactcache/caches/:id
func (h *Handler) routeUpload(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}

	cache := &Cache{}
	db, err := h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()
	if err := getCache(db, id, cache); err != nil {
		if errors.Is(err, bolthold.ErrNotFound) {
			h.responseJSON(w, r, 400, fmt.Errorf("cache %d: not reserved", id))
			return
		}
		h.responseJSON(w, r, 500, err)
		return
	}

	if cache.Complete {
		h.responseJSON(w, r, 400, fmt.Errorf("cache %v %q: already complete", cache.ID, cache.Key))
		return
	}
	db.Close()
	start, _, err := parseContentRange(r.Header.Get("Content-Range"))
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}
	if err := h.storage.Write(cache.ID, start, r.Body); err != nil {
		h.responseJSON(w, r, 500, err)
	}
	h.useCache(id)
	h.responseJSON(w, r, 200)
}

// POST /_apis/artifactcache/caches/:id
func (h *Handler) routeCommit(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}

	cache := &Cache{}
	db, err := h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()
	if err := getCache(db, id, cache); err != nil {
		if errors.Is(err, bolthold.ErrNotFound) {
			h.responseJSON(w, r, 400, fmt.Errorf("cache %d: not reserved", id))
			return
		}
		h.responseJSON(w, r, 500, err)
		return
	}

	if cache.Complete {
		h.responseJSON(w, r, 400, fmt.Errorf("cache %v %q: already complete", cache.ID, cache.Key))
		return
	}

	db.Close()

	size, err := h.storage.Commit(cache.ID, cache.Size)
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	// write real size back to cache, it may be different from the current value when the request doesn't specify it.
	cache.Size = size

	db, err = h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()

	cache.Complete = true
	if err := updateCache(db, cache.ID, cache); err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}

	filename := h.storage.Filename(cache.ID)
	h.nexus.PutCache(cache.Key, cache.Version, filename)

	h.responseJSON(w, r, 200)
}

// GET /_apis/artifactcache/artifacts/:id
func (h *Handler) routeGet(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}
	h.useCache(id) // update cache time for retention
	h.storage.Serve(w, r, uint64(id))
}

// POST /_apis/artifactcache/clean
func (h *Handler) routeClean(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	// TODO: don't support force deleting cache entries
	// see: https://docs.github.com/en/actions/using-workflows/caching-dependencies-to-speed-up-workflows#force-deleting-cache-entries

	h.responseJSON(w, r, 200)
}

func (h *Handler) middleware(handler httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		h.logger.Debugf("%s %s", r.Method, r.RequestURI)
		handler(w, r, params)
		go h.gcCache()
	}
}

func (h *Handler) useCache(id int64) {
	db, err := h.openDB()
	if err != nil {
		return
	}
	defer db.Close()
	cache := &Cache{}
	if err := getCache(db, id, cache); err != nil {
		return
	}
	cache.UsedAt = time.Now().Unix()
	_ = updateCache(db, cache.ID, cache)
}

const (
	keepUsed   = 30 * 24 * time.Hour
	keepUnused = 7 * 24 * time.Hour
	keepTemp   = 5 * time.Minute
	keepOld    = 5 * time.Minute
)

// TODO this method can be removed if we store content to Nexus
func (h *Handler) gcCache() {
	if h.gcing.Load() {
		return
	}
	if !h.gcing.CompareAndSwap(false, true) {
		return
	}
	defer h.gcing.Store(false)

	if time.Since(h.gcAt) < time.Hour {
		h.logger.Debugf("skip gc: %v", h.gcAt.String())
		return
	}
	h.gcAt = time.Now()
	h.logger.Debugf("gc: %v", h.gcAt.String())

	db, err := h.openDB()
	if err != nil {
		return
	}
	defer db.Close()

	// Remove the caches which are not completed for a while, they are most likely to be broken.
	var caches []*Cache
	if err := findIncompleteCaches(db, caches); err != nil {
		h.logger.Warnf("find caches: %v", err)
	} else {
		for _, cache := range caches {
			h.storage.Remove(cache.ID)
			if err := deleteCache(db, cache.ID, cache); err != nil {
				h.logger.Warnf("delete cache: %v", err)
				continue
			}
			h.logger.Infof("deleted cache: %+v", cache)
		}
	}

	// Remove the old caches which have not been used recently.
	caches = caches[:0]
	if err := findUnusedCaches(db, caches); err != nil {
		h.logger.Warnf("find caches: %v", err)
	} else {
		for _, cache := range caches {
			h.storage.Remove(cache.ID)
			if err := deleteCache(db, cache.ID, cache); err != nil {
				h.logger.Warnf("delete cache: %v", err)
				continue
			}
			h.logger.Infof("deleted cache: %+v", cache)
		}
	}

	// Remove the old caches which are too old.
	caches = caches[:0]
	if err := findOldCaches(db, caches); err != nil {
		h.logger.Warnf("find caches: %v", err)
	} else {
		for _, cache := range caches {
			h.storage.Remove(cache.ID)
			if err := deleteCache(db, cache.ID, cache); err != nil {
				h.logger.Warnf("delete cache: %v", err)
				continue
			}
			h.logger.Infof("deleted cache: %+v", cache)
		}
	}

	// Remove the old caches with the same key and version, keep the latest one.
	// Also keep the olds which have been used recently for a while in case of the cache is still in use.
	if results, err := findCompletedCaches(db); err != nil {
		h.logger.Warnf("find aggregate caches: %v", err)
	} else {
		for _, result := range results {
			if result.Count() <= 1 {
				continue
			}
			result.Sort("CreatedAt")
			caches = caches[:0]
			result.Reduction(&caches)
			for _, cache := range caches[:len(caches)-1] {
				if time.Since(time.Unix(cache.UsedAt, 0)) < keepOld {
					// Keep it since it has been used recently, even if it's old.
					// Or it could break downloading in process.
					continue
				}
				h.storage.Remove(cache.ID)
				if err := deleteCache(db, cache.ID, cache); err != nil {
					h.logger.Warnf("delete cache: %v", err)
					continue
				}
				h.logger.Infof("deleted cache: %+v", cache)
			}
		}
	}
}

func (h *Handler) responseJSON(w http.ResponseWriter, r *http.Request, code int, v ...any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var data []byte
	if len(v) == 0 || v[0] == nil {
		data, _ = json.Marshal(struct{}{})
	} else if err, ok := v[0].(error); ok {
		h.logger.Errorf("%v %v: %v", r.Method, r.RequestURI, err)
		data, _ = json.Marshal(map[string]any{
			"error": err.Error(),
		})
	} else {
		data, _ = json.Marshal(v[0])
	}
	w.WriteHeader(code)
	_, _ = w.Write(data)
}
