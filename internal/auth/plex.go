// Package auth ports the Node server's Plex PIN-based OAuth flow
// (services/auth.ts, routes/auth.ts) and its session middleware
// (middleware/auth.ts) to Go.
package auth

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const plexAPIBase = "https://plex.tv/api/v2"

type PlexClient struct {
	ClientID    string
	ProductName string
	httpClient  *http.Client
}

func NewPlexClient(clientID, productName string) *PlexClient {
	return &PlexClient{
		ClientID:    clientID,
		ProductName: productName,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
	}
}

type PlexPin struct {
	ID        int    `json:"id"`
	Code      string `json:"code"`
	AuthToken string `json:"authToken"`
}

type PlexUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Title    string `json:"title"`
	Email    string `json:"email"`
	Friendly string `json:"friendlyName"`
	Thumb    string `json:"thumb"`
}

// DisplayName picks a name for a Plex account the same way plexDisplayName()
// in routes/auth.ts does: managed (restricted) Home users have no username,
// only a title.
func (u PlexUser) DisplayName() string {
	switch {
	case u.Username != "":
		return u.Username
	case u.Title != "":
		return u.Title
	case u.Friendly != "":
		return u.Friendly
	case u.Email != "":
		return u.Email
	default:
		return fmt.Sprintf("plex-%d", u.ID)
	}
}

func (c *PlexClient) headers(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plex-Product", c.ProductName)
	req.Header.Set("X-Plex-Client-Identifier", c.ClientID)
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
}

// StartAuth creates a PIN the user will authorize the app with.
func (c *PlexClient) StartAuth() (*PlexPin, error) {
	req, err := http.NewRequest(http.MethodPost, plexAPIBase+"/pins?strong=true", nil)
	if err != nil {
		return nil, err
	}
	c.headers(req, "")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to start Plex auth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("failed to start Plex auth: status %d", resp.StatusCode)
	}
	var pin PlexPin
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, err
	}
	return &pin, nil
}

// PollAuth checks whether the PIN has been authorized. Returns nil, nil if
// the PIN expired or was consumed (Plex answers 404) - a normal poll-loop
// end state, not an error.
func (c *PlexClient) PollAuth(pinID int, code string) (*PlexPin, error) {
	u := fmt.Sprintf("%s/pins/%d?code=%s", plexAPIBase, pinID, url.QueryEscape(code))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.headers(req, "")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to poll Plex auth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("failed to poll Plex auth: status %d", resp.StatusCode)
	}
	var pin PlexPin
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return nil, err
	}
	return &pin, nil
}

// AuthURL builds the app.plex.tv sign-in URL for the given PIN code.
// forwardURL, if set, is where Plex redirects the browser after sign-in.
func (c *PlexClient) AuthURL(code, forwardURL string) string {
	params := url.Values{}
	params.Set("clientID", c.ClientID)
	params.Set("code", code)
	params.Set("context[device][product]", c.ProductName)
	if forwardURL != "" {
		params.Set("forwardUrl", forwardURL)
	}
	return "https://app.plex.tv/auth#?" + params.Encode()
}

// GetUserInfo fetches the Plex account for authToken.
func (c *PlexClient) GetUserInfo(authToken string) (*PlexUser, error) {
	req, err := http.NewRequest(http.MethodGet, plexAPIBase+"/user", nil)
	if err != nil {
		return nil, err
	}
	c.headers(req, authToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid or expired Plex token")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("failed to get user info: status %d", resp.StatusCode)
	}
	var u PlexUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

type homeUsersResponse struct {
	Users []struct {
		ID int `json:"id"`
	} `json:"users"`
}

// GetHomeUsers returns the admin's Plex Home (managed users).
func (c *PlexClient) GetHomeUsers(adminToken string) ([]int, error) {
	req, err := http.NewRequest(http.MethodGet, plexAPIBase+"/home/users", nil)
	if err != nil {
		return nil, err
	}
	c.headers(req, adminToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get home users: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid or expired Plex token")
	}

	// plex.tv answers either a bare array or {"users": [...]}.
	body, err := decodeUsersArrayOrWrapped(resp)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func decodeUsersArrayOrWrapped(resp *http.Response) ([]int, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var arr []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		ids := make([]int, len(arr))
		for i, u := range arr {
			ids[i] = u.ID
		}
		return ids, nil
	}
	var wrapped homeUsersResponse
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	ids := make([]int, len(wrapped.Users))
	for i, u := range wrapped.Users {
		ids[i] = u.ID
	}
	return ids, nil
}

type PlexConnection struct {
	URI   string `json:"uri"`
	Local bool   `json:"local"`
	Relay bool   `json:"relay"`
}

type PlexServer struct {
	Name             string           `json:"name"`
	ClientIdentifier string           `json:"clientIdentifier"`
	Provides         string           `json:"provides"`
	Owned            bool             `json:"owned"`
	AccessToken      string           `json:"accessToken"`
	PublicAddress    string           `json:"publicAddress"`
	Connections      []PlexConnection `json:"connections"`
}

// BestURL prefers a local connection over remote, and remote over relay -
// same preference order as services/auth.ts's getBestServerUrl.
func (s PlexServer) BestURL() string {
	for _, c := range s.Connections {
		if c.Local && !c.Relay {
			return c.URI
		}
	}
	for _, c := range s.Connections {
		if !c.Local && !c.Relay {
			return c.URI
		}
	}
	for _, c := range s.Connections {
		if c.Relay {
			return c.URI
		}
	}
	return fmt.Sprintf("https://%s:32400", s.PublicAddress)
}

// GetServers returns the account's Plex Media Server resources (filtering
// out player/client-only resources, which also appear in this listing).
func (c *PlexClient) GetServers(authToken string) ([]PlexServer, error) {
	req, err := http.NewRequest(http.MethodGet, plexAPIBase+"/resources?includeHttps=1&includeRelay=1", nil)
	if err != nil {
		return nil, err
	}
	c.headers(req, authToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get servers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid or expired Plex token")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("failed to get servers: status %d", resp.StatusCode)
	}

	var all []PlexServer
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, err
	}
	servers := make([]PlexServer, 0, len(all))
	for _, s := range all {
		if strings.Contains(s.Provides, "server") {
			servers = append(servers, s)
		}
	}
	return servers, nil
}

// GetFriends returns the account IDs of the admin's Plex friends. plex.tv's
// v2 /friends endpoint answers 410 Gone, so - same as the Node server - this
// reads the still-supported XML list at /api/users and regexes the id
// attribute out rather than pulling in an XML-parsing dependency for one
// field.
func (c *PlexClient) GetFriends(authToken string) ([]int, error) {
	req, err := http.NewRequest(http.MethodGet, "https://plex.tv/api/users", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("X-Plex-Token", authToken)
	req.Header.Set("X-Plex-Client-Identifier", c.ClientID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get friends: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid or expired Plex token")
	}

	return parseFriendIDs(resp.Body), nil
}

// parseFriendIDs pulls each <User id="..."> attribute out of plex.tv's XML
// user list, split out of GetFriends so the parsing itself can be tested
// without a real HTTP round trip.
func parseFriendIDs(body io.Reader) []int {
	dec := xml.NewDecoder(body)
	var ids []int
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "User" {
			for _, attr := range start.Attr {
				if attr.Name.Local == "id" {
					var id int
					fmt.Sscanf(attr.Value, "%d", &id)
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// SwitchToManagedUser obtains a Plex Home managed user's own auth token via
// the account-level switch endpoint - requires the Plex Home owner's
// (admin's) token.
func (c *PlexClient) SwitchToManagedUser(adminToken, plexHomeUserID string) (string, error) {
	req, err := http.NewRequest(http.MethodPost, plexAPIBase+"/home/users/"+plexHomeUserID+"/switch", nil)
	if err != nil {
		return "", err
	}
	c.headers(req, adminToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to obtain token for Plex Home user %s: %w", plexHomeUserID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("invalid or expired Plex token")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("failed to obtain token for Plex Home user %s: status %d", plexHomeUserID, resp.StatusCode)
	}
	var data struct {
		AuthToken string `json:"authToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.AuthToken == "" {
		return "", fmt.Errorf("failed to obtain token for Plex Home user %s", plexHomeUserID)
	}
	return data.AuthToken, nil
}
