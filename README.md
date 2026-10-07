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

| Target | Headers | Redirects | Cannot carry (reported as a warning) |
|---|---|---|---|
| `netlify` | `_headers` | `_redirects` | nothing |
| `cloudflare` | `_headers` (100 rules) | `_redirects` (2000 static + 100 dynamic; 301/302/307/308) | 410; anything over a limit |
| `vercel` | `vercel.json` `headers` | `vercel.json` `redirects` (301/302/307/308) | 410; an existing `vercel.json` in the output is a build error, never overwritten |
| `github-pages` | not written: one summary warning with the count of header names and paths lost | one meta-refresh page per literal `From` (`<link rel="canonical">`, `noindex`), and `.nojekyll` | 410, the permanent/temporary distinction, patterned `From`s |

The collage build itself writes GitHub Pages' 404 page. Patterned redirects are
translated to the host's syntax: `{name}` becomes `:name`, and `{name...}` becomes
`*` with the host's splat token (`:splat` on Netlify and Cloudflare, `:name*` on
Vercel).

The writers are added in the next release of the plugin; this version reads the
configuration, checks the build and compacts the headers.

## Headers

The headers the application answered each page and asset with are compacted into
the fewest rules that give every captured file exactly its own back: what every
captured file shares goes under `/*`, what every file of a directory shares under
`/dir/*`, and the rest at the file's own path.

A file the build did not ask for (the 404 pages it writes, the root redirect)
carries no headers of its own, but a host cannot tell it from the rest, so the
`/*` headers reach it, the 404 pages included. A `/dir/*` rule is written only
when every file under the directory is a captured 2xx file sharing the headers, so
it never reaches such a file. A file that was asked for and answered otherwise than
2xx (a redirect, an error) blocks `/*`: its headers are not the pages', and the
rules fall back to directories and single paths.

## Configuration

```json
{ "elagoht/deploy": { "target": "netlify" } }
```

- `target`: `netlify`, `cloudflare`, `vercel` or `github-pages`. Left empty, the
  plugin warns that nothing is written and the build goes on. Any other value
  stops the application from starting.

A control character in a redirect or a header value is an error that fails the
build: it would split a line of the host's file and write a rule nobody declared.
