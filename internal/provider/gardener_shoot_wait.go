package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	api "github.com/cleura/terraform-provider-cleura/api"
	"github.com/cleura/terraform-provider-cleura/cleura"
)

const (
	shootReconcilePollInterval   = 15 * time.Second
	shootReconcileTimeout        = 60 * time.Minute
	shootReconcileRequestTimeout = 2 * time.Minute // Per-request limit; connection may be stale after sleep
	shootReconcileMaxRetries     = 5
)

// isRetriableStatus returns true for HTTP statuses that may be transient (e.g. 403 Forbidden IP after wake from sleep).
func isRetriableStatus(statusCode int) bool {
	switch statusCode {
	// 403 = Forbidden IP (can occur during a network transition, e.g. wake from
	// sleep); 429 = Too Many Requests; 502/503/504 = transient gateway errors.
	case 403, 429, 502, 503, 504:
		return true
	}
	return false
}

func WaitForShootReconcile(ctx context.Context, client *cleura.Client, gardenerRegionTag, openStackRegionTag, openStackProjectId, shootName string, waitForDelete bool) diag.Diagnostics {
	var diagnostics diag.Diagnostics

	// Use wall-clock deadline so timeout is correct after machine sleep/suspend.
	// context.WithTimeout uses monotonic timers that don't advance when suspended.
	deadline := time.Now().Add(shootReconcileTimeout)

	ticker := time.NewTicker(shootReconcilePollInterval)
	defer ticker.Stop()

	for {
		// Check wall-clock deadline (survives suspend/resume)
		if time.Now().After(deadline) {
			if waitForDelete {
				diagnostics.AddError("Timeout waiting for shoot deletion",
					"shoot still exists after the timeout")
			} else {
				diagnostics.AddError("Timeout waiting for shoot reconciliation",
					"shoot did not reach Progress=100 and State=Succeeded within the timeout")
			}
			return diagnostics
		}

		// Respect Terraform context cancellation (e.g. Ctrl+C)
		select {
		case <-ctx.Done():
			diagnostics.AddError("Shoot reconciliation cancelled", ctx.Err().Error())
			return diagnostics
		default:
		}

		// Per-request timeout so a single call can't hang forever after connection drop
		reqCtx, reqCancel := context.WithTimeout(ctx, shootReconcileRequestTimeout)

		var response *api.GardenerGetShootResponse
		var err error
		for attempt := 0; attempt < shootReconcileMaxRetries; attempt++ {
			response, err = client.GardenerGetShootWithResponse(reqCtx, gardenerRegionTag, openStackRegionTag, openStackProjectId, shootName)
			if err == nil && response != nil {
				// For delete: 404 means shoot is gone, which is success
				if waitForDelete && response.StatusCode() == 404 {
					reqCancel()
					return diagnostics
				}
				// Retry on transient HTTP errors (e.g. 403 Forbidden IP when network changes during screen lock)
				if !isRetriableStatus(response.StatusCode()) {
					break
				}
			}
			// Retry on error or retriable status (e.g. connection reset, 403 after sleep)
			if attempt < shootReconcileMaxRetries-1 {
				backoff := time.Duration(attempt+1) * 5 * time.Second
				select {
				case <-ctx.Done():
					reqCancel()
					diagnostics.AddError("Shoot reconciliation cancelled", ctx.Err().Error())
					return diagnostics
				case <-time.After(backoff):
				}
			}
		}
		reqCancel()

		if err != nil {
			diagnostics.AddError("Failed to get shoot status", err.Error())
			return diagnostics
		}

		// For delete: 404 means shoot is gone, which is success (may have been set in retry loop)
		if waitForDelete && response != nil && response.StatusCode() == 404 {
			return diagnostics
		}

		if response == nil || response.JSON200 == nil {
			statusStr := "unknown"
			bodyStr := ""
			if response != nil {
				statusStr = fmt.Sprintf("%d", response.StatusCode())
				bodyStr = string(response.Body)
			}
			diagnostics.AddError("API error", fmt.Sprintf("unexpected response status %s: %s", statusStr, bodyStr))
			return diagnostics
		}

		shoot := response.JSON200
		lastOp := shoot.LastOperation

		if lastOp == nil {
			// No last operation yet (e.g. shoot just created), keep polling
		} else if lastOp.Progress == 100 && lastOp.State == api.GardenerShootLastOperationStateSucceeded {
			return diagnostics
		} else if lastOp.State == api.GardenerShootLastOperationStateError || lastOp.State == api.GardenerShootLastOperationStateFailed || lastOp.State == api.GardenerShootLastOperationStateAborted {
			diagnostics.AddError("Shoot reconciliation failed",
				fmt.Sprintf("last operation state: %s, description: %s", lastOp.State, lastOp.Description))
			return diagnostics
		}

		select {
		case <-ctx.Done():
			diagnostics.AddError("Shoot reconciliation cancelled", ctx.Err().Error())
			return diagnostics
		case <-ticker.C:
			// Poll again
		}
	}
}
