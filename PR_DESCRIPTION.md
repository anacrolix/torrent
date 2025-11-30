# Fix IP resolution for proxied trackers (#727)

## Summary
This PR addresses the issue where tracker IPs are being resolved for blocklist matching even when the trackers are accessed through a proxy. This is unnecessary since the proxy handles DNS resolution and actual connections, making IP-based blocklist checks irrelevant for proxied connections.

## Problem Description
Previously, the `getIp()` function in `tracker_scraper.go` would always resolve tracker hostnames to IP addresses and check them against the blocklist, regardless of whether:
- An HTTP proxy was configured (`HTTPProxy`)
- A custom dial context was set (`TrackerDialContext`)

This behavior caused several issues:
1. **Unnecessary DNS lookups**: When using a proxy, DNS resolution is handled by the proxy, not the client
2. **Incorrect blocklist enforcement**: Blocklists should only apply to direct connections, not proxied ones
3. **Performance impact**: Extra DNS resolution overhead for proxied trackers
4. **Potential privacy issues**: Exposing tracker IPs to the client when they should be hidden behind the proxy

## Solution
The fix modifies the `getIp()` function to detect proxy usage and skip IP resolution entirely when:
1. `HTTPProxy` is configured (HTTP trackers will go through the proxy)
2. `TrackerDialContext` is set (custom dialing, potentially through proxy)

When proxy usage is detected, the function returns a dummy IP (`127.0.0.1`) that won't be used for actual connections since the proxy/custom dial context handles the networking.

## Changes Made

### Core Fix (`tracker_scraper.go`)
```go
func (me *trackerScraper) getIp() (ip net.IP, err error) {
	// Skip IP resolution if using proxy - the proxy handles DNS resolution
	// and we don't need to check against blocklists for proxied connections.
	// This applies to both HTTP proxy and custom dial contexts that might be using proxies.
	if me.t.cl.config.HTTPProxy != nil || me.t.cl.config.TrackerDialContext != nil {
		// Return a dummy IP that won't be used for actual connections when proxy is set.
		// The actual connection will be handled by the proxy or custom dial context.
		return net.ParseIP("127.0.0.1"), nil
	}
	// ... rest of existing logic for non-proxied connections
}
```

### Test Coverage
Added comprehensive unit tests in `tracker_proxy_unit_test.go`:
- `TestGetIpProxyLogic_HTTP_proxy_set`: Verifies dummy IP is returned when HTTP proxy is configured
- `TestGetIpProxyLogic_TrackerDialContext_set`: Verifies dummy IP is returned when custom dial context is set  
- `TestGetIpProxyLogic_Both_proxy_and_dial_context_set`: Verifies behavior when both are configured

## Behavior Changes

### Before Fix
1. Proxy configured → DNS resolution performed → Blocklist checked → Real IP used
2. Custom dial context → DNS resolution performed → Blocklist checked → Real IP used

### After Fix  
1. Proxy configured → **No DNS resolution** → **No blocklist check** → Dummy IP returned
2. Custom dial context → **No DNS resolution** → **No blocklist check** → Dummy IP returned
3. No proxy → Normal DNS resolution → Blocklist check → Real IP used (unchanged)

## Security Considerations
- **Blocklist bypass is intentional**: When using proxies, blocklists should not apply since the connection doesn't go through the client's network stack
- **Privacy improvement**: Client no longer resolves tracker IPs when using proxies
- **No security regression**: Direct connections (non-proxied) still respect blocklists as before

## Backward Compatibility
- **Fully backward compatible**: No changes to public APIs
- **Behavior only changes for proxy users**: Non-proxy configurations work exactly as before
- **No configuration changes required**: Existing proxy setups automatically benefit

## Performance Impact
- **Improved performance**: Eliminates unnecessary DNS lookups for proxied trackers
- **Reduced network traffic**: Fewer DNS queries when using proxies
- **Faster tracker startup**: No waiting for DNS resolution in proxy scenarios

## Testing
- Unit tests verify the proxy detection logic works correctly
- Manual testing confirms proxied trackers work without IP resolution
- Existing test suite continues to pass for non-proxy scenarios

## Alternative Approaches Considered
1. **Always resolve but skip blocklist**: Would still incur DNS overhead
2. **Add explicit proxy flag**: Would require API changes and configuration complexity  
3. **Use proxy callback to determine IP**: Complex and unreliable across different proxy types

The chosen approach is minimal, safe, and automatically handles all proxy scenarios without requiring user configuration changes.

## Files Changed
- `tracker_scraper.go`: Core fix to skip IP resolution for proxied trackers
- `tracker_proxy_unit_test.go`: Unit tests for the new behavior
