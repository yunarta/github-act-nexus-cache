package nexus

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Cache struct {
	ArchiveLocation string
	CacheKey        string
}

type Search struct {
	searchKey string
	key       string
}

type CacheService struct {
	endPoint   string
	repository string
	prefix     string // prefix path will always had trailing slash
}

func NewCacheService(fullPath string) *CacheService {
	// parse url
	parsedUrl, err := url.Parse(fullPath)
	if err != nil {
		panic(err)
	}

	// extract endpoint, repository, and prefix
	pathParts := strings.Split(parsedUrl.Path, "/")
	endPoint := parsedUrl.Scheme + "://" + parsedUrl.Host                // e.g., https://nxrm.mobilesolutionworks.com
	repository, prefix := pathParts[2], strings.Join(pathParts[3:], "/") // gh-action-cache, prefix/act-nexus-cache
	// convert from url which in this example "https://nxrm.mobilesolutionworks.com/repository/gh-action-cache/prefix/act-nexus-cache"
	// where endpoint is https://nxrm.mobilesolutionworks.com
	// repository is gh-action-cache
	// prefix is whatever after gh-action-cache, in this case prefix/act-nexus-cache

	return &CacheService{
		endPoint:   endPoint,
		repository: repository,
		prefix:     prefix,
	}
}

func (n *CacheService) fetchJSON(url string, target any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	username := os.Getenv("NEXUS_USERNAME")
	secret := os.Getenv("NEXUS_SECRET")

	req.SetBasicAuth(username, secret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	content, err := io.ReadAll(resp.Body)
	defer resp.Body.Close()

	return json.Unmarshal(content, &target)
}

func (n *CacheService) uploadFile(url string, file *os.File) error {
	req, err := http.NewRequest("PUT", url, file)
	if err != nil {
		return err
	}
	username := os.Getenv("NEXUS_USERNAME")
	secret := os.Getenv("NEXUS_SECRET")

	req.SetBasicAuth(username, secret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	return nil
}

func (n *CacheService) FindCache(keys []string, version string) (*Cache, error) {
	searchKeys := make([]Search, 0)
	searchKeys = append(searchKeys, Search{
		key:       keys[0],
		searchKey: fmt.Sprintf("%s/%s-%s", n.prefix, keys[0], version),
	})

	for _, key := range keys[1:] {
		searchKeys = append(searchKeys, Search{
			key:       "",
			searchKey: fmt.Sprintf("%s/%s*", n.prefix, key),
		})
	}

	for _, search := range searchKeys {
		items := make([]SearchAssetItem, 0)

		key := search.searchKey
		searchKeyUrl := fmt.Sprintf("%s/service/rest/v1/search/assets?repository=%s&format=raw&name=%s",
			n.endPoint,
			n.repository,
			key,
		)

		for {
			// run with search url above, also take not of continuationToken, iterate until none found
			var searchResponse SearchAssetResponse
			err := n.fetchJSON(searchKeyUrl, &searchResponse)
			if err != nil {
				return nil, err
			}

			// parse response json into SearchAssetResponse
			items = append(items, searchResponse.Items...)

			//Check if ContinuationToken is empty
			if searchResponse.ContinuationToken == nil {
				break
			}

			// if continuationToken is not empty, append it to the URL for next fetch
			searchKeyUrl = fmt.Sprintf("https://%s/service/rest/v1/search/assets?repository=%s&format=raw&name=%s*&continuationToken=%s",
				n.endPoint,
				n.repository,
				key,
				*searchResponse.ContinuationToken,
			)
		}

		// sort items based on LastModified, desc
		sort.Slice(items, func(i, j int) bool {
			return items[i].LastModified > items[j].LastModified
		})

		if len(items) > 0 {

			// return first item
			// archiveLocation is item downladUrl
			parsedUrl, err := url.Parse(items[0].DownloadUrl)
			if err != nil {
				return nil, err
			}

			// if search.key is not empty
			key = search.key
			if key == "" {
				// find key from item[0]
				// items[0].Path is file path like
				// get the file
				filename := filepath.Base(items[0].Path)
				lastIndex := strings.LastIndex(filename, "-")
				key = filename[:lastIndex]
			}

			return &Cache{
				ArchiveLocation: strings.ReplaceAll(items[0].DownloadUrl,
					fmt.Sprintf("%s://%s", parsedUrl.Scheme, parsedUrl.Host),
					n.endPoint,
				),
				CacheKey: key,
			}, nil
		}
	}

	return nil, nil
}

func (n *CacheService) PutCache(key string, version string, filename string) {
	storeKey := fmt.Sprintf("%s/%s-%s", n.prefix, key, version)
	searchKeyUrl := fmt.Sprintf("%s/repository/%s/%s",
		n.endPoint,
		n.repository,
		storeKey,
	)

	// upload file on filename to nexus
	file, err := os.Open(filename)
	if err != nil {
		// handle error
	}
	defer file.Close()

	n.uploadFile(searchKeyUrl, file)
}
