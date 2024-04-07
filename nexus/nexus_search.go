package nexus

type SearchAssetItem struct {
	DownloadUrl    string                 `json:"downloadUrl"`
	Path           string                 `json:"path"`
	Id             string                 `json:"id"`
	Repository     string                 `json:"repository"`
	Format         string                 `json:"format"`
	Checksum       map[string]interface{} `json:"checksum"`
	ContentType    string                 `json:"contentType"`
	LastModified   string                 `json:"lastModified"`
	LastDownloaded string                 `json:"lastDownloaded"`
	Uploader       string                 `json:"uploader"`
	UploaderIp     string                 `json:"uploaderIp"`
	FileSize       int                    `json:"fileSize"`
	BlobCreated    string                 `json:"blobCreated"`
}

type SearchAssetResponse struct {
	Items             []SearchAssetItem `json:"items"`
	ContinuationToken *string           `json:"continuationToken,omitempty"`
}
