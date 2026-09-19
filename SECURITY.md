# Security policy

This is an experimental native plugin, not a sandbox. It runs inside CPA and can
read credentials required for authorized Codex probes. Only install trusted builds.

- Do not publish CPA management keys, OAuth files, real turn-state values, proxy
  passwords, database exports, private deployment URLs, or unredacted logs.
- Keep the private proxy-selection directory mode 0700 and its file mode 0600.
  Proxy credentials are stored as plaintext protected by file permissions, not encrypted.
- Use HTTPS or a localhost SSH tunnel for the settings page. Its management key
  is held only in page memory. The static HTML is public; all runtime APIs require
  CPA Management API authentication.
- Enable `commercial-mode: true` before starting CPA. `request-log: false` alone
  does not disable CPA v7.3.8's error-only request-header capture.
- Config references and proxy failures must not fall back to direct connections.
- Pause or disable the plugin immediately if it behaves unexpectedly.

## Reporting

Use GitHub's private vulnerability report feature if available. If it is not
available, open an issue asking the maintainer for a private reporting channel,
without exploit details, credentials, or private data. Do not attach production
configurations or auth files to a public issue.

No security response time or upstream quality guarantee is provided.
