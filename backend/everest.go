package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const everestRequestTimeout = 10 * time.Second

func everestAPIURL() string {
	if v := os.Getenv("EVEREST_API_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	// Injected by Kubernetes when the plugin runs in the same namespace as the "everest" Service.
	host := os.Getenv("EVEREST_SERVICE_HOST")
	port := os.Getenv("EVEREST_SERVICE_PORT")
	if host != "" && port != "" {
		return fmt.Sprintf("http://%s:%s", host, port)
	}
	return "http://everest.everest-system.svc.cluster.local:8080"
}

// statusError carries an HTTP status to return to the caller.
type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string { return e.message }

// instance is the subset of the Instance CR the plugin needs.
type instance struct {
	Spec struct {
		ProviderRef struct {
			Name string `json:"name"`
		} `json:"providerRef"`
	} `json:"spec"`
}

type everestClient struct {
	baseURL string
	http    *http.Client
}

func newEverestClient(baseURL string) *everestClient {
	return &everestClient{baseURL: baseURL, http: &http.Client{Timeout: everestRequestTimeout}}
}

func (c *everestClient) instanceURL(ref instanceRef, suffix string) string {
	return fmt.Sprintf("%s/v1/clusters/%s/namespaces/%s/instances/%s%s",
		c.baseURL,
		url.PathEscape(ref.k8sCluster),
		url.PathEscape(ref.namespace),
		url.PathEscape(ref.name),
		suffix,
	)
}

func (c *everestClient) get(ctx context.Context, token, apiURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &statusError{status: http.StatusBadGateway, message: "everest API unreachable"}
	}
	return resp, nil
}

// getInstance fetches the Instance as the calling user, which doubles as the
// authorization check for reading its users: the Everest API enforces the
// user's own RBAC.
func (c *everestClient) getInstance(ctx context.Context, token string, ref instanceRef) (*instance, error) {
	resp, err := c.get(ctx, token, c.instanceURL(ref, ""))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return nil, &statusError{status: resp.StatusCode, message: http.StatusText(resp.StatusCode)}
	default:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, &statusError{status: http.StatusBadGateway, message: fmt.Sprintf("everest API returned %d", resp.StatusCode)}
	}

	var in instance
	if err := json.NewDecoder(resp.Body).Decode(&in); err != nil {
		return nil, &statusError{status: http.StatusBadGateway, message: "invalid instance response"}
	}
	return &in, nil
}

// canManageUsers reports whether the calling user may change the instance's
// users. That takes the read-connection permission: whoever can read the
// instance's admin credentials can already manage its users with them.
func (c *everestClient) canManageUsers(ctx context.Context, token string, ref instanceRef) (bool, error) {
	resp, err := c.get(ctx, token, c.instanceURL(ref, "/connection"))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	// Only the status matters; drop the credentials unread.
	_, _ = io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	// 404 until the instance publishes connection details, i.e. while it is not ready.
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false, nil
	default:
		return false, &statusError{status: http.StatusBadGateway, message: fmt.Sprintf("everest API returned %d", resp.StatusCode)}
	}
}
