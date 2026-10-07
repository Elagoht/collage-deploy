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
| `netlify` | `_headers` | `_redirects` (301/302) | 410; 307 and 308, written as 302 and 301; placeholders with text around them |
| `cloudflare` | `_headers` (100 rules, 2000-character lines) | `_redirects` (2000 static + 100 dynamic, 1000-character lines; 301/302/307/308) | 410; anything over a limit; placeholders with text around them; a catch-all whose base is a page |
| `vercel` | `vercel.json` `headers` | `vercel.json` `redirects` (301/302/307/308, as `statusCode`) | 410; more than 2048 routes |
| `github-pages` | not written: one summary warning with the count of header names and paths lost | one meta-refresh page per literal `From` (`<link rel="canonical">`, `noindex`), and `.nojekyll` | 410, the redirect status, patterned `From`s, a `From` with an extension other than `.html` |

The collage build itself writes the 404 pages. Patterned redirects are translated
to the host's syntax: `{name}` becomes `:name`; a catch-all `{name...}` becomes
`*` with `:splat` in the destination on Netlify and Cloudflare, and `:name+` on
Vercel (one or more segments, as collage's catch-all needs one). A placeholder
name a host cannot read (anything but letters, digits and `_`, or `splat`) is
renamed `p1`, `p2`, … within its rule. A rule the host cannot express is left out
with a warning naming it; the plugin never writes a rule that would redirect
differently from the application.

Redirects are written most specific first, as collage's router prefers them
(literal segments, then placeholders with text around them, then placeholders,
then catch-alls), since every host takes the first rule that matches.

An existing `_headers`, `_redirects` or `vercel.json` in the output, whether the
build wrote it or it was there before, is an error naming it, and nothing is
written: the plugin never overwrites or merges into a file it did not write. The
same goes for a file at a GitHub Pages redirect page's path. An existing
`.nojekyll` is left as it is.

## Host formats

Checked against each host's documentation on 2026-10-07.

**Netlify**
- [Redirect options](https://docs.netlify.com/manage/routing/redirects/redirect-options/):
  301 (default) and 302; "Use this status code instead of `307`, which is
  currently unsupported"; 308 and 410 are not listed (staff: "We support 301 and
  302 rules", [forum](https://answers.netlify.com/t/307-temporary-redirect-does-not-work-anymore/15516);
  a 410 rule was reported served as 404,
  [forum](https://answers.netlify.com/t/410-redirects-served-as-404/161825)). A
  307 is written as 302 and a 308 as 301 with a warning: for the GET and HEAD a
  static site answers, a browser treats each pair alike. 410 is left out.
- A placeholder "matches a path segment from one `/` to the next `/` or … a final
  path segment"; a splat `*` only at the end, its value `:splat`. Rules match
  "regardless of whether or not they contain a trailing slash"; the first match
  wins; a file at the path shadows a rule that is not forced, so a catch-all over
  a page is safe. Query strings are passed on for 301 and 302.
- Pretty URLs (on by default) forward `/about` to `/about/`, so a header rule at
  the served path `/about/` reaches the page.
- [Custom headers](https://docs.netlify.com/manage/routing/headers/): `path` then
  indented `Name: value` lines; several lines with one name are concatenated into
  one header. The docs do not say how two matching paths combine; staff said they
  would not merge them ([forum](https://answers.netlify.com/t/behavior-for-overlapping-paths-in-custom-headers/124217)).
  Netlify CLI's local emulation (`headersForPath` in
  [src/utils/headers.ts](https://github.com/netlify/cli/blob/main/src/utils/headers.ts))
  applies every matching rule, a later one replacing a header an earlier one set,
  as `Expand` models it. Compaction never sets one header name in two rules that
  match one captured file, so concatenating and replacing give the same headers.

**Cloudflare Pages**
- [Redirects](https://developers.cloudflare.com/pages/configuration/redirects/):
  301, 302, 303, 307, 308; at most 2000 static and 100 dynamic redirects, each
  line at most 1000 characters; `:name` of letters, digits and `_`; one splat,
  `:splat`; "Redirects are always followed, regardless of whether or not an asset
  matches", so a catch-all whose base is a page is left out with a warning. The
  documented `/trailing /trailing/ 301` shows a trailing slash is not normalised:
  each non-catch-all redirect is written at both spellings, its own first; the
  twins get only the budget left after every rule's own line.
- [Headers](https://developers.cloudflare.com/pages/configuration/headers/): at
  most 100 rules, 2000-character lines; a request matching several rules "will
  inherit all rules' headers", the same header "joined with a comma separator".
  Compaction keeps one header name to one matching rule, so nothing is joined.
- [Serving pages](https://developers.cloudflare.com/pages/configuration/serving-pages/):
  `/about/index.html` is served at `/about/`; whether `/about` is redirected to
  `/about/` is not documented.

**Vercel**
- [vercel.json](https://vercel.com/docs/project-configuration/vercel-json):
  `headers` of `{source, headers: [{key, value}]}`; `redirects` of `{source,
  destination, statusCode}` (`permanent` gives only 307/308, so `statusCode` is
  always written). `source` is path-to-regexp, matched strictly and
  case-sensitively ([routing-utils](https://github.com/vercel/vercel/blob/main/packages/routing-utils/src/superstatic.ts)),
  so `/(.*)` is everything, and literal `:()*+?{}[]\` are escaped. With
  `trailingSlash` unset, "both `/about` and `/about/` will serve the same content",
  so a directory's header rule and every redirect are written at both spellings.
  `vercel.json` is read from the project's root directory, and the plugin writes
  it into the build output: deploy the output directory as the project (for
  example `vercel deploy dist`), or the file is not read.
  At most 2048 routes per deployment
  ([limits](https://vercel.com/kb/guide/how-can-i-increase-the-limit-of-redirects-or-use-dynamic-redirects-on-vercel)).

**GitHub Pages**
- No custom headers. An empty `.nojekyll` at the root publishes files and
  directories beginning with `_`
  ([docs](https://docs.github.com/en/pages/getting-started-with-github-pages/creating-a-github-pages-site)).
  A redirect is a page served with status 200 whose meta refresh sends the browser
  on; GitHub Pages serves `old/index.html` at `/old/` and `old.html` at `/old`.

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
