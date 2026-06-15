package cleura

import (
	"context"
	"net/http"

	api "github.com/cleura/terraform-provider-cleura/api"
)

type Client struct {
	*api.ClientWithResponses
}

// NewClientWithCredentials creates a wrapped API client with credentials.
func NewClientWithCredentials(url, username, token string) (*Client, error) {
	cleura, err := api.NewClientWithResponses(url,
		api.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			req.Header.Set("X-AUTH-LOGIN", username)
			req.Header.Set("X-AUTH-TOKEN", token)
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}

	return &Client{
		cleura,
	}, nil
}
