# Changelog

## 0.2.0

First standalone open-source release.

- Experimental per-account, identity and model ticket harvesting/cache/injection.
- Chinese proxy-settings UI with custom HTTP/HTTPS/SOCKS5/SOCKS5h support.
- Select existing CPA global, provider or file-backed account proxies without
  changing their business-routing configuration.
- Redacted proxy options, private atomic storage, bounded connectivity test.
- Stable config-entry references across reorder; missing/ambiguous selections
  require reselection rather than silently selecting another proxy.
- Bounded account discovery, cancellation, retries and failure-open requests.
- HTTP/SSE only; WebSocket injection and model-quality guarantees are excluded.
- Independent repository metadata and reproducible local test commands.
