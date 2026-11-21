package source

import (
	"net/http"
	"strconv"
)

// AssetRequest is how to fetch one release file: where to send the GET and the
// headers that go with it.
type AssetRequest struct {
	URL    string
	Header http.Header
}

// AssetRequest says how to fetch a release file. With a token the file comes
// from the site's authenticated asset endpoint, so files of private
// repositories can be read; without one, or for a file cached before its id
// was recorded, the public link is used and no credentials are sent.
func (g *GitHub) AssetRequest(ref RepoRef, a Asset) AssetRequest {
	header := http.Header{"User-Agent": {g.userAgent}}
	token := g.currentToken()
	if token == "" || a.ID == 0 {
		return AssetRequest{URL: a.URL, Header: header}
	}
	header.Set("Accept", "application/octet-stream")
	header.Set("Authorization", "Bearer "+token)
	header.Set("X-Github-Api-Version", apiVersion)
	return AssetRequest{
		URL:    g.baseURL + repoPath(ref) + "/releases/assets/" + strconv.FormatInt(a.ID, 10),
		Header: header,
	}
}
