# elagoht/deploy

A collage plugin that turns a static build's captured headers and redirects into
the configuration files a static host reads, so a site's redirects, caching and
security headers survive being exported.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{deploy.NewWith(deploy.Config{Target: "netlify"})},
})
```

Requires collage v0.52.0 or later.

## Targets

| Target         | Writes                                        |
| -------------- | --------------------------------------------- |
| `netlify`      | `_headers` and `_redirects`                   |
| `cloudflare`   | `_headers` and `_redirects`                   |
| `vercel`       | `vercel.json`                                 |
| `github-pages` | redirect pages and a 404 page; no headers    |

The writers are added in the next release of the plugin; this version reads the
configuration, checks the build and compacts the headers.

## Headers

The headers the application answered each page and asset with are compacted into
the fewest rules that give every captured file exactly its own back: what every
file shares goes under `/*`, what every file of a directory shares under
`/dir/*`, and the rest at the file's own path. A file whose status was not 2xx, or
whose headers were not captured, carries none.

## What a host cannot carry

| Host         | Cannot carry                                                         |
| ------------ | -------------------------------------------------------------------- |
| Netlify      | a redirect status of 410 (answered as 404); regular expressions      |
| Cloudflare   | a redirect status of 410 (answered as 404); regular expressions      |
| Vercel       | `:splat` is spelled `:path*`; headers apply by pattern, not by status |
| GitHub Pages | any header; any redirect but a page that sends the reader on         |

Anything a host cannot carry is reported as a finding in the build's report rather
than dropped silently.

## Configuration

```json
{ "elagoht/deploy": { "target": "netlify" } }
```

- `target`: `netlify`, `cloudflare`, `vercel` or `github-pages`. Left empty, the
  plugin warns that nothing is written and the build goes on. Any other value
  stops the application from starting.

A control character in a redirect or a header value is an error that fails the
build: it would split a line of the host's file and write a rule nobody declared.
