package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/yggy-tech/yggy-cli/internal/devicelogin"
)

// DeviceLogin runs the browser login against one API.
type DeviceLogin interface {
	Start(ctx context.Context) (devicelogin.Code, error)
	Wait(ctx context.Context, code devicelogin.Code) (string, error)
}

// browserLogin signs in through the browser and returns the API key the server issues.
func (a *App) browserLogin(ctx context.Context, baseURL string, noBrowser bool) (string, int) {
	var login DeviceLogin = &devicelogin.Client{BaseURL: baseURL}
	if a.NewDeviceLogin != nil {
		login = a.NewDeviceLogin(baseURL)
	}
	code, err := login.Start(ctx)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return "", 1
	}

	fmt.Fprintf(a.Err, "First, copy your one-time code: %s\n", code.UserCode)
	page := code.URL()
	opened := false
	if !noBrowser && a.OpenBrowser != nil {
		opened = a.OpenBrowser(page) == nil
	}
	if opened {
		fmt.Fprintf(a.Err, "Opened %s in your browser. If nothing opened, go there yourself.\n", page)
	} else {
		fmt.Fprintf(a.Err, "Open %s in your browser.\n", page)
	}
	fmt.Fprintln(a.Err, "Check that the page shows the same code, then approve. Do not approve a code you did not start here.")
	fmt.Fprintln(a.Err, "Waiting for approval. Press Ctrl+C to cancel.")

	key, err := login.Wait(ctx, code)
	switch {
	case err == nil:
		return key, 0
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(a.Err, "login cancelled")
		return "", 1
	default:
		fmt.Fprintln(a.Err, err)
		return "", 1
	}
}
